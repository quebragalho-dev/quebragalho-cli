package mcp

// A tools/call that a server may already have received is never sent again;
// the model gets the failure. A 429 says the server did not process it, so
// that call is sent again after Retry-After, within the attempt limit.
// Esc stops a redial, /mcp never waits for a reconnect, and expanded ${VAR}
// values of 8 or more characters are redacted from error and status text,
// never from a successful result. A server must name its protocolVersion, a
// reason shows a ${VAR} reference as written, a closed runtime starts
// nothing, Skipped can be read while /mcp reconnects, and a deleted approval
// stays deleted.

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func acceptanceStateLines(state, name string) int {
	data, _ := os.ReadFile(filepath.Join(state, name))
	return strings.Count(string(data), "\n")
}

// The server fails the first tools/call after reading it; every later call
// gets "ok".
func TestAcceptanceMCPCallDeliveredToAnHTTPServerIsNeverResent(t *testing.T) {
	failures := []struct {
		name string
		fail func(w http.ResponseWriter, id any)
	}{
		{"500-with-json-rpc-error", func(w http.ResponseWriter, id any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id,
				"error": map[string]any{"code": -32603, "message": "failed after running"}})
		}},
		{"502", func(w http.ResponseWriter, _ any) { w.WriteHeader(http.StatusBadGateway) }},
		{"503", func(w http.ResponseWriter, _ any) { w.WriteHeader(http.StatusServiceUnavailable) }},
		{"connection-cut-after-reading", func(w http.ResponseWriter, _ any) { hijackClose(w) }},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			var calls atomic.Int32
			server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
				if message.Method != "tools/call" || calls.Add(1) > 1 {
					return false
				}
				failure.fail(w, message.ID)
				return true
			})
			registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))

			result, returned := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second)
			if !returned {
				t.Fatal("the failed call never returned")
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("one model call reached the server %d times (answer %s %q), want 1", got, result.Status, result.Output)
			}
			if result.Status == tools.StatusOK {
				t.Fatalf("the call the server failed came back ok: %q", result.Output)
			}
			if next, returned := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second); !returned || next.Status != tools.StatusOK {
				t.Fatalf("next call = %s %q (returned=%v), want ok from the healthy server", next.Status, next.Output, returned)
			}
		})
	}
}

func TestAcceptanceMCPCallRefusedWith429IsResentAfterRetryAfter(t *testing.T) {
	acceptanceHermeticEnv(t)
	var mu sync.Mutex
	var calls []time.Time
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
		if message.Method != "tools/call" {
			return false
		}
		mu.Lock()
		calls = append(calls, time.Now())
		first := len(calls) == 1
		mu.Unlock()
		if !first {
			return false
		}
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))

	result, returned := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second)
	if !returned || result.Status != tools.StatusOK {
		t.Fatalf("call refused once with 429 = %s %q (returned=%v), want it sent again and answered ok", result.Status, result.Output, returned)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("one model call reached the server %d times, want 2 (the refused send and one resend)", len(calls))
	}
	if gap := calls[1].Sub(calls[0]); gap < 900*time.Millisecond {
		t.Fatalf("resent %v after a 429 with Retry-After: 1, want at least 1s", gap.Round(time.Millisecond))
	}
}

func TestAcceptanceMCPCallAlwaysRefusedWith429StopsAtTheAttemptLimit(t *testing.T) {
	acceptanceHermeticEnv(t)
	var calls atomic.Int32
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
		if message.Method != "tools/call" {
			return false
		}
		calls.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))

	result, returned := acceptanceRun(t, registry, "mcp_s_lookup", 60*time.Second)
	if !returned {
		t.Fatalf("a call the server always refuses with 429 never returned (%d sends so far)", calls.Load())
	}
	if result.Status == tools.StatusOK {
		t.Fatalf("a call the server always refuses came back ok: %q", result.Output)
	}
	if got := calls.Load(); got > 6 {
		t.Fatalf("one model call reached the server %d times, want at most 6 (the call and 5 attempts)", got)
	}
}

