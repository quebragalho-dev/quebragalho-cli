package mcp

// MCP acceptance tests for the life of a server process: which failures
// restart it, who owns the restarted process, and what a pending catalog
// refresh may hold up.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

const (
	lifecycleModeEnv  = "QG_ACCEPT_LIFECYCLE_MODE"
	lifecycleStateEnv = "QG_ACCEPT_LIFECYCLE_STATE"
)

// lifecycleServer is a stdio MCP server with tools "a" and "b": this test
// binary re-executed in one mode of TestAcceptanceLifecycleHelperProcess.
type lifecycleServer struct {
	mode  string
	state string
}

func newLifecycleServer(t *testing.T, mode string) lifecycleServer {
	t.Helper()
	return lifecycleServer{mode: mode, state: t.TempDir()}
}

func (server lifecycleServer) configJSON(t *testing.T, extraArgs ...string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable(): %v", err)
	}
	args, _ := json.Marshal(append([]string{"-test.run=^TestAcceptanceLifecycleHelperProcess$"}, extraArgs...))
	env, _ := json.Marshal(map[string]string{lifecycleModeEnv: server.mode, lifecycleStateEnv: server.state})
	return fmt.Sprintf(`{"type":"stdio","command":%q,"args":%s,"env":%s}`, binary, args, env)
}

func (server lifecycleServer) starts() int {
	data, _ := os.ReadFile(filepath.Join(server.state, "starts"))
	return strings.Count(string(data), "\n")
}

// lifecycleCall runs one tool call the way the agent loop does, under ctx.
func lifecycleCall(ctx context.Context, registry *tools.Registry, name string) (tools.Result, time.Duration) {
	start := time.Now()
	result := registry.RunWithOptions(ctx, name, map[string]any{}, tools.RunOptions{PermissionGranted: true})
	return result, time.Since(start)
}

// lifecycleBoundedCall gives the call its own deadline, so a stuck call fails
// the test instead of the suite.
func lifecycleBoundedCall(registry *tools.Registry, name string, bound time.Duration) (tools.Result, time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	return lifecycleCall(ctx, registry, name)
}

// A JSON-RPC error answer (here invalid params, the usual result of a bad
// argument from the model) comes from a live server: the model gets the error
// at once and the process keeps running.
func TestAcceptanceMCPToolErrorAnswerKeepsTheServerProcess(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newLifecycleServer(t, "rpc-error")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":`+server.configJSON(t)+`}`))

	result, elapsed := lifecycleBoundedCall(registry, "mcp_s_a", 10*time.Second)
	if starts := server.starts(); starts != 1 {
		t.Fatalf("one JSON-RPC error answer restarted the server: %d process starts, want 1 (call took %s)", starts, elapsed.Round(100*time.Millisecond))
	}
	if elapsed > 8*time.Second {
		t.Fatalf("one JSON-RPC error answer held the call for %s", elapsed.Round(100*time.Millisecond))
	}
	if result.Status == tools.StatusOK || !strings.Contains(result.Output, "invalid params") {
		t.Fatalf("model did not get the server's error: status=%s output=%q", result.Status, result.Output)
	}
}

// After a crash, the process that replaces it belongs to the session: the
// turn that started it ending (its context cancelled) must not kill it.
func TestAcceptanceMCPRestartedServerOutlivesTheTurn(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newLifecycleServer(t, "crash-once")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":`+server.configJSON(t)+`}`))

	statuses := []tools.Status{}
	var last tools.Result
	for turn := 1; turn <= 3; turn++ {
		ctx, endTurn := context.WithTimeout(context.Background(), 10*time.Second)
		last, _ = lifecycleCall(ctx, registry, "mcp_s_a")
		endTurn()
		statuses = append(statuses, last.Status)
		time.Sleep(time.Second)
	}
	if starts := server.starts(); starts != 2 {
		t.Fatalf("one crash cost %d process starts over three turns, want 2 (original + one restart); statuses per turn %v", starts, statuses)
	}
	if last.Status != tools.StatusOK {
		t.Fatalf("third turn after the restart = %s %q, want ok", last.Status, last.Output)
	}
}

