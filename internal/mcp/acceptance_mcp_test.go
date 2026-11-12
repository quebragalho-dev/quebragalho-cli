package mcp

// MCP acceptance tests, written from the behavior users and servers observe,
// not from an implementation.

import (
	"context"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// ---------------------------------------------------------------------------
// Tool timeout: MCP_TOOL_TIMEOUT (ms) and per-server `timeout` free the turn
// with a clear error naming tool and server, on all three transports.
// MCP_TIMEOUT bounds the handshake.
// ---------------------------------------------------------------------------

const acceptanceTimeoutBound = 5 * time.Second

func assertToolTimedOutForModel(t *testing.T, result tools.Result, returned bool, server string, tool string) {
	t.Helper()
	if !returned {
		t.Fatalf("tool call still blocked after %s: the turn never got a timeout back", acceptanceTimeoutBound)
	}
	if result.Status != tools.StatusError {
		t.Fatalf("status = %q, want error for a timed-out tool; output=%q", result.Status, result.Output)
	}
	if !strings.Contains(result.Output, tool) || !strings.Contains(result.Output, server) {
		t.Fatalf("timeout error %q must name tool %q and server %q for the model", result.Output, tool, server)
	}
	if !containsAnyFold(result.Output, "passou de", "timed out", "timeout", "tempo limite", "excedeu") {
		t.Fatalf("timeout error %q does not say the tool ran out of time", result.Output)
	}
}

func TestAcceptanceMCPToolTimeoutEnvHTTP(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TOOL_TIMEOUT", "300")
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method == "tools/call" {
			s.block(r)
			return true
		}
		return false
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"slowhttp":{"type":"http","url":"`+server.URL+`"}}`))
	result, returned := acceptanceRun(t, registry, "mcp_slowhttp_lookup", acceptanceTimeoutBound)
	assertToolTimedOutForModel(t, result, returned, "slowhttp", "lookup")
}

func TestAcceptanceMCPToolTimeoutEnvSSE(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TOOL_TIMEOUT", "300")
	server := newAcceptanceSSEServer(t, true)
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"slowsse":{"type":"sse","url":"`+server.URL+`/sse"}}`))
	result, returned := acceptanceRun(t, registry, "mcp_slowsse_lookup", acceptanceTimeoutBound)
	assertToolTimedOutForModel(t, result, returned, "slowsse", "lookup")
}

func TestAcceptanceMCPToolTimeoutEnvStdio(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TOOL_TIMEOUT", "300")
	helper := newAcceptanceStdio(t, "hang")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"slowstdio":`+helper.configJSON("")+`}`))
	result, returned := acceptanceRun(t, registry, "mcp_slowstdio_lookup", acceptanceTimeoutBound)
	assertToolTimedOutForModel(t, result, returned, "slowstdio", "lookup")
}

// Per-server `timeout` is read from config.json in milliseconds, the same
// unit as MCP_TOOL_TIMEOUT. The env var is unset, so only the per-server value
// can free the turn before the 60s default.
func TestAcceptanceMCPToolTimeoutPerServerConfig(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method == "tools/call" {
			s.block(r)
			return true
		}
		return false
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"perserver":{"type":"http","url":"`+server.URL+`","timeout":300}}`))
	result, returned := acceptanceRun(t, registry, "mcp_perserver_lookup", acceptanceTimeoutBound)
	assertToolTimedOutForModel(t, result, returned, "perserver", "lookup")
}

func TestAcceptanceMCPHandshakeTimeoutEnv(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "300")
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method == "initialize" {
			s.block(r)
			return true
		}
		return false
	})
	done := make(chan error, 1)
	go func() {
		client, err := Connect(context.Background(), Server{Name: "slowinit", Type: ServerTypeHTTP, URL: server.URL})
		if err == nil {
			_ = client.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Connect() succeeded against a server that never answers initialize")
		}
	case <-time.After(acceptanceTimeoutBound):
		t.Fatalf("handshake still blocked after %s with MCP_TIMEOUT=300ms", acceptanceTimeoutBound)
	}
}

