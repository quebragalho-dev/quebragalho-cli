package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

var goalTaskTestSeq int

func goalTaskTestSetup(t *testing.T, objective string) (*sessions.Store, sessions.Metadata) {
	t.Helper()
	goalTaskTestSeq++
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: fmt.Sprintf("goal_tasks_%d", goalTaskTestSeq)})
	if err != nil {
		t.Fatal(err)
	}
	// A manual title keeps the background auto-titler from spending provider
	// requests mid-test (it would shift the scripted provider's script index).
	session, err = store.UpdateTitle(session.SessionID, "Manual test title")
	if err != nil {
		t.Fatal(err)
	}
	session, _, err = store.CreateGoal(session.SessionID, objective, 0)
	if err != nil {
		t.Fatal(err)
	}
	return store, session
}

func goalTaskTestModel(store *sessions.Store, session sessions.Metadata, provider *scriptedProvider) model {
	m := newModel(context.Background(), Options{
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session
	return m
}

func updatedModel(t *testing.T, m model, msg tea.Msg) (model, tea.Cmd) {
	t.Helper()
	out, cmd := m.Update(msg)
	next, ok := out.(model)
	if !ok {
		t.Fatalf("Update returned %T, want model", out)
	}
	return next, cmd
}

// execAgentResponses executes every sub-command in a (possibly batched) tea
// command and returns the agent responses in order, ignoring spinner ticks
// and unrelated messages. Test models leave background commands (title,
// sweep, recap, PR refresh) unconfigured, so walking the whole batch stays
// side-effect free.
func execAgentResponses(cmd tea.Cmd) []agentResponseMsg {
	var out []agentResponseMsg
	var walk func(c tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			for _, sub := range msg {
				walk(sub)
			}
		case agentResponseMsg:
			out = append(out, msg)
		}
	}
	walk(cmd)
	return out
}

func updatedModel2(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	next, _ := updatedModel(t, m, msg)
	if next.runCancel != nil {
		next.runCancel()
	}
	return next
}

func TestGoalCreateMultilineListsTasks(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_create_tasks"})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Provider:     &scriptedProvider{},
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session

	next, cmd := m.handleGoalCommand("Ship it\n1. Code\n2. Test")
	if cmd == nil || !next.pending {
		t.Fatal("multiline goal should start its first run")
	}
	if next.activeSession.Goal == nil || len(next.activeSession.Goal.Tasks) != 2 {
		t.Fatalf("goal tasks = %#v", next.activeSession.Goal)
	}
	if !transcriptContains(next.transcript, "tarefas (0/2 feitas)") ||
		!transcriptContains(next.transcript, "[ativa] Code") {
		t.Fatalf("task list missing from transcript: %#v", next.transcript)
	}
	if next.runCancel != nil {
		next.runCancel()
	}
}

func TestGoalStatusListsTasks(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	m := goalTaskTestModel(store, session, &scriptedProvider{})

	next, _ := m.handleGoalCommand("status")
	if !transcriptContains(next.transcript, "tarefas (0/2 feitas)") ||
		!transcriptContains(next.transcript, "[ativa] First") ||
		!transcriptContains(next.transcript, "[pendente] Second") {
		t.Fatalf("status missing tasks: %#v", next.transcript)
	}

	legacyStore := testSessionStore(t)
	legacySession, err := legacyStore.Create(sessions.CreateInput{SessionID: "legacy_status"})
	if err != nil {
		t.Fatal(err)
	}
	legacySession, _, err = legacyStore.CreateGoal(legacySession.SessionID, "Single sentence", 0)
	if err != nil {
		t.Fatal(err)
	}
	legacy := goalTaskTestModel(legacyStore, legacySession, &scriptedProvider{})
	legacyNext, _ := legacy.handleGoalCommand("status")
	if transcriptContains(legacyNext.transcript, "tarefas") {
		t.Fatalf("legacy status should not list tasks: %#v", legacyNext.transcript)
	}
}

func TestGoalFooterShowsTaskProgress(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second\n3. Third")
	m := goalTaskTestModel(store, session, &scriptedProvider{})
	if summary := m.goalFooterSummary(); summary != "meta 0/3 First · ativa" {
		t.Fatalf("footer = %q", summary)
	}
	if _, _, err := store.CompleteGoalTask(session.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	m.activeSession = *loaded
	if summary := m.goalFooterSummary(); summary != "meta 1/3 Second · ativa" {
		t.Fatalf("footer after advance = %q", summary)
	}
	legacy := newModel(context.Background(), Options{})
	legacy.activeSession.Goal = &sessions.Goal{Objective: "Ship", Status: sessions.GoalStatusActive}
	if summary := legacy.goalFooterSummary(); summary != "meta ativa" {
		t.Fatalf("legacy footer = %q", summary)
	}
}

func TestGoalStatuslineShowsTaskProgress(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second\n3. Third")
	if _, _, err := store.CompleteGoalTask(session.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	m := goalTaskTestModel(store, *loaded, &scriptedProvider{})
	status := plainRender(t, m.statusLine(51))
	if !strings.Contains(status, "1/3") || !strings.Contains(status, "Second") {
		t.Fatalf("narrow status missing task progress: %q", status)
	}
}

// completeCurrentTaskScript ends the current task. The run stops right
// after update_goal when more tasks remain, so this script is one turn.
func completeCurrentTaskScript(reason string) []qgruntime.StreamEvent {
	return []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "task_done", ToolName: "update_goal"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "task_done", ArgumentsFragment: `{"status":"complete","task_status":"complete","reason":"` + reason + `"}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "task_done"},
		{Type: qgruntime.StreamEventDone},
	}
}

// completeCurrentTaskWithHistoryScript ends the current task after streaming
// historyText in the same turn, so the finished task era carries history the
// between-tasks mode must then cut (reset) or preserve (keep).
func completeCurrentTaskWithHistoryScript(reason, historyText string) []qgruntime.StreamEvent {
	return []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: historyText},
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "task_done", ToolName: "update_goal"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "task_done", ArgumentsFragment: `{"status":"complete","task_status":"complete","reason":"` + reason + `"}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "task_done"},
		{Type: qgruntime.StreamEventDone},
	}
}

const goalHistoryMarker = "TEXTO-ANTIGO-JACA-456"

func goalFillerScript() []qgruntime.StreamEvent {
	return []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: goalHistoryMarker},
		{Type: qgruntime.StreamEventDone},
	}
}

