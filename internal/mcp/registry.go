package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// defaultConnectTimeout bounds how long startup waits for ONE MCP server to
// connect and list its tools. A server that exceeds it is abandoned and skipped
// so a slow or unreachable server (e.g. a hosted endpoint blocked by the local
// network) cannot delay the first model response. Servers connect concurrently,
// so total startup cost is the slowest reachable server, not the sum.
const defaultConnectTimeout = 8 * time.Second

type RegisterOptions struct {
	PermissionStore *PermissionStore
	Autonomy        PermissionAutonomy
	// AdvertiseInAuto exposes prompt-gated MCP tools to model surfaces whose
	// default mode is auto while preserving the execution-time prompt.
	AdvertiseInAuto bool
	ClientFactory   func(context.Context, Server) (ToolClient, error)
	// ConnectTimeout bounds the per-server connect+list at startup. QG uses
	// defaultConnectTimeout.
	ConnectTimeout time.Duration
	// ProjectRoot is the workspace whose .qg/config.json was read. Project-layer
	// servers need a one-time user approval recorded for this root before they
	// run. Empty means no project layer is in play.
	ProjectRoot string
	// ProjectApprovals carries the user's approvals. Nil loads the default
	// user store when ProjectRoot is set, and approves nothing otherwise.
	ProjectApprovals *ProjectApprovals
	Execution        *execution.Runner
	WorkspaceRoot    string
}

// SkippedServer records an MCP server that was not registered because it could
// not be reached or its tools could not be validated. Registration is
// best-effort per server: one unreachable server is skipped (and reported here)
// rather than aborting startup or disabling the others.
type SkippedServer struct {
	Name string
	Err  error
	// UnconfiguredDefault mirrors Server.UnconfiguredDefault: true when this
	// server is an out-of-the-box default the user never configured, so a
	// caller can skip warning loudly about it.
	UnconfiguredDefault bool
}

type Runtime struct {
	clients []ToolClient
	// cancels releases the per-server connect contexts of the clients we KEPT.
	// A stdio server's subprocess is tied to its context, so the context must
	// stay live for the session and be cancelled only at Close (after the client
	// is closed). Same length/order as clients is not required.
	cancels []context.CancelFunc
	skipped []SkippedServer
	// Refresh state for tools/list_changed: the registry and factory needed to
	// re-list a stale server, the tool names each server currently owns, the
	// live client per server, and the ledger of applied catalog changes.
	registry *tools.Registry
	options  RegisterOptions
	servers  []Server
	factory  func(context.Context, Server) (ToolClient, error)
	// lifeCtx outlives turns: reconnects spawn subprocesses bound to it so a
	// revived stdio server survives the turn that revived it. Cancelled at
	// Close, after the clients are closed.
	lifeCtx     context.Context
	lifeCancel  context.CancelFunc
	disabled    map[string]bool
	serverTools map[string][]string
	liveClients map[string]*mcpServerConn
	changes     []CatalogChange
	refreshMu   sync.Mutex
	// closed latches at Close: later Reconnect calls fail instead of
	// redialing, and pending refreshes stay pending.
	closed bool
	once   sync.Once
	err    error
}

// Skipped returns the servers that were skipped during registration (unreachable,
// unapproved, or invalid), so the caller can warn the user without failing the
// launch. Safe for concurrent use with Reconnect and RefreshPendingTools.
func (runtime *Runtime) Skipped() []SkippedServer {
	if runtime == nil {
		return nil
	}
	runtime.refreshMu.Lock()
	defer runtime.refreshMu.Unlock()
	return append([]SkippedServer{}, runtime.skipped...)
}

