package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

type goalBoundarySpyTool struct {
	calls int
}

func (*goalBoundarySpyTool) Name() string        { return "spy_tool" }
func (*goalBoundarySpyTool) Description() string { return "records whether the loop ran it" }
func (*goalBoundarySpyTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object"}
}
func (*goalBoundarySpyTool) Safety() tools.Safety {
	return tools.Safety{Permission: tools.PermissionAllow, SideEffect: tools.SideEffectRead}
}
func (tool *goalBoundarySpyTool) Run(context.Context, map[string]any) tools.Result {
	tool.calls++
	return tools.Result{Status: tools.StatusOK, Output: "spy ran"}
}

func goalBoundaryTestRegistry(t *testing.T, sessionID string, store *sessions.Store, spy *goalBoundarySpyTool) *tools.Registry {
	t.Helper()
	registry := tools.NewRegistry()
	for _, tool := range tools.NewGoalTools(store, sessionID) {
		registry.Register(tool)
	}
	registry.Register(spy)
	return registry
}

func goalBoundaryTestSession(t *testing.T, tasks ...string) (*sessions.Store, string) {
	t.Helper()
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_boundary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Work", 0); err != nil {
		t.Fatal(err)
	}
	if len(tasks) > 0 {
		if _, _, err := store.SetGoalTasks(session.SessionID, tasks); err != nil {
			t.Fatal(err)
		}
	}
	return store, session.SessionID
}

func goalBoundaryBatchTurn(updateArgs string) []qgruntime.StreamEvent {
	return []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "update-1", ToolName: "update_goal"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "update-1", ArgumentsFragment: updateArgs},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "update-1"},
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "spy-1", ToolName: "spy_tool"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "spy-1", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "spy-1"},
		{Type: qgruntime.StreamEventDone},
	}
}

// The model finishes the current task while more tasks remain: the turn must
// end there (no new provider call), and the rest of the batch must not run.
func TestRunEndsTurnWhenGoalTaskCompletesWithPendingWork(t *testing.T) {
	store, sessionID := goalBoundaryTestSession(t, "First", "Second")
	spy := &goalBoundarySpyTool{}
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		goalBoundaryBatchTurn(`{"status":"complete","task_status":"complete","reason":"done"}`),
		{{Type: qgruntime.StreamEventDone}},
	}}

	result, err := Run(context.Background(), "work", provider, Options{
		Registry:       goalBoundaryTestRegistry(t, sessionID, store, spy),
		PermissionMode: PermissionModeAuto,
		MaxTurns:       5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != StopReasonGoalTaskBoundary {
		t.Fatalf("StopReason = %q, want %q", result.StopReason, StopReasonGoalTaskBoundary)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 (turn ends at update_goal)", len(provider.requests))
	}
	if spy.calls != 0 {
		t.Fatalf("spy tool ran %d times, want 0 (same-batch calls after update_goal do not run)", spy.calls)
	}
	paired := false
	for _, message := range result.Messages {
		if message.Role == qgruntime.MessageRoleTool && message.ToolCallID == "spy-1" && strings.Contains(message.Content, "aborted") {
			paired = true
		}
	}
	if !paired {
		t.Fatalf("skipped batch call has no paired tool result, messages = %#v", result.Messages)
	}
}

func TestRunEndsTurnWhenGoalTaskBlocksWithPendingWork(t *testing.T) {
	store, sessionID := goalBoundaryTestSession(t, "First", "Second")
	spy := &goalBoundarySpyTool{}
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		goalBoundaryBatchTurn(`{"status":"blocked","task_status":"blocked","reason":"need input"}`),
		{{Type: qgruntime.StreamEventDone}},
	}}

	result, err := Run(context.Background(), "work", provider, Options{
		Registry:       goalBoundaryTestRegistry(t, sessionID, store, spy),
		PermissionMode: PermissionModeAuto,
		MaxTurns:       5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != StopReasonGoalTaskBoundary {
		t.Fatalf("StopReason = %q, want %q", result.StopReason, StopReasonGoalTaskBoundary)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 (turn ends at update_goal)", len(provider.requests))
	}
	if spy.calls != 0 {
		t.Fatalf("spy tool ran %d times, want 0", spy.calls)
	}
}

func TestRunContinuesWhenGoalTaskCompletesLastTask(t *testing.T) {
	store, sessionID := goalBoundaryTestSession(t, "First", "Second")
	if _, _, err := store.CompleteGoalTask(sessionID, "first done"); err != nil {
		t.Fatal(err)
	}
	spy := &goalBoundarySpyTool{}
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		goalBoundaryBatchTurn(`{"status":"complete","task_status":"complete","reason":"done"}`),
		{
			{Type: qgruntime.StreamEventText, Content: "all done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}

	result, err := Run(context.Background(), "work", provider, Options{
		Registry:       goalBoundaryTestRegistry(t, sessionID, store, spy),
		PermissionMode: PermissionModeAuto,
		MaxTurns:       5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != "" {
		t.Fatalf("StopReason = %q, want empty (last task closes the run itself)", result.StopReason)
	}
	if spy.calls != 1 {
		t.Fatalf("spy tool ran %d times, want 1 (batch continues on the last task)", spy.calls)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	if result.FinalAnswer != "all done" {
		t.Fatalf("FinalAnswer = %q, want model summary", result.FinalAnswer)
	}
}

func TestRunContinuesAfterSetGoalTasks(t *testing.T) {
	store, sessionID := goalBoundaryTestSession(t)
	spy := &goalBoundarySpyTool{}
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "set-1", ToolName: "set_goal_tasks"},
			{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "set-1", ArgumentsFragment: `{"tasks":["First","Second"]}`},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "set-1"},
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "spy-1", ToolName: "spy_tool"},
			{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "spy-1", ArgumentsFragment: `{}`},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "spy-1"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "starting first"},
			{Type: qgruntime.StreamEventDone},
		},
	}}

	result, err := Run(context.Background(), "work", provider, Options{
		Registry:       goalBoundaryTestRegistry(t, sessionID, store, spy),
		PermissionMode: PermissionModeAuto,
		MaxTurns:       5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != "" {
		t.Fatalf("StopReason = %q, want empty (first task starts in the same run)", result.StopReason)
	}
	if spy.calls != 1 {
		t.Fatalf("spy tool ran %d times, want 1", spy.calls)
	}
}
