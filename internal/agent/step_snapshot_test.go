package agent

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

type stepEchoTool struct{}

func (stepEchoTool) Name() string { return "step_echo" }
func (stepEchoTool) Description() string {
	return "echo"
}
func (stepEchoTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (stepEchoTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (stepEchoTool) Run(_ context.Context, _ map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "echo-out"}
}

func TestRunCallsOnStepMessagesPerStep(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(stepEchoTool{})
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "c1", ToolName: "step_echo"},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "c1"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	var steps [][]qgruntime.Message
	result, err := Run(context.Background(), "go", provider, Options{
		Registry: registry,
		Cwd:      t.TempDir(),
		OnStepMessages: func(messages []qgruntime.Message, loaded []string) {
			cp := append([]qgruntime.Message(nil), messages...)
			steps = append(steps, cp)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) == 0 {
		t.Fatal("expected OnStepMessages to fire")
	}
	last := steps[len(steps)-1]
	if len(last) != len(result.Messages) {
		t.Fatalf("last step messages = %d, result = %d", len(last), len(result.Messages))
	}
	foundTool := false
	for _, m := range last {
		if m.Role == qgruntime.MessageRoleTool && m.ToolCallID == "c1" {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatalf("step snapshot must carry the tool result: %#v", last)
	}
}
