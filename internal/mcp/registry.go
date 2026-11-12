package mcp

import (
	"context"
	"fmt"
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
	registry    *tools.Registry
	options     RegisterOptions
	servers     []Server
	factory     func(context.Context, Server) (ToolClient, error)
	disabled    map[string]bool
	serverTools map[string][]string
	serverIDs   map[string][]*registryTool
	liveClients map[string]ToolClient
	changes     []CatalogChange
	refreshMu   sync.Mutex
	once        sync.Once
	err         error
}

// Skipped returns the servers that were skipped during registration (unreachable
// or invalid), so the caller can warn the user without failing the launch.
func (runtime *Runtime) Skipped() []SkippedServer {
	if runtime == nil {
		return nil
	}
	return runtime.skipped
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
		serverIDs:   make(map[string][]*registryTool),
		liveClients: make(map[string]ToolClient),
		disabled:    make(map[string]bool),
	}
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
		expanded, _ := servers[index].withExpandedEnv()
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
		client ToolClient
		remote []RemoteTool
		cancel context.CancelFunc
		err    error
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
				client, remote, err := connectWithRetry(serverCtx, factory, server, timeout)
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
				results[index] = connectResult{err: fmt.Errorf("connect timed out after %s", timeout)}
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
	for index, server := range servers {
		res := results[index]
		if res.err != nil {
			runtime.skipped = append(runtime.skipped, SkippedServer{Name: server.Name, Err: res.err, UnconfiguredDefault: server.UnconfiguredDefault})
			continue
		}
		serverTools, validateErr := buildServerTools(registry, server, res.remote, res.client, options, factory, stagedNames, nil)
		if validateErr != nil {
			if res.cancel != nil {
				res.cancel()
			}
			_ = res.client.Close()
			runtime.skipped = append(runtime.skipped, SkippedServer{Name: server.Name, Err: validateErr, UnconfiguredDefault: server.UnconfiguredDefault})
			continue
		}
		runtime.clients = append(runtime.clients, res.client)
		if res.cancel != nil {
			runtime.cancels = append(runtime.cancels, res.cancel)
		}
		runtime.liveClients[server.Name] = res.client
		for _, tool := range serverTools {
			tool.refreshCheck = runtime.refreshIfStale
			stagedNames[tool.Name()] = struct{}{}
			staged = append(staged, tool)
			runtime.serverIDs[server.Name] = append(runtime.serverIDs[server.Name], tool)
			runtime.serverTools[server.Name] = append(runtime.serverTools[server.Name], tool.Name())
		}
	}
	registered := make([]tools.Tool, 0, len(staged))
	for index := range staged {
		registered = append(registered, staged[index])
	}
	registry.RegisterBatch(registered)
	return runtime, nil
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
func buildServerTools(registry *tools.Registry, server Server, remoteTools []RemoteTool, client ToolClient, options RegisterOptions, factory func(context.Context, Server) (ToolClient, error), stagedNames map[string]struct{}, keep map[string]bool) ([]*registryTool, error) {
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
		built := newRegistryTool(server, remote, client, options)
		if factory != nil {
			built.reconnect = func(ctx context.Context) (ToolClient, error) {
				return factory(ctx, server)
			}
		}
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
// tools/list_changed notification and applies the new catalog. It runs between
// turns — from the tool Run preamble, during turn dispatch after the current
// turn's partition was computed — so the catalog only ever changes between
// turns, never mid-request. It reports whether anything changed. A failed
// re-list re-arms the signal instead of mutating anything.
func (runtime *Runtime) RefreshPendingTools(ctx context.Context) (bool, error) {
	if runtime == nil || runtime.registry == nil {
		return false, nil
	}
	runtime.refreshMu.Lock()
	defer runtime.refreshMu.Unlock()
	changed := false
	for _, server := range runtime.servers {
		client, ok := runtime.liveClients[server.Name]
		if !ok {
			continue
		}
		notifier, ok := client.(staleNotifier)
		if !ok {
			continue
		}
		select {
		case <-notifier.ToolsStale():
		default:
			continue
		}
		updated, err := runtime.relistServerLocked(ctx, server, notifier)
		if err != nil {
			notifier.markToolsStale()
			continue
		}
		changed = changed || updated
	}
	return changed, ctx.Err()
}

// refreshIfStale is the Run-preamble hook: apply pending list_changed
// refreshes so the next turn sees the new catalog.
func (runtime *Runtime) refreshIfStale(ctx context.Context) {
	_, _ = runtime.RefreshPendingTools(ctx)
}

// relistServerLocked re-lists one server against its live client, reconnecting
// once when the client is dead, and applies the new catalog. It reports
// whether the catalog changed. Callers hold refreshMu.
func (runtime *Runtime) relistServerLocked(ctx context.Context, server Server, notifier staleNotifier) (bool, error) {
	return runtime.applyRelistLocked(ctx, server, nil, notifier)
}

// adoptRelistLocked swaps in a freshly connected client and applies its tool
// list. Callers hold refreshMu.
func (runtime *Runtime) adoptRelistLocked(ctx context.Context, server Server, fresh ToolClient, remote []RemoteTool) error {
	client, ok := runtime.liveClients[server.Name]
	if ok && client != nil {
		_ = client.Close()
	}
	client = fresh
	runtime.liveClients[server.Name] = fresh
	runtime.clients = append(runtime.clients, fresh)
	notifier, _ := fresh.(staleNotifier)
	_, err := runtime.applyRelistLocked(ctx, server, remote, notifier)
	return err
}

// applyRelistLocked applies a tool list to the registry: new tools register,
// vanished tools unregister, conflicts keep the old catalog. A nil remote
// re-lists from the live client first. It returns whether the catalog changed.
// Callers hold refreshMu.
func (runtime *Runtime) applyRelistLocked(ctx context.Context, server Server, remote []RemoteTool, notifier staleNotifier) (bool, error) {
	if remote == nil {
		client, ok := runtime.liveClients[server.Name]
		if !ok {
			return false, fmt.Errorf("MCP server %s has no live client", server.Name)
		}
		listCtx, cancel := context.WithTimeout(ctx, connectTimeout())
		listed, err := client.ListTools(listCtx)
		cancel()
		if err != nil && runtime.factory != nil {
			fresh, _, freshErr := connectWithRetry(ctx, runtime.factory, server, connectTimeout())
			if freshErr != nil {
				return false, freshErr
			}
			_ = client.Close()
			client = fresh
			runtime.liveClients[server.Name] = fresh
			runtime.clients = append(runtime.clients, fresh)
			if freshNotifier, ok := fresh.(staleNotifier); ok {
				notifier = freshNotifier
			}
			listCtx, cancel = context.WithTimeout(ctx, connectTimeout())
			listed, err = client.ListTools(listCtx)
			cancel()
		}
		if err != nil {
			return false, err
		}
		remote = listed
	}
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
	client := runtime.liveClients[server.Name]
	rebuilt, err := buildServerTools(runtime.registry, server, remote, client, runtime.options, runtime.factory, current, keep)
	if err != nil {
		return false, err
	}
	old := make(map[string]bool, len(runtime.serverTools[server.Name]))
	for _, name := range runtime.serverTools[server.Name] {
		old[name] = true
	}
	next := make([]string, 0, len(rebuilt))
	for _, tool := range rebuilt {
		tool.refreshCheck = runtime.refreshIfStale
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
	runtime.serverIDs[server.Name] = rebuilt
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
	name      string
	server    Server
	remote    RemoteTool
	client    ToolClient
	clientMu  *sync.Mutex
	reconnect func(context.Context) (ToolClient, error)
	// refreshCheck re-lists stale servers. It runs at the start of every Run —
	// during turn dispatch, after the current turn's partition was computed —
	// so any catalog change lands in the NEXT turn, never mid-request.
	refreshCheck func(context.Context)
	parameters   tools.Schema
	safety       tools.Safety
}

func newRegistryTool(server Server, remote RemoteTool, client ToolClient, options RegisterOptions) registryTool {
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
		client:     client,
		clientMu:   &sync.Mutex{},
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

// callRemote runs one tool call against the current client.
func (tool *registryTool) callRemote(ctx context.Context, args map[string]any) (CallToolResult, error) {
	if tool.clientMu != nil {
		tool.clientMu.Lock()
		defer tool.clientMu.Unlock()
	}
	return tool.client.CallTool(ctx, tool.remote.Name, args)
}

// swapClient replaces the transport after a redial, closing the dead one.
func (tool *registryTool) swapClient(client ToolClient) {
	if tool.clientMu != nil {
		tool.clientMu.Lock()
		defer tool.clientMu.Unlock()
	}
	old := tool.client
	tool.client = client
	if old != nil {
		_ = old.Close()
	}
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

func (tool *registryTool) Run(ctx context.Context, args map[string]any) tools.Result {
	if tool.refreshCheck != nil {
		tool.refreshCheck(ctx)
	}
	result, err := tool.callRemote(ctx, args)
	// A dead transport restarts on next use (a fresh initialize) and the call
	// is retried: once immediately for a stdio process, with exponential
	// backoff up to 5 times for a remote server. Timeouts, caller
	// cancellation, and JSON-RPC error answers (the server is alive and said
	// no) never reconnect — retrying a hung or refusing server would only
	// burn the turn.
	maxReconnects := 1
	if tool.server.Type != ServerTypeStdio {
		maxReconnects = maxConnectAttempts
	}
	for attempt := 1; err != nil && tool.reconnect != nil && redialable(ctx, err) && attempt <= maxReconnects; attempt++ {
		if tool.server.Type != ServerTypeStdio && !sleepForReconnect(ctx, attempt) {
			err = ctx.Err()
			break
		}
		revived, reconnectErr := tool.reconnect(ctx)
		if reconnectErr != nil {
			err = reconnectErr
			if !isTransientConnectError(ctx, reconnectErr) {
				break
			}
			continue
		}
		tool.swapClient(revived)
		result, err = tool.callRemote(ctx, args)
	}
	if err != nil {
		return tools.Result{
			Status: tools.StatusError,
			Output: "Error: MCP tool " + tool.server.Name + "/" + tool.remote.Name + " failed: " + err.Error(),
			Meta:   tool.meta(),
		}
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
