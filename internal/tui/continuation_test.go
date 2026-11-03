package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/peermsg"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// continuationEchoTool stands in for a read-only tool so turn 1 can carry a
// real tool call and result in the stored history.
type continuationEchoTool struct{}

func (continuationEchoTool) Name() string { return "read_it" }
func (continuationEchoTool) Description() string {
	return "read something"
}
func (continuationEchoTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (continuationEchoTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (continuationEchoTool) Run(_ context.Context, _ map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "file contents here"}
}

func textScripts(answers ...string) [][]qgruntime.StreamEvent {
	scripts := make([][]qgruntime.StreamEvent, 0, len(answers))
	for _, answer := range answers {
		scripts = append(scripts, []qgruntime.StreamEvent{
			{Type: qgruntime.StreamEventText, Content: answer},
			{Type: qgruntime.StreamEventDone},
		})
	}
	return scripts
}

// continuationHarness builds a model whose provider answers every turn from
// scripts, recording each request. One scripted answer is consumed per
// completion call; the last script repeats when exhausted.
func continuationHarness(t *testing.T, store *sessions.Store, provider *scriptedProvider, registry *tools.Registry) model {
	t.Helper()
	if registry == nil {
		registry = tools.NewRegistry()
	}
	return newModel(context.Background(), Options{
		Cwd:          t.TempDir(),
		ProviderName: "test",
		ModelName:    "test-model",
		Provider:     provider,
		Registry:     registry,
		SessionStore: store,
	})
}

// runTextTurn submits prompt through the composer, runs the agent command to
// completion, and feeds the response back, returning the settled model.
func runTextTurn(t *testing.T, m model, prompt string) model {
	t.Helper()
	m.input.SetValue(prompt)
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected an agent command after submit")
	}
	msg, ok := execCmd(cmd).(agentResponseMsg)
	if !ok {
		t.Fatalf("expected agentResponseMsg, got %T", execCmd(cmd))
	}
	updated, _ = m.Update(msg)
	return updated.(model)
}

func lastUserMessage(req qgruntime.CompletionRequest) qgruntime.Message {
	return req.Messages[len(req.Messages)-1]
}

func requestContains(req qgruntime.CompletionRequest, substr string) bool {
	for _, message := range req.Messages {
		if strings.Contains(message.Content, substr) {
			return true
		}
	}
	return false
}

func TestTUISecondTurnSendsRealHistory(t *testing.T) {
	store := testSessionStore(t)
	registry := tools.NewRegistry()
	registry.Register(continuationEchoTool{})
	provider := &scriptedProvider{scripts: append([][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "read_it"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-1", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
		{Type: qgruntime.StreamEventDone},
	}}, textScripts("Should I continue?", "Yes, go on.")...)}
	m := continuationHarness(t, store, provider, registry)

	m = runTextTurn(t, m, "read it")
	m = runTextTurn(t, m, "Sim")

	if len(provider.requests) != 3 {
		t.Fatalf("provider requests = %d, want 3 (tool turn + answer + second turn)", len(provider.requests))
	}
	req := provider.requests[2]
	if len(req.Messages) != 6 {
		t.Fatalf("second-turn messages = %d, want 6 (history + prompt)", len(req.Messages))
	}
	roles := []qgruntime.MessageRole{
		qgruntime.MessageRoleSystem,
		qgruntime.MessageRoleUser,
		qgruntime.MessageRoleAssistant,
		qgruntime.MessageRoleTool,
		qgruntime.MessageRoleAssistant,
		qgruntime.MessageRoleUser,
	}
	for i, role := range roles {
		if req.Messages[i].Role != role {
			t.Fatalf("message %d role = %q, want %q", i, req.Messages[i].Role, role)
		}
	}
	if req.Messages[0].Content != provider.requests[0].Messages[0].Content {
		t.Fatal("second turn must reuse the frozen system message byte-for-byte")
	}
	if req.Messages[1].Content != "read it" {
		t.Fatalf("history user message = %q, want the first prompt", req.Messages[1].Content)
	}
	if len(req.Messages[2].ToolCalls) != 1 || req.Messages[2].ToolCalls[0].ID != "call-1" || req.Messages[2].ToolCalls[0].Name != "read_it" {
		t.Fatalf("history tool call lost: %#v", req.Messages[2].ToolCalls)
	}
	if req.Messages[3].ToolCallID != "call-1" || req.Messages[3].Content != "file contents here" {
		t.Fatalf("history tool result lost: %#v", req.Messages[3])
	}
	if req.Messages[4].Content != "Should I continue?" {
		t.Fatalf("history must carry the whole closing question, got %q", req.Messages[4].Content)
	}
	if last := lastUserMessage(req); last.Content != "Sim" {
		t.Fatalf("last user message = %q, want the clean prompt", last.Content)
	}
	if requestContains(req, "Previous session context") {
		t.Fatal("second turn must not use the textual summary")
	}
}