// ---------------------------------------------------------------------------
// Reconnection: remote servers get up to 5 attempts with exponential backoff
// starting at 1s; stdio restarts the process on next use.
// ---------------------------------------------------------------------------

// The server "restarts": after the drop every request is cut until the client
// performs a fresh initialize, and the first two initialize attempts are cut
// too. Recovery therefore requires a real reconnect with at least 3 attempts.
func TestAcceptanceMCPRemoteReconnectsWithExponentialBackoff(t *testing.T) {
	if testing.Short() {
		t.Skip("backoff starts at 1s")
	}
	acceptanceHermeticEnv(t)
	var down atomic.Bool
	var mu sync.Mutex
	var attempts []time.Time
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if !down.Load() {
			return false
		}
		if message.Method == "initialize" {
			mu.Lock()
			attempts = append(attempts, time.Now())
			n := len(attempts)
			mu.Unlock()
			if n <= 2 {
				hijackClose(w)
				return true
			}
			down.Store(false)
			return false
		}
		hijackClose(w)
		return true
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"flaky":{"type":"http","url":"`+server.URL+`"}}`))
	if result, ok := acceptanceRun(t, registry, "mcp_flaky_lookup", acceptanceTimeoutBound); !ok || result.Status != tools.StatusOK {
		t.Fatalf("baseline call failed before the drop: %#v", result)
	}

	down.Store(true)
	dropped := time.Now()
	recovered := false
	for time.Since(dropped) < 25*time.Second {
		result, ok := acceptanceRun(t, registry, "mcp_flaky_lookup", 20*time.Second)
		if ok && result.Status == tools.StatusOK {
			recovered = true
			break
		}
		mu.Lock()
		n := len(attempts)
		mu.Unlock()
		if n == 0 && time.Since(dropped) > 6*time.Second {
			t.Fatal("no reconnect attempt (initialize) within 6s of the connection drop")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !recovered {
		t.Fatal("server came back but the tool never worked again in the same session")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) < 3 || len(attempts) > 5 {
		t.Fatalf("reconnect attempts = %d, want 3 (2 cut + 1 ok) and never more than 5", len(attempts))
	}
	if gap := attempts[1].Sub(attempts[0]); gap < 900*time.Millisecond {
		t.Fatalf("first backoff = %v, want >= 1s", gap)
	}
	if gap := attempts[2].Sub(attempts[1]); gap < 1800*time.Millisecond {
		t.Fatalf("second backoff = %v, want >= 2s (exponential)", gap)
	}
}

// The reconnect may start in the call that saw the drop or at the next use,
// since a call the server may have received is not sent again.
func TestAcceptanceMCPRemoteReconnectGivesUpAfterFiveAttempts(t *testing.T) {
	if testing.Short() {
		t.Skip("backoff 1+2+4+8s")
	}
	acceptanceHermeticEnv(t)
	var down atomic.Bool
	var attempts atomic.Int32
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if !down.Load() {
			return false
		}
		if message.Method == "initialize" {
			attempts.Add(1)
		}
		hijackClose(w)
		return true
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"gone":{"type":"http","url":"`+server.URL+`"}}`))
	if result, ok := acceptanceRun(t, registry, "mcp_gone_lookup", acceptanceTimeoutBound); !ok || result.Status != tools.StatusOK {
		t.Fatalf("baseline call failed before the drop: %#v", result)
	}
	reached := func(want int32, within time.Duration) bool {
		deadline := time.Now().Add(within)
		for attempts.Load() < want {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(50 * time.Millisecond)
		}
		return true
	}

	down.Store(true)
	if _, ok := acceptanceRun(t, registry, "mcp_gone_lookup", 40*time.Second); !ok {
		t.Fatal("the call that saw the drop never returned")
	}
	if !reached(1, 3*time.Second) {
		go registry.RunWithOptions(context.Background(), "mcp_gone_lookup", map[string]any{}, tools.RunOptions{PermissionGranted: true})
		if !reached(1, 6*time.Second) {
			t.Fatal("no reconnect attempt (initialize) within 6s of the next use after the drop")
		}
	}
	reached(5, 40*time.Second)
	// A sixth attempt would follow the fifth by at least 16s.
	time.Sleep(20 * time.Second)
	if got := attempts.Load(); got != 5 {
		t.Fatalf("reconnect attempts = %d, want exactly 5 and then stop", got)
	}
}