var unsafeToolNameChars = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func RegisterTools(ctx context.Context, registry *tools.Registry, cfg config.MCPConfig, options RegisterOptions) (*Runtime, error) {
	if registry == nil {
		return nil, fmt.Errorf("MCP tool registry is required")
	}
	servers, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		registry:    registry,
		options:     options,
		factory:     nil,
		serverTools: make(map[string][]string),
		liveClients: make(map[string]*mcpServerConn),
		disabled:    make(map[string]bool),
	}
	lifeCtx, lifeCancel := context.WithCancel(ctx)
	runtime.lifeCtx = lifeCtx
	runtime.lifeCancel = lifeCancel
	for name, raw := range cfg.Servers {
		if raw.Disabled {
			runtime.disabled[name] = true
		}
	}
	approvals := options.ProjectApprovals
	if approvals == nil && strings.TrimSpace(options.ProjectRoot) != "" {
		loaded, err := OpenProjectApprovals()
		if err == nil {
			approvals = loaded
		}
	}
	// Project-layer servers need a one-time user approval for this root before
	// they run. A nil store approves nothing: with a project root in play,
	// unapproved servers are skipped fail-closed.
	servers, approvalSkips := FilterUnapproved(servers, options.ProjectRoot, approvals)
	runtime.skipped = append(runtime.skipped, approvalSkips...)
	// Expand ${VAR}/${VAR:-default} for the connection. The identity stays
	// literal (computed above), so env rotation never invalidates approvals,
	// permissions, or OAuth tokens. Missing names expand to "": the target is
	// validated by the connection itself and skipped with the reason.
	for index := range servers {
		expanded, _ := servers[index].WithExpandedEnv()
		servers[index] = expanded
	}
	if len(servers) == 0 {
		return runtime, nil
	}

	factory := options.ClientFactory
	if factory == nil {
		factory = func(ctx context.Context, server Server) (ToolClient, error) {
			return ConnectWithOptions(ctx, server, ConnectOptions{Execution: options.Execution, WorkspaceRoot: options.WorkspaceRoot})
		}
	}
	runtime.factory = factory
	runtime.servers = servers

	timeout := options.ConnectTimeout
	if timeout <= 0 {
		timeout = connectTimeout()
	}

	// Connect every server CONCURRENTLY: connect + list-tools is network/process
	// I/O, so connecting serially makes startup wait for the SUM of all servers —
	// one slow or unreachable server would block every other server AND the first
	// model response. Each server gets its own cancelable context bounded by the
	// startup timeout; a server that does not connect + list in time is abandoned
	// (its context cancelled to tear down the half-open connection/subprocess) and
	// recorded as skipped. The concurrent phase does ONLY I/O and touches no shared
	// state; all validation, conflict detection, and registration happen in the
	// deterministic serial phase below, so the result is identical regardless of
	// completion order.
	type connectResult struct {
		client   ToolClient
		remote   []RemoteTool
		cancel   context.CancelFunc
		err      error
		timedOut bool
	}
	results := make([]connectResult, len(servers))
	var wg sync.WaitGroup
	for index := range servers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			server := servers[index]
			serverCtx, cancel := context.WithCancel(ctx)
			done := make(chan connectResult, 1)
			go func() {
				// One synchronous attempt: startup never waits out connect
				// backoff. A transient failure is retried in the background
				// (retryServerInBackground) so the first model response and
				// the first frame never wait on a dead server.
				client, remote, err := connectAndList(serverCtx, factory, server)
				done <- connectResult{client: client, remote: remote, err: err}
			}()
			select {
			case res := <-done:
				if res.err != nil {
					cancel() // failed: nothing to keep
				} else {
					res.cancel = cancel // keep the context alive; released at Close
				}
				results[index] = res
			case <-time.After(timeout):
				cancel() // abandon the slow connect: tears down the conn/subprocess
				// Reap the goroutine + any partial client in the background so a
				// slow server never blocks startup.
				go func() {
					if res := <-done; res.client != nil {
						_ = res.client.Close()
					}
				}()
				results[index] = connectResult{err: fmt.Errorf("connect timed out after %s", timeout), timedOut: true}
			}
		}(index)
	}
	wg.Wait()

	// Serial, deterministic commit in server order. Building tools reads the
	// permission store and the registry, so it stays single-goroutine. A server is
	// best-effort: one that failed to connect, timed out, returned a nameless tool,
	// or conflicts with an already-committed tool is SKIPPED (recorded, not fatal),
	// and still contributes its tools all-or-none. The conflict check spans the
	// registry plus every tool committed by an earlier server.
	staged := make([]*registryTool, 0)
	stagedNames := make(map[string]struct{})
	var retryLater []Server
	for index, server := range servers {
		res := results[index]
		if res.err != nil {
			runtime.skipped = append(runtime.skipped, SkippedServer{Name: server.Name, Err: redactError(server, res.err), UnconfiguredDefault: server.UnconfiguredDefault})
			// Only remote servers retry in the background, mirroring
			// connectWithRetry: a stdio server must not gain a new
			// subprocess spawn at startup. The failed attempt's context
			// is already cancelled, so classification uses a live one.
			// A connect budget (the initialize timeout firing while the
			// startup bound was also expiring) is a slow server, not a
			// permanent failure: it retries like any other timeout. Without
			// this, MCP_TIMEOUT collapsing the connect and initialize
			// budgets to the same value races the two timers and the retry
			// is silently never scheduled.
			if (server.Type == ServerTypeHTTP || server.Type == ServerTypeSSE) && (res.timedOut || isConnectBudgetError(res.err) || isTransientConnectError(context.Background(), res.err)) {
				retryLater = append(retryLater, server)
			}
			continue
		}
		conn := newMCPServerConn(server, res.client)
		conn.reconnect = func(context.Context) (ToolClient, error) {
			return factory(runtime.lifeCtx, server)
		}
		serverTools, validateErr := buildServerTools(registry, server, res.remote, conn, options, stagedNames, nil)
		if validateErr != nil {
			if res.cancel != nil {
				res.cancel()
			}
			_ = res.client.Close()
			runtime.skipped = append(runtime.skipped, SkippedServer{Name: server.Name, Err: validateErr, UnconfiguredDefault: server.UnconfiguredDefault})
			continue
		}
		runtime.clients = append(runtime.clients, conn)
		if res.cancel != nil {
			runtime.cancels = append(runtime.cancels, res.cancel)
		}
		runtime.liveClients[server.Name] = conn
		for _, tool := range serverTools {
			stagedNames[tool.Name()] = struct{}{}
			staged = append(staged, tool)
			runtime.serverTools[server.Name] = append(runtime.serverTools[server.Name], tool.Name())
		}
	}
	registered := make([]tools.Tool, 0, len(staged))
	for index := range staged {
		registered = append(registered, staged[index])
	}
	registry.RegisterBatch(registered)
	// Servers that failed transiently keep retrying in the background (same
	// attempts and backoff as a manual reconnect) and join the registry when
	// they answer, so a server that comes up after startup still lands in a
	// following turn. Permanent failures stay skipped with their real error.
	for _, server := range retryLater {
		go runtime.retryServerInBackground(server)
	}
	return runtime, nil
}

