package agent

import (
	"context"
	"strings"
	"testing"
	"time"

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