func TestAcceptanceMCPStdioRestartsProcessOnNextUse(t *testing.T) {
	acceptanceHermeticEnv(t)
	helper := newAcceptanceStdio(t, "crash-once")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"local":`+helper.configJSON("")+`}`))
	if _, ok := acceptanceRun(t, registry, "mcp_local_lookup", 10*time.Second); !ok {
		t.Fatal("call that crashed the stdio server never returned")
	}
	result, ok := acceptanceRun(t, registry, "mcp_local_lookup", 10*time.Second)
	if !ok || result.Status != tools.StatusOK || !strings.Contains(result.Output, "ok") {
		t.Fatalf("next use after the stdio server died = %#v (returned=%v), want a restarted process answering", result, ok)
	}
	if got := helper.starts(); got != 2 {
		t.Fatalf("stdio process starts = %d, want 2 (original + one restart)", got)
	}
}

// ---------------------------------------------------------------------------
// protocolVersion negotiation: announce the newest version qg implements,
// accept any known version the server picks, refuse (with the version in the
// visible reason) a version newer than qg knows.
// ---------------------------------------------------------------------------

var acceptanceNewerKnownProtocolVersions = []string{"2025-03-26", "2025-06-18", "2025-11-25"}

func TestAcceptanceMCPHTTPAnnouncesNewestKnownProtocolVersion(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newAcceptanceHTTPServer(t, nil) // echoes the client's version
	client, err := Connect(context.Background(), Server{Name: "ver", Type: ServerTypeHTTP, URL: server.URL})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	announced := server.announcedVersion()
	if !slices.Contains(acceptanceNewerKnownProtocolVersions, announced) {
		t.Fatalf("initialize announced protocolVersion %q, want the newest qg implements (one of %v)", announced, acceptanceNewerKnownProtocolVersions)
	}
	// MCP Streamable HTTP, 2025-06-18+: every request after initialize carries
	// the negotiated version in MCP-Protocol-Version.
	if announced >= "2025-06-18" {
		if got := server.protocolHeader("tools/list"); got != announced {
			t.Fatalf("tools/list MCP-Protocol-Version header = %q, want negotiated %q", got, announced)
		}
	}
}

func TestAcceptanceMCPStdioAnnouncesNewestKnownProtocolVersion(t *testing.T) {
	acceptanceHermeticEnv(t)
	helper := newAcceptanceStdio(t, "basic")
	acceptanceRegister(t, acceptanceConfig(t, `{"verstdio":`+helper.configJSON("")+`}`))
	if announced := helper.announced(); !slices.Contains(acceptanceNewerKnownProtocolVersions, announced) {
		t.Fatalf("stdio initialize announced protocolVersion %q, want one of %v", announced, acceptanceNewerKnownProtocolVersions)
	}
}

// A server that answers with the older 2024-11-05 is still accepted.
func TestAcceptanceMCPAcceptsOlderKnownServerVersion(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method == "initialize" {
			writeHTTPRPCResponse(t, w, message.ID, map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}})
			return true
		}
		return false
	})
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"old":{"type":"http","url":"`+server.URL+`"}}`))
	if reason, skipped := acceptanceSkippedReason(runtime, "old"); skipped {
		t.Fatalf("server on 2024-11-05 was skipped: %s", reason)
	}
	if !registryHasTool(registry, "mcp_old_lookup") {
		t.Fatal("server on 2024-11-05 lost its tools")
	}
}

