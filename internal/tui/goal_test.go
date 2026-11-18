package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestGoalCommandCreatesPersistentGoalAndStartsRun(t *testing.T) {
	store := testSessionStore(t)
	m := newModel(context.Background(), Options{
		Provider:     &scriptedProvider{},
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})

	next, cmd := m.handleGoalCommand("--tokens 500 Ship the release")
	if cmd == nil || !next.pending {
		t.Fatal("creating a goal should start its first run")
	}
	if next.activeSession.Goal == nil ||
		next.activeSession.Goal.Objective != "Ship the release" ||
		next.activeSession.Goal.TokenBudget != 500 ||
		next.activeSession.Goal.Status != sessions.GoalStatusActive {
		t.Fatalf("active goal = %#v", next.activeSession.Goal)
	}
	loaded, err := store.Get(next.activeSession.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || loaded.Goal == nil || loaded.Goal.Objective != "Ship the release" {
		t.Fatalf("persisted session = %#v", loaded)
	}
}

func TestGoalCommandDoesNotCreateGoalDuringActiveRun(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_pending"})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Provider:     &scriptedProvider{},
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session
	m.pending = true

	next, cmd := m.handleGoalCommand("Do not replace the active run")
	if cmd != nil {
		t.Fatal("goal creation during an active run returned a command")
	}
	if next.activeSession.Goal != nil {
		t.Fatalf("goal was created during an active run: %#v", next.activeSession.Goal)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal != nil {
		t.Fatalf("goal was persisted during an active run: %#v", loaded.Goal)
	}
	if !transcriptContains(next.transcript, "Já há uma execução em andamento.") {
		t.Fatalf("missing active-run explanation: %#v", next.transcript)
	}
}

func TestGoalRunRegistryContainsSessionBoundTools(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_tools"})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session

	registry := m.goalRegistry()
	for _, name := range []string{"get_goal", "create_goal", "update_goal"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("goal registry missing %q", name)
		}
	}
	if _, ok := m.registry.Get("get_goal"); ok {
		t.Fatal("goal tools should not mutate the shared base registry")
	}
}

func TestLoopRunExcludesGoalToolsAndInstructions(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_loop"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err = store.CreateGoal(session.SessionID, "Keep this out of loops", 0)
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: "Loop iteration complete."},
		{Type: qgruntime.StreamEventDone},
	}}}
	m := newModel(context.Background(), Options{
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session
	m.activeLoopID = "loop-1"

	_ = execCmd(m.runAgentWithOptions(1, context.Background(), "run loop", nil, tuiAgentRunOptions{}))
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(provider.requests))
	}
	request := provider.requests[0]
	for _, definition := range request.Tools {
		switch definition.Name {
		case "get_goal", "create_goal", "update_goal":
			t.Fatalf("loop request exposed goal tool %q", definition.Name)
		}
	}
	for _, message := range request.Messages {
		if strings.Contains(message.Content, "Persistent goal for this session:") {
			t.Fatalf("loop request included goal instructions: %q", message.Content)
		}
	}
}

func TestLoopRunDoesNotConsumeGoalBudgetOrLaunchContinuation(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_loop_usage"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err = store.CreateGoal(session.SessionID, "Keep loop usage separate", 10)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Provider:     &scriptedProvider{},
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session
	m.pending = true
	m.activeRunID = 1
	m.activeLoopID = "loop-1"

	updated, _ := m.Update(agentResponseMsg{
		runID:       1,
		goalAware:   false,
		usageEvents: []qgruntime.Usage{{InputTokens: 8, OutputTokens: 4}},
		rows:        []transcriptRow{{kind: rowAssistant, text: "loop result", final: true}},
	})
	next := updated.(model)
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Goal.TokensUsed != 0 || loaded.Goal.ContinuationCount != 0 {
		t.Fatalf("loop run mutated goal accounting: %#v", loaded.Goal)
	}
	if next.pending {
		t.Fatal("loop completion launched a goal continuation")
	}
}

func TestAgentCanCompleteGoalWithoutAnotherContinuation(t *testing.T) {
	store := testSessionStore(t)
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "goal_done", ToolName: "update_goal"},
			{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "goal_done", ArgumentsFragment: `{"status":"complete"}`},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "goal_done"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "The goal is complete."},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := newModel(context.Background(), Options{
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})

	running, cmd := m.handleGoalCommand("Finish the task")
	response := execCmd(cmd)
	if response == nil {
		t.Fatal("goal run did not return an agent response")
	}
	updated, nextCmd := running.Update(response)
	settled := updated.(model)
	if settled.activeSession.Goal == nil || settled.activeSession.Goal.Status != sessions.GoalStatusComplete {
		t.Fatalf("completed goal = %#v", settled.activeSession.Goal)
	}
	if settled.pending {
		t.Fatal("completed goal started another continuation")
	}
	// Background title/recap/sweep commands may still be returned; none should
	// have changed the settled goal back to active.
	_ = nextCmd
}

