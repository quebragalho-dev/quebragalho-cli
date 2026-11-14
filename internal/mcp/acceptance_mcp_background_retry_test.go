package mcp

// MCP acceptance tests for the background retry of remote servers that were
// down at startup. A server that comes back is adopted for the rest of the
// session exactly like one that was up from the start; the retry gives way to
// a manual reconnect and to Close; a hung attempt costs only that attempt;
// and only failures that can heal are retried at all.

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// lateServerBound is how long a server that comes back may take to be
// adopted: the background retry waits 1s, then 2s, then 4s between attempts.
const lateServerBound = 10 * time.Second

func waitForLateTool(registry *tools.Registry, name string, bound time.Duration) bool {
	for deadline := time.Now().Add(bound); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if registryHasTool(registry, name) {
			return true
		}
	}
	return false
}

// lateSSEServer is a legacy HTTP+SSE MCP server that answers 503 while down
// and counts the event streams clients open and close. Every stream gets its
// own message endpoint, so a reply always reaches the stream that asked.
type lateSSEServer struct {
	*httptest.Server
	down    atomic.Bool
	opens   atomic.Int32
	closes  atomic.Int32
	mu      sync.Mutex
	streams map[string]chan string
}

func newLateSSEServer(t *testing.T) *lateSSEServer {
	t.Helper()
	server := &lateSSEServer{streams: map[string]chan string{}}
	server.down.Store(true)
	release := make(chan struct{})
	var sessions atomic.Int32
	server.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sse" && r.Method == http.MethodGet:
			if server.down.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			session := fmt.Sprint(sessions.Add(1))
			events := make(chan string, 16)
			server.mu.Lock()
			server.streams[session] = events
			server.mu.Unlock()
			server.opens.Add(1)
			defer server.closes.Add(1)
			flusher, _ := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /messages?session=%s\n\n", session)
			flusher.Flush()
			for {
				select {
				case event := <-events:
					_, _ = fmt.Fprint(w, event)
					flusher.Flush()
				case <-r.Context().Done():
					return
				case <-release:
					return
				}
			}
		case r.URL.Path == "/messages" && r.Method == http.MethodPost:
			message := readHTTPRPCMessage(t, r)
			server.mu.Lock()
			events := server.streams[r.URL.Query().Get("session")]
			server.mu.Unlock()
			var reply string
			switch message.Method {
			case "initialize":
				reply = formatSSERPCResponse(t, message.ID, map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "late-sse", "version": "1"},
				})
			case "tools/list":
				reply = formatSSERPCResponse(t, message.ID, map[string]any{
					"tools": []map[string]any{{"name": "lookup", "inputSchema": map[string]any{"type": "object"}}},
				})
			case "tools/call":
				reply = formatSSERPCResponse(t, message.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "ok"}},
				})
			}
			w.WriteHeader(http.StatusAccepted)
			if reply != "" && events != nil {
				events <- reply
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) }) // LIFO: runs before Close
	return server
}

