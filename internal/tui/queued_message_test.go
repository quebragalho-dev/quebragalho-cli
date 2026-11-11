package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestEnterWhilePendingQueuesPromptWithoutStartingRun(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.input.SetValue("second prompt")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected queued prompt not to start another run immediately")
	}
	if !next.pending {
		t.Fatal("expected existing run to remain pending")
	}
	if got := next.input.Value(); got != "" {
		t.Fatalf("expected queued prompt to clear composer, got %q", got)
	}
	if transcriptContains(next.transcript, "second prompt") {
		t.Fatalf("queued prompt should not append to transcript before it runs, got %#v", next.transcript)
	}
}

func TestQueuedPromptRendersInTranscript(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("summarize the failing test output")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	view := plainRender(t, viewString(next.View()))
	// Sussurro: a dimmed `❯ <text>` row in the transcript, with a faint
	// hint naming the send/edit shortcuts below it.
	if !strings.Contains(view, "❯ summarize the failing test output") {
		t.Fatalf("expected the queued message in the transcript with a plain glyph, got:\n%s", view)
	}
	if !strings.Contains(view, "na fila — ") {
		t.Fatalf("expected the Sussurro hint below the queued message, got:\n%s", view)
	}
	if !strings.Contains(view, "envia agora") {
		t.Fatalf("expected the send shortcut in the hint, got:\n%s", view)
	}
}

func TestQueuedMessageShowsOnlyTheHeadInTranscript(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("MARKONE head message")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	next.input.SetValue("MARKTWO second queued")
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)

	view := plainRender(t, viewString(next.View()))
	if !strings.Contains(view, "MARKONE") {
		t.Fatalf("expected the head of the queue to render, got:\n%s", view)
	}
	if strings.Contains(view, "MARKTWO") {
		t.Fatalf("expected only the head of the queue to render, got:\n%s", view)
	}
}

func TestAgentResponseLaunchesQueuedPrompt(t *testing.T) {
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: "queued answer"},
		{Type: qgruntime.StreamEventDone},
	}}}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 7
	m.runID = 7
	m.input.SetValue("run queued followup")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd != nil {
		t.Fatal("expected queued prompt not to launch until active run completes")
	}

	updated, cmd = next.Update(agentResponseMsg{runID: 7})
	next = updated.(model)

	if cmd == nil {
		t.Fatal("expected queued prompt to launch after active run completes")
	}
	if !next.pending {
		t.Fatal("expected queued prompt launch to mark model pending")
	}
	_ = execCmd(cmd)
	if len(provider.requests) != 1 {
		t.Fatalf("expected queued prompt to make one provider request, got %d", len(provider.requests))
	}
	messages := provider.requests[0].Messages
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "run queued followup") {
		t.Fatalf("expected provider request to contain queued prompt, got %#v", messages)
	}
}

func TestEscCancelsAndDrainsQueuedPrompt(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	// A talkative scripted provider so the drained turn produces a real
	// answer instead of hitting the empty-turn guard.
	m.provider = &scriptedProvider{scripts: textScripts("drained answer")}
	cancelled := false
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.runCancel = func() { cancelled = true }
	m.width = 96
	m.input.SetValue("queued followup")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	// First Esc only arms the cancel confirmation; the queue is untouched
	// and the run stays pending.
	updated, _ = next.Update(testKey(tea.KeyEsc))
	next = updated.(model)

	if cancelled {
		t.Fatal("first Esc must only arm the cancel confirmation")
	}
	if !next.hasQueuedMessage() {
		t.Fatal("first Esc must not drop the queued message")
	}
	if !next.pending {
		t.Fatal("expected the active run to remain pending after the confirm arm")
	}

	// Second Esc cancels the run AND drains the queue: the queued prompt
	// launches as the next turn.
	updated, cmd := next.Update(testKey(tea.KeyEsc))
	next = updated.(model)

	if !cancelled {
		t.Fatal("second Esc must cancel the active run")
	}
	if cmd == nil {
		t.Fatal("expected the queued prompt to launch as the drained next turn")
	}
	if !next.pending {
		t.Fatal("expected the drained queue to start a new turn")
	}
	if next.hasQueuedMessage() {
		t.Fatal("expected the drained queue to be empty")
	}
	_ = execCmd(cmd)
	provider := m.provider.(*scriptedProvider)
	if len(provider.requests) == 0 {
		t.Fatal("expected the drained prompt to reach the provider")
	}
	messages := provider.requests[len(provider.requests)-1].Messages
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "queued followup") {
		t.Fatalf("expected the drained prompt to be the provider's last message, got %#v", messages)
	}
}

func TestEnterWhileExitingDoesNotQueuePrompt(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.exiting = true
	m.activeRunID = 1
	m.runID = 1
	m.input.SetValue("do not run during shutdown")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected submit during deferred exit to stay idle")
	}
	if next.hasQueuedMessage() {
		t.Fatalf("expected no queued prompt while exiting, got %v", next.queuedMessages)
	}
	if got := next.input.Value(); got != "do not run during shutdown" {
		t.Fatalf("expected composer to remain untouched, got %q", got)
	}
}

