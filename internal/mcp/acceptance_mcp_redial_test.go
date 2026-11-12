package mcp

// MCP acceptance tests for calls that never reached the server and for the
// redial that brings it back: such a call runs on the restarted server,
// concurrent callers share one restart, a heal never dials a server another
// call already healed, and a 429 never holds a call past its own timeout.

import (
	"context"
	"fmt"
	"net"
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

// killWhileIdle kills the server's current process between calls, the way a
// server that crashes or exits on its own while idle disappears. The pause
// lets the client see the process's output end before the next call.
func killWhileIdle(t *testing.T, server lifecycleServer) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(server.state, "starts"))
	if err != nil {
		t.Fatalf("read the server's starts: %v", err)
	}
	pids := strings.Fields(string(data))
	if len(pids) == 0 {
		t.Fatal("the server never started")
	}
	pid, err := strconv.Atoi(pids[len(pids)-1])
	if err != nil {
		t.Fatalf("server pid %q: %v", pids[len(pids)-1], err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("find server process %d: %v", pid, err)
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("kill server process %d: %v", pid, err)
	}
	time.Sleep(500 * time.Millisecond)
}

// A process that dies while idle takes no call with it: the next call never
// reached it, so it runs on a restarted process instead of failing.
func TestAcceptanceMCPCallAfterAnIdleExitRunsOnARestartedProcess(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newLifecycleServer(t, "ok")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":`+server.configJSON(t)+`}`))
	if result, ok := acceptanceRun(t, registry, "mcp_s_a", 30*time.Second); !ok || result.Status != tools.StatusOK {
		t.Fatalf("first call = %s %q (returned=%v)", result.Status, result.Output, ok)
	}

	killWhileIdle(t, server)
	result, ok := acceptanceRun(t, registry, "mcp_s_a", 30*time.Second)
	if !ok || result.Status != tools.StatusOK {
		t.Fatalf("call after the process died while idle = %s %q (returned=%v, starts=%d), want ok on a restarted process", result.Status, result.Output, ok, server.starts())
	}
	if got := server.starts(); got != 2 {
		t.Fatalf("process starts = %d, want 2 (original + one restart)", got)
	}
}

// Three calls arrive together after the process died while idle. None of them
// reached the dead process: one restarts it, and all three run on that one
// restart.
func TestAcceptanceMCPParallelCallsAfterAnIdleExitShareOneRestart(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newLifecycleServer(t, "ok")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":`+server.configJSON(t)+`}`))
	if result, ok := acceptanceRun(t, registry, "mcp_s_a", 30*time.Second); !ok || result.Status != tools.StatusOK {
		t.Fatalf("first call = %s %q (returned=%v)", result.Status, result.Output, ok)
	}

	killWhileIdle(t, server)
	const calls = 3
	results := make([]tools.Result, calls)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := range calls {
		wg.Go(func() {
			<-start
			results[index], _ = lifecycleBoundedCall(registry, "mcp_s_a", 30*time.Second)
		})
	}
	close(start)
	wg.Wait()
	for index, result := range results {
		if result.Status != tools.StatusOK {
			t.Errorf("parallel call %d after the idle exit = %s %q, want ok: it never reached the dead process", index+1, result.Status, result.Output)
		}
	}
	if got := server.starts(); got != 2 {
		t.Errorf("%d parallel calls after one idle exit started the server %d times, want 2 (original + one restart)", calls, got)
	}
}

// A call made while the dial is refused never reached the server: it is sent
// once the server is back on the same address instead of failing.
func TestAcceptanceMCPCallMadeWhileTheDialIsRefusedRunsOnceTheServerIsBack(t *testing.T) {
	acceptanceHermeticEnv(t)
	var received atomic.Int32
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, _ http.ResponseWriter, _ *http.Request) bool {
		if message.Method == "tools/call" {
			received.Add(1)
		}
		return false
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	if result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second); !ok || result.Status != tools.StatusOK {
		t.Fatalf("first call = %s %q (returned=%v)", result.Status, result.Output, ok)
	}

	addr := server.Listener.Addr().String()
	handler := server.Config.Handler
	_ = server.Listener.Close()
	server.CloseClientConnections()
	time.Sleep(200 * time.Millisecond)
	revived := make(chan *http.Server, 1)
	go func() {
		time.Sleep(700 * time.Millisecond)
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
		t.Fatalf("call made while the dial was refused = %s %q (returned=%v), want ok once the server is back", result.Status, result.Output, ok)
	}
	if got := received.Load(); got != 2 {
		t.Fatalf("the server received %d tools/call, want 2 (the first call and this one, once)", got)
	}
}