func TestAcceptanceMCPRefusesUnknownNewerServerVersionWithReason(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method == "initialize" {
			writeHTTPRPCResponse(t, w, message.ID, map[string]any{"protocolVersion": "2099-01-01", "capabilities": map[string]any{}})
			return true
		}
		return false
	})
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"future":{"type":"http","url":"`+server.URL+`"}}`))
	reason, skipped := acceptanceSkippedReason(runtime, "future")
	if !skipped {
		t.Fatal("server demanding protocol 2099-01-01 was connected silently; qg must warn and not pretend to speak it")
	}
	if !strings.Contains(reason, "2099-01-01") {
		t.Fatalf("visible reason %q does not name the unsupported protocol version", reason)
	}
}

// ---------------------------------------------------------------------------
// list_changed never mutates the catalog mid-turn.
// ---------------------------------------------------------------------------

func TestAcceptanceMCPListChangedNeverMutatesCatalogMidTurn(t *testing.T) {
	acceptanceHermeticEnv(t)
	helper := newAcceptanceStdio(t, "list-changed")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"live":`+helper.configJSON("")+`}`))
	if result, ok := acceptanceRun(t, registry, "mcp_live_lookup", 10*time.Second); !ok || result.Status != tools.StatusOK {
		t.Fatalf("call that triggers list_changed failed: %#v", result)
	}
	time.Sleep(300 * time.Millisecond)
	if registryHasTool(registry, "mcp_live_lookup_v2") || !registryHasTool(registry, "mcp_live_lookup") {
		t.Fatal("catalog changed in the middle of the turn; list_changed may only apply on the next turn")
	}
}

// ---------------------------------------------------------------------------
// HTTP->SSE fallback only when type is omitted; explicit sse and explicit http
// keep their meaning.
// ---------------------------------------------------------------------------

func TestAcceptanceMCPUntypedURLFallsBackToSSE(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newAcceptanceSSEServer(t, false)
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"auto":{"url":"`+server.URL+`/sse"}}`))
	if reason, skipped := acceptanceSkippedReason(runtime, "auto"); skipped {
		t.Fatalf("untyped legacy SSE server was skipped instead of falling back: %s", reason)
	}
	result, ok := acceptanceRun(t, registry, "mcp_auto_lookup", acceptanceTimeoutBound)
	if !ok || result.Status != tools.StatusOK || !strings.Contains(result.Output, "ok") {
		t.Fatalf("tool over the SSE fallback = %#v", result)
	}
	if !server.streamOpened.Load() {
		t.Fatal("fallback never opened the SSE stream")
	}
	// Permissions and OAuth tokens stay anchored to the configured identity;
	// falling back to SSE must not re-key the server.
	configured, err := NormalizeConfig(acceptanceConfig(t, `{"auto":{"url":"`+server.URL+`/sse"}}`))
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if got := result.Meta["mcp.identity"]; got != configured[0].Identity {
		t.Fatalf("tool identity after SSE fallback = %q, want the configured identity %q", got, configured[0].Identity)
	}
}

// Explicit sse goes straight to the SSE stream, no Streamable POST probe.
func TestAcceptanceMCPExplicitSSEDoesNotProbeHTTP(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newAcceptanceSSEServer(t, false)
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"legacy":{"type":"sse","url":"`+server.URL+`/sse"}}`))
	if !registryHasTool(registry, "mcp_legacy_lookup") {
		t.Fatal("explicit sse server lost its tools")
	}
	if got := server.postsToStream.Load(); got != 0 {
		t.Fatalf("explicit sse server got %d Streamable HTTP probes, want 0", got)
	}
}

