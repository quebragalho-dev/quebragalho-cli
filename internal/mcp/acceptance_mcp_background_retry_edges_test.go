package mcp

// MCP acceptance tests for the edges of the background retry: a server that
// hangs at startup, an attempt cut by the connect bound, an attempt that loses
// the race to a manual reconnect, and the reason /mcp shows meanwhile.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// A server that accepts the connection at startup and does not answer within
// MCP_TIMEOUT is slow, not broken: it is retried in the background like any
// other transient failure, and adopted once it answers.
func TestAcceptanceMCPServerThatHangsAtStartupIsRetriedInTheBackground(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1500")
	var initializes atomic.Int32
	server := newAcceptanceHTTPServer(t, func(server *acceptanceHTTPServer, message rpcMessage, _ http.ResponseWriter, r *http.Request) bool {
		if message.Method == "initialize" && initializes.Add(1) == 1 {
			server.block(r)
			return true
		}
		return false
	})
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	if registryHasTool(registry, "mcp_s_lookup") {
		t.Fatal("precondition: the server never answered the startup initialize, yet its tools were registered")
	}
	if !waitForLateTool(registry, "mcp_s_lookup", lateServerBound) {
		reason, _ := acceptanceSkippedReason(runtime, "s")
		t.Fatalf("the server hung only at startup and answered every request after it, but it was not adopted within %v (%d initialize requests, reason %q): a startup timeout must be retried in the background", lateServerBound, initializes.Load(), reason)
	}
}

// hungBackgroundAttemptServer answers the startup initialize with a 503 and
// holds the first background initialize until the client gives up on it.
// held closes when that attempt arrives, ended when the client abandoned it.
func hungBackgroundAttemptServer(t *testing.T) (server *acceptanceHTTPServer, held, ended chan struct{}) {
	t.Helper()
	held = make(chan struct{})
	ended = make(chan struct{})
	var initializes atomic.Int32
	server = newAcceptanceHTTPServer(t, func(server *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method != "initialize" {
			return false
		}
		switch initializes.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		case 2:
			close(held)
			server.block(r)
			close(ended)
			return true
		}
		return false
	})
	return server, held, ended
}

func waitFor(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(lateServerBound):
		t.Fatalf("%s did not happen within %v", what, lateServerBound)
	}
}

// A background attempt cut by the connect bound reports the timeout, the way
// the same hang reports it at startup, not the cancellation the bound caused.
func TestAcceptanceMCPHungBackgroundAttemptIsReportedAsATimeout(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1500")
	server, _, ended := hungBackgroundAttemptServer(t)
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	waitFor(t, ended, "the client giving up on the hung background attempt")
	var reason string
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		reason = acceptanceStatus(t, runtime, "s").Reason
		if strings.Contains(reason, "timed out") {
			break
		}
	}
	if !strings.Contains(reason, "timed out") || strings.Contains(reason, "context canceled") {
		t.Fatalf("status reason after the hung background attempt = %q, want the connect timeout (MCP_TIMEOUT 1.5s), not the cancellation it caused", reason)
	}
}

// When a manual reconnect brings the server live while a background attempt
// still hangs, the later failure of that attempt says nothing about the live
// server: /mcp keeps showing it connected.
func TestAcceptanceMCPBackgroundAttemptFailingAfterAManualReconnectKeepsTheServerConnected(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1500")
	server, held, ended := hungBackgroundAttemptServer(t)
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	waitFor(t, held, "the background attempt reaching the server")
	if err := runtime.Reconnect(context.Background(), "s"); err != nil {
		t.Fatalf("Reconnect() = %v", err)
	}
	waitFor(t, ended, "the client giving up on the hung background attempt")
	time.Sleep(300 * time.Millisecond)
	if status := acceptanceStatus(t, runtime, "s"); status.State != ServerStateConnected {
		t.Fatalf("status after the stale background attempt failed = %s %q, want connected: the user reconnected the server and its tools work", status.State, status.Reason)
	}
	if reason, skipped := acceptanceSkippedReason(runtime, "s"); skipped {
		t.Fatalf("the live server is listed as skipped again (%q)", reason)
	}
	if result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second); !ok || result.Status != tools.StatusOK {
		t.Fatalf("call after the manual reconnect = %s %q (returned=%v)", result.Status, result.Output, ok)
	}
}