// retryServerInBackground keeps connecting a remote server whose initial
// connect failed transiently: attempts 2..maxConnectAttempts with the redial
// schedule (1s, 2s, 4s, 8s), then gives up. Each attempt runs on a child
// context bounded by connectTimeout, so a server that accepts TCP and never
// answers stalls only that attempt, never the loop. A connect that succeeds
// adopts its tools into the live registry (visible to the next turn) and
// clears the skip; a permanent failure or a closed runtime stops the loop.
// Stdio never reaches here: only HTTP/SSE retry, mirroring connectWithRetry,
// so startup never spawns a new subprocess in the background.
func (runtime *Runtime) retryServerInBackground(server Server) {
	if server.Type != ServerTypeHTTP && server.Type != ServerTypeSSE {
		return
	}
	for attempt := 2; attempt <= maxConnectAttempts; attempt++ {
		if !sleepForReconnect(runtime.lifeCtx, attempt-1) {
			return
		}
		if runtime.backgroundDone(server.Name) {
			return
		}
		// The attempt context outlives a successful connect (an SSE stream
		// derives from it), so the connect bound is a timer, not a deadline:
		// a deadline would kill an adopted stream when it fires.
		attemptCtx, cancel := context.WithCancel(runtime.lifeCtx)
		bound := time.AfterFunc(connectTimeout(), cancel)
		client, remote, err := connectAndList(attemptCtx, runtime.factory, server)
		timedOut := !bound.Stop()
		if timedOut {
			if err == nil {
				_ = client.Close()
			}
			err = fmt.Errorf("connect timed out after %s", connectTimeout())
		}
		if err == nil {
			runtime.adoptBackgroundServer(server, client, remote, cancel)
			return
		}
		// Classify before releasing the attempt: the failure outlives its
		// context, and a cancelled context must not mask the real error.
		// lifeCtx is live here (Close aborts the sleep above), so a
		// cancellation verdict only reflects a genuine shutdown. A bound
		// that fired is transient even though DeadlineExceeded alone is
		// not: the server may simply be slow, so later attempts continue.
		// A connect budget (the initialize timeout winning the race against
		// the attempt bound when both are MCP_TIMEOUT) is the same slow
		// server: retry instead of stopping the loop.
		transient := timedOut || isConnectBudgetError(err) || isTransientConnectError(runtime.lifeCtx, err)
		cancel()
		if !transient {
			runtime.noteBackgroundFailure(server, err)
			return
		}
		runtime.noteBackgroundFailure(server, err)
	}
}

// backgroundDone reports whether the background retry must stop: the
// runtime closed, or the server is already live (a manual reconnect won
// the race, and swapping its client would drop an in-flight call).
func (runtime *Runtime) backgroundDone(name string) bool {
	runtime.refreshMu.Lock()
	defer runtime.refreshMu.Unlock()
	return runtime.closed || runtime.liveClients[name] != nil
}

// noteBackgroundFailure refreshes a skipped entry with the latest background
// error so /mcp keeps naming the server's answer while it retries.
func (runtime *Runtime) noteBackgroundFailure(server Server, err error) {
	runtime.refreshMu.Lock()
	defer runtime.refreshMu.Unlock()
	// A manual reconnect may have brought this server live while the
	// background attempt was in flight: never rewrite the skip of a live
	// server with the stale failure of the superseded attempt.
	if runtime.closed || runtime.liveClients[server.Name] != nil {
		return
	}
	runtime.replaceSkippedLocked(SkippedServer{Name: server.Name, Err: redactError(server, err), UnconfiguredDefault: server.UnconfiguredDefault})
}

// adoptBackgroundServer commits a background-connected server to the live
// registry and clears its skip, mirroring a manual reconnect. The attempt
// context stays alive for the adopted client: the connect bound was a timer
// (stopped before adopting), not a deadline, and an SSE stream derives from
// this context, so cancelling it would kill the fresh stream. Released at
// Close like every kept server context. A closed runtime — or a server a
// manual reconnect already brought live — closes the fresh client instead
// of adopting it.
func (runtime *Runtime) adoptBackgroundServer(server Server, client ToolClient, remote []RemoteTool, cancel context.CancelFunc) {
	runtime.refreshMu.Lock()
	defer runtime.refreshMu.Unlock()
	// A manual reconnect (the /mcp panel) may have brought this server live
	// while the background loop was backing off: keep its client and drop
	// the fresh one instead of swapping a live transport underneath a call.
	if runtime.closed || runtime.liveClients[server.Name] != nil {
		cancel()
		_ = client.Close()
		return
	}
	if err := runtime.adoptRelistLocked(server, client, remote); err != nil {
		runtime.replaceSkippedLocked(SkippedServer{Name: server.Name, Err: redactError(server, err), UnconfiguredDefault: server.UnconfiguredDefault})
		cancel()
		_ = client.Close()
		return
	}
	runtime.cancels = append(runtime.cancels, cancel)
	runtime.dropSkippedLocked(server.Name)
}