// Explicit http never silently degrades to SSE.
func TestAcceptanceMCPExplicitHTTPDoesNotFallBack(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newAcceptanceSSEServer(t, false)
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"strict":{"type":"http","url":"`+server.URL+`/sse"}}`))
	if _, skipped := acceptanceSkippedReason(runtime, "strict"); !skipped {
		t.Fatal("explicit http server connected through SSE; fallback is only for untyped servers")
	}
	if server.streamOpened.Load() {
		t.Fatal("explicit http server opened an SSE stream")
	}
}

// Server identity anchors stored permissions and OAuth tokens, so untyped-URL
// configs keep the identity they always had.
func TestAcceptanceMCPUntypedURLKeepsIdentity(t *testing.T) {
	acceptanceHermeticEnv(t)
	untyped, err := NormalizeConfig(acceptanceConfig(t, `{"same":{"url":"https://mcp.example.test/mcp"}}`))
	if err != nil {
		t.Fatalf("NormalizeConfig(untyped) error = %v", err)
	}
	typed, err := NormalizeConfig(acceptanceConfig(t, `{"same":{"type":"http","url":"https://mcp.example.test/mcp"}}`))
	if err != nil {
		t.Fatalf("NormalizeConfig(http) error = %v", err)
	}
	if untyped[0].Identity != typed[0].Identity {
		t.Fatal("untyped URL server changed identity; users would lose permissions and OAuth tokens")
	}
}

// ---------------------------------------------------------------------------
// Large MCP output: warn above 10k tokens, cap at 25k (MAX_MCP_OUTPUT_TOKENS
// adjusts), spill the excess to a file the model is pointed to, lose nothing.
// Token = 4 bytes, the estimator qg already uses.
// ---------------------------------------------------------------------------

func acceptanceBigOutputRegistry(t *testing.T, payload string) *tools.Registry {
	t.Helper()
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method == "tools/call" {
			writeHTTPRPCResponse(t, w, message.ID, map[string]any{"content": []Content{{Type: "text", Text: payload}}})
			return true
		}
		return false
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"big":{"type":"http","url":"`+server.URL+`"}}`))
	return registry
}

func acceptancePayload(bytes int) string {
	return "A" + strings.Repeat("b", bytes-2) + "Z"
}

func TestAcceptanceMCPOutputAbove10kTokensWarnsWithoutTruncating(t *testing.T) {
	acceptanceHermeticEnv(t)
	payload := acceptancePayload(48_000) // ~12k tokens
	result, ok := acceptanceRun(t, acceptanceBigOutputRegistry(t, payload), "mcp_big_lookup", acceptanceTimeoutBound)
	if !ok || result.Status != tools.StatusOK {
		t.Fatalf("call failed: %#v", result)
	}
	if result.Truncated || !strings.Contains(result.Output, payload) {
		t.Fatal("12k-token output was cut; only output above the 25k cap may be cut")
	}
	rest := strings.Replace(result.Output, payload, "", 1)
	if !containsAnyFold(rest, "token") && strings.TrimSpace(result.Meta["mcp.output_warning"]) == "" {
		t.Fatalf("no warning for output above 10k tokens (looked in output outside the payload %q and meta mcp.output_warning)", rest)
	}
}

func TestAcceptanceMCPOutputDefaultCapIs25kTokens(t *testing.T) {
	acceptanceHermeticEnv(t)
	payload := acceptancePayload(88_000) // ~22k tokens
	result, ok := acceptanceRun(t, acceptanceBigOutputRegistry(t, payload), "mcp_big_lookup", acceptanceTimeoutBound)
	if !ok || result.Status != tools.StatusOK {
		t.Fatalf("call failed: %#v", result)
	}
	if result.Truncated || !strings.Contains(result.Output, payload) {
		t.Fatalf("22k-token output was truncated (%d bytes kept of %d); default MCP cap is 25k tokens", len(result.Output), len(payload))
	}
}

func assertSpilledWithoutLoss(t *testing.T, result tools.Result, payload string, maxBytes int) {
	t.Helper()
	if !result.Truncated {
		t.Fatalf("output of %d bytes was not capped (cap %d bytes)", len(payload), maxBytes)
	}
	if len(result.Output) > maxBytes+1024 {
		t.Fatalf("model-visible output = %d bytes, want <= cap %d (+notice)", len(result.Output), maxBytes)
	}
	path := result.Meta["spill_path"]
	if path == "" || !strings.Contains(result.Output, path) {
		t.Fatalf("model was not given the spill file path (meta spill_path=%q)", path)
	}
	spilled, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read spill %q: %v", path, err)
	}
	if string(spilled) != payload {
		t.Fatalf("spill holds %d bytes, want the full %d-byte output", len(spilled), len(payload))
	}
}

