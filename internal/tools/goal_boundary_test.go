package tools

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

func goalBoundaryTestSetup(t *testing.T, objective string, tasks ...string) (*sessions.Store, string) {
	t.Helper()
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_boundary"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, objective, 0); err != nil {
		t.Fatal(err)
	}
	if len(tasks) > 0 {
		if _, _, err := store.SetGoalTasks(session.SessionID, tasks); err != nil {
			t.Fatal(err)
		}
	}
	return store, session.SessionID
}

func goalBoundaryUpdate(t *testing.T, store *sessions.Store, sessionID string, args map[string]any) Result {
	t.Helper()
	update := NewGoalTools(store, sessionID)[3]
	return update.Run(context.Background(), args)
}

func TestUpdateGoalCompleteWithPendingTaskSignalsTurnEnd(t *testing.T) {
	store, sessionID := goalBoundaryTestSetup(t, "Work", "First", "Second")

	result := goalBoundaryUpdate(t, store, sessionID, map[string]any{
		"status": "complete", "task_status": "complete", "reason": "done",
	})
	if result.Status != StatusOK {
		t.Fatalf("complete result = %#v", result)
	}
	if result.Meta["control"] != "goal_task_boundary" {
		t.Fatalf("complete with pending task must signal the turn end, meta = %#v", result.Meta)
	}
}

func TestUpdateGoalBlockedWithPendingTaskSignalsTurnEnd(t *testing.T) {
	store, sessionID := goalBoundaryTestSetup(t, "Work", "First", "Second")

	result := goalBoundaryUpdate(t, store, sessionID, map[string]any{
		"status": "blocked", "task_status": "blocked", "reason": "need input",
	})
	if result.Status != StatusOK {
		t.Fatalf("blocked result = %#v", result)
	}
	if result.Meta["control"] != "goal_task_boundary" {
		t.Fatalf("blocked task with pending task must signal the turn end, meta = %#v", result.Meta)
	}
}

func TestUpdateGoalLastTaskDoesNotSignalTurnEnd(t *testing.T) {
	store, sessionID := goalBoundaryTestSetup(t, "Work", "First", "Second")

	first := goalBoundaryUpdate(t, store, sessionID, map[string]any{
		"status": "complete", "task_status": "complete", "reason": "done",
	})
	if first.Status != StatusOK {
		t.Fatalf("first complete result = %#v", first)
	}

	last := goalBoundaryUpdate(t, store, sessionID, map[string]any{
		"status": "complete", "task_status": "complete", "reason": "done",
	})
	if last.Status != StatusOK {
		t.Fatalf("complete result = %#v", last)
	}
	if _, ok := last.Meta["control"]; ok {
		t.Fatalf("last task must not end the turn, meta = %#v", last.Meta)
	}
}

func TestUpdateGoalWholeGoalDoesNotSignalTurnEnd(t *testing.T) {
	store, sessionID := goalBoundaryTestSetup(t, "Work")

	result := goalBoundaryUpdate(t, store, sessionID, map[string]any{
		"status": "complete",
	})
	if result.Status != StatusOK {
		t.Fatalf("complete result = %#v", result)
	}
	if _, ok := result.Meta["control"]; ok {
		t.Fatalf("taskless goal must not end the turn, meta = %#v", result.Meta)
	}
}

func TestSetGoalTasksDoesNotSignalTurnEnd(t *testing.T) {
	store, sessionID := goalBoundaryTestSetup(t, "Work")
	set := NewGoalTools(store, sessionID)[2]

	result := set.Run(context.Background(), map[string]any{"tasks": []any{"First", "Second"}})
	if result.Status != StatusOK {
		t.Fatalf("set result = %#v", result)
	}
	if _, ok := result.Meta["control"]; ok {
		t.Fatalf("set_goal_tasks must not end the turn, meta = %#v", result.Meta)
	}
}