// The /mcp panel reads Status while a reconnect dials; it never waits for it.
func TestAcceptanceMCPStatusNeverWaitsForAReconnect(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "10000")
	var hang atomic.Bool
	dialing := make(chan struct{}, 1)
	hold := make(chan struct{})
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, _ http.ResponseWriter, r *http.Request) bool {
		if message.Method == "initialize" && hang.Load() {
			select {
			case dialing <- struct{}{}:
			default:
			}
			select {
			case <-hold:
			case <-r.Context().Done():
			}
		}
		return false
	})
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))

	hang.Store(true)
	reconnected := make(chan error, 1)
	go func() { reconnected <- runtime.Reconnect(context.Background(), "s") }()
	select {
	case <-dialing:
	case <-time.After(10 * time.Second):
		t.Fatal("precondition: the reconnect never reached the server")
	}
	statusDone := make(chan struct{})
	started := time.Now()
	go func() {
		defer close(statusDone)
		_ = runtime.Status()
	}()
	var blocked time.Duration
	select {
	case <-statusDone:
	case <-time.After(time.Second):
		blocked = time.Since(started)
	}
	close(hold)
	if err := <-reconnected; err != nil {
		t.Fatalf("precondition: reconnect once the server answered: %v", err)
	}
	<-statusDone
	if blocked > 0 {
		t.Fatalf("Status() was still waiting %v after a reconnect started dialing; /mcp freezes until the dial ends", blocked.Round(time.Millisecond))
	}
}

// acceptanceCallWithEsc runs a tool the way a turn does and presses Esc
// (cancels the turn's context) after esc; it returns how long the call took.
func acceptanceCallWithEsc(t *testing.T, registry *tools.Registry, name string, esc time.Duration) (tools.Result, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(esc, cancel)
	defer timer.Stop()
	started := time.Now()
	done := make(chan tools.Result, 1)
	go func() {
		done <- registry.RunWithOptions(ctx, name, map[string]any{}, tools.RunOptions{PermissionGranted: true})
	}()
	select {
	case result := <-done:
		return result, time.Since(started)
	case <-time.After(60 * time.Second):
		t.Fatalf("%s never returned after Esc", name)
		return tools.Result{}, 0
	}
}

// Esc stops a redial inside a call: the wait between attempts, a dial that
// hangs, and a Retry-After wait. The redial may start in the call that saw
// the failure or at the next use, so the first two cases press Esc on two
// calls; a 429 is sent again inside the same call. Esc comes at 1.5s for the
// wait between attempts so it lands inside a wait of seconds: a wait that
// ignores Esc then overruns the slack.
func TestAcceptanceMCPEscStopsTheRedial(t *testing.T) {
	const slack = time.Second
	for _, redial := range []struct {
		name  string
		calls int
		esc   time.Duration
		hook  func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool
	}{
		{"wait-between-attempts", 2, 1500 * time.Millisecond, func(_ *acceptanceHTTPServer, _ rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
			hijackClose(w)
			return true
		}},
		{"dial-that-hangs", 2, 300 * time.Millisecond, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
			if message.Method == "initialize" {
				s.block(r)
				return true
			}
			hijackClose(w)
			return true
		}},
		{"retry-after-wait", 1, 300 * time.Millisecond, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
			if message.Method != "tools/call" {
				return false
			}
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			return true
		}},
	} {
		t.Run(redial.name, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			t.Setenv("MCP_TIMEOUT", "3000")
			var down atomic.Bool
			var calls atomic.Int32
			server := newAcceptanceHTTPServer(t, func(s *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
				if message.Method == "tools/call" {
					calls.Add(1)
				}
				return down.Load() && redial.hook(s, message, w, r)
			})
			registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
			if result, ok := acceptanceRun(t, registry, "mcp_s_lookup", acceptanceTimeoutBound); !ok || result.Status != tools.StatusOK {
				t.Fatalf("baseline call failed: %s %q", result.Status, result.Output)
			}

			down.Store(true)
			sent := calls.Load()
			for call := 1; call <= redial.calls; call++ {
				result, took := acceptanceCallWithEsc(t, registry, "mcp_s_lookup", redial.esc)
				if took > redial.esc+slack {
					t.Fatalf("call %d took %v with Esc at %v, want it back within %v of Esc", call, took.Round(time.Millisecond), redial.esc, slack)
				}
				if result.Status == tools.StatusOK {
					t.Fatalf("call %d came back ok while the server was down: %q", call, result.Output)
				}
			}
			if redial.name == "retry-after-wait" {
				if got := calls.Load() - sent; got != 1 {
					t.Fatalf("the call refused with 429 reached the server %d times before Esc, want 1 (Esc during the Retry-After wait)", got)
				}
			}
			down.Store(false)
			if result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second); !ok || result.Status != tools.StatusOK {
				t.Fatalf("call after the server came back = %s %q (returned=%v), want ok", result.Status, result.Output, ok)
			}
		})
	}
}

