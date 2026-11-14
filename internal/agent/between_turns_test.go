package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

type betweenTurnsEchoTool struct{}

func (betweenTurnsEchoTool) Name() string        { return "echo_ok" }
func (betweenTurnsEchoTool) Description() string { return "answers ok" }
func (betweenTurnsEchoTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (betweenTurnsEchoTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (betweenTurnsEchoTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "ok"}
}

// BetweenTurns fires at the top of every turn iteration, before the turn's
// tool partition is computed: the turn driver's between-turn maintenance
// point (MCP catalog refreshes land on the next request, never mid-turn).
func TestBetweenTurnsHookFiresEveryTurn(t *testing.T) {
	var calls atomic.Int32
	registry := tools.NewRegistry()
	registry.Register(betweenTurnsEchoTool{})
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		toolTurn("c1", "echo_ok", `{}`),
		toolTurn("c2", "echo_ok", `{}`),
		textTurn("done"),
	}}
	result, err := Run(context.Background(), "count the turns", provider, Options{
		Registry: registry,
		MaxTurns: 10,
		BetweenTurns: func(context.Context) {
			calls.Add(1)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Turns != 3 {
		t.Fatalf("Turns = %d, want 3", result.Turns)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("BetweenTurns calls = %d, want one per turn (3)", got)
	}
}
