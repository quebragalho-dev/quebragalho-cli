package mcp

// Contracts the re-audit of the session lifecycle and of redaction found
// broken: a reconnect never repeats a call, a live server is never restarted
// by a refresh, a legacy SSE stream that ends is redialed, and an expanded
// ${VAR} value never reaches the user in any form, nor rewrites data that is
// not a secret.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

const slowHelperStateEnv = "QG_ACCEPT_SLOW_STATE"

func countStateLines(state, name string) int {
	data, _ := os.ReadFile(filepath.Join(state, name))
	return strings.Count(string(data), "\n")
}

// Reconnecting a server from /mcp while one of its tools runs never delivers
// that call to the server a second time.
func TestAcceptanceMCPPanelReconnectDuringACallRunsTheToolOnce(t *testing.T) {
	acceptanceHermeticEnv(t)
	state := t.TempDir()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}
	server := fmt.Sprintf(`{"s":{"type":"stdio","command":%q,"args":["-test.run=^TestAcceptanceSlowHelperProcess$"],"env":{%q:%q}}}`, binary, slowHelperStateEnv, state)
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, server))

	done := make(chan tools.Result, 1)
	go func() {
		result, _ := lifecycleBoundedCall(registry, "mcp_s_a", 30*time.Second)
		done <- result
	}()
	deadline := time.Now().Add(10 * time.Second)
	for countStateLines(state, "executions") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("precondition: the call never reached the server")
		}
		time.Sleep(10 * time.Millisecond)
	}
	release := func() { _ = os.WriteFile(filepath.Join(state, "release"), nil, 0o600) }
	// A reconnect that drains the running call before swapping must not wait
	// on this test forever.
	timer := time.AfterFunc(5*time.Second, release)
	defer timer.Stop()
	if err := runtime.Reconnect(context.Background(), "s"); err != nil {
		t.Fatalf("reconnect s: %v", err)
	}
	release()
	result := <-done
	if executions := countStateLines(state, "executions"); executions != 1 {
		t.Fatalf("one model call reached the server %d times (answered %s %q)", executions, result.Status, result.Output)
	}
}

// A server that announced tools/list_changed and then does not answer
// tools/list in time is alive: the refresh between model requests fails and
// the process keeps running.
func TestAcceptanceMCPUnansweredRefreshKeepsTheServerProcess(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1000")
	stale := newLifecycleServer(t, "stale-hang")
	registry, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"s":`+stale.configJSON(t)+`}`))
	if result, _ := lifecycleBoundedCall(registry, "mcp_s_a", 10*time.Second); result.Status != tools.StatusOK {
		t.Fatalf("precondition: first call to s = %s %q, want ok", result.Status, result.Output)
	}
	for request := 0; request < 3; request++ {
		_, _ = runtime.RefreshPendingTools(context.Background())
	}
	if starts := stale.starts(); starts != 1 {
		t.Fatalf("3 refreshes of a live server whose tools/list timed out started it %d times, want 1", starts)
	}
}

// A legacy SSE stream that the server ends cleanly (a restart, an idle proxy)
// is a dead transport: the next call opens a new stream and gets its answer.
func TestAcceptanceMCPEndedLegacySSEStreamIsRedialed(t *testing.T) {
	acceptanceHermeticEnv(t)
	var streams atomic.Int32
	endFirst, firstEnded := make(chan struct{}), make(chan struct{})
	events := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/sse" && r.Method == http.MethodGet:
			n := streams.Add(1)
			end := endFirst
			if n == 1 {
				defer close(firstEnded)
			} else {
				end = nil
			}
			flusher, _ := w.(http.Flusher)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "event: endpoint\ndata: /messages\n\n")
			flusher.Flush()
			for {
				select {
				case event := <-events:
					_, _ = fmt.Fprint(w, event)
					flusher.Flush()
				case <-r.Context().Done():
					return
				case <-end:
					return
				}
			}
		case r.URL.Path == "/messages" && r.Method == http.MethodPost:
			message := readHTTPRPCMessage(t, r)
			switch message.Method {
			case "initialize":
				events <- formatSSERPCResponse(t, message.ID, map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "legacy", "version": "1"}})
			case "tools/list":
				events <- formatSSERPCResponse(t, message.ID, map[string]any{"tools": []map[string]any{{"name": "lookup", "inputSchema": map[string]any{"type": "object"}}}})
			case "tools/call":
				events <- formatSSERPCResponse(t, message.ID, map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}})
			}
			w.WriteHeader(http.StatusAccepted)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"r":{"type":"sse","url":"`+server.URL+`/sse","timeout":2000}}`))

	close(endFirst)
	<-firstEnded
	time.Sleep(100 * time.Millisecond)
	result, _ := lifecycleBoundedCall(registry, "mcp_r_lookup", 20*time.Second)
	if result.Status != tools.StatusOK || !strings.Contains(result.Output, "ok") {
		t.Fatalf("call after the server ended its SSE stream = %s %q, want ok on a new stream (streams opened: %d)", result.Status, result.Output, streams.Load())
	}
}

