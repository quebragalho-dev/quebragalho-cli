package agent

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/yieldnow"
)

// yieldBlockTool stands in for exec_command's yield wait: it parks on the
// run's yieldnow Signal and returns the normal still-running partial result
// whichever way the wait ends.
type yieldBlockTool struct {
	parked chan struct{}
}

func (yieldBlockTool) Name() string { return "yield_block" }
func (yieldBlockTool) Description() string {
	return "blocks like an exec yield wait"
}
func (yieldBlockTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (yieldBlockTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (t yieldBlockTool) Run(ctx context.Context, _ map[string]any) tools.Result {
	wake, release := yieldnow.FromContext(ctx).Park()
	defer release()
	select {
	case <-t.parked:
	default:
		close(t.parked)
	}
	select {
	case <-wake:
		return tools.Result{Status: tools.StatusOK, Output: "still running (yielded early)"}
	case <-time.After(3 * time.Second):
		return tools.Result{Status: tools.StatusOK, Output: "still running (yield expired)"}
	case <-ctx.Done():
		return tools.Result{Status: tools.StatusError, Output: "context done"}
	}
}

// A send-now Request during a backgroundable tool wait wakes it at once, and
// the operator message drained right after reaches the provider in the SAME
// turn — the turn is never interrupted.
func TestYieldNowWakesToolWaitAndInterleavesSameTurn(t *testing.T) {
	registry := tools.NewRegistry()
	blocker := yieldBlockTool{parked: make(chan struct{}, 1)}
	registry.Register(blocker)
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "c1", ToolName: "yield_block"},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "c1"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	signal := yieldnow.New()
	interleaved := false
	type runOut struct {
		result Result
		err    error
	}
	done := make(chan runOut, 1)
	go func() {
		result, err := Run(context.Background(), "start", provider, Options{
			Registry: registry,
			Cwd:      t.TempDir(),
			YieldNow: signal,
			InterleaveMessages: func() []qgruntime.Message {
				if interleaved {
					return nil
				}
				interleaved = true
				return []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "operator sends now"}}
			},
		})
		done <- runOut{result: result, err: err}
	}()
	select {
	case <-blocker.parked:
	case <-time.After(10 * time.Second):
		t.Fatal("tool wait never parked")
	}
	if !signal.Request() {
		t.Fatal("Request must report the parked tool wait")
	}
	var out runOut
	select {
	case out = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("run did not finish after the yield wake")
	}
	if out.err != nil {
		t.Fatal(out.err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected 2 provider requests in one turn, got %d", len(provider.requests))
	}
	last := provider.requests[1].Messages
	found := false
	for _, message := range last {
		if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "operator sends now") {
			found = true
		}
	}
	if !found {
		t.Fatalf("interleaved message must reach the provider in the same turn: %#v", last)
	}
}