// dropTurns runs two text turns (the second proves the continuation is live)
// and returns the model and provider for the trigger step.
func dropTurns(t *testing.T, m model) model {
	t.Helper()
	m = runTextTurn(t, m, "first question")
	return runTextTurn(t, m, "again")
}

func assertSecondTurnUsedHistory(t *testing.T, provider *scriptedProvider) {
	t.Helper()
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	req := provider.requests[1]
	if last := lastUserMessage(req); last.Content != "again" {
		t.Fatalf("positive control: last user message = %q, want the clean prompt", last.Content)
	}
	if !requestContains(req, "first answer") {
		t.Fatal("positive control: second turn must carry the first turn's answer")
	}
}

func assertTextualSummaryTurn(t *testing.T, provider *scriptedProvider, index int, prompt string) {
	t.Helper()
	if len(provider.requests) != index+1 {
		t.Fatalf("provider requests = %d, want %d", len(provider.requests), index+1)
	}
	req := provider.requests[index]
	if last := lastUserMessage(req); !strings.Contains(last.Content, "Previous session context") {
		t.Fatalf("turn must fall back to the textual summary, got %q", last.Content)
	}
	// Note: the textual summary legitimately quotes prior events (truncated),
	// so their content may appear here. The contract is the SHAPE: a
	// FormatExecPrompt wrapper ending with the prompt, not the clean prompt
	// of a real-history turn.
	if got := lastUserMessage(req).Content; !strings.HasSuffix(got, prompt) {
		t.Fatalf("summary turn must end with the prompt, got %q", got)
	}
}