func TestAgentResponseLaunchesQueuedPromptAfterError(t *testing.T) {
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: "queued answer"},
		{Type: qgruntime.StreamEventDone},
	}}}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 9
	m.runID = 9
	m.input.SetValue("retry with more detail")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	updated, cmd := next.Update(agentResponseMsg{runID: 9, err: errors.New("first run failed")})

	if cmd == nil {
		t.Fatal("expected queued prompt to launch after active run errors")
	}
	next = updated.(model)
	if !next.pending {
		t.Fatal("expected queued prompt launch after error to mark model pending")
	}
}

func TestUpArrowPopsQueuedMessageIntoComposer(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("queued followup")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	updated, _ = next.Update(testKey(tea.KeyUp))
	next = updated.(model)

	if next.hasQueuedMessage() {
		t.Fatalf("expected ↑ to pop the queued message, still queued: %v", next.queuedMessages)
	}
	if got := next.composerValue(); got != "queued followup" {
		t.Fatalf("expected queued message back in the composer, got %q", got)
	}
}

func TestUpArrowPutsQueuedMessageAboveDraft(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("first queued")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	next.input.SetValue("in-progress draft")

	updated, _ = next.Update(testKey(tea.KeyUp))
	next = updated.(model)

	if next.hasQueuedMessage() {
		t.Fatalf("expected ↑ to pop the queued message, still queued: %v", next.queuedMessages)
	}
	if got := next.composerValue(); got != "first queued\nin-progress draft" {
		t.Fatalf("expected queued text above the draft, got %q", got)
	}
}

func TestQueuedMessageShowsUpEditHint(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("queued followup")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	view := viewString(next.View())
	if !strings.Contains(view, queuedEditHint) {
		t.Fatalf("expected composer placeholder %q while a message is queued, got:\n%s", queuedEditHint, view)
	}

	updated, _ = next.Update(testKey(tea.KeyUp))
	next = updated.(model)
	view = viewString(next.View())
	if strings.Contains(view, queuedEditHint) {
		t.Fatalf("expected hint to clear once the queued message is popped, got:\n%s", view)
	}
}

func TestSecondQueuedMessageStacksUnderFirst(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.input.SetValue("first queued")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	next.input.SetValue("second queued")

	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)

	if got := next.queuedMessages; len(got) != 2 || got[0] != "first queued" || got[1] != "second queued" {
		t.Fatalf("expected a FIFO of two queued prompts, got %v", got)
	}
}

func newQueuedMessageTestModel(t *testing.T) model {
	t.Helper()

	return newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider: &fakeProvider{events: []qgruntime.StreamEvent{
			{Type: qgruntime.StreamEventDone},
		}},
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
}

func testInterleaveEnter(t *testing.T, super bool) tea.KeyPressMsg {
	t.Helper()
	if super {
		return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModSuper}
	}
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}
}

// A mid-turn ⌘⏎/Ctrl+Enter injects the composer message into the running
// turn: it rides the very next provider request (after this step's tool
// result), withoutcanceling anything and without leaving the composer's
// history behind.
func TestInterleaveEnterInjectsIntoRunningTurn(t *testing.T) {
	store := testSessionStore(t)
	release := make(chan struct{})
	registry := tools.NewRegistry()
	registry.Register(deafEchoTool{release: release})
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-slow", ToolName: "slow_echo"},
			{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-slow", ArgumentsFragment: `{}`},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-slow"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "interleaved answer"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := continuationHarness(t, store, provider, registry)
	m.width = 96

	m.input.SetValue("run A")
	updated, cmdA := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmdA == nil {
		t.Fatal("expected an agent command for turn A")
	}
	doneA := make(chan tea.Msg, 1)
	go func() { doneA <- execCmd(cmdA) }()
	snapPath := filepath.Join(store.RootDir, m.activeSession.SessionID, sessions.ContinuationFile)
	waitForSnapshotCall(t, snapPath, "call-slow")

	m.input.SetValue("operator interleaves now")
	updated, _ = m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if got := m.composerValue(); got != "" {
		t.Fatalf("expected interleave to consume the composer, got %q", got)
	}

	close(release)
	late := <-doneA
	updated, _ = m.Update(late)
	m = updated.(model)

	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (turn + interleaved)", len(provider.requests))
	}
	last := provider.requests[1]
	found := false
	for _, message := range last.Messages {
		if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "operator interleaves now") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the next request after the tool result must include the interleaved message, got %#v", last.Messages)
	}
	if m.pending {
		t.Fatal("expected the run to complete")
	}
	if !transcriptContains(m.transcript, "operator interleaves now") {
		t.Fatal("expected the interleaved message as a transcript row")
	}
}