// connectAndList connects to one server and lists its tools. It does ONLY I/O
// (no registry, permission-store, or other shared state), so it is safe to run
// concurrently for every server. On a list error it closes the client.
func connectAndList(ctx context.Context, factory func(context.Context, Server) (ToolClient, error), server Server) (ToolClient, []RemoteTool, error) {
	client, err := factory(ctx, server)
	if err != nil {
		return nil, nil, err
	}
	remoteTools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		return nil, nil, fmt.Errorf("list MCP tools for %s: %w", server.Name, err)
	}
	return client, remoteTools, nil
}

// buildServerTools validates a server's remote tools against the registry and the
// names already committed by earlier servers, returning the server's tools only
// when every one is named and conflict-free. It runs in the serial commit phase
// (single goroutine), so its registry and permission-store reads are race-free.
// Names in keep (a refresh replacing this server's own tools) do not conflict
// with the registry copies they are about to overwrite.
// On error the caller closes the client (it owns the result), so this never does.
func buildServerTools(registry *tools.Registry, server Server, remoteTools []RemoteTool, conn *mcpServerConn, options RegisterOptions, stagedNames map[string]struct{}, keep map[string]bool) ([]*registryTool, error) {
	serverTools := make([]*registryTool, 0, len(remoteTools))
	localNames := make(map[string]struct{})
	filtered := false
	for _, remote := range remoteTools {
		if strings.TrimSpace(remote.Name) == "" {
			return nil, fmt.Errorf("MCP server %s returned a tool without a name", server.Name)
		}
		if !serverToolAllowed(server, remote.Name) {
			filtered = true
			continue
		}
		built := newRegistryTool(server, remote, conn, options)
		tool := &built
		if existing, ok := registry.Get(tool.Name()); ok && !keep[tool.Name()] {
			return nil, fmt.Errorf("MCP tool %s from %s conflicts with existing tool %s", remote.Name, server.Name, existing.Name())
		}
		if _, ok := stagedNames[tool.Name()]; ok {
			return nil, fmt.Errorf("MCP tool %s from %s conflicts with another MCP tool named %s", remote.Name, server.Name, tool.Name())
		}
		if _, ok := localNames[tool.Name()]; ok {
			return nil, fmt.Errorf("MCP tool %s from %s conflicts with another tool from the same server", remote.Name, server.Name)
		}
		localNames[tool.Name()] = struct{}{}
		serverTools = append(serverTools, tool)
	}
	if len(serverTools) == 0 && filtered {
		return nil, fmt.Errorf("MCP server %s has no tools left after enabled_tools/disabled_tools filtering", server.Name)
	}
	return serverTools, nil
}

// serverToolAllowed reports whether a remote tool passes the server's
// enabled_tools allow-list (empty means all) and disabled_tools deny-list
// (deny wins).
func serverToolAllowed(server Server, remoteName string) bool {
	for _, denied := range server.DisabledTools {
		if denied == remoteName {
			return false
		}
	}
	if len(server.EnabledTools) == 0 {
		return true
	}
	for _, allowed := range server.EnabledTools {
		if allowed == remoteName {
			return true
		}
	}
	return false
}

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.once.Do(func() {
		runtime.refreshMu.Lock()
		runtime.closed = true
		runtime.refreshMu.Unlock()
		for _, client := range runtime.clients {
			if err := client.Close(); err != nil && runtime.err == nil {
				runtime.err = err
			}
		}
		// Release the kept servers' connect contexts AFTER closing the clients: a
		// stdio subprocess is already terminated by Close, so cancelling is then a
		// no-op; it frees the context (and any tied subprocess) either way.
		for _, cancel := range runtime.cancels {
			cancel()
		}
		// Release the runtime lifetime last: any reconnect spawned from it is
		// already closed above, so this only frees the context itself.
		if runtime.lifeCancel != nil {
			runtime.lifeCancel()
		}
	})
	return runtime.err
}

// CatalogChanges returns the applied catalog refreshes, oldest first, so cache
// accounting can record "ferramentas mudaram" as the invalidation reason.
func (runtime *Runtime) CatalogChanges() []CatalogChange {
	if runtime == nil {
		return nil
	}
	runtime.refreshMu.Lock()
	defer runtime.refreshMu.Unlock()
	return append([]CatalogChange{}, runtime.changes...)
}