func TestTUIContinuationDroppedOn(t *testing.T) {
	newTextHarness := func(t *testing.T) (model, *scriptedProvider) {
		provider := &scriptedProvider{scripts: textScripts("first answer", "second answer", "third answer", "fourth answer")}
		return continuationHarness(t, testSessionStore(t), provider, nil), provider
	}

	t.Run("new", func(t *testing.T) {
		m, provider := newTextHarness(t)
		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, provider)

		m.input.SetValue("/new")
		updated, _ := m.Update(testKey(tea.KeyEnter))
		m = updated.(model)

		m = runTextTurn(t, m, "after new")
		req := provider.requests[2]
		if len(req.Messages) != 2 {
			t.Fatalf("fresh session turn = %d messages, want [system user]", len(req.Messages))
		}
		if last := lastUserMessage(req); last.Content != "after new" {
			t.Fatalf("fresh session prompt = %q, want it clean", last.Content)
		}
		if requestContains(req, "first answer") || requestContains(req, "second answer") {
			t.Fatal("new session must not see the previous session's history")
		}
	})

	t.Run("resume", func(t *testing.T) {
		store := testSessionStore(t)
		provider := &scriptedProvider{scripts: textScripts("first answer", "second answer", "third answer")}
		m := continuationHarness(t, store, provider, nil)
		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, provider)

		id := m.activeSession.SessionID
		m.input.SetValue("/resume " + id)
		updated, _ := m.Update(testKey(tea.KeyEnter))
		m = updated.(model)

		m = runTextTurn(t, m, "after resume")
		assertTextualSummaryTurn(t, provider, 2, "after resume")
	})

	t.Run("rewind", func(t *testing.T) {
		m, provider := newTextHarness(t)
		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, provider)

		m.input.SetValue("/rewind 0")
		updated, _ := m.Update(testKey(tea.KeyEnter))
		m = updated.(model)

		m = runTextTurn(t, m, "after rewind")
		assertTextualSummaryTurn(t, provider, 2, "after rewind")
	})

	t.Run("compact", func(t *testing.T) {
		m, provider := newTextHarness(t)
		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, provider)

		m.input.SetValue("/compact now")
		updated, cmd := m.Update(testKey(tea.KeyEnter))
		m = updated.(model)
		if cmd == nil {
			t.Fatal("expected a compaction command")
		}
		if msg := execCmd(cmd); msg != nil {
			if _, ok := msg.(compactResultMsg); !ok {
				t.Fatalf("expected compactResultMsg, got %T", msg)
			}
			updated, _ = m.Update(msg)
			m = updated.(model)
		}

		m = runTextTurn(t, m, "after compact")
		assertTextualSummaryTurn(t, provider, 2, "after compact")
	})

	t.Run("model", func(t *testing.T) {
		store := testSessionStore(t)
		provider := &scriptedProvider{scripts: textScripts("first answer", "second answer", "third answer")}
		m := newModel(context.Background(), Options{
			Cwd:             t.TempDir(),
			ProviderName:    "test",
			ModelName:       "test-model",
			Provider:        provider,
			Registry:        tools.NewRegistry(),
			SessionStore:    store,
			ProviderProfile: config.ProviderProfile{Name: "test", APIKey: "test-key"},
			NewProvider: func(config.ProviderProfile) (qgruntime.Provider, error) {
				return provider, nil
			},
		})
		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, provider)

		switched, text, ok, err := m.handleModelCommandScope("muse-spark-1.3-contributor", false)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatalf("model switch refused: %s", text)
		}
		m = switched
		if m.modelName == "test-model" {
			t.Fatalf("model switch did not change the model: %s", text)
		}

		m = runTextTurn(t, m, "after switch")
		assertTextualSummaryTurn(t, provider, 2, "after switch")
	})

	t.Run("error", func(t *testing.T) {
		inner := &scriptedProvider{scripts: textScripts("first answer", "second answer", "after error")}
		m := continuationHarness(t, testSessionStore(t), inner, nil)
		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, inner)

		failing := &failOnceProvider{inner: inner, failAt: 1}
		m.provider = failing
		m = runTextTurn(t, m, "failing turn")
		if !transcriptContains(m.transcript, "explosion") {
			t.Fatal("error turn must surface the provider failure")
		}

		m.provider = inner
		m = runTextTurn(t, m, "after error")
		assertTextualSummaryTurn(t, inner, 2, "after error")
	})

	t.Run("cancelled", func(t *testing.T) {
		m, provider := newTextHarness(t)
		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, provider)

		m.input.SetValue("cancelled turn")
		updated, cmd := m.Update(testKey(tea.KeyEnter))
		m = updated.(model)
		if cmd == nil {
			t.Fatal("expected an agent command")
		}
		if m.runCancel != nil {
			m.runCancel()
		}
		updated, _ = m.Update(agentResponseMsg{runID: m.activeRunID, err: context.Canceled})
		m = updated.(model)

		m = runTextTurn(t, m, "after cancel")
		assertTextualSummaryTurn(t, provider, 2, "after cancel")
	})

	t.Run("peer", func(t *testing.T) {
		m, provider := newTextHarness(t)
		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, provider)

		updated, cmd := m.launchPromptInternal("peer says hi", &peermsg.InboundMessage{ID: "p1", Body: "peer says hi"})
		m = updated
		if cmd == nil {
			t.Fatal("expected a peer run command")
		}
		if msg, ok := execCmd(cmd).(agentResponseMsg); ok {
			updated, _ := m.Update(msg)
			m = updated.(model)
		}

		m = runTextTurn(t, m, "after peer")
		assertTextualSummaryTurn(t, provider, 3, "after peer")
	})

	t.Run("goal", func(t *testing.T) {
		store := testSessionStore(t)
		provider := &scriptedProvider{scripts: textScripts("first answer", "second answer", "goal progress", "after goal")}
		m := continuationHarness(t, store, provider, nil)
		m.goalContinuationsSuspended = true
		session, err := store.Create(sessions.CreateInput{SessionID: "goal_session"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.CreateGoal(session.SessionID, "ship it", 0); err != nil {
			t.Fatal(err)
		}
		reloaded, err := store.Get(session.SessionID)
		if err != nil || reloaded == nil {
			t.Fatal("goal session missing")
		}
		m.activeSession = *reloaded

		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, provider)

		m.goalContinuationsSuspended = false
		updated, goalCmd := m.launchGoalContinuationIfReady()
		m = updated
		if goalCmd == nil {
			t.Fatal("expected a goal continuation run")
		}
		m.goalContinuationsSuspended = true
		if msg, ok := execCmd(goalCmd).(agentResponseMsg); ok {
			updated, _ := m.Update(msg)
			m = updated.(model)
		}

		m = runTextTurn(t, m, "after goal")
		assertTextualSummaryTurn(t, provider, 3, "after goal")
	})

	t.Run("goal change", func(t *testing.T) {
		store := testSessionStore(t)
		provider := &scriptedProvider{scripts: textScripts("first answer", "second answer", "after change")}
		m := continuationHarness(t, store, provider, nil)
		m.goalContinuationsSuspended = true
		session, err := store.Create(sessions.CreateInput{SessionID: "goal_change"})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.CreateGoal(session.SessionID, "ship it", 0); err != nil {
			t.Fatal(err)
		}
		reloaded, err := store.Get(session.SessionID)
		if err != nil || reloaded == nil {
			t.Fatal("goal session missing")
		}
		m.activeSession = *reloaded

		m = dropTurns(t, m)
		assertSecondTurnUsedHistory(t, provider)

		updatedGoal, _, err := store.UpdateGoal(session.SessionID, sessions.GoalStatusComplete, "done")
		if err != nil {
			t.Fatal(err)
		}
		m.activeSession = updatedGoal

		m = runTextTurn(t, m, "after change")
		assertTextualSummaryTurn(t, provider, 2, "after change")
	})
}