// The background retry tells the server's answer to /mcp, and the server may
// echo the request URL, which carries the expanded ${VAR}. The recorded reason
// must hold the reference, never the value.
func TestAcceptanceMCPBackgroundRetryReasonNeverCarriesAnExpandedSecret(t *testing.T) {
	acceptanceHermeticEnv(t)
	const secret = "qg-acceptance-dummy-key-5b1e9c"
	t.Setenv("QG_ACCEPTANCE_DUMMY_KEY", secret)
	var initializes, carried atomic.Int32
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method != "initialize" {
			return false
		}
		if r.URL.Query().Get("key") == secret {
			carried.Add(1)
		}
		status := http.StatusBadGateway
		if initializes.Add(1) == 1 {
			status = http.StatusServiceUnavailable
		}
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, "overloaded: url=%s", r.URL.String())
		return true
	})
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`/mcp?key=${QG_ACCEPTANCE_DUMMY_KEY}"}}`))
	var reason string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if reason, _ = acceptanceSkippedReason(runtime, "s"); strings.Contains(reason, "502") {
			break
		}
	}
	if !strings.Contains(reason, "502") {
		t.Fatalf("3s after startup the skipped reason is %q, want the 502 of the background attempt", reason)
	}
	if carried.Load() < 2 {
		t.Fatalf("the server got the expanded key on %d initialize requests, want 2: the URL must carry the value for this test to mean anything", carried.Load())
	}
	if strings.Contains(reason, secret) {
		t.Fatalf("the background reason carries the expanded value of ${QG_ACCEPTANCE_DUMMY_KEY}: %q", reason)
	}
}

// labeledClient is a connected client that answers every call with its label
// and records Close, so a test can tell which connect serves the calls.
type labeledClient struct {
	label  string
	closed atomic.Int32
}

func (client *labeledClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "lookup"}}, nil
}

func (client *labeledClient) CallTool(context.Context, string, map[string]any) (CallToolResult, error) {
	return CallToolResult{Content: []Content{{Type: "text", Text: client.label}}}, nil
}

func (client *labeledClient) Close() error {
	client.closed.Add(1)
	return nil
}

func registerWithFactory(t *testing.T, factory func(context.Context, Server) (ToolClient, error)) (*tools.Registry, *Runtime) {
	t.Helper()
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry, acceptanceConfig(t, `{"s":{"type":"http","url":"http://127.0.0.1:9/mcp"}}`), RegisterOptions{ClientFactory: factory})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return registry, runtime
}

var errStartupUnavailable = &MCPHTTPStatusError{Server: "s", Transport: ServerTypeHTTP, Status: http.StatusServiceUnavailable}

// A background connect that completes after a manual reconnect brought the
// server live is dropped: the calls stay on the client the user connected.
// The two clients are indistinguishable over a real HTTP transport, so this
// test goes through the client factory and labels them.
func TestAcceptanceMCPBackgroundConnectCompletingAfterAManualReconnectIsDropped(t *testing.T) {
	acceptanceHermeticEnv(t)
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	background := &labeledClient{label: "background"}
	manual := &labeledClient{label: "manual"}
	registry, runtime := registerWithFactory(t, func(ctx context.Context, _ Server) (ToolClient, error) {
		switch calls.Add(1) {
		case 1:
			return nil, errStartupUnavailable
		case 2:
			close(entered)
			select {
			case <-release:
				return background, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return manual, nil
	})
	waitFor(t, entered, "the background attempt")
	if err := runtime.Reconnect(context.Background(), "s"); err != nil {
		t.Fatalf("Reconnect() = %v", err)
	}
	close(release)
	for deadline := time.Now().Add(2 * time.Second); background.closed.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second)
	if !ok || result.Output != "manual" {
		t.Fatalf("call after the background connect completed = %s %q (returned=%v), want it served by the client the user reconnected", result.Status, result.Output, ok)
	}
	if background.closed.Load() != 1 || manual.closed.Load() != 0 {
		t.Fatalf("closed: background client %d time(s), manual client %d time(s); want the late background client closed once and the live one untouched", background.closed.Load(), manual.closed.Load())
	}
}

// A connect that succeeds only after the connect bound fired has lost its
// context, so its client is closed and the attempt counts as timed out; the
// next attempt connects again and that client serves the calls. A real
// transport fails as soon as the bound cancels it, so only the client factory
// can finish a connect after the bound.
func TestAcceptanceMCPConnectSucceedingAfterTheBoundIsDiscarded(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1500")
	var calls atomic.Int32
	late := &labeledClient{label: "late"}
	fresh := &labeledClient{label: "fresh"}
	registry, runtime := registerWithFactory(t, func(ctx context.Context, _ Server) (ToolClient, error) {
		switch calls.Add(1) {
		case 1:
			return nil, errStartupUnavailable
		case 2:
			<-ctx.Done()
			return late, nil
		}
		return fresh, nil
	})
	if !waitForLateTool(registry, "mcp_s_lookup", lateServerBound) {
		reason, _ := acceptanceSkippedReason(runtime, "s")
		t.Fatalf("no attempt was adopted within %v (%d connects, reason %q)", lateServerBound, calls.Load(), reason)
	}
	result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second)
	if !ok || result.Output != "fresh" {
		t.Fatalf("call = %s %q (returned=%v), want it served by the connect after the timed-out one", result.Status, result.Output, ok)
	}
	if got := late.closed.Load(); got != 1 {
		t.Fatalf("the client that connected after the bound was closed %d time(s), want 1", got)
	}
}