func TestAcceptanceMCPCallDeliveredToAStdioServerIsNeverResent(t *testing.T) {
	acceptanceHermeticEnv(t)
	helper := newAcceptanceStdio(t, "exit-after-reading-the-call")
	helper.args = []string{"-test.run=^TestAcceptanceExitingHelperProcess$"}
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"local":`+helper.configJSON("")+`}`))

	result, returned := acceptanceRun(t, registry, "mcp_local_lookup", 30*time.Second)
	if !returned {
		t.Fatal("the call whose server died never returned")
	}
	if got := acceptanceStateLines(helper.state, "executions"); got != 1 {
		t.Fatalf("one model call reached a server process %d times (answer %s %q), want 1", got, result.Status, result.Output)
	}
	if result.Status == tools.StatusOK {
		t.Fatalf("the call whose server died came back ok: %q", result.Output)
	}
	if next, returned := acceptanceRun(t, registry, "mcp_local_lookup", 30*time.Second); !returned || next.Status != tools.StatusOK {
		t.Fatalf("next use after the process died = %s %q (returned=%v), want a restarted process answering", next.Status, next.Output, returned)
	}
}

// The server echoes the header it got in its initialize error, so each
// reason carries the expanded value unless qg redacts it.
func TestAcceptanceMCPRedactionStartsAtEightCharacters(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("QG_ACCEPT_SEVEN", "q7q7q7q")
	t.Setenv("QG_ACCEPT_EIGHT", "e8e8e8e8")
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method != "initialize" {
			return false
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID,
			"error": map[string]any{"code": -32001, "message": "label refused: " + r.Header.Get("X-Label")}})
		return true
	})
	_, runtime := acceptanceRegister(t, acceptanceConfig(t,
		`{"seven":{"type":"http","url":"`+server.URL+`","headers":{"X-Label":"${QG_ACCEPT_SEVEN}"}},`+
			`"eight":{"type":"http","url":"`+server.URL+`","headers":{"X-Label":"${QG_ACCEPT_EIGHT}"}}}`))

	for _, check := range []struct {
		server string
		value  string
		shown  bool
	}{{"seven", "q7q7q7q", true}, {"eight", "e8e8e8e8", false}} {
		reason, skipped := acceptanceSkippedReason(runtime, check.server)
		status := acceptanceStatus(t, runtime, check.server)
		if !skipped || !strings.Contains(reason, "label refused") || !strings.Contains(status.Reason, "label refused") {
			t.Fatalf("precondition: %s should fail with the server's message: skipped=%v reason=%q status=%q", check.server, skipped, reason, status.Reason)
		}
		for where, text := range map[string]string{"reason": reason, "status": status.Reason} {
			if strings.Contains(text, check.value) != check.shown {
				t.Errorf("%s %s = %q: %d-character value %q shown=%v, want shown=%v",
					check.server, where, text, len(check.value), check.value, !check.shown, check.shown)
			}
		}
	}
}

func TestAcceptanceMCPSuccessfulResultKeepsExpandedValues(t *testing.T) {
	acceptanceHermeticEnv(t)
	const token = "tok-echo-90210"
	t.Setenv("QG_ACCEPT_TOKEN", token)
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method != "tools/call" {
			return false
		}
		writeHTTPRPCResponse(t, w, message.ID, map[string]any{"content": []Content{{Type: "text", Text: "session " + r.Header.Get("X-Token")}}})
		return true
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`","headers":{"X-Token":"${QG_ACCEPT_TOKEN}"}}}`))

	result, returned := acceptanceRun(t, registry, "mcp_s_lookup", acceptanceTimeoutBound)
	if !returned || result.Status != tools.StatusOK {
		t.Fatalf("precondition: call = %s %q (returned=%v), want ok", result.Status, result.Output, returned)
	}
	if !strings.Contains(result.Output, "session "+token) {
		t.Fatalf("successful result = %q, want the server's text with %q verbatim", result.Output, token)
	}
}

func TestAcceptanceMCPReasonShowsVariableReferenceReadably(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("QG_ACCEPT_PATH_KEY", "path-key-31415")
	cfg := acceptanceConfig(t, `{"down":{"type":"http","url":"http://127.0.0.1:1/${QG_ACCEPT_PATH_KEY}/mcp"}}`)
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry, cfg, RegisterOptions{ConnectTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })

	reason, skipped := acceptanceSkippedReason(runtime, "down")
	if !skipped || !strings.Contains(reason, "127.0.0.1:1") {
		t.Fatalf("precondition: server on a closed port: skipped=%v reason=%q, want a reason naming the address", skipped, reason)
	}
	for where, text := range map[string]string{"reason": reason, "status": acceptanceStatus(t, runtime, "down").Reason} {
		if containsAnyFold(text, "%7B", "%7D") {
			t.Errorf("%s = %q shows the ${VAR} reference percent-encoded", where, text)
		}
	}
}

