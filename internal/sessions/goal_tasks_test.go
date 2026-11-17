package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseGoalTextNumberedList(t *testing.T) {
	objective, tasks := ParseGoalText("Ship the release\n1. Write code\n2) Test it\n3. Deploy")
	if objective != "Ship the release" {
		t.Fatalf("objective = %q", objective)
	}
	if len(tasks) != 3 {
		t.Fatalf("tasks = %#v", tasks)
	}
	if tasks[0].Title != "Write code" || tasks[0].ID != "t1" || tasks[0].Status != GoalTaskActive {
		t.Fatalf("first task = %#v", tasks[0])
	}
	if tasks[1].Title != "Test it" || tasks[1].Status != GoalTaskPending {
		t.Fatalf("second task = %#v", tasks[1])
	}
	if tasks[2].Title != "Deploy" || tasks[2].ID != "t3" {
		t.Fatalf("third task = %#v", tasks[2])
	}
}

func TestParseGoalTextBullets(t *testing.T) {
	objective, tasks := ParseGoalText("Organizar a casa\n- lavar a louça\n* varrer o chão\n+ tirar o lixo")
	if objective != "Organizar a casa" {
		t.Fatalf("objective = %q", objective)
	}
	if len(tasks) != 3 || tasks[0].Title != "lavar a louça" || tasks[2].Title != "tirar o lixo" {
		t.Fatalf("tasks = %#v", tasks)
	}
}

func TestParseGoalTextSingleSentenceHasNoTasks(t *testing.T) {
	objective, tasks := ParseGoalText("Ship the release")
	if objective != "Ship the release" || tasks != nil {
		t.Fatalf("single sentence = %q %#v", objective, tasks)
	}
}

func TestParseGoalTextWithoutObjectiveSummarizesTitles(t *testing.T) {
	objective, tasks := ParseGoalText("1. Write code\n2. Test it")
	if len(tasks) != 2 {
		t.Fatalf("tasks = %#v", tasks)
	}
	if !strings.Contains(objective, "Write code") || !strings.Contains(objective, "Test it") {
		t.Fatalf("objective summary = %q", objective)
	}
}

func TestParseGoalTextStripsCheckboxes(t *testing.T) {
	_, tasks := ParseGoalText("Meta\n- [ ] open item\n- [x] finished item")
	if len(tasks) != 2 {
		t.Fatalf("tasks = %#v", tasks)
	}
	if tasks[0].Title != "open item" || tasks[0].Status != GoalTaskActive {
		t.Fatalf("open task = %#v", tasks[0])
	}
	if tasks[1].Title != "finished item" || tasks[1].Status != GoalTaskDone {
		t.Fatalf("checked task = %#v", tasks[1])
	}
}

func TestCreateGoalParsesMultilineIntoTasks(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "goal_multiline"})
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.CreateGoal(session.SessionID, "Ship it\n1. Code\n2. Test", 0)
	if err != nil {
		t.Fatal(err)
	}
	if created.Goal.Objective != "Ship it" {
		t.Fatalf("objective = %q", created.Goal.Objective)
	}
	if len(created.Goal.Tasks) != 2 || CurrentGoalTask(created.Goal).Title != "Code" {
		t.Fatalf("tasks = %#v", created.Goal.Tasks)
	}
}