// With the composer empty and a queue pending, interleave sends the head of
// the queue now instead of waiting for the turn to end.
func TestInterleaveEnterSendsQueueHead(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.queuedMessages = []string{"MARKHEAD queued followup"}

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if len(next.queuedMessages) != 0 {
		t.Fatalf("expected the interleave to pop the head, queue = %v", next.queuedMessages)
	}
	if got := next.interleave; got == nil || len(got.texts) != 1 || got.texts[0] != "MARKHEAD queued followup" {
		t.Fatalf("expected the head to be staged for the running turn, got %+v", got)
	}
}

func TestInterleaveEnterIdleActsAsSubmit(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.input.SetValue("hello agent")

	updated, cmd := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if cmd == nil {
		t.Fatal("expected ⌘⏎ with an idle composer to submit the prompt")
	}
	if !next.pending {
		t.Fatal("expected the submitted prompt to start a run")
	}
	if len(next.queuedMessages) != 0 {
		t.Fatal("idle interleave must not consult the queue")
	}
}

func TestInterleaveEnterWithNoWorkIsIdle(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1

	updated, cmd := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected ⌘⏎ with nothing pending to stay idle")
	}
	if !next.pending {
		t.Fatal("expected the run to remain pending")
	}
	if next.interleave == nil || len(next.interleave.texts) != 0 {
		t.Fatal("expected no interleave to be staged")
	}
}

// A mid-turn interleave that never sees a tool batch (text-only answer)
// would strand in the buffer; it requeues to the head of the FIFO at the
// end of the turn instead of vanishing.
func TestInterleaveStrandedAfterTextOnlyTurnIsRequeued(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("answer", "second")}
	m := continuationHarness(t, testSessionStore(t), provider, nil)
	m.width = 96
	m.pending = true
	m.activeRunID = 1
	m.runID = 1

	m.input.SetValue("interleaved for later")
	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if m.interleave == nil || len(m.interleave.texts) != 1 {
		t.Fatalf("expected the interleave staged for the running turn, got %+v", m.interleave)
	}

	// The turn ends with a text-only answer; no tool batch ever drains the
	// interleave. The stranded message requeues and launches as the next
	// turn instead of vanishing.
	updated, cmd := m.Update(agentResponseMsg{runID: 1})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected the stranded interleave to launch as the next turn")
	}
	if !m.pending {
		t.Fatal("expected the stranded interleave to start a new turn")
	}
	if len(m.queuedMessages) != 0 {
		t.Fatalf("expected the drained interleave consumed, queue = %v", m.queuedMessages)
	}
	if m.interleave != nil && len(m.interleave.texts) != 0 {
		t.Fatal("expected the interleave buffer drained")
	}
	_ = execCmd(cmd)
	req := provider.requests[len(provider.requests)-1]
	last := req.Messages[len(req.Messages)-1]
	if !strings.Contains(last.Content, "interleaved for later") {
		t.Fatalf("expected the drained interleave as the provider's last message, got %#v", last)
	}
}

func TestEscCancelLaunchesDrainedFirstQueuedMessage(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("first answer", "second answer")}
	m := continuationHarness(t, testSessionStore(t), provider, nil)
	m.width = 96
	m.pending = true
	m.activeRunID = 5
	m.runID = 5
	m.queuedMessages = []string{"first queued", "second queued"}

	updated, _ := m.Update(testKey(tea.KeyEsc))
	m = updated.(model)
	if m.pending != true || !m.hasQueuedMessage() {
		t.Fatal("first Esc must only arm the confirm and keep the queue")
	}

	updated, cmd := m.Update(testKey(tea.KeyEsc))
	m = updated.(model)

	if cmd == nil {
		t.Fatal("expected the drained queued message to launch")
	}
	if !m.pending {
		t.Fatal("expected the drained queue to start a turn")
	}
	if len(m.queuedMessages) != 1 || m.queuedMessages[0] != "second queued" {
		t.Fatalf("expected only the head consumed by the drain, queue = %v", m.queuedMessages)
	}
	updated, _ = m.Update(execCmd(cmd))
	m = updated.(model)
	_ = m
	if len(provider.requests) != 1 {
		t.Fatalf("expected the drain to hit the provider, got %d requests", len(provider.requests))
	}
	messages := provider.requests[0].Messages
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "first queued") {
		t.Fatalf("expected the drained head to be the provider's last message, got %#v", messages)
	}
}

func TestUpArrowEditsOnlyTheLastQueuedMessage(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.queuedMessages = []string{"head", "middle", "tail"}

	updated, _ := m.Update(testKey(tea.KeyUp))
	next := updated.(model)

	if got := next.queuedMessages; len(got) != 2 || got[0] != "head" || got[1] != "middle" {
		t.Fatalf("expected only the tail popped, queue = %v", got)
	}
	if got := next.composerValue(); got != "tail" {
		t.Fatalf("expected the tail in the composer, got %q", got)
	}
}