func TestAcceptanceMCPRefusesInitializeWithoutProtocolVersion(t *testing.T) {
	for name, answer := range map[string]map[string]any{
		"missing": {"capabilities": map[string]any{}},
		"empty":   {"protocolVersion": "", "capabilities": map[string]any{}},
	} {
		t.Run(name, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
				if message.Method != "initialize" {
					return false
				}
				writeHTTPRPCResponse(t, w, message.ID, answer)
				return true
			})
			registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"bare":{"type":"http","url":"`+server.URL+`"}}`))
			reason, skipped := acceptanceSkippedReason(runtime, "bare")
			if !skipped || registryHasTool(registry, "mcp_bare_lookup") {
				t.Fatal("server whose initialize names no protocolVersion was connected; qg must refuse it with a reason")
			}
			if !strings.Contains(reason, "protocolVersion") {
				t.Fatalf("reason %q does not say the protocolVersion is missing", reason)
			}
		})
	}
}

func TestAcceptanceMCPReconnectAfterCloseStartsNothing(t *testing.T) {
	acceptanceHermeticEnv(t)
	helper := newAcceptanceStdio(t, "basic")
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"local":`+helper.configJSON("")+`}`))
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = runtime.Reconnect(ctx, "local")
	if got := helper.starts(); got != 1 {
		t.Fatalf("reconnect after Close started the server again (%d starts, want 1); nothing would ever stop it", got)
	}
}

// Fails under -race when Skipped reads the list Reconnect rewrites.
func TestAcceptanceMCPSkippedIsSafeWhileReconnecting(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
		if message.Method != "initialize" {
			return false
		}
		writeHTTPRPCResponse(t, w, message.ID, map[string]any{"protocolVersion": "2099-01-01", "capabilities": map[string]any{}})
		return true
	})
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"future":{"type":"http","url":"`+server.URL+`"}}`))

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 20 {
			_ = runtime.Reconnect(context.Background(), "future")
		}
	}()
	sawFuture := false
	for reading := true; reading; {
		select {
		case <-done:
			reading = false
		default:
		}
		for _, skipped := range runtime.Skipped() {
			sawFuture = sawFuture || skipped.Name == "future"
		}
	}
	if !sawFuture {
		t.Fatal("precondition: a server on an unsupported version stays skipped")
	}
}

// Deleting the approvals file revokes every approval, even while a session
// holds the store open and approves another server.
func TestAcceptanceMCPDeletedApprovalStaysDeleted(t *testing.T) {
	acceptanceHermeticEnv(t)
	first := ProjectApproval{ProjectRoot: "/work/repo", ServerName: "a", Identity: "identity-a"}
	second := ProjectApproval{ProjectRoot: "/work/repo", ServerName: "b", Identity: "identity-b"}
	session, err := OpenProjectApprovals()
	if err != nil {
		t.Fatalf("OpenProjectApprovals() error = %v", err)
	}
	if err := session.Approve(first); err != nil {
		t.Fatalf("Approve(a) error = %v", err)
	}
	// The path comes from the same product function the store uses, so the
	// test deletes the live file on every platform (LOCALAPPDATA on Windows,
	// XDG_STATE_HOME elsewhere) instead of assuming the XDG layout.
	approvalsPath, err := projectApprovalsPath()
	if err != nil {
		t.Fatalf("projectApprovalsPath() error = %v", err)
	}
	if err := os.Remove(approvalsPath); err != nil {
		t.Fatalf("precondition: delete the approvals file: %v", err)
	}
	if err := session.Approve(second); err != nil {
		t.Fatalf("Approve(b) error = %v", err)
	}

	fresh, err := OpenProjectApprovals()
	if err != nil {
		t.Fatalf("OpenProjectApprovals() error = %v", err)
	}
	if fresh.IsApproved(first) {
		t.Fatal("the approval deleted from disk came back when the open session approved another server")
	}
	if !fresh.IsApproved(second) {
		t.Fatal("the approval given after the deletion was lost")
	}
}

// TestAcceptanceExitingHelperProcess is not a test: it is a stdio MCP server
// and returns at once unless spawned with QG_ACCEPT_HELPER_MODE. Each start
// appends to <state>/starts and each tools/call it reads to
// <state>/executions. The first process exits right after reading a
// tools/call, without answering; later processes answer "ok".
func TestAcceptanceExitingHelperProcess(t *testing.T) {
	if os.Getenv(acceptanceHelperModeEnv) == "" {
		return
	}
	state := os.Getenv(acceptanceHelperStateEnv)
	first := acceptanceStateLines(state, "starts") == 0
	appendLine(filepath.Join(state, "starts"), strconv.Itoa(os.Getpid()))

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var request struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == nil {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(request.Params, &params)
			result = map[string]any{
				"protocolVersion": params.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "accept-exiting", "version": "1"},
			}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "lookup", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			appendLine(filepath.Join(state, "executions"), strconv.Itoa(os.Getpid()))
			if first {
				os.Exit(3)
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		_, _ = os.Stdout.Write(append(body, '\n'))
	}
	os.Exit(0)
}