func TestLegacyGoalOnDiskHasNoTasks(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(StoreOptions{RootDir: dir})
	session, err := store.Create(CreateInput{SessionID: "legacy_goal"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Old objective", 0); err != nil {
		t.Fatal(err)
	}
	// Simulate a session file written before task support: drop the tasks key.
	data, err := os.ReadFile(filepath.Join(dir, session.SessionID, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	goal, ok := decoded["goal"].(map[string]any)
	if !ok {
		t.Fatalf("metadata has no goal: %s", data)
	}
	delete(goal, "tasks")
	decoded["goal"] = goal
	rewritten, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, session.SessionID, "metadata.json"), rewritten, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(StoreOptions{RootDir: dir}).Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal == nil || loaded.Goal.Tasks != nil {
		t.Fatalf("legacy goal = %#v", loaded.Goal)
	}
	if CurrentGoalTask(loaded.Goal) != nil {
		t.Fatalf("legacy goal should have no current task: %#v", loaded.Goal)
	}
	if GoalTasksPending(loaded.Goal) {
		t.Fatal("legacy goal should report no pending tasks")
	}
	// Whole-goal completion still works without tasks.
	updated, _, err := NewStore(StoreOptions{RootDir: dir}).UpdateGoal(session.SessionID, GoalStatusComplete, "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Goal.Status != GoalStatusComplete {
		t.Fatalf("legacy completion = %#v", updated.Goal)
	}
}

func TestCompleteGoalTaskAdvancesAndResetsLimit(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "goal_advance"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Work\n1. First\n2. Second", 0); err != nil {
		t.Fatal(err)
	}
	// Burn part of the per-task continuation allowance.
	if _, _, ok, err := store.ReserveGoalContinuation(session.SessionID); err != nil || !ok {
		t.Fatalf("reserve = %v %v", ok, err)
	}
	advanced, _, err := store.CompleteGoalTask(session.SessionID, "done quickly")
	if err != nil {
		t.Fatal(err)
	}
	if advanced.Goal.Tasks[0].Status != GoalTaskDone || advanced.Goal.Tasks[0].Note != "done quickly" {
		t.Fatalf("first task = %#v", advanced.Goal.Tasks[0])
	}
	if advanced.Goal.Tasks[1].Status != GoalTaskActive {
		t.Fatalf("second task = %#v", advanced.Goal.Tasks[1])
	}
	if advanced.Goal.ContinuationCount != 0 {
		t.Fatalf("continuation count did not reset on task switch: %d", advanced.Goal.ContinuationCount)
	}
	if advanced.Goal.Status != GoalStatusActive {
		t.Fatalf("goal status = %q", advanced.Goal.Status)
	}
	// Finishing the last task completes the whole goal.
	finished, _, err := store.CompleteGoalTask(session.SessionID, "")
	if err != nil {
		t.Fatal(err)
	}
	if finished.Goal.Status != GoalStatusComplete {
		t.Fatalf("finished goal = %#v", finished.Goal)
	}
	done, total := GoalTaskProgress(finished.Goal)
	if done != 2 || total != 2 {
		t.Fatalf("progress = %d/%d", done, total)
	}
}

func TestUpdateGoalRefusesCompleteWithPendingTasks(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "goal_refuse"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Work\n1. First\n2. Second", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpdateGoal(session.SessionID, GoalStatusComplete, ""); err == nil ||
		!strings.Contains(err.Error(), "pending") {
		t.Fatalf("complete with pending tasks error = %v", err)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal.Status != GoalStatusActive {
		t.Fatalf("refused completion mutated goal: %#v", loaded.Goal)
	}
}

func TestBlockGoalTaskPausesAndResumeRetries(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "goal_block"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Work\n1. First\n2. Second", 0); err != nil {
		t.Fatal(err)
	}
	blocked, _, err := store.BlockGoalTask(session.SessionID, "need credentials")
	if err != nil {
		t.Fatal(err)
	}
	if blocked.Goal.Status != GoalStatusPaused || blocked.Goal.StatusReason != "need credentials" {
		t.Fatalf("blocked goal = %#v", blocked.Goal)
	}
	current := CurrentGoalTask(blocked.Goal)
	if current == nil || current.Status != GoalTaskBlocked || current.Note != "need credentials" {
		t.Fatalf("blocked task = %#v", current)
	}
	if blocked.Goal.Tasks[0].Status != GoalTaskBlocked || blocked.Goal.Tasks[1].Status != GoalTaskPending {
		t.Fatalf("blocked tasks = %#v", blocked.Goal.Tasks)
	}
	// Resume retries the blocked task.
	resumed, _, err := store.UpdateGoal(session.SessionID, GoalStatusActive, "")
	if err != nil {
		t.Fatal(err)
	}
	reactivated, _, err := store.ReactivateGoalTask(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	_ = resumed
	if reactivated.Goal.Tasks[0].Status != GoalTaskActive {
		t.Fatalf("resumed task = %#v", reactivated.Goal.Tasks[0])
	}
}

func TestSetGoalTasksValidatesBounds(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "goal_split"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateGoal(session.SessionID, "Single sentence", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SetGoalTasks(session.SessionID, []string{"only one"}); err == nil {
		t.Fatal("one task should be rejected")
	}
	many := make([]string, 13)
	for i := range many {
		many[i] = "task"
	}
	if _, _, err := store.SetGoalTasks(session.SessionID, many); err == nil {
		t.Fatal("thirteen tasks should be rejected")
	}
	split, _, err := store.SetGoalTasks(session.SessionID, []string{"one", "two", "three"})
	if err != nil {
		t.Fatal(err)
	}
	if len(split.Goal.Tasks) != 3 || split.Goal.Tasks[0].Status != GoalTaskActive {
		t.Fatalf("split tasks = %#v", split.Goal.Tasks)
	}
	if _, _, err := store.SetGoalTasks(session.SessionID, []string{"a", "b"}); err == nil {
		t.Fatal("second split should be refused")
	}
}