func requestContents(request qgruntime.CompletionRequest) string {
	var parts []string
	for _, message := range request.Messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

func TestGoalKeepModePreservesHistoryBetweenTasks(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskWithHistoryScript("feito", goalHistoryMarker),
		goalFillerScript(),
		{
			{Type: qgruntime.StreamEventText, Content: "second task"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := goalTaskTestModel(store, session, provider)
	m.goalBetweenTasks = "keep"

	next, cmd := m.launchGoalContinuationIfReady()
	if cmd == nil || !next.pending {
		t.Fatal("first task run did not launch")
	}
	resp := execCmd(cmd)
	if resp == nil {
		t.Fatal("first run returned no response")
	}
	// The task boundary ends the run: one provider call, no second turn.
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 (run ends at update_goal)", len(provider.requests))
	}
	updated, goalCmd := updatedModel(t, next, resp)
	if goalCmd == nil || !updated.pending {
		t.Fatalf("next task did not launch: pending=%v transcript=%#v", updated.pending, updated.transcript)
	}
	if !transcriptContains(updated.transcript, "Tarefa concluída. A próxima começa em seguida.") {
		t.Fatalf("boundary closing line missing from transcript: %#v", updated.transcript)
	}
	if !transcriptContains(updated.transcript, "Tarefa concluída: First. Próxima (2/2): Second.") {
		t.Fatalf("task switch missing from transcript: %#v", updated.transcript)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessions.CurrentGoalTask(loaded.Goal); got == nil || got.Title != "Second" {
		t.Fatalf("current task = %#v", got)
	}
	if loaded.Goal.ContinuationCount != 1 {
		t.Fatalf("continuation count = %d, want 1 (reset on switch, reserved by next run)", loaded.Goal.ContinuationCount)
	}
	// Drive the next-task run, then the one it queues: the last request
	// still carries the finished task era, one task per run.
	resp2 := execCmd(goalCmd)
	if resp2 == nil {
		t.Fatal("second run returned no response")
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (one turn per run)", len(provider.requests))
	}
	second, thirdCmd := updatedModel(t, updated, resp2)
	if thirdCmd == nil || !second.pending {
		t.Fatal("second run did not queue the next step")
	}
	resp3 := execCmd(thirdCmd)
	if resp3 == nil {
		t.Fatal("third run returned no response")
	}
	if len(provider.requests) != 3 {
		t.Fatalf("provider requests = %d, want 3 (one turn per run)", len(provider.requests))
	}
	contents := requestContents(provider.requests[len(provider.requests)-1])
	if !strings.Contains(contents, goalHistoryMarker) {
		t.Fatalf("keep mode should preserve history, request =\n%s", contents)
	}
	if !strings.Contains(contents, "Second") {
		t.Fatalf("keep mode request should name the current task:\n%s", contents)
	}
	// The third run sees an unfinished goal, so it queues a fourth run; drop
	// it without executing — the request assertions above are the point.
	finished := updatedModel2(t, second, resp3)
	if !finished.pending {
		t.Fatal("unfinished goal should keep working")
	}
}

func continuationContents(messages []qgruntime.Message) string {
	var parts []string
	for _, message := range messages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}

func TestGoalResetModeClearsHistoryBetweenTasks(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventText, Content: "ok, anotado"},
			{Type: qgruntime.StreamEventDone},
		},
		completeCurrentTaskWithHistoryScript("feito", goalHistoryMarker),
		{
			{Type: qgruntime.StreamEventText, Content: "second task, turn one"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "user submit reply"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := goalTaskTestModel(store, session, provider)
	m.goalBetweenTasks = "reset"

	// Task-one history enters through a real user submit: only normal submits
	// populate the structured wire (goal runs never store one).
	m.input.SetValue("detalhe da tarefa um: " + goalHistoryMarker)
	submitted, submitCmd := updatedModel(t, m, testKey(tea.KeyEnter))
	if submitCmd == nil || !submitted.pending {
		t.Fatal("user submit did not start a run")
	}
	afterSubmit, goalCmd := updatedModel(t, submitted, execCmd(submitCmd))
	if goalCmd == nil {
		t.Fatal("submit end did not auto-launch the goal continuation")
	}
	if wireContents := continuationContents(afterSubmit.continuation.messages); !strings.Contains(wireContents, goalHistoryMarker) {
		t.Fatalf("precondition: submit wire missing task-one marker:\n%s", wireContents)
	}
	// The auto-launched continuation completes task one in a single turn:
	// the run ends at the task boundary and the switch cuts it.
	switched, taskTwoCmd := updatedModel(t, afterSubmit, execCmd(goalCmd))
	if taskTwoCmd == nil || !switched.pending {
		t.Fatal("reset-mode next task did not launch")
	}
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (submit plus one single-turn continuation)", len(provider.requests))
	}
	if !transcriptContains(switched.transcript, "Contexto limpo entre tarefas") {
		t.Fatalf("reset notice missing: %#v", switched.transcript)
	}
	if cutContents := continuationContents(switched.continuation.messages); strings.Contains(cutContents, goalHistoryMarker) {
		t.Fatalf("cut wire replays previous task history:\n%s", cutContents)
	}
	// One task-two continuation still filters the previous task out.
	taskTwoResp := execCmd(taskTwoCmd)
	if taskTwoResp == nil {
		t.Fatal("task-two continuation returned no response")
	}
	afterTwo, _ := updatedModel(t, switched, taskTwoResp)
	latest := requestContents(provider.requests[len(provider.requests)-1])
	for _, want := range []string{"Objective: Work", "First", "Second", "[done]", "Current task context"} {
		if !strings.Contains(latest, want) {
			t.Fatalf("reset request missing %q:\n%s", want, latest)
		}
	}
	if strings.Contains(latest, goalHistoryMarker) {
		t.Fatalf("task-two request replays previous task history:\n%s", latest)
	}
	// Park the chained continuation so a real user message can go through.
	if afterTwo.runCancel != nil {
		afterTwo.runCancel()
	}
	afterTwo.runCancel = nil
	afterTwo.pending = false
	afterTwo.activeRunID = 0
	afterTwo.input.SetValue("e a tarefa dois?")
	_, idleCmd := updatedModel(t, afterTwo, testKey(tea.KeyEnter))
	if idleCmd == nil {
		t.Fatal("idle user submit did not start a run")
	}
	if userResp := execCmd(idleCmd); userResp == nil {
		t.Fatal("user submit returned no response")
	}
	userContents := requestContents(provider.requests[len(provider.requests)-1])
	if strings.Contains(userContents, goalHistoryMarker) {
		t.Fatalf("user submit replays previous task history:\n%s", userContents)
	}
	if !strings.Contains(userContents, "e a tarefa dois?") {
		t.Fatalf("user submit lost its prompt:\n%s", userContents)
	}
}

func TestGoalResetSurvivesSessionReload(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskWithHistoryScript("feito", goalHistoryMarker),
		{
			{Type: qgruntime.StreamEventText, Content: "second task"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := goalTaskTestModel(store, session, provider)
	m.goalBetweenTasks = "reset"

	next, cmd := m.launchGoalContinuationIfReady()
	updated, goalCmd := updatedModel(t, next, execCmd(cmd))
	if goalCmd == nil || !updated.pending {
		t.Fatal("reset-mode next task did not launch")
	}
	// The task boundary ends the first run: one provider call, no follow-up
	// turn inside the finished task era.
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 (run ends at update_goal)", len(provider.requests))
	}
	if nextRun := execCmd(goalCmd); nextRun == nil {
		t.Fatal("second run returned no response")
	}
	// The persisted snapshot must hold the cut wire, not the full history.
	snap, err := store.ReadContinuation(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snap == nil {
		t.Fatal("no continuation snapshot persisted after the switch")
	}
	snapContents := ""
	for _, message := range snap.Messages {
		snapContents += "\n" + message.Content
	}
	if strings.Contains(snapContents, goalHistoryMarker) {
		t.Fatalf("persisted snapshot replays previous task history:\n%s", snapContents)
	}
	// Reload the session from disk like /resume does: continuations and wire
	// rebuilds still filter the previous task out.
	reloaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	resumed := newModel(context.Background(), Options{
		Provider:         &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{{Type: qgruntime.StreamEventText, Content: "resumed work"}, {Type: qgruntime.StreamEventDone}}}},
		Registry:         tools.NewRegistry(),
		SessionStore:     store,
		GoalBetweenTasks: "reset",
	})
	resumed.activeSession = *reloaded
	resumed.sessionEvents = append([]sessions.Event{}, events...)
	rebuilt, wire, ok := resumed.rebuiltContinuation()
	if !ok {
		t.Fatal("rebuilt continuation missing after reload")
	}
	_ = rebuilt
	wireContents := ""
	for _, message := range wire.messages {
		wireContents += "\n" + message.Content
	}
	if strings.Contains(wireContents, goalHistoryMarker) {
		t.Fatalf("rebuilt wire replays previous task history:\n%s", wireContents)
	}
	_, resumeCmd := resumed.launchGoalContinuationIfReady()
	resumeResp := execCmd(resumeCmd)
	if resumeResp == nil {
		t.Fatal("resumed continuation returned no response")
	}
	resumedProvider := resumed.provider.(*scriptedProvider)
	resumeContents := requestContents(resumedProvider.requests[0])
	if strings.Contains(resumeContents, goalHistoryMarker) {
		t.Fatalf("resumed continuation replays previous task history:\n%s", resumeContents)
	}
	if !strings.Contains(resumeContents, "Current task context") {
		t.Fatalf("resumed continuation missing task block:\n%s", resumeContents)
	}
}

func TestGoalResetResumeWithoutSnapshotFiltersHistory(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskScript("feito"),
		goalFillerScript(),
	}}
	m := goalTaskTestModel(store, session, provider)
	m.goalBetweenTasks = "reset"

	next, cmd := m.launchGoalContinuationIfReady()
	switched, _ := updatedModel(t, next, execCmd(cmd))
	if !transcriptContains(switched.transcript, "Contexto limpo entre tarefas") {
		t.Fatalf("reset notice missing: %#v", switched.transcript)
	}
	// Drain the async snapshot writer, then drop the snapshot: /resume takes
	// the reconstructed path (no usable snapshot) instead of the exact one.
	switched.snapshotWriter.Flush(2 * time.Second)
	if err := store.InvalidateContinuation(session.SessionID); err != nil {
		t.Fatal(err)
	}
	if snap, err := store.ReadContinuation(session.SessionID); err != nil || snap != nil {
		t.Fatalf("precondition: snapshot still present: snap=%v err=%v", snap != nil, err)
	}
	fresh := newModel(context.Background(), Options{
		Provider:         &scriptedProvider{},
		Registry:         tools.NewRegistry(),
		SessionStore:     store,
		GoalBetweenTasks: "reset",
	})
	resumed, notice := fresh.handleResumeCommand(session.SessionID)
	if notice != "" {
		t.Fatalf("resume notice = %q", notice)
	}
	if !resumed.reconstructedResume {
		t.Fatal("expected the reconstructed resume path without a snapshot")
	}
	if contents := continuationContents(resumed.continuation.messages); strings.Contains(contents, goalHistoryMarker) {
		t.Fatalf("resumed wire replays previous task history:\n%s", contents)
	}
}