func TestAcceptanceMCPOutputCapFollowsMaxMCPOutputTokens(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MAX_MCP_OUTPUT_TOKENS", "2000")
	payload := acceptancePayload(40_000) // ~10k tokens
	result, ok := acceptanceRun(t, acceptanceBigOutputRegistry(t, payload), "mcp_big_lookup", acceptanceTimeoutBound)
	if !ok {
		t.Fatal("call never returned")
	}
	assertSpilledWithoutLoss(t, result, payload, 2000*4)
}

// Beyond the default 25k cap nothing is lost.
func TestAcceptanceMCPOutputAboveDefaultCapSpillsWithoutLoss(t *testing.T) {
	acceptanceHermeticEnv(t)
	payload := acceptancePayload(120_000) // ~30k tokens
	result, ok := acceptanceRun(t, acceptanceBigOutputRegistry(t, payload), "mcp_big_lookup", acceptanceTimeoutBound)
	if !ok {
		t.Fatal("call never returned")
	}
	assertSpilledWithoutLoss(t, result, payload, 25_000*4)
}

// ---------------------------------------------------------------------------
// ${VAR} and ${VAR:-default} in command/args/env/url/headers, from the qg
// process environment only. Asserted on the wire: what the spawned process and
// the HTTP server actually receive.
// ---------------------------------------------------------------------------

func TestAcceptanceMCPExpandsVarsForStdio(t *testing.T) {
	acceptanceHermeticEnv(t)
	helper := newAcceptanceStdio(t, "basic")
	t.Setenv("QG_ACCEPT_BIN", helper.binary)
	t.Setenv("QG_ACCEPT_TOKEN", "from-process-env")
	_ = os.Unsetenv("QG_ACCEPT_MISSING")
	entry := `{"type":"stdio","command":"${QG_ACCEPT_BIN}",` +
		`"args":["-test.run=^TestAcceptanceStdioHelperProcess$","--","tok=${QG_ACCEPT_TOKEN}"],` +
		`"env":{"` + acceptanceHelperModeEnv + `":"basic","` + acceptanceHelperStateEnv + `":"` + helper.state + `",` +
		`"ACCEPT_A":"${QG_ACCEPT_TOKEN}","ACCEPT_B":"${QG_ACCEPT_MISSING:-fallback-value}"}}`
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"expand":`+entry+`}`))
	if reason, skipped := acceptanceSkippedReason(runtime, "expand"); skipped {
		t.Fatalf("command ${QG_ACCEPT_BIN} was not expanded, server skipped: %s", reason)
	}
	args, env := helper.observed(t)
	if !slices.Contains(args, "tok=from-process-env") {
		t.Fatalf("args = %q, want ${QG_ACCEPT_TOKEN} expanded", args)
	}
	if env["ACCEPT_A"] != "from-process-env" || env["ACCEPT_B"] != "fallback-value" {
		t.Fatalf("env = %#v, want ${VAR} and ${VAR:-default} expanded", env)
	}
}

func TestAcceptanceMCPExpandsVarsForHTTPURLAndHeaders(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("QG_ACCEPT_TOKEN", "from-process-env")
	_ = os.Unsetenv("QG_ACCEPT_MISSING")
	var mu sync.Mutex
	var query, header, fallback string
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method == "initialize" {
			mu.Lock()
			query, header, fallback = r.URL.Query().Get("k"), r.Header.Get("X-Accept-Token"), r.Header.Get("X-Accept-Default")
			mu.Unlock()
		}
		return false
	})
	acceptanceRegister(t, acceptanceConfig(t, `{"remote":{"type":"http","url":"`+server.URL+`/mcp?k=${QG_ACCEPT_TOKEN}",`+
		`"headers":{"X-Accept-Token":"${QG_ACCEPT_TOKEN}","X-Accept-Default":"${QG_ACCEPT_MISSING:-fallback-value}"}}}`))
	mu.Lock()
	defer mu.Unlock()
	if query != "from-process-env" || header != "from-process-env" || fallback != "fallback-value" {
		t.Fatalf("server received query=%q header=%q default=%q, want expansion from the process env", query, header, fallback)
	}
}

// Expansion reads the process env, never sibling config values.
func TestAcceptanceMCPExpansionIgnoresSiblingConfigValues(t *testing.T) {
	acceptanceHermeticEnv(t)
	_ = os.Unsetenv("ACCEPT_A")
	helper := newAcceptanceStdio(t, "basic")
	entry := `{"type":"stdio","command":"` + helper.binary + `","args":["-test.run=^TestAcceptanceStdioHelperProcess$"],` +
		`"env":{"` + acceptanceHelperModeEnv + `":"basic","` + acceptanceHelperStateEnv + `":"` + helper.state + `",` +
		`"ACCEPT_A":"sibling-literal","ACCEPT_C":"${ACCEPT_A}"}}`
	acceptanceRegister(t, acceptanceConfig(t, `{"sibling":`+entry+`}`))
	_, env := helper.observed(t)
	if env["ACCEPT_C"] == "sibling-literal" {
		t.Fatal("${ACCEPT_A} resolved from a sibling config value; only the qg process env may be used")
	}
}

// ---------------------------------------------------------------------------
// enabled_tools / disabled_tools per server.
// ---------------------------------------------------------------------------

func acceptanceThreeToolServer(t *testing.T) *acceptanceHTTPServer {
	t.Helper()
	return newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method == "tools/list" {
			writeHTTPRPCResponse(t, w, message.ID, map[string]any{"tools": []RemoteTool{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}})
			return true
		}
		return false
	})
}

func assertCatalog(t *testing.T, registry *tools.Registry, server string, want map[string]bool) {
	t.Helper()
	for tool, present := range want {
		if got := registryHasTool(registry, "mcp_"+server+"_"+tool); got != present {
			t.Fatalf("tool %s present = %v, want %v", tool, got, present)
		}
	}
}

func TestAcceptanceMCPDisabledToolsAreHidden(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := acceptanceThreeToolServer(t)
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"f":{"type":"http","url":"`+server.URL+`","disabled_tools":["beta"]}}`))
	assertCatalog(t, registry, "f", map[string]bool{"alpha": true, "beta": false, "gamma": true})
}