// After a crash, every tool of the server uses the one replacement process
// instead of each tool starting its own.
func TestAcceptanceMCPToolsOfOneServerShareTheRestartedProcess(t *testing.T) {
	acceptanceHermeticEnv(t)
	server := newLifecycleServer(t, "crash-once")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":`+server.configJSON(t)+`}`))
	session, endSession := context.WithTimeout(context.Background(), 40*time.Second)
	defer endSession()

	statuses := []tools.Status{}
	for _, name := range []string{"mcp_s_a", "mcp_s_b", "mcp_s_a", "mcp_s_b"} {
		result, _ := lifecycleCall(session, registry, name)
		statuses = append(statuses, result.Status)
	}
	if starts := server.starts(); starts != 2 {
		t.Fatalf("one crash of a two-tool server cost %d process starts, want 2; statuses a,b,a,b = %v", starts, statuses)
	}
	if statuses[2] != tools.StatusOK || statuses[3] != tools.StatusOK {
		t.Fatalf("calls after the restart = %v, want ok for both tools", statuses[2:])
	}
}

// A server that announced tools/list_changed and then never answers tools/list
// must not hold up calls: neither to another, healthy server nor to its own
// tools, which still answer.
func TestAcceptanceMCPPendingRefreshNeverHoldsUpToolCalls(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("MCP_TIMEOUT", "1000")
	stale, healthy := newLifecycleServer(t, "stale-hang"), newLifecycleServer(t, "ok")
	registry, _ := acceptanceRegister(t, acceptanceConfig(t, `{"s":`+stale.configJSON(t)+`,"o":`+healthy.configJSON(t)+`}`))
	if result, _ := lifecycleBoundedCall(registry, "mcp_s_a", 10*time.Second); result.Status != tools.StatusOK {
		t.Fatalf("precondition: first call to s = %s %q, want ok", result.Status, result.Output)
	}

	other, otherElapsed := lifecycleBoundedCall(registry, "mcp_o_a", 12*time.Second)
	if otherElapsed > 6*time.Second || other.Status != tools.StatusOK {
		t.Fatalf("call to healthy server o took %s (status %s %q) while server s had a refresh it never answers", otherElapsed.Round(100*time.Millisecond), other.Status, other.Output)
	}
	own, ownElapsed := lifecycleBoundedCall(registry, "mcp_s_b", 12*time.Second)
	if ownElapsed > 6*time.Second || own.Status != tools.StatusOK {
		t.Fatalf("call to s's own tool took %s (status %s %q) behind its unanswered refresh", ownElapsed.Round(100*time.Millisecond), own.Status, own.Output)
	}
}

// TestAcceptanceLifecycleHelperProcess is not a test: it is a stdio MCP
// server with tools "a" and "b" and returns at once unless spawned with
// QG_ACCEPT_LIFECYCLE_MODE. Each start appends a line to <state>/starts.
//
// Modes:
//   - "ok": every call answers "ok".
//   - "rpc-error": every call gets a JSON-RPC error (-32602 invalid params).
//   - "crash-once": the first process exits on its first call without
//     answering; later processes answer "ok".
//   - "stale-hang": every call announces tools/list_changed, then answers;
//     only the first process's first tools/list is ever answered.
//   - "echo-args": writes its argv to stderr and exits before initialize, as
//     a server rejecting a credential passed on its command line does.
func TestAcceptanceLifecycleHelperProcess(t *testing.T) {
	mode := os.Getenv(lifecycleModeEnv)
	if mode == "" {
		return
	}
	state := os.Getenv(lifecycleStateEnv)
	startsFile := filepath.Join(state, "starts")
	previous, _ := os.ReadFile(startsFile)
	firstProcess := len(previous) == 0
	appendLine(startsFile, strconv.Itoa(os.Getpid()))
	if mode == "echo-args" {
		fmt.Fprintf(os.Stderr, "fatal: bad credentials in argv %v\n", os.Args[1:])
		os.Exit(1)
	}

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	writer := bufio.NewWriter(os.Stdout)
	write := func(message map[string]any) {
		body, _ := json.Marshal(message)
		_, _ = writer.Write(append(body, '\n'))
		_ = writer.Flush()
	}
	lists := 0
	for scanner.Scan() {
		var request struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == nil {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(request.Params, &params)
			reply["result"] = map[string]any{
				"protocolVersion": params.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo":      map[string]any{"name": "accept-lifecycle", "version": "1"},
			}
		case "tools/list":
			lists++
			if mode == "stale-hang" && (!firstProcess || lists > 1) {
				continue
			}
			reply["result"] = map[string]any{"tools": []map[string]any{
				{"name": "a", "inputSchema": map[string]any{"type": "object"}},
				{"name": "b", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			switch mode {
			case "crash-once":
				if firstProcess {
					os.Exit(3)
				}
			case "stale-hang":
				write(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
			}
			if mode == "rpc-error" {
				reply["error"] = map[string]any{"code": -32602, "message": "invalid params: missing field query"}
			} else {
				reply["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
			}
		default:
			reply["result"] = map[string]any{}
		}
		write(reply)
	}
	os.Exit(0)
}