// failOnceProvider fails completion calls from failAt onward so the error-turn
// drop path runs against a real agent failure (a single failure may be
// retried by the loop, so only a sticky failure fails the run).
type failOnceProvider struct {
	inner  *scriptedProvider
	failAt int
	calls  int
}

func (provider *failOnceProvider) StreamCompletion(ctx context.Context, request qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	provider.calls++
	if provider.calls >= provider.failAt {
		return nil, errors.New("provider explosion")
	}
	return provider.inner.StreamCompletion(ctx, request)
}

// /clear starts a fresh session like /new: the next turn carries no history.
func TestTUIClearStartsFreshSession(t *testing.T) {
	store := testSessionStore(t)
	provider := &scriptedProvider{scripts: textScripts("first answer", "second answer", "after clear")}
	m := continuationHarness(t, store, provider, nil)

	m = dropTurns(t, m)
	assertSecondTurnUsedHistory(t, provider)

	m.input.SetValue("/clear")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)

	m = runTextTurn(t, m, "after clear")
	if len(provider.requests) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(provider.requests))
	}
	req := provider.requests[2]
	if len(req.Messages) != 2 {
		t.Fatalf("fresh session turn = %d messages, want [system user]", len(req.Messages))
	}
	if last := lastUserMessage(req); last.Content != "after clear" {
		t.Fatalf("fresh session prompt = %q, want it clean", last.Content)
	}
	if requestContains(req, "first answer") || requestContains(req, "second answer") {
		t.Fatal("the pre-clear history must not leak into the fresh session")
	}
}

// submitHeldTurn submits a prompt and returns the model with the agent command
// held back, so the test can cancel, switch sessions, or forge the response.
func submitHeldTurn(t *testing.T, m model, prompt string) (model, tea.Cmd) {
	t.Helper()
	m.input.SetValue(prompt)
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected an agent command")
	}
	if !m.pending {
		t.Fatal("expected a pending run")
	}
	return m, cmd
}