// A server adopted by the background retry keeps its SSE stream for the
// session, like a server that was up at startup: the connect bound
// (MCP_TIMEOUT) limits the connect, not the life of the stream it opened.
func TestAcceptanceMCPServerAdoptedInTheBackgroundKeepsItsSSEStream(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1500")
	server := newLateSSEServer(t)
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"sse","url":"`+server.URL+`/sse"}}`))
	server.down.Store(false)
	if !waitForLateTool(registry, "mcp_s_lookup", lateServerBound) {
		reason, _ := acceptanceSkippedReason(runtime, "s")
		t.Fatalf("server that came back was not adopted within %v (reason %q)", lateServerBound, reason)
	}
	const idle = 2500 * time.Millisecond
	time.Sleep(idle)
	if closes := server.closes.Load(); closes != 0 {
		t.Fatalf("the SSE stream of a server adopted in the background was closed %d time(s) within %v of adoption, with the session still open: MCP_TIMEOUT (1.5s) must bound the connect, not the stream", closes, idle)
	}
	result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second)
	if !ok || result.Status != tools.StatusOK {
		t.Fatalf("call on the adopted server = %s %q (returned=%v)", result.Status, result.Output, ok)
	}
	if opens := server.opens.Load(); opens != 1 {
		t.Fatalf("the adopted server saw %d SSE streams, want 1: the call must run on the stream opened at adoption", opens)
	}
}

// A manual reconnect (the /mcp "reconectar" action) makes the server live, so
// the background retry has nothing left to do: it must not dial again and
// swap out the client the user just connected.
func TestAcceptanceMCPManualReconnectEndsTheBackgroundRetry(t *testing.T) {
	acceptanceHermeticEnv(t)
	var down atomic.Bool
	down.Store(true)
	var initializes atomic.Int32
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
		if message.Method != "initialize" {
			return false
		}
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		initializes.Add(1)
		return false
	})
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	down.Store(false)
	if err := runtime.Reconnect(context.Background(), "s"); err != nil {
		t.Fatalf("Reconnect() = %v", err)
	}
	time.Sleep(2500 * time.Millisecond)
	if got := initializes.Load(); got != 1 {
		t.Fatalf("the server was initialized %d times, want 1: after a manual reconnect the background retry must stop instead of dialing again and replacing the live client", got)
	}
	if result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second); !ok || result.Status != tools.StatusOK {
		t.Fatalf("call after the manual reconnect = %s %q (returned=%v)", result.Status, result.Output, ok)
	}
}

// A server that takes a background attempt and never answers costs only that
// attempt: the retry goes on, and the server is adopted once it answers.
func TestAcceptanceMCPHungAttemptDoesNotEndTheBackgroundRetry(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1500")
	var initializes atomic.Int32
	server := newAcceptanceHTTPServer(t, func(server *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, r *http.Request) bool {
		if message.Method != "initialize" {
			return false
		}
		switch initializes.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		case 2:
			server.block(r)
			return true
		}
		return false
	})
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	if !waitForLateTool(registry, "mcp_s_lookup", lateServerBound) {
		reason, _ := acceptanceSkippedReason(runtime, "s")
		t.Fatalf("one background attempt hung past MCP_TIMEOUT (1.5s) and the server answered every request after it, but it was not adopted within %v (%d initialize requests, reason %q): a hung attempt must not end the retry", lateServerBound, initializes.Load(), reason)
	}
}

// A certificate the client rejects is rejected the same way on every attempt,
// so the server is not retried in the background either.
func TestAcceptanceMCPCertificateErrorIsNeverRetriedInTheBackground(t *testing.T) {
	acceptanceHermeticEnv(t)
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	const after = 1500 * time.Millisecond
	time.Sleep(after)
	if got := connections.Load(); got != 1 {
		t.Fatalf("the server with an untrusted certificate saw %d connections in the %v after startup, want 1: a certificate error must not be retried in the background", got, after)
	}
	if reason, _ := acceptanceSkippedReason(runtime, "s"); !containsAnyFold(reason, "certificate", "x509") {
		t.Fatalf("skipped reason = %q, want the certificate error", reason)
	}
}

// A server that was down at startup and then answers with an error that will
// not heal (a 400) ends the background retry at that answer, and the skipped
// reason names it.
func TestAcceptanceMCPBackgroundRetryStopsAtAnErrorThatWillNotHeal(t *testing.T) {
	acceptanceHermeticEnv(t)
	var initializes atomic.Int32
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
		if message.Method != "initialize" {
			return false
		}
		if initializes.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return true
	})
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	// The first background attempt is due at 1s; another would be due at 3s.
	const after = 3500 * time.Millisecond
	time.Sleep(after)
	if got := initializes.Load(); got != 2 {
		t.Fatalf("the server got %d initialize requests in the %v after startup, want 2 (startup and one background attempt): a 400 will not heal, so the retry must stop there", got, after)
	}
	if reason, _ := acceptanceSkippedReason(runtime, "s"); !strings.Contains(reason, "400") {
		t.Fatalf("skipped reason = %q, want the 400 that ended the retry", reason)
	}
}

// While a server stays down, each background attempt refreshes its reason, so
// /mcp names the server's latest answer instead of the one from startup.
func TestAcceptanceMCPBackgroundRetryRefreshesTheStatusReason(t *testing.T) {
	acceptanceHermeticEnv(t)
	var initializes atomic.Int32
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
		if message.Method != "initialize" {
			return false
		}
		if initializes.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusBadGateway)
		}
		return true
	})
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	if reason := acceptanceStatus(t, runtime, "s").Reason; !strings.Contains(reason, "503") {
		t.Fatalf("status reason at startup = %q, want the 503", reason)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		reason := acceptanceStatus(t, runtime, "s").Reason
		if strings.Contains(reason, "502") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("3s after startup the status reason is still %q, want the 502 the server answered the background attempt with", reason)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// closeCountingClient is a connected client that only records Close.
type closeCountingClient struct {
	closed atomic.Int32
}

func (client *closeCountingClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "lookup"}}, nil
}

func (client *closeCountingClient) CallTool(context.Context, string, map[string]any) (CallToolResult, error) {
	return CallToolResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
}

func (client *closeCountingClient) Close() error {
	client.closed.Add(1)
	return nil
}

// A background connect that finishes after Close is never adopted: its tools
// stay out of the registry and its client is closed. A real transport aborts
// on the cancelled runtime context, so only the client factory can hold a
// connect open across Close; this is the one test here that goes through it.
func TestAcceptanceMCPConnectFinishingAfterCloseIsNeverAdopted(t *testing.T) {
	acceptanceHermeticEnv(t)
	var calls atomic.Int32
	var enteredOnce sync.Once
	entered := make(chan struct{})
	release := make(chan struct{})
	late := &closeCountingClient{}
	factory := func(context.Context, Server) (ToolClient, error) {
		if calls.Add(1) == 1 {
			return nil, &MCPHTTPStatusError{Server: "s", Transport: ServerTypeHTTP, Status: http.StatusServiceUnavailable}
		}
		enteredOnce.Do(func() { close(entered) })
		<-release
		return late, nil
	}
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry, acceptanceConfig(t, `{"s":{"type":"http","url":"http://127.0.0.1:9/mcp"}}`), RegisterOptions{ClientFactory: factory})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	select {
	case <-entered:
	case <-time.After(lateServerBound):
		t.Fatalf("the background retry made no second attempt within %v", lateServerBound)
	}
	_ = runtime.Close()
	close(release)
	for deadline := time.Now().Add(2 * time.Second); late.closed.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if registryHasTool(registry, "mcp_s_lookup") {
		t.Fatal("a connect that finished after Close was adopted: its tools reached the registry of a closed runtime")
	}
	if late.closed.Load() == 0 {
		t.Fatal("a connect that finished after Close left its client open")
	}
}
