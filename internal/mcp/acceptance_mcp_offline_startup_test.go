package mcp

// MCP acceptance tests for remote servers that cannot be reached. An error
// that will not heal in seconds (a host that does not resolve, a certificate
// the client rejects) fails the initial connect at once and names the real
// failure. A server that was live and goes away mid-session keeps its full
// redial, whatever startup does with servers that never came up.

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// permanentFailureBound is how long the initial connect may take when the
// failure cannot heal: no retry, so well under the first 1s backoff.
const permanentFailureBound = time.Second

// timedRegister runs the real startup registration and reports how long it
// took.
func timedRegister(t *testing.T, servers string) (*Runtime, time.Duration) {
	t.Helper()
	start := time.Now()
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, servers))
	return runtime, time.Since(start)
}

// A host that does not resolve will not resolve a second later: the initial
// connect gives up on the first attempt, and the reason names the lookup
// failure.
func TestAcceptanceMCPUnresolvableHostFailsTheInitialConnectAtOnce(t *testing.T) {
	for _, transport := range []string{"http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			runtime, elapsed := timedRegister(t, `{"s":{"type":"`+transport+`","url":"https://mcp.qg-offline.invalid/`+transport+`"}}`)
			reason, skipped := acceptanceSkippedReason(runtime, "s")
			if !skipped {
				t.Fatalf("server on an unresolvable host was not skipped")
			}
			if elapsed >= permanentFailureBound {
				t.Errorf("initial connect to an unresolvable %s server took %v, want under %v: a lookup failure must not be retried (reason %q)", transport, elapsed.Round(10*time.Millisecond), permanentFailureBound, reason)
			}
			if !strings.Contains(reason, "mcp.qg-offline.invalid") || !containsAnyFold(reason, "no such host", "lookup") {
				t.Errorf("skipped reason = %q, want the lookup failure for mcp.qg-offline.invalid", reason)
			}
		})
	}
}

// A certificate the client rejects is rejected the same way on every attempt:
// the initial connect makes one connection, gives up at once, and says why.
func TestAcceptanceMCPRejectedCertificateFailsTheInitialConnectAtOnce(t *testing.T) {
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

	runtime, elapsed := timedRegister(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`)
	reason, skipped := acceptanceSkippedReason(runtime, "s")
	if !skipped {
		t.Fatalf("server with an untrusted certificate was not skipped")
	}
	if elapsed >= permanentFailureBound || connections.Load() != 1 {
		t.Errorf("initial connect with an untrusted certificate took %v over %d connections, want under %v over 1: a certificate error must not be retried (reason %q)", elapsed.Round(10*time.Millisecond), connections.Load(), permanentFailureBound, reason)
	}
	if !containsAnyFold(reason, "certificate", "x509") {
		t.Errorf("skipped reason = %q, want the certificate error", reason)
	}
}

// A server that was live and goes away mid-session keeps its full redial: a
// call made while the dial is refused still runs once the server is back,
// even when that takes several seconds of backoff. Startup never waits on a
// server that is down, but a live one is worth waiting for.
func TestAcceptanceMCPLiveServerRedialWaitsSecondsForTheServerToComeBack(t *testing.T) {
	acceptanceHermeticEnv(t)
	var received atomic.Int32
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, _ http.ResponseWriter, _ *http.Request) bool {
		if message.Method == "tools/call" {
			received.Add(1)
		}
		return false
	})
	server.Config.SetKeepAlivesEnabled(false)
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	if result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second); !ok || result.Status != tools.StatusOK {
		t.Fatalf("first call = %s %q (returned=%v)", result.Status, result.Output, ok)
	}

	addr := server.Listener.Addr().String()
	handler := server.Config.Handler
	_ = server.Listener.Close()
	server.CloseClientConnections()
	const downFor = 2500 * time.Millisecond
	revived := make(chan *http.Server, 1)
	go func() {
		time.Sleep(downFor)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			revived <- nil
			return
		}
		back := &http.Server{Handler: handler}
		revived <- back
		_ = back.Serve(listener)
	}()
	result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second)
	back := <-revived
	if back == nil {
		t.Fatalf("could not listen again on %s", addr)
	}
	t.Cleanup(func() { _ = back.Close() })
	if !ok || result.Status != tools.StatusOK {
		t.Fatalf("call made while a live server was down for %v = %s %q (returned=%v), want ok once it is back", downFor, result.Status, result.Output, ok)
	}
	if got := received.Load(); got != 2 {
		t.Fatalf("the server received %d tools/call, want 2 (the first call and this one, once)", got)
	}
}