func TestTUILateResponseDoesNotLeakIntoNewSession(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("first answer", "late answer", "bee answer")}
	m := continuationHarness(t, testSessionStore(t), provider, nil)

	// Turn 1 in session A stores a continuation.
	m = runTextTurn(t, m, "first question")

	// Turn 2 starts in A but the command is held back.
	m, lateCmd := submitHeldTurn(t, m, "second question")

	// Real cancel path: first Esc arms the confirm, second Esc cancels.
	updated, _ := m.Update(testKey(tea.KeyEsc))
	m = updated.(model)
	updated, _ = m.Update(testKey(tea.KeyEsc))
	m = updated.(model)
	if m.pending {
		t.Fatal("expected the run cancelled after the confirming Esc")
	}

	// New session before the late response lands.
	m.input.SetValue("/new")
	updated, _ = m.Update(testKey(tea.KeyEnter))
	m = updated.(model)

	// Only now the cancelled run's response arrives.
	if msg, ok := execCmd(lateCmd).(agentResponseMsg); ok {
		updated, _ = m.Update(msg)
		m = updated.(model)
	} else {
		t.Fatalf("expected agentResponseMsg, got %T", execCmd(lateCmd))
	}

	m = runTextTurn(t, m, "hello bee")
	req := provider.requests[len(provider.requests)-1]
	if len(req.Messages) != 2 {
		t.Fatalf("fresh session turn = %d messages, want [system user]", len(req.Messages))
	}
	if last := lastUserMessage(req); last.Content != "hello bee" {
		t.Fatalf("fresh session prompt = %q, want it clean", last.Content)
	}
	if requestContains(req, "first answer") || requestContains(req, "second question") {
		t.Fatal("session A's history leaked into session B's turn")
	}
}

func TestTUILateResponseForeignSessionDropped(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("first answer", "second answer")}
	m := continuationHarness(t, testSessionStore(t), provider, nil)

	// Turn 1 stores session A's continuation.
	m = runTextTurn(t, m, "first question")

	// A stale duplicate delivery for the (idle) active run id carries a
	// foreign session's messages. The store-time session guard must drop it
	// without clobbering the valid continuation.
	forged := agentResponseMsg{
		runID: m.activeRunID,
		continuation: &turnContinuation{
			sessionID: "foreign-session",
			modelID:   m.modelName,
			messages: []qgruntime.Message{
				{Role: qgruntime.MessageRoleSystem, Content: "foreign system"},
				{Role: qgruntime.MessageRoleUser, Content: "FOREIGN MARKER"},
			},
		},
	}
	updated, _ := m.Update(forged)
	m = updated.(model)

	m = runTextTurn(t, m, "second question")
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2", len(provider.requests))
	}
	req := provider.requests[1]
	if last := lastUserMessage(req); last.Content != "second question" {
		t.Fatalf("last user message = %q, want the clean prompt", last.Content)
	}
	if !requestContains(req, "first answer") {
		t.Fatal("valid continuation was clobbered by the foreign response")
	}
	if requestContains(req, "FOREIGN MARKER") {
		t.Fatal("foreign continuation leaked into the turn")
	}
}

func TestTUIRealHistoryKillSwitch(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("first answer", "second answer", "third answer")}
	m := continuationHarness(t, testSessionStore(t), provider, nil)

	m = runTextTurn(t, m, "first question")
	m = runTextTurn(t, m, "again")
	assertSecondTurnUsedHistory(t, provider)

	t.Setenv("QG_TUI_REAL_HISTORY", "0")
	m = runTextTurn(t, m, "third")
	assertTextualSummaryTurn(t, provider, 2, "third")
}

func TestTUISystemPromptStableAcrossTurns(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("one", "two", "three")}
	m := continuationHarness(t, testSessionStore(t), provider, nil)

	m = runTextTurn(t, m, "first")
	m = runTextTurn(t, m, "second")

	// Drift the system prompt mid-session through the real /style command.
	// The default style is concise, so switch to explanatory: the next run's
	// sections then contain a directive the frozen system lacks.
	m.input.SetValue("/style explanatory")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if m.responseStyle != "explanatory" {
		t.Fatal("expected the explanatory style active")
	}

	m = runTextTurn(t, m, "third")

	if len(provider.requests) != 3 {
		t.Fatalf("provider requests = %d, want 3", len(provider.requests))
	}
	first := provider.requests[0].Messages[0].Content
	for i, req := range provider.requests {
		if len(req.Messages) == 0 || req.Messages[0].Role != qgruntime.MessageRoleSystem {
			t.Fatalf("request %d must start with the system message", i)
		}
		if req.Messages[0].Content != first {
			t.Fatalf("request %d system message drifted", i)
		}
	}
	// The drift must arrive as a [context update] note on the user turn, not
	// in the cached system prefix.
	last := lastUserMessage(provider.requests[2])
	if !strings.HasPrefix(last.Content, "[context update]\n") {
		t.Fatalf("changed-section turn must open with the context-update note, got %q", last.Content)
	}
	if !strings.HasSuffix(last.Content, "third") {
		t.Fatalf("changed-section turn must end with the prompt, got %q", last.Content)
	}
}