// RefreshPendingTools re-lists every server whose client observed a
// tools/list_changed notification and applies the new catalog. It is called
// by the turn driver BETWEEN turns — never from a tool Run — so the catalog
// only ever changes between turns, never mid-request. It reports whether
// anything changed. Listing happens without holding the catalog lock, bounded
// per server by the connect timeout, so one hung server cannot freeze the
// others; only the catalog swap takes the lock. A failed re-list re-arms the
// signal instead of mutating anything.
func (runtime *Runtime) RefreshPendingTools(ctx context.Context) (bool, error) {
	if runtime == nil || runtime.registry == nil {
		return false, nil
	}
	type pendingRelist struct {
		server Server
		conn   *mcpServerConn
	}
	var pending []pendingRelist
	runtime.refreshMu.Lock()
	if runtime.closed {
		runtime.refreshMu.Unlock()
		return false, nil
	}
	for _, server := range runtime.servers {
		conn, ok := runtime.liveClients[server.Name]
		if !ok || conn == nil {
			continue
		}
		select {
		case <-conn.ToolsStale():
			pending = append(pending, pendingRelist{server: server, conn: conn})
		default:
		}
	}
	runtime.refreshMu.Unlock()
	changed := false
	for _, item := range pending {
		updated, err := runtime.relistServer(ctx, item.server, item.conn)
		if err != nil {
			item.conn.markToolsStale()
			continue
		}
		changed = changed || updated
	}
	return changed, nil
}

// relistServer re-lists one server against its shared connection, reconnecting
// once when the client is dead, and applies the new catalog under the lock.
// I/O runs without holding refreshMu; only the catalog swap takes it.
func (runtime *Runtime) relistServer(ctx context.Context, server Server, conn *mcpServerConn) (bool, error) {
	listCtx, cancel := context.WithTimeout(ctx, connectTimeout())
	remote, err := conn.ListTools(listCtx)
	cancel()
	if err != nil {
		// A live server that answers slowly (or not at all) is not a dead
		// transport: leave its process alone and retry on the next
		// notification instead of restarting it on every refresh.
		if conn.reconnect == nil || !redialable(ctx, err) {
			return false, err
		}
		fresh, freshErr := conn.reconnect(context.Background())
		if freshErr != nil {
			return false, freshErr
		}
		conn.swap(fresh)
		listCtx, cancel = context.WithTimeout(ctx, connectTimeout())
		remote, err = conn.ListTools(listCtx)
		cancel()
		if err != nil {
			return false, err
		}
	}
	runtime.refreshMu.Lock()
	defer runtime.refreshMu.Unlock()
	return runtime.applyRelistLocked(server, remote, conn)
}

// adoptRelistLocked swaps in a freshly connected client (Reconnect) and
// applies its tool list. Callers hold refreshMu. It is the single writer of
// the live side of the membership machine: a disabled server never adopts,
// so even a caller that dialed first cannot publish its tools.
func (runtime *Runtime) adoptRelistLocked(server Server, fresh ToolClient, remote []RemoteTool) error {
	if runtime.disabled[server.Name] {
		return disabledServerError(server.Name)
	}
	conn, ok := runtime.liveClients[server.Name]
	if !ok || conn == nil {
		conn = newMCPServerConn(server, fresh)
		conn.reconnect = func(context.Context) (ToolClient, error) {
			return runtime.factory(runtime.lifeCtx, server)
		}
		runtime.liveClients[server.Name] = conn
		runtime.clients = append(runtime.clients, conn)
	} else {
		conn.swap(fresh)
	}
	_, err := runtime.applyRelistLocked(server, remote, conn)
	return err
}

// applyRelistLocked applies a tool list to the registry: new tools register,
// vanished tools unregister, conflicts keep the old catalog. It returns
// whether the catalog changed. Callers hold refreshMu; callers perform all
// I/O before taking it.
func (runtime *Runtime) applyRelistLocked(server Server, remote []RemoteTool, conn *mcpServerConn) (bool, error) {
	keep := make(map[string]bool, len(runtime.serverTools[server.Name]))
	for _, name := range runtime.serverTools[server.Name] {
		keep[name] = true
	}
	current := make(map[string]struct{})
	for _, tool := range runtime.registry.All() {
		if !keep[tool.Name()] {
			current[tool.Name()] = struct{}{}
		}
	}
	rebuilt, err := buildServerTools(runtime.registry, server, remote, conn, runtime.options, current, keep)
	if err != nil {
		return false, err
	}
	old := make(map[string]bool, len(runtime.serverTools[server.Name]))
	for _, name := range runtime.serverTools[server.Name] {
		old[name] = true
	}
	next := make([]string, 0, len(rebuilt))
	for _, tool := range rebuilt {
		next = append(next, tool.Name())
		delete(old, tool.Name())
	}
	removed := make([]string, 0, len(old))
	for name := range old {
		removed = append(removed, name)
		runtime.registry.Unregister(name)
	}
	added := make([]string, 0)
	registered := make([]tools.Tool, 0, len(rebuilt))
	for _, tool := range rebuilt {
		registered = append(registered, tool)
		if !keep[tool.Name()] {
			added = append(added, tool.Name())
		}
	}
	runtime.registry.RegisterBatch(registered)
	runtime.serverTools[server.Name] = next
	changed := len(added) > 0 || len(removed) > 0
	if changed {
		runtime.changes = append(runtime.changes, CatalogChange{
			Server:  server.Name,
			At:      time.Now(),
			Added:   added,
			Removed: removed,
			Reason:  catalogChangedReason,
		})
	}
	return changed, nil
}