func TestGoalBudgetStopsAutomaticContinuation(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_budget"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err = store.CreateGoal(session.SessionID, "Stay bounded", 20)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Provider:     &scriptedProvider{},
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session

	m = m.reconcileGoalAfterRun([]qgruntime.Usage{{InputTokens: 12, OutputTokens: 8}}, nil)
	if m.activeSession.Goal.Status != sessions.GoalStatusBudgetLimited {
		t.Fatalf("budgeted goal status = %q", m.activeSession.Goal.Status)
	}
	next, cmd := m.launchGoalContinuationIfReady()
	if cmd != nil || next.pending {
		t.Fatal("a budget-paused goal must not launch another run")
	}
}

func TestActiveGoalLaunchesContinuation(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_continue"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err = store.CreateGoal(session.SessionID, "Keep going", 0)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Provider:     &scriptedProvider{},
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session

	next, cmd := m.launchGoalContinuationIfReady()
	if cmd == nil || !next.pending {
		t.Fatal("active goal should launch a continuation while idle")
	}
	if !transcriptContains(next.transcript, "Continuando meta: Keep going") {
		t.Fatalf("continuation was not surfaced: %#v", next.transcript)
	}
}

func TestGoalContinuationShowsOnlyFirstLineOfLongObjective(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_continue_long"})
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Repeat("x", 100)
	session, _, err = store.CreateGoal(session.SessionID, first+"\nsecond line with details\nthird line", 0)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Provider:     &scriptedProvider{},
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session

	next, cmd := m.launchGoalContinuationIfReady()
	if cmd == nil || !next.pending {
		t.Fatal("active goal should launch a continuation while idle")
	}
	want := "Continuando meta: " + strings.Repeat("x", 80) + "…"
	if !transcriptContains(next.transcript, want) {
		t.Fatalf("continuation did not shorten the objective: %#v", next.transcript)
	}
	if transcriptContains(next.transcript, "second line") {
		t.Fatalf("continuation leaked later lines: %#v", next.transcript)
	}
}

func TestParseGoalObjectiveRejectsOverlongObjective(t *testing.T) {
	if _, _, err := parseGoalObjective(strings.Repeat("a", sessions.GoalObjectiveMaxLength+1)); err == nil ||
		!strings.Contains(err.Error(), "4.000") || !strings.Contains(err.Error(), "SPEC.md") {
		t.Fatalf("overlong objective error = %v", err)
	}
}

func TestGoalContinuationChainStopsAtPersistedLimit(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_hard_stop"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err = store.CreateGoal(session.SessionID, "Never run forever", 0)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Provider:     &scriptedProvider{},
		Registry:     tools.NewRegistry(),
		SessionStore: store,
	})
	m.activeSession = session

	for continuation := 1; continuation <= sessions.GoalMaxConsecutiveContinuations; continuation++ {
		next, cmd := m.launchGoalContinuationIfReady()
		if cmd == nil || !next.pending {
			t.Fatalf("continuation %d did not launch", continuation)
		}
		if next.runCancel != nil {
			next.runCancel()
		}
		next.runCancel = nil
		next.pending = false
		next.activeRunID = 0
		m = next
	}
	stopped, cmd := m.launchGoalContinuationIfReady()
	if cmd != nil || stopped.pending {
		t.Fatal("goal exceeded its automatic continuation limit")
	}
	if stopped.activeSession.Goal.Status != sessions.GoalStatusPaused ||
		!transcriptContains(stopped.transcript, "Meta pausada após") {
		t.Fatalf("hard stop was not surfaced: goal=%#v transcript=%#v", stopped.activeSession.Goal, stopped.transcript)
	}
}

func TestGoalActionsRejectTrailingArguments(t *testing.T) {
	for _, action := range []string{"status extra", "pause extra", "resume extra", "clear extra"} {
		m := newModel(context.Background(), Options{})
		next, cmd := m.handleGoalCommand(action)
		if cmd != nil || !transcriptContains(next.transcript, "Uso: /goal") {
			t.Fatalf("%q silently accepted trailing arguments: %#v", action, next.transcript)
		}
	}
}

func TestCancelRunPausesActiveGoal(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_cancel"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err = store.CreateGoal(session.SessionID, "Keep going", 0)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{SessionStore: store})
	m.activeSession = session
	m.pending = true
	m.activeRunID = 1

	m.cancelRun()

	if m.activeSession.Goal.Status != sessions.GoalStatusPaused {
		t.Fatalf("cancelled goal status = %q", m.activeSession.Goal.Status)
	}
	if !transcriptContains(m.transcript, "Meta pausada") {
		t.Fatalf("cancel did not explain goal pause: %#v", m.transcript)
	}
}

func TestParseGoalObjective(t *testing.T) {
	objective, budget, err := parseGoalObjective("--tokens 1200 finish the migration")
	if err != nil {
		t.Fatal(err)
	}
	if objective != "finish the migration" || budget != 1200 {
		t.Fatalf("parsed objective/budget = %q/%d", objective, budget)
	}
	if _, _, err := parseGoalObjective("--tokens nope task"); err == nil ||
		!strings.Contains(err.Error(), "inteiro não negativo") {
		t.Fatalf("invalid budget error = %v", err)
	}
}