func TestGoalResetRewriteSnapshotFiltersHistory(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskScript("feito"),
		goalFillerScript(),
	}}
	m := goalTaskTestModel(store, session, provider)
	m.goalBetweenTasks = "reset"

	next, cmd := m.launchGoalContinuationIfReady()
	switched, _ := updatedModel(t, next, execCmd(cmd))
	if switched.goalResetBoundary() <= 0 {
		t.Fatal("precondition: reset boundary not set after the switch")
	}
	// Task-two activity lands after the boundary.
	if _, err := store.AppendEvents(session.SessionID, []sessions.AppendEventInput{{
		Type:    sessions.EventMessage,
		Payload: map[string]any{"role": "user", "content": "nota da tarefa dois"},
	}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	switched.activeSession = *loaded
	events, err := store.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	switched.sessionEvents = append([]sessions.Event{}, events...)
	rewritten := switched.rewriteSnapshotFromEvents()
	rewritten.snapshotWriter.Flush(2 * time.Second)
	contents := continuationContents(rewritten.continuation.messages)
	if strings.Contains(contents, goalHistoryMarker) {
		t.Fatalf("rewritten snapshot replays previous task history:\n%s", contents)
	}
	if !strings.Contains(contents, "nota da tarefa dois") {
		t.Fatalf("rewritten snapshot lost the current task era:\n%s", contents)
	}
	snap, err := store.ReadContinuation(session.SessionID)
	if err != nil || snap == nil {
		t.Fatalf("rewritten snapshot missing: snap=%v err=%v", snap != nil, err)
	}
	if snapContents := continuationContents(snap.Messages); strings.Contains(snapContents, goalHistoryMarker) {
		t.Fatalf("persisted snapshot replays previous task history:\n%s", snapContents)
	}
}

type fakeGoalCompactor struct {
	calls int
	last  CompactRequest
}

func (f *fakeGoalCompactor) CompactSession(ctx context.Context, request CompactRequest) (CompactResult, error) {
	f.calls++
	f.last = request
	return CompactResult{Compacted: true, BeforeTokens: 1000, AfterTokens: 100, Summary: "compacted"}, nil
}

func TestGoalCompactModeCompactsBetweenTasks(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskScript("feito"),
		goalFillerScript(),
		{
			{Type: qgruntime.StreamEventText, Content: "second task"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	compactor := &fakeGoalCompactor{}
	m := goalTaskTestModel(store, session, provider)
	m.sessionCompactor = compactor

	next, cmd := m.launchGoalContinuationIfReady()
	resp := execCmd(cmd)
	// The task boundary ends the run before any follow-up provider call.
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 (run ends at update_goal)", len(provider.requests))
	}
	updated, goalCmd := updatedModel(t, next, resp)
	if goalCmd == nil {
		t.Fatal("compact-mode switch returned no command")
	}
	compactMsg := execCmd(goalCmd)
	if compactor.calls != 1 {
		t.Fatalf("compactor calls = %d, want 1", compactor.calls)
	}
	if compactMsg == nil {
		t.Fatal("compaction returned no message")
	}
	after, nextCmd := updatedModel(t, updated, compactMsg)
	_ = nextCmd
	if !after.pending {
		t.Fatal("continuation did not launch after between-tasks compaction")
	}
	if !transcriptContains(after.transcript, "Compactando contexto antes da próxima tarefa") {
		t.Fatalf("compact notice missing: %#v", after.transcript)
	}
}

type failingGoalCompactor struct {
	calls int
}

func (f *failingGoalCompactor) CompactSession(_ context.Context, _ CompactRequest) (CompactResult, error) {
	f.calls++
	return CompactResult{}, fmt.Errorf("disco cheio")
}

func TestGoalCompactErrorContinuesWithoutCompacting(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskScript("feito"),
		goalFillerScript(),
		{
			{Type: qgruntime.StreamEventText, Content: "second task"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	compactor := &failingGoalCompactor{}
	m := goalTaskTestModel(store, session, provider)
	m.sessionCompactor = compactor

	next, cmd := m.launchGoalContinuationIfReady()
	resp := execCmd(cmd)
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 (run ends at update_goal)", len(provider.requests))
	}
	updated, goalCmd := updatedModel(t, next, resp)
	if goalCmd == nil {
		t.Fatal("compact-mode switch returned no command")
	}
	compactMsg := execCmd(goalCmd)
	if compactor.calls != 1 {
		t.Fatalf("compactor calls = %d, want 1", compactor.calls)
	}
	after, nextCmd := updatedModel(t, updated, compactMsg)
	_ = nextCmd
	if !after.pending {
		t.Fatal("goal stalled after a failed between-tasks compaction")
	}
	if !transcriptContains(after.transcript, "Compactação falhou; seguindo para a próxima tarefa sem compactar.") {
		t.Fatalf("failure notice missing: %#v", after.transcript)
	}
	if after.goalContinueAfterCompact {
		t.Fatal("goalContinueAfterCompact stayed armed after the failure")
	}
}

func TestGoalBlockedPausesAndResumeRetriesBlockedTask(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventText, Content: "stuck"},
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "task_block", ToolName: "update_goal"},
			{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "task_block", ArgumentsFragment: `{"status":"blocked","task_status":"blocked","reason":"need input"}`},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "task_block"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "resumed work"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := goalTaskTestModel(store, session, provider)

	next, cmd := m.launchGoalContinuationIfReady()
	updated, _ := updatedModel(t, next, execCmd(cmd))
	if updated.pending {
		t.Fatal("blocked task must not launch another continuation")
	}
	// The block ends the run in a single turn: the model never gets a
	// follow-up call to keep working on the blocked task.
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 (run ends at update_goal)", len(provider.requests))
	}
	if !transcriptContains(updated.transcript, "Tarefa pausada aqui. Use /goal resume para continuar.") {
		t.Fatalf("boundary pause line missing from transcript: %#v", updated.transcript)
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
	updated.activeSession = *loaded
	resumed, resumeCmd := updated.handleGoalCommand("resume")
	if resumeCmd == nil || !resumed.pending {
		t.Fatal("resume should retry the blocked task")
	}
	if !transcriptContains(resumed.transcript, "Retomando tarefa bloqueada: First") {
		t.Fatalf("resume notice missing: %#v", resumed.transcript)
	}
	reloaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if current := sessions.CurrentGoalTask(reloaded.Goal); current == nil || current.Status != sessions.GoalTaskActive {
		t.Fatalf("resumed task = %#v", current)
	}
	if resumed.runCancel != nil {
		resumed.runCancel()
	}
}

func TestGoalAutoSplitInstructsDivideOnFirstRun(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_autosplit_first"})
	if err != nil {
		t.Fatal(err)
	}
	session, err = store.UpdateTitle(session.SessionID, "Manual test title")
	if err != nil {
		t.Fatal(err)
	}
	filler := []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "working"},
		{Type: qgruntime.StreamEventDone},
	}
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{filler, filler}}
	m := goalTaskTestModel(store, session, provider)

	// The real first execution goes through /goal create (no continuation
	// reserved yet), so the split instruction must be in its system prompt.
	created, cmd := m.handleGoalCommand("Single sentence objective")
	if cmd == nil || !created.pending {
		t.Fatal("goal creation did not start its first run")
	}
	resp := execCmd(cmd)
	if resp == nil {
		t.Fatal("first run returned no response")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(provider.requests))
	}
	if contents := requestContents(provider.requests[0]); !strings.Contains(contents, "set_goal_tasks") {
		t.Fatalf("first run should instruct the split, system =\n%s", contents)
	}
	// A model that never splits keeps the legacy behavior on later runs: the
	// next continuation (count now reserved) carries no instruction.
	updated, nextCmd := updatedModel(t, created, resp)
	_ = updated
	secondResps := execAgentResponses(nextCmd)
	if len(secondResps) != 1 {
		t.Fatalf("second run responses = %d, want 1", len(secondResps))
	}
	if contents := requestContents(provider.requests[len(provider.requests)-1]); strings.Contains(contents, "set_goal_tasks") {
		t.Fatalf("later runs should not repeat the split instruction:\n%s", contents)
	}
}

func TestGoalAutoSplitOffSkipsSplitInstruction(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Single sentence objective")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: "working"},
		{Type: qgruntime.StreamEventDone},
	}}}
	m := goalTaskTestModel(store, session, provider)
	off := false
	m.goalAutoSplit = &off

	_, cmd := m.launchGoalContinuationIfReady()
	if cmd == nil {
		t.Fatal("run did not launch")
	}
	_ = execCmd(cmd)
	if contents := requestContents(provider.requests[0]); strings.Contains(contents, "set_goal_tasks") {
		t.Fatalf("disabled autoSplit should not instruct the split:\n%s", contents)
	}
}