type registryTool struct {
	name       string
	server     Server
	remote     RemoteTool
	conn       *mcpServerConn
	parameters tools.Schema
	safety     tools.Safety
}

// mcpServerConn is the single live connection of one MCP server, shared by
// all its tools. A reconnect swaps the client once and every tool — and the
// runtime bookkeeping — observes the same transport. It implements ToolClient
// by delegating to the current client, so registries and runtimes hold the
// holder, never a bare client that a redial could leave behind.
type mcpServerConn struct {
	mu     sync.Mutex
	server Server
	client ToolClient
	// reconnect dials a fresh transport. It must use the runtime lifetime
	// context (captured at wiring time), never a turn context, so a revived
	// process outlives the turn that revived it.
	reconnect func(context.Context) (ToolClient, error)
	// redialMu serializes redials: a single redial runs per server at a time,
	// so two calls failing together cannot start two processes.
	redialMu sync.Mutex
	// dead latches when a call fails on a broken transport after its request
	// may already have reached the server: that error goes to the model
	// without a resend, and the next use heals (redials) before sending.
	dead bool
}

func newMCPServerConn(server Server, client ToolClient) *mcpServerConn {
	return &mcpServerConn{server: server, client: client}
}

// call runs one tool call against the current client.
func (conn *mcpServerConn) call(ctx context.Context, name string, args map[string]any) (CallToolResult, error) {
	conn.mu.Lock()
	client := conn.client
	conn.mu.Unlock()
	if client == nil {
		return CallToolResult{}, fmt.Errorf("MCP server %s has no live connection", conn.server.Name)
	}
	return client.CallTool(ctx, name, args)
}

// swap replaces the transport, closing the dead one. The holder keeps its
// identity, so every tool and the runtime bookkeeping observe the revived
// client without further updates. A fresh transport is alive by definition.
func (conn *mcpServerConn) swap(client ToolClient) {
	conn.mu.Lock()
	old := conn.client
	conn.client = client
	conn.dead = false
	conn.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// isDead reports whether the previous call left the transport for dead: the
// next use heals (redials) before sending anything on it.
func (conn *mcpServerConn) isDead() bool {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.dead
}

// markDeadIfCurrent latches the transport for dead only if it is still the
// client that failed: a concurrent redial (or the /mcp panel) already healed
// the server, so the fresh client must not be poisoned by a stale failure.
func (conn *mcpServerConn) markDeadIfCurrent(failed ToolClient) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	if conn.client == failed {
		conn.dead = true
	}
}

// redialIfCurrent swaps in a fresh transport only if the current one is
// still the client that failed: a concurrent redial (another call, or the
// /mcp panel) already healed the server, so this caller must NOT redial
// again — and must not re-send a call another generation may be serving.
// At most one redial runs per server at a time. A caller that gave up
// (ctx done) never waits on a spawn; a spawn that finishes late is closed
// at once so no process leaks. It reports whether this call performed the
// swap.
func (conn *mcpServerConn) redialIfCurrent(ctx context.Context, failed ToolClient) (bool, error) {
	conn.redialMu.Lock()
	defer conn.redialMu.Unlock()
	if cur := conn.current(); cur != failed {
		return false, nil
	}
	type outcome struct {
		client ToolClient
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		fresh, err := conn.reconnect(context.Background())
		done <- outcome{fresh, err}
	}()
	select {
	case out := <-done:
		if out.err != nil {
			return false, out.err
		}
		if out.client == nil {
			return false, fmt.Errorf("MCP server %s reconnect returned no client", conn.server.Name)
		}
		conn.swap(out.client)
		return true, nil
	case <-ctx.Done():
		go func() {
			if out := <-done; out.err == nil && out.client != nil {
				_ = out.client.Close()
			}
		}()
		return false, ctx.Err()
	}
}

func (conn *mcpServerConn) current() ToolClient {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.client
}

func (conn *mcpServerConn) ListTools(ctx context.Context) ([]RemoteTool, error) {
	client := conn.current()
	if client == nil {
		return nil, fmt.Errorf("MCP server %s has no live connection", conn.server.Name)
	}
	return client.ListTools(ctx)
}

func (conn *mcpServerConn) CallTool(ctx context.Context, name string, args map[string]any) (CallToolResult, error) {
	return conn.call(ctx, name, args)
}

func (conn *mcpServerConn) Close() error {
	client := conn.current()
	if client == nil {
		return nil
	}
	return client.Close()
}

// ToolsStale returns the current client's list-changed signal.
func (conn *mcpServerConn) ToolsStale() <-chan struct{} {
	client := conn.current()
	if notifier, ok := client.(staleNotifier); ok {
		return notifier.ToolsStale()
	}
	return nil
}