// After a call is cut mid-flight, the next uses heal the transport before
// sending. Two calls heal while the server is still down, and it comes back
// between their attempts: whichever call heals first wins, and the other runs
// on that fresh connection instead of dialing the server again.
func TestAcceptanceMCPHealNeverRedialsAServerAnotherCallHealed(t *testing.T) {
	acceptanceHermeticEnv(t)
	var down atomic.Bool
	var initializes atomic.Int32
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
		if down.Load() {
			hijackClose(w)
			return true
		}
		if message.Method == "initialize" {
			initializes.Add(1)
		}
		return false
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":{"type":"http","url":"`+server.URL+`"}}`))
	if result, ok := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second); !ok || result.Status != tools.StatusOK {
		t.Fatalf("first call = %s %q (returned=%v)", result.Status, result.Output, ok)
	}
	down.Store(true)
	if result, _ := acceptanceRun(t, registry, "mcp_s_lookup", 30*time.Second); result.Status == tools.StatusOK {
		t.Fatalf("call cut after its write came back ok: %q", result.Output)
	}

	before := initializes.Load()
	healed := make(chan tools.Result, 2)
	heal := func() {
		result, _ := lifecycleBoundedCall(registry, "mcp_s_lookup", 30*time.Second)
		healed <- result
	}
	go heal()
	time.Sleep(500 * time.Millisecond)
	go heal()
	time.Sleep(300 * time.Millisecond)
	down.Store(false)
	for range 2 {
		if result := <-healed; result.Status != tools.StatusOK {
			t.Errorf("healing call = %s %q, want ok once the server is back", result.Status, result.Output)
		}
	}
	if got := initializes.Load() - before; got != 1 {
		t.Fatalf("the server got %d initializes after it came back, want 1: a heal dialed again a server another call had already healed", got)
	}
}

// callRefusedWith429 makes one call to a server that answers every tools/call
// with 429 and the given Retry-After. It stops waiting at bound and reports
// whether the call came back by then, and how many sends reached the server.
func callRefusedWith429(t *testing.T, timeoutMillis int, retryAfter string, bound time.Duration) (tools.Result, bool, int32) {
	t.Helper()
	var sends atomic.Int32
	server := newAcceptanceHTTPServer(t, func(_ *acceptanceHTTPServer, message rpcMessage, w http.ResponseWriter, _ *http.Request) bool {
		if message.Method != "tools/call" {
			return false
		}
		sends.Add(1)
		w.Header().Set("Retry-After", retryAfter)
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	})
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, fmt.Sprintf(`{"s":{"type":"http","url":%q,"timeout":%d}}`, server.URL, timeoutMillis)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan tools.Result, 1)
	go func() {
		done <- registry.RunWithOptions(ctx, "mcp_s_lookup", map[string]any{}, tools.RunOptions{PermissionGranted: true})
	}()
	select {
	case result := <-done:
		return result, true, sends.Load()
	case <-time.After(bound):
		cancel()
		<-done
		return tools.Result{}, false, sends.Load()
	}
}

// A Retry-After longer than the call's whole timeout cannot be waited out:
// the 429 goes back to the model at once, without a wait or a resend.
func TestAcceptanceMCPRetryAfterLongerThanTheCallTimeoutReturnsThe429AtOnce(t *testing.T) {
	acceptanceHermeticEnv(t)
	result, returned, sends := callRefusedWith429(t, 5000, "20", time.Second)
	if !returned {
		t.Fatalf("a call with timeout 5000ms refused with 429 and Retry-After: 20 was still waiting after 1s (%d sends), want the 429 back at once", sends)
	}
	if result.Status == tools.StatusOK || !strings.Contains(result.Output, "429") {
		t.Fatalf("result = %s %q, want the 429 error", result.Status, result.Output)
	}
	if sends != 1 {
		t.Fatalf("the call reached the server %d times, want 1", sends)
	}
}

// Each Retry-After fits the timeout, but the waits add up: the call stops
// once the next wait no longer fits in what is left, and never outlasts its
// own timeout.
func TestAcceptanceMCPRetryAfterWaitsNeverAddUpPastTheCallTimeout(t *testing.T) {
	acceptanceHermeticEnv(t)
	const timeout = 2500 * time.Millisecond
	const slack = time.Second
	result, returned, sends := callRefusedWith429(t, int(timeout/time.Millisecond), "1", timeout+slack)
	if !returned {
		t.Fatalf("a call with timeout %v refused with 429 and Retry-After: 1 every time was still waiting after %v (%d sends), want it back within its timeout", timeout, timeout+slack, sends)
	}
	if result.Status == tools.StatusOK || !strings.Contains(result.Output, "429") {
		t.Fatalf("result = %s %q, want the 429 error", result.Status, result.Output)
	}
}
