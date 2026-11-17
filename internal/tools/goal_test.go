package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

func TestGoalToolsAreBoundToTheirSession(t *testing.T) {
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	first, err := store.Create(sessions.CreateInput{SessionID: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Create(sessions.CreateInput{SessionID: "second"})
	if err != nil {
		t.Fatal(err)
	}
	goalTools := NewGoalTools(store, first.SessionID)

	result := goalTools[1].Run(context.Background(), map[string]any{"objective": "Finish first"})
	if result.Status != StatusOK {
		t.Fatalf("create result = %#v", result)
	}
	loadedFirst, _ := store.Get(first.SessionID)
	loadedSecond, _ := store.Get(second.SessionID)
	if loadedFirst.Goal == nil || loadedFirst.Goal.Objective != "Finish first" {
		t.Fatalf("first goal = %#v", loadedFirst.Goal)
	}
	if loadedSecond.Goal != nil {
		t.Fatalf("second goal unexpectedly changed = %#v", loadedSecond.Goal)
	}
}

func TestCreateGoalToolDeclaresTokenBudgetMaximum(t *testing.T) {
	create := NewGoalTools(nil, "goal")[1]
	maximum := create.Parameters().Properties["token_budget"].Maximum
	if maximum == nil || *maximum != 1_000_000_000 {
		t.Fatalf("token_budget maximum = %v, want 1000000000", maximum)
	}
	maxLength := create.Parameters().Properties["objective"].MaxLength
	if maxLength == nil || *maxLength != sessions.GoalObjectiveMaxLength {
		t.Fatalf("objective maxLength = %v, want %d", maxLength, sessions.GoalObjectiveMaxLength)
	}
	safety := create.Safety()
	if safety.SideEffect != SideEffectWrite || safety.Permission != PermissionPrompt || !safety.AdvertiseInAuto {
		t.Fatalf("create_goal safety = %#v, want prompted persistent write advertised in auto mode", safety)
	}
}

func TestUpdateGoalToolRestrictsAgentTransitions(t *testing.T) {
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{SessionID: "goal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Finish it", 0); err != nil {
		t.Fatal(err)
	}
	update := NewGoalTools(store, session.SessionID)[3]

	result := update.Run(context.Background(), map[string]any{"status": "paused"})
	if result.Status != StatusError || !strings.Contains(result.Output, "complete") {
		t.Fatalf("paused result = %#v", result)
	}
	result = update.Run(context.Background(), map[string]any{"status": "blocked"})
	if result.Status != StatusError || !strings.Contains(result.Output, "require a reason") {
		t.Fatalf("reasonless blocked result = %#v", result)
	}
	result = update.Run(context.Background(), map[string]any{"status": "complete"})
	if result.Status != StatusOK {
		t.Fatalf("complete result = %#v", result)
	}
}

func TestSetGoalTasksToolSplitsSingleObjective(t *testing.T) {
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_split"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Finish it", 0); err != nil {
		t.Fatal(err)
	}
	tools := NewGoalTools(store, session.SessionID)
	if len(tools) != 4 {
		t.Fatalf("goal tools = %d, want 4 (get, create, set_goal_tasks, update)", len(tools))
	}
	set := tools[2]
	if set.Name() != "set_goal_tasks" {
		t.Fatalf("third goal tool = %q, want set_goal_tasks", set.Name())
	}
	result := set.Run(context.Background(), map[string]any{"tasks": []any{"one", "two"}})
	if result.Status != StatusOK {
		t.Fatalf("split result = %#v", result)
	}
	for _, want := range []string{"[active] one", "[pending] two"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("split result missing %q: %q", want, result.Output)
		}
	}
	result = set.Run(context.Background(), map[string]any{"tasks": []any{"only"}})
	if result.Status != StatusError {
		t.Fatalf("single task split = %#v, want error", result)
	}
}

func TestUpdateGoalToolCompletesCurrentTask(t *testing.T) {
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_task_advance"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Work\n1. First\n2. Second", 0); err != nil {
		t.Fatal(err)
	}
	update := NewGoalTools(store, session.SessionID)[3]
	result := update.Run(context.Background(), map[string]any{"status": "complete", "task_status": "complete", "reason": "first notes"})
	if result.Status != StatusOK {
		t.Fatalf("task complete result = %#v", result)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal.Tasks[0].Status != sessions.GoalTaskDone || loaded.Goal.Tasks[0].Note != "first notes" {
		t.Fatalf("first task = %#v", loaded.Goal.Tasks[0])
	}
	if loaded.Goal.Tasks[1].Status != sessions.GoalTaskActive {
		t.Fatalf("second task = %#v", loaded.Goal.Tasks[1])
	}
	if loaded.Goal.Status != sessions.GoalStatusActive {
		t.Fatalf("goal status = %q, want active while tasks remain", loaded.Goal.Status)
	}
}

func TestUpdateGoalToolRefusesWholeGoalCompleteWithPendingTasks(t *testing.T) {
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_task_refuse"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Work\n1. First\n2. Second", 0); err != nil {
		t.Fatal(err)
	}
	update := NewGoalTools(store, session.SessionID)[3]
	result := update.Run(context.Background(), map[string]any{"status": "complete"})
	if result.Status != StatusError || !strings.Contains(result.Output, "pending") {
		t.Fatalf("whole-goal complete result = %#v, want pending-tasks refusal", result)
	}
}

func TestUpdateGoalToolBlocksCurrentTask(t *testing.T) {
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_task_block"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Work\n1. First\n2. Second", 0); err != nil {
		t.Fatal(err)
	}
	update := NewGoalTools(store, session.SessionID)[3]
	result := update.Run(context.Background(), map[string]any{"status": "blocked", "task_status": "blocked"})
	if result.Status != StatusError || !strings.Contains(result.Output, "require a reason") {
		t.Fatalf("reasonless task block = %#v", result)
	}
	result = update.Run(context.Background(), map[string]any{"status": "blocked", "task_status": "blocked", "reason": "need input"})
	if result.Status != StatusOK {
		t.Fatalf("task block result = %#v", result)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal.Status != sessions.GoalStatusPaused {
		t.Fatalf("blocked goal status = %q", loaded.Goal.Status)
	}
	if current := sessions.CurrentGoalTask(loaded.Goal); current == nil || current.Status != sessions.GoalTaskBlocked {
		t.Fatalf("blocked task = %#v", current)
	}
	if loaded.Goal.Tasks[1].Status != sessions.GoalTaskPending {
		t.Fatalf("finished tasks must stay finished: %#v", loaded.Goal.Tasks)
	}
}