func TestParseGoalObjectivePreservesMultilineAfterTokens(t *testing.T) {
	objective, budget, err := parseGoalObjective("--tokens 5000 Obj\n1. A\n2. B")
	if err != nil {
		t.Fatal(err)
	}
	if objective != "Obj\n1. A\n2. B" || budget != 5000 {
		t.Fatalf("parsed objective/budget = %q/%d", objective, budget)
	}
}

func TestGoalStatusIsVisibleInNarrowFooter(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.activeSession.Goal = &sessions.Goal{Objective: "Ship", Status: sessions.GoalStatusActive}
	status := plainRender(t, m.statusLine(51))
	if !strings.Contains(status, "meta ativa") {
		t.Fatalf("narrow status omitted active goal: %q", status)
	}
}

func TestGoalBudgetCountsManualCompactionSummaryTokens(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "file")
	store := testSessionStore(t)
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "Resumo da meta."},
		{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{InputTokens: 1200, OutputTokens: 80}},
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		ModelName:    "muse-spark-1.3-contributor",
		Provider:     provider,
		SessionStore: store,
	})
	var err error
	m, err = m.ensureActiveSession("goal compact budget")
	if err != nil {
		t.Fatal(err)
	}
	m = seedCompactableHistory(t, m, 12)
	updatedMeta, _, err := store.CreateGoal(m.activeSession.SessionID, "Concluir migração", 1500)
	if err != nil {
		t.Fatal(err)
	}
	updatedMeta, _, err = store.AddGoalUsage(updatedMeta.SessionID, 300)
	if err != nil {
		t.Fatal(err)
	}
	m.activeSession = updatedMeta

	m.input.SetValue("/compact")
	compacted := runCompactCommand(t, m)
	if compacted.lastCompactError != "" {
		t.Fatalf("compact failed: %s", compacted.lastCompactError)
	}
	if compacted.activeSession.Goal == nil {
		t.Fatal("expected goal to remain attached after /compact")
	}
	if got := compacted.activeSession.Goal.TokensUsed; got != 1580 {
		t.Fatalf("goal TokensUsed after /compact = %d, want 1580 (300 + 1280 summary tokens)", got)
	}
	if got := compacted.activeSession.Goal.Status; got != sessions.GoalStatusBudgetLimited {
		t.Fatalf("goal status after /compact reached budget = %q, want %q", got, sessions.GoalStatusBudgetLimited)
	}

	// Also verify when the goal was paused before /compact: summary tokens count
	// and /goal resume refuses once the budget is exhausted.
	pausedStore := testSessionStore(t)
	paused := newModel(context.Background(), Options{
		ModelName:    "muse-spark-1.3-contributor",
		Provider:     provider,
		SessionStore: pausedStore,
	})
	paused, err = paused.ensureActiveSession("goal paused compact budget")
	if err != nil {
		t.Fatal(err)
	}
	paused = seedCompactableHistory(t, paused, 12)
	meta, _, err := pausedStore.CreateGoal(paused.activeSession.SessionID, "Concluir revisão", 1000)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = pausedStore.UpdateGoal(meta.SessionID, sessions.GoalStatusPaused, "paused by user")
	if err != nil {
		t.Fatal(err)
	}
	paused.activeSession = meta
	paused.input.SetValue("/compact")
	paused = runCompactCommand(t, paused)
	if got := paused.activeSession.Goal.TokensUsed; got != 1280 {
		t.Fatalf("paused goal TokensUsed after /compact = %d, want 1280", got)
	}
	resumed, cmd := paused.handleGoalCommand("resume")
	if cmd != nil || resumed.pending {
		t.Fatal("/goal resume must refuse when compaction exhausted the token budget")
	}
	if !transcriptContains(resumed.transcript, "O orçamento de tokens esgotou") {
		t.Fatalf("expected budget-exhausted error on /goal resume, got: %#v", resumed.transcript)
	}
}

func TestGoalBudgetCountsCancelledAutoCompactionSummaryTokens(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "goal_cancel_compact"})
	if err != nil {
		t.Fatal(err)
	}
	session, _, err = store.CreateGoal(session.SessionID, "Manter orçamento real", 500)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		ModelName:    "muse-spark-1.3-contributor",
		SessionStore: store,
	})
	m.activeSession = session
	m.pending = true
	m.activeRunID = 7
	m.cancelRun() // moves run 7 into flushRunIDs and pauses goal

	updated, _ := m.Update(agentResponseMsg{
		runID:     7,
		goalAware: true,
		usageEvents: []qgruntime.Usage{{
			InputTokens:  520,
			OutputTokens: 80,
			Summarizer:   true,
		}},
		usageModelID: "muse-spark-1.3-contributor",
	})
	settled := updated.(model)
	if got := settled.activeSession.Goal.TokensUsed; got != 600 {
		t.Fatalf("cancelled turn compaction summary tokens = %d, want 600", got)
	}
	resumed, cmd := settled.handleGoalCommand("resume")
	if cmd != nil || resumed.pending {
		t.Fatal("/goal resume must refuse after cancelled compaction exhausted budget")
	}
}