func TestAcceptanceMCPEnabledToolsIsAnAllowlist(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := acceptanceThreeToolServer(t)
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"f":{"type":"http","url":"`+server.URL+`","enabled_tools":["alpha"]}}`))
	assertCatalog(t, registry, "f", map[string]bool{"alpha": true, "beta": false, "gamma": false})
}

// Codex parity: disabled_tools applies after enabled_tools.
func TestAcceptanceMCPDisabledToolsWinsOverEnabledTools(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := acceptanceThreeToolServer(t)
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"f":{"type":"http","url":"`+server.URL+`","enabled_tools":["alpha","beta"],"disabled_tools":["beta"]}}`))
	assertCatalog(t, registry, "f", map[string]bool{"alpha": true, "beta": false, "gamma": false})
}

// AGENTS.md (redact success and error paths): a secret that reaches the URL
// through ${VAR} must not come back to the model inside a transport error
// (net/http errors echo the full URL).
func TestAcceptanceMCPExpandedSecretNotEchoedToModelOnTransportError(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("QG_ACCEPT_SECRET", "ambient-secret-value")
	var dead atomic.Bool
	server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if dead.Load() {
			hijackClose(w)
			return true
		}
		return false
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"leaky":{"type":"http","url":"`+server.URL+`/mcp?k=${QG_ACCEPT_SECRET}"}}`))
	dead.Store(true)
	result, ok := acceptanceRun(t, registry, "mcp_leaky_lookup", 40*time.Second)
	if !ok {
		t.Fatal("call never returned")
	}
	if strings.Contains(result.Output, "ambient-secret-value") {
		t.Fatalf("model-visible error carries the expanded secret: %q", result.Output)
	}
}