func (conn *mcpServerConn) markToolsStale() {
	client := conn.current()
	if notifier, ok := client.(staleNotifier); ok {
		notifier.markToolsStale()
	}
}

func newRegistryTool(server Server, remote RemoteTool, conn *mcpServerConn, options RegisterOptions) registryTool {
	remote.Name = strings.TrimSpace(remote.Name)
	name := registryToolName(server.Name, remote.Name)
	permission := tools.PermissionPrompt
	if isPersistentlyApproved(options.PermissionStore, server, remote.Name, defaultAutonomy(options.Autonomy)) {
		permission = tools.PermissionAllow
	}
	tool := registryTool{
		name:       name,
		server:     server,
		remote:     remote,
		conn:       conn,
		parameters: SchemaFromMCP(remote.InputSchema),
		safety: tools.Safety{
			SideEffect:      tools.SideEffectNetwork,
			Permission:      permission,
			Reason:          fmt.Sprintf("MCP tool %s/%s runs through the configured %s server.", server.Name, remote.Name, server.Type),
			AdvertiseInAuto: options.AdvertiseInAuto,
		},
	}
	return tool
}

// callRemote runs one tool call against the shared connection.
func (tool *registryTool) callRemote(ctx context.Context, args map[string]any) (CallToolResult, error) {
	if tool.conn == nil {
		return CallToolResult{}, fmt.Errorf("MCP server %s has no live connection", tool.server.Name)
	}
	return tool.conn.call(ctx, tool.remote.Name, args)
}

func (tool registryTool) Name() string {
	return tool.name
}

func (tool registryTool) Description() string {
	if strings.TrimSpace(tool.remote.Description) != "" {
		return tool.remote.Description
	}
	return fmt.Sprintf("Call MCP tool %s/%s", tool.server.Name, tool.remote.Name)
}

func (tool registryTool) Parameters() tools.Schema {
	return tool.parameters
}

func (tool registryTool) Safety() tools.Safety {
	return tool.safety
}

// Deferred marks every MCP tool as deferred-eligible: when many MCP tools are
// registered the agent loop may withhold their full schema and advertise them
// via tool_search. Built-in tools do not implement this interface and stay
// eager.
func (tool registryTool) Deferred() bool {
	return true
}

// MCPServerName reports the tool's originating MCP server name so the deferred-
// tools reminder labels it correctly, even when the sanitized server token in the
// synthesized tool name contains an underscore (which the name-only parser would
// truncate). It returns the true configured server name, not the sanitized token.
func (tool registryTool) MCPServerName() string {
	return tool.server.Name
}

// errorResult reports a failed call with the expanded secrets traded back
// for ${VAR} references. Successful results are never redacted.
func (tool *registryTool) errorResult(err error) tools.Result {
	output := "Error: MCP tool " + tool.server.Name + "/" + tool.remote.Name + " failed: " + err.Error()
	return tools.Result{
		Status: tools.StatusError,
		Output: tool.server.Redact(output),
		Meta:   tool.meta(),
	}
}