// The real chain, end to end: executeToolCall -> exec_command.run ->
// manager.Start -> collect with the run's Signal in the context. A send-now
// Request during the yield wakes the real `sleep` wait in well under a
// second with the normal still-running partial result, and the process keeps
// running as a background session (proven by its session id). The same holds
// for write_stdin polling a live session. Without a Request the full yield
// runs out.
func TestYieldNowWakesRealExecCommandChain(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses a POSIX shell")
	}
	dir := t.TempDir()
	manager := execution.NewProcessManager(execution.ProcessManagerOptions{})
	registry := tools.NewRegistry()
	registry.Register(tools.NewExecCommandTool(dir, manager))
	registry.Register(tools.NewWriteStdinTool(manager))
	signal := yieldnow.New()
	options := Options{Cwd: dir, YieldNow: signal}

	callTool := func(name, args string) (ToolResult, error) {
		return executeToolCall(context.Background(), registry, ToolCall{ID: "c1", Name: name, Arguments: args}, PermissionModeUnsafe, options)
	}
	waitParked := func(t *testing.T) {
		t.Helper()
		fired := false
		for i := 0; i < 500 && !fired; i++ {
			time.Sleep(10 * time.Millisecond)
			fired = signal.Request()
		}
		if !fired {
			t.Fatal("send-now Request never found the parked exec wait")
		}
	}
	sessionAlive := func(t *testing.T, res ToolResult) int {
		t.Helper()
		raw, ok := res.Meta["session_id"]
		if !ok || raw == "" {
			t.Fatalf("woken wait must return the still-running partial result with a session_id, got meta %#v output %.120q", res.Meta, res.Output)
		}
		id, err := strconv.Atoi(raw)
		if err != nil || id <= 0 {
			t.Fatalf("invalid session_id %q", raw)
		}
		if _, ok := manager.Snapshot(id); !ok {
			t.Fatalf("session %d is gone, the process must keep running in background", id)
		}
		return id
	}

	// exec_command yield wait wakes early; the sleep keeps running.
	type callOut struct {
		res  ToolResult
		err  error
		took time.Duration
	}
	done := make(chan callOut, 1)
	go func() {
		start := time.Now()
		res, err := callTool("exec_command", `{"cmd":"sleep 30","yield_time_ms":30000}`)
		done <- callOut{res: res, err: err, took: time.Since(start)}
	}()
	waitParked(t)
	var out callOut
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("exec_command did not wake on send-now")
	}
	if out.err != nil {
		t.Fatal(out.err)
	}
	if out.took >= 10*time.Second {
		t.Fatalf("woken exec_command took %v, want an early return well before the 30s yield", out.took)
	}
	if !strings.Contains(out.res.Output, "still running") {
		t.Fatalf("woken exec_command must report the still-running partial result, got %.200q", out.res.Output)
	}
	sessionID := sessionAlive(t, out.res)
	t.Cleanup(func() { manager.Stop(sessionID) })

	// write_stdin polling the live session wakes early too.
	donePoll := make(chan callOut, 1)
	go func() {
		start := time.Now()
		res, err := callTool("write_stdin", `{"session_id":`+strconv.Itoa(sessionID)+`,"chars":"","yield_time_ms":30000}`)
		donePoll <- callOut{res: res, err: err, took: time.Since(start)}
	}()
	waitParked(t)
	var poll callOut
	select {
	case poll = <-donePoll:
	case <-time.After(10 * time.Second):
		t.Fatal("write_stdin poll did not wake on send-now")
	}
	if poll.err != nil {
		t.Fatal(poll.err)
	}
	if poll.took >= 10*time.Second {
		t.Fatalf("woken write_stdin poll took %v, want an early return", poll.took)
	}
	if poll.res.Status != tools.StatusOK {
		t.Fatalf("woken write_stdin poll must stay OK, got %#v", poll.res)
	}
	sessionAlive(t, poll.res)
}

// The same real chain without a Request runs the full yield out: attaching
// the Signal alone changes nothing.
func TestRealExecCommandChainWaitsOutYieldWithoutRequest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses a POSIX shell")
	}
	dir := t.TempDir()
	manager := execution.NewProcessManager(execution.ProcessManagerOptions{})
	registry := tools.NewRegistry()
	registry.Register(tools.NewExecCommandTool(dir, manager))
	signal := yieldnow.New()
	options := Options{Cwd: dir, YieldNow: signal}

	start := time.Now()
	res, err := executeToolCall(context.Background(), registry,
		ToolCall{ID: "c1", Name: "exec_command", Arguments: `{"cmd":"sleep 30","yield_time_ms":2000}`},
		PermissionModeUnsafe, options)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took < 1500*time.Millisecond {
		t.Fatalf("without a Request the full yield must run out, took only %v", took)
	}
	raw := res.Meta["session_id"]
	id, convErr := strconv.Atoi(raw)
	if convErr != nil || id <= 0 {
		t.Fatalf("yield expiry must return the still-running partial result with a session_id, got %#v", res.Meta)
	}
	defer manager.Stop(id)
	if _, ok := manager.Snapshot(id); !ok {
		t.Fatal("process must keep running after the normal yield")
	}
}