// The error /mcp shows when a reconnect fails names the server's address
// without the expanded ${VAR} value.
func TestAcceptanceMCPReconnectErrorNeverShowsExpandedSecret(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1000")
	const secret = "sk-reconnect-5151"
	t.Setenv("QG_ACCEPT_SECRET", secret)
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"down":{"type":"http","url":"http://127.0.0.1:1/${QG_ACCEPT_SECRET}/mcp"}}`))

	err := runtime.Reconnect(context.Background(), "down")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Fatalf("reconnect to a closed port = %v, want an error naming the address", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("reconnect error shows the expanded secret: %v", err)
	}
}

// A secret with characters that URLs escape is still redacted in its escaped
// form.
func TestAcceptanceMCPEscapedSecretIsNeverShown(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1000")
	t.Setenv("QG_ACCEPT_SECRET", "spc secret 4242")
	_, runtime := acceptanceRegister(t, acceptanceConfig(t, `{"down":{"type":"http","url":"http://127.0.0.1:1/${QG_ACCEPT_SECRET}/mcp"}}`))

	reason, skipped := acceptanceSkippedReason(runtime, "down")
	if !skipped || !strings.Contains(reason, "127.0.0.1:1") {
		t.Fatalf("server on a closed port: skipped=%v reason=%q, want a reason naming the address", skipped, reason)
	}
	for _, text := range []string{reason, acceptanceStatus(t, runtime, "down").Reason} {
		if strings.Contains(text, "secret%204242") || strings.Contains(text, "spc secret 4242") {
			t.Fatalf("escaped secret shown: %s", text)
		}
	}
}

// Redaction trades back secrets, not every value a ${VAR} carried: a short
// value such as a mode or a port never rewrites what a tool answers.
func TestAcceptanceMCPShortExpandedValueNeverRewritesToolOutput(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("QG_ACCEPT_LABEL", "ok")
	server := newLifecycleServer(t, "ok")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":`+server.configJSON(t, "${QG_ACCEPT_LABEL}")+`}`))

	result, _ := lifecycleBoundedCall(registry, "mcp_s_a", 10*time.Second)
	if result.Status != tools.StatusOK || !strings.Contains(result.Output, "ok") || strings.Contains(result.Output, "${") {
		t.Fatalf("tool answer = %s %q, want the server's literal ok", result.Status, result.Output)
	}
}

// TestAcceptanceSlowHelperProcess is not a test: it is a stdio MCP server
// with tool "a" and returns at once unless spawned with QG_ACCEPT_SLOW_STATE.
// Each start appends a line to <state>/starts and each tools/call it receives
// to <state>/executions; a call is answered once <state>/release exists.
func TestAcceptanceSlowHelperProcess(t *testing.T) {
	state := os.Getenv(slowHelperStateEnv)
	if state == "" {
		return
	}
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
			result = map[string]any{"protocolVersion": params.ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "accept-slow", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "a", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			appendLine(filepath.Join(state, "executions"), strconv.Itoa(os.Getpid()))
			for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
				if _, err := os.Stat(filepath.Join(state, "release")); err == nil {
					break
				}
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		_, _ = os.Stdout.Write(append(body, '\n'))
	}
	os.Exit(0)
}