// heal redials a transport the previous call left for dead, single-flight
// with the remote backoff, before anything is sent on it. The earlier
// failure already went to the model; heal only decides whether this call
// runs on a fresh client or fails fast.
func (tool *registryTool) heal(ctx context.Context) error {
	maxAttempts := 1
	if tool.server.Type != ServerTypeStdio {
		maxAttempts = maxConnectAttempts
	}
	// Pin the dead client up front: re-reading current() every lap would
	// redial a server another call already healed.
	dead := tool.conn.current()
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if tool.server.Type != ServerTypeStdio && attempt > 1 && !sleepForReconnect(ctx, attempt-1) {
			return ctx.Err()
		}
		_, err = tool.conn.redialIfCurrent(ctx, dead)
		if err != nil {
			if !isTransientConnectError(ctx, err) {
				return err
			}
			continue
		}
		// Swapped, or another call healed first: either way the holder is
		// fresh (a swap clears the dead latch).
		return nil
	}
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (tool *registryTool) Run(ctx context.Context, args map[string]any) tools.Result {
	// Refreshes never run here: the turn driver calls RefreshPendingTools
	// between turns. A run re-sends a call only when the failure happened
	// BEFORE the request was written — a refused dial, or a process already
	// dead before the send. Anything after the write (an answer, a status,
	// a cut stream, a timeout) goes to the model, and the next use heals the
	// transport instead. The single exception is HTTP 429: the server says it
	// did NOT process the call, so it may be re-sent after Retry-After.
	if tool.conn == nil {
		return tools.Result{
			Status: tools.StatusError,
			Output: "Error: MCP tool " + tool.server.Name + "/" + tool.remote.Name + " failed: no live connection",
			Meta:   tool.meta(),
		}
	}
	// A transport the previous call left for dead heals on next use, before
	// anything is sent on it: the earlier failure already went to the model,
	// and this call must not run on a client known to be broken.
	if tool.conn.isDead() {
		if err := tool.heal(ctx); err != nil {
			return tool.errorResult(err)
		}
	}
	// The call's own budget bounds every 429 wait below: a wait that no
	// longer fits in the time left returns the 429 at once instead.
	deadline := time.Now().Add(toolTimeoutFor(tool.server))
	before := tool.conn.current()
	result, err := tool.callRemote(ctx, args)
	if ctx.Err() != nil && err != nil {
		return tool.errorResult(err)
	}
	maxReconnects := 1
	if tool.server.Type != ServerTypeStdio {
		maxReconnects = maxConnectAttempts
	}
	for attempt := 1; err != nil && attempt <= maxReconnects; attempt++ {
		var httpStatus *MCPHTTPStatusError
		if errors.As(err, &httpStatus) && httpStatus.Status == http.StatusTooManyRequests {
			// The server explicitly did not process the call: wait out
			// Retry-After (or the attempt backoff) and re-send on the same
			// client. The transport is alive, so no redial. A wait past the
			// time left on the call's own budget returns the 429 at once:
			// the model gets it now instead of after accumulated waits that
			// would outlast the timeout anyway.
			delay := connectBackoffFor(attempt)
			if retry, ok := retryAfterDelay(httpStatus.RetryAfter, time.Now()); ok {
				delay = retry
			}
			if delay > time.Until(deadline) {
				break
			}
			if !sleepForStatusRetry(ctx, delay) {
				err = ctx.Err()
				break
			}
			result, err = tool.callRemote(ctx, args)
			continue
		}
		if tool.conn.reconnect == nil || !errors.Is(err, errMCPNotDelivered) {
			// The server may already have the call: never re-send it. When
			// the transport itself looks dead, latch it so the next use
			// heals before sending — unless something else already healed
			// it (a concurrent redial or the /mcp panel).
			var answered *MCPHTTPStatusError
			if !errors.As(err, &answered) && isDeadTransportError(err) {
				tool.conn.markDeadIfCurrent(before)
			}
			break
		}
		if tool.server.Type != ServerTypeStdio && attempt > 1 && !sleepForReconnect(ctx, attempt-1) {
			err = ctx.Err()
			break
		}
		_, reconnectErr := tool.conn.redialIfCurrent(ctx, before)
		if reconnectErr != nil {
			err = reconnectErr
			if !isTransientConnectError(ctx, reconnectErr) {
				break
			}
			continue
		}
		// Swapped here or by a concurrent caller: either way the call never
		// reached the dead client, so it goes to whichever client is current.
		before = tool.conn.current()
		result, err = tool.callRemote(ctx, args)
	}
	if err != nil {
		return tool.errorResult(err)
	}
	status := tools.StatusOK
	if result.IsError {
		status = tools.StatusError
	}
	output := TextContent(result.Content)
	// Say what was thrown away. Without this an image-only result reads as
	// "(empty MCP tool result)", the model concludes the call produced nothing
	// and retries, and the user never learns an image came back (#823). The note
	// is appended only when something was actually dropped, so a text-only
	// result is byte-for-byte what it was before.
	//
	// It says retrying cannot RECOVER the payload rather than that a retry
	// returns the same thing. Each retry is a fresh call, so the server may well
	// answer differently; what cannot change is that QG still has nowhere to
	// put a non-text block. Claiming the response would be identical would be a
	// promise this code is in no position to make.
	if dropped := DroppedContentSummary(result.Content); dropped != "" {
		note := "[qg] this server also returned " + dropped + ", which QG cannot forward yet. Retrying cannot recover this payload."
		if output == "" {
			note = "[qg] this server returned " + dropped + ", which QG cannot forward yet. Retrying cannot recover this payload."
		}
		output = strings.TrimSpace(output + "\n\n" + note)
	}
	if output == "" {
		output = "(empty MCP tool result)"
	}
	return tools.Result{
		Status: status,
		Output: output,
		Meta:   tool.meta(),
	}
}

func (tool registryTool) meta() map[string]string {
	return map[string]string{
		"mcp.server":   tool.server.Name,
		"mcp.tool":     tool.remote.Name,
		"mcp.identity": tool.server.Identity,
	}
}

func registryToolName(serverName string, toolName string) string {
	serverPart := sanitizeToolNamePart(serverName)
	toolPart := sanitizeToolNamePart(toolName)
	if toolPart == "" {
		toolPart = "tool"
	}
	return "mcp_" + serverPart + "_" + toolPart
}

func sanitizeToolNamePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	value = unsafeToolNameChars.ReplaceAllString(value, "_")
	value = strings.Trim(value, "_")
	if value == "" {
		return "server"
	}
	return value
}

func isPersistentlyApproved(store *PermissionStore, server Server, toolName string, autonomy PermissionAutonomy) bool {
	if store == nil {
		return false
	}
	approved, err := store.IsToolPersistentlyApproved(CheckToolInput{
		ServerName:        server.Name,
		ServerIdentity:    server.Identity,
		ToolName:          toolName,
		RequestedAutonomy: autonomy,
	})
	return err == nil && approved
}
