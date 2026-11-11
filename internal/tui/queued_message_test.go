package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
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

func TestPendingInterleaveRendersDimmedWithNotice(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.interleave.push("MARKINTER pending interleave")

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "❯ MARKINTER pending interleave") {
		t.Fatalf("expected the pending interleave as a dimmed Sussurro row, got:\n%s", view)
	}
	if !strings.Contains(view, "enviando após a ação atual…") {
		t.Fatalf("expected the interleave notice below the row, got:\n%s", view)
	}
}

func TestPendingInterleaveAndQueueRenderTogether(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.interleave.push("MARKINTER pending interleave")
	m.queuedMessages = []string{"MARKONE queued"}

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "MARKINTER pending interleave") {
		t.Fatalf("expected the pending interleave to stay visible, got:\n%s", view)
	}
	if !strings.Contains(view, "MARKONE queued") {
		t.Fatalf("expected the queued message to stay visible, got:\n%s", view)
	}
	if !strings.Contains(view, "enviando após a ação atual…") {
		t.Fatalf("expected the interleave notice, got:\n%s", view)
	}
	if !strings.Contains(view, "na fila (1)") {
		t.Fatalf("expected the queue notice with the count, got:\n%s", view)
	}
}

func TestComposerPlaceholderEditsLastQueuedMessage(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("queued followup")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	view := viewString(next.View())
	if !strings.Contains(view, "Pressione ↑ para editar a última da fila") {
		t.Fatalf("expected the placeholder to point at the last queued message, got:\n%s", view)
	}
	if strings.Contains(view, "Pressione ↑ para editar as mensagens na fila") {
		t.Fatalf("the ambiguous placeholder must be gone, got:\n%s", view)
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
	if !strings.Contains(view, "na fila (1) — ") {
		t.Fatalf("expected the Sussurro hint below the queued message, got:\n%s", view)
	}
	if !strings.Contains(view, "envia agora") {
		t.Fatalf("expected the send shortcut in the hint, got:\n%s", view)
	}
}

func TestQueuedMessagesRenderAllSeparatedInOrder(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.queuedMessages = []string{"MARKONE head message", "MARKTWO second queued", "MARKTHREE tail queued"}

	view := plainRender(t, viewString(m.View()))
	first := strings.Index(view, "MARKONE")
	second := strings.Index(view, "MARKTWO")
	third := strings.Index(view, "MARKTHREE")
	if first == -1 || second == -1 || third == -1 {
		t.Fatalf("expected every queued message to render, got:\n%s", view)
	}
	if !(first < second && second < third) {
		t.Fatalf("expected the queue in FIFO order (MARKONE < MARKTWO < MARKTHREE), got:\n%s", view)
	}
	if !strings.Contains(view, "na fila (3)") {
		t.Fatalf("expected the notice to count all queued messages, got:\n%s", view)
	}
	if !strings.Contains(view, "↑ edita a última") {
		t.Fatalf("expected the notice to say ↑ edits the last queued message, got:\n%s", view)
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

func TestEscCancelsAndReturnsQueueToComposerWithoutLaunching(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.provider = &scriptedProvider{scripts: textScripts("answer one", "answer two")}
	cancelled := false
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.runCancel = func() { cancelled = true }
	m.width = 96
	m.input.SetValue("queued followup")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	// First Esc only arms the cancel confirmation; the queue stays untouched.
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

	// The user keeps typing; the second Esc cancels and returns the queue.
	next.input.SetValue("in-progress draft")
	updated, _ = next.Update(testKey(tea.KeyEsc))
	next = updated.(model)

	if !cancelled {
		t.Fatal("second Esc must cancel the active run")
	}
	if next.pending {
		t.Fatal("double Esc must not start a new turn on its own")
	}
	if provider, ok := next.provider.(*fakeProvider); ok && len(provider.requests) != 0 {
		t.Fatalf("double Esc must not hit the provider, got %d request(s)", len(provider.requests))
	}
	if next.hasQueuedMessage() {
		t.Fatalf("expected the queue back in the composer, still queued: %v", next.queuedMessages)
	}
	if got := next.composerValue(); got != "queued followup\nin-progress draft" {
		t.Fatalf("expected the queued message above the draft, got %q", got)
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

// A mid-turn Ctrl+Enter injects the composer message into the running
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
// the queue now instead of waiting for the turn to end. The effect must be
// observable on the wire: the queued message rides the very next provider
// request, mid-turn.
func TestInterleaveEnterSendsQueueHead(t *testing.T) {
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
	doneA := make(chan tea.Msg, 1)
	go func() { doneA <- execCmd(cmdA) }()
	snapPath := filepath.Join(store.RootDir, m.activeSession.SessionID, sessions.ContinuationFile)
	waitForSnapshotCall(t, snapPath, "call-slow")

	m.input.SetValue("MARKHEAD queued followup")
	updated, _ = m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if !m.hasQueuedMessage() {
		t.Fatal("expected the follow-up queued while the turn runs")
	}

	updated, _ = m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if m.hasQueuedMessage() {
		t.Fatal("expected Ctrl+Enter to pop the queue head into the interleave")
	}

	close(release)
	late := <-doneA
	updated, _ = m.Update(late)
	m = updated.(model)

	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (turn + interleaved)", len(provider.requests))
	}
	found := false
	for _, message := range provider.requests[1].Messages {
		if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "MARKHEAD queued followup") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the queue head must ride the next provider request, got %#v", provider.requests[1].Messages)
	}
	if !transcriptContains(m.transcript, "MARKHEAD queued followup") {
		t.Fatal("expected the interleaved head as a transcript row")
	}
}

func TestInterleaveEnterIdleActsAsSubmit(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.input.SetValue("hello agent")

	updated, cmd := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if cmd == nil {
		t.Fatal("expected Ctrl+Enter with an idle composer to submit the prompt")
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
		t.Fatal("expected Ctrl+Enter with nothing pending to stay idle")
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

func TestDoubleEscReturnsStrandedInterleavesThenQueueToComposer(t *testing.T) {
	m := continuationHarness(t, testSessionStore(t), &scriptedProvider{scripts: textScripts("answer one", "answer two")}, nil)
	m.width = 96
	m.pending = true
	m.activeRunID = 5
	m.runID = 5
	cancelled := false
	m.runCancel = func() { cancelled = true }
	m.interleave.push("MARKSTRANDED interleave")
	m.queuedMessages = []string{"MARKQUEUED one", "MARKQUEUED two"}
	draft := "in-progress draft"
	m.input.SetValue(draft)

	updated, _ := m.Update(testKey(tea.KeyEsc))
	m = updated.(model)
	updated, _ = m.Update(testKey(tea.KeyEsc))
	m = updated.(model)

	if !cancelled {
		t.Fatal("second Esc must cancel the active run")
	}
	if m.pending {
		t.Fatal("double Esc must not start a new turn on its own")
	}
	if provider := m.provider.(*scriptedProvider); len(provider.requests) != 0 {
		t.Fatalf("double Esc must not hit the provider, got %d request(s)", len(provider.requests))
	}
	if got := m.composerValue(); got != "MARKSTRANDED interleave\nMARKQUEUED one\nMARKQUEUED two\n"+draft {
		t.Fatalf("expected stranded interleaves first, then the queue, then the draft, got %q", got)
	}
	if len(m.queuedMessages) != 0 {
		t.Fatalf("expected the queue back in the composer, still queued: %v", m.queuedMessages)
	}
	if got := m.interleave.drain(); len(got) != 0 {
		t.Fatalf("expected the stranded interleaves returned to the composer, still pending: %v", got)
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

// With the production sink wired (run.go), an interleaved operator message
// reaches the transcript live during the turn AND again in the end-of-turn
// rows: both rows carry one run-scoped dedup identity, so exactly one copy
// stays. The same text submitted in two different turns keeps both copies.
func TestInterleavedMessageAppearsOnceWithLiveSink(t *testing.T) {
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

	var mu sync.Mutex
	var liveRows []agentRowMsg
	sink := func(msg tea.Msg) {
		if row, ok := msg.(agentRowMsg); ok {
			mu.Lock()
			liveRows = append(liveRows, row)
			mu.Unlock()
		}
	}
	m := newModel(context.Background(), Options{
		Cwd:                t.TempDir(),
		ProviderName:       "test",
		ModelName:          "test-model",
		Provider:           provider,
		Registry:           registry,
		SessionStore:       store,
		RuntimeMessageSink: sink,
	})
	if writer := m.snapshotWriter; writer != nil {
		t.Cleanup(func() { writer.Flush(10 * time.Second) })
	}
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

	close(release)
	late := <-doneA

	// Bubble Tea delivers the sink's messages to Update BEFORE the Cmd's own
	// return value (the agentResponseMsg). Replicate that order.
	mu.Lock()
	rows := append([]agentRowMsg(nil), liveRows...)
	mu.Unlock()
	for _, row := range rows {
		updated, _ = m.Update(row)
		m = updated.(model)
	}
	updated, _ = m.Update(late)
	m = updated.(model)

	count := 0
	for _, row := range m.transcript {
		if row.kind == rowUser && strings.Contains(row.text, "operator interleaves now") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("interleave rowUser appears %d times, want exactly 1 (live row + end-of-turn row must dedupe)", count)
	}
}

// A turn in flight owns its interleaves: polling the launcher while the run
// is active must not drain the buffer and demote the message to the FIFO.
func TestLaunchWhileRunActiveKeepsInterleaveBuffer(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.interleave.push("BELONGS TO THE ACTIVE TURN")

	next, cmd := m.launchQueuedMessageIfReady()
	if cmd != nil {
		t.Fatal("no queued message was staged, so no new run must launch")
	}
	if got := next.interleave.drain(); len(got) != 1 {
		t.Fatalf("launcher drained the active turn's interleave (remaining %d, want 1); queuedMessages=%#v",
			len(got), next.queuedMessages)
	}
	if len(next.queuedMessages) != 0 {
		t.Fatalf("active turn interleave was moved into the FIFO: %#v (want empty)", next.queuedMessages)
	}
}

// A cancelled run's goroutine can outlive the next launch and finish a tool
// step late: its drain touches only its own buffer, never the next run's.
// Each beginRun mints a fresh buffer for exactly this reason.
func TestCancelledRunDrainCannotStealNextRunInterleaves(t *testing.T) {
	m := newQueuedMessageTestModel(t)

	first := m.beginRun(nil)
	bufA := first.interleave
	if bufA == nil {
		t.Fatal("run A has no interleave buffer")
	}
	bufA.push("pushed while run A is active")

	// Esc: run A cancels; the next run starts while A's goroutine may
	// still be alive finishing a tool.
	second := first.beginRun(nil)
	if second.interleave == nil {
		t.Fatal("run B has no interleave buffer")
	}
	if second.interleave == bufA {
		t.Fatal("run B shares run A's interleave buffer: run A's late drain would steal run B's interleaves")
	}
	second.interleave.push("for run B")

	// The cancelled run's late drain (its InterleaveMessages closure holds
	// only bufA) fires after the next launch.
	if got := bufA.drain(); len(got) != 1 {
		t.Fatalf("run A's late drain must take only its own message, got %#v", got)
	}
	got := second.interleave.drain()
	if len(got) != 1 || got[0].Content != "for run B" {
		t.Fatalf("run B's interleave was stolen by run A's late drain: %#v", got)
	}
}

// A run cancelled outside the end-of-turn seam (Ctrl+C first press, /goal
// pause/clear) must not silently drop the operator's unconsumed interleaves:
// cancelRun returns them to the composer with the queue — nothing vanishes
// in the per-run buffer swap and nothing launches on its own.
func TestCancelRunRequeuesStrandedInterleaveForNextTurn(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("answer", "second")}
	m := continuationHarness(t, testSessionStore(t), provider, nil)
	m.width = 96
	m.pending = true
	m.activeRunID = 1
	m.runID = 1

	m.input.SetValue("stranded by cancel")
	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if m.interleave == nil || len(m.interleave.texts) != 1 {
		t.Fatalf("expected the interleave staged for the running turn, got %+v", m.interleave)
	}

	// Ctrl+C first press: cancels the run without reaching
	// launchQueuedMessageIfReady. Every cancel returns the pending queue
	// to the composer — the stranded interleave included.
	updated, _ = m.Update(testKeyCtrl('c'))
	m = updated.(model)
	if m.pending {
		t.Fatal("expected Ctrl+C to cancel the running turn")
	}
	if len(m.queuedMessages) != 0 {
		t.Fatalf("expected the stranded interleave back in the composer, still queued: %#v", m.queuedMessages)
	}
	if got := m.composerValue(); !strings.Contains(got, "stranded by cancel") {
		t.Fatalf("expected the stranded interleave in the composer, got %q", got)
	}
	updated, cmd := m.launchQueuedMessageIfReady()
	m = updated.(model)
	if cmd != nil {
		t.Fatal("nothing must launch on its own after a cancel")
	}
	if len(provider.requests) != 0 {
		t.Fatalf("expected no provider request after the cancel, got %d", len(provider.requests))
	}
}

func TestQueuedHintDegradesToAgoraUntilTerminalConfirmsDisambiguation(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("queued followup")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	view := plainRender(t, viewString(next.View()))
	if !strings.Contains(view, "/agora envia agora") {
		t.Fatalf("expected the hint to offer /agora before the terminal confirms kitty disambiguation, got:\n%s", view)
	}
	if strings.Contains(view, "Ctrl+Enter envia agora") {
		t.Fatalf("hint must not advertise Ctrl+Enter before the terminal confirms it, got:\n%s", view)
	}
	if strings.Contains(view, "⌘") {
		t.Fatalf("hint must never speak of ⌘, got:\n%s", view)
	}
}

func TestQueuedHintAdvertisesCtrlEnterAfterTerminalConfirmsDisambiguation(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("queued followup")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	updated, _ = next.Update(tea.KeyboardEnhancementsMsg{Flags: ansi.KittyDisambiguateEscapeCodes})
	next = updated.(model)

	view := plainRender(t, viewString(next.View()))
	if !strings.Contains(view, "Ctrl+Enter envia agora") {
		t.Fatalf("expected the hint to advertise Ctrl+Enter once the terminal confirms kitty disambiguation, got:\n%s", view)
	}
	if strings.Contains(view, "/agora envia agora") {
		t.Fatalf("hint must not fall back to /agora after the terminal confirms, got:\n%s", view)
	}
	if strings.Contains(view, "⌘") {
		t.Fatalf("hint must never speak of ⌘, got:\n%s", view)
	}
}

func TestInterleaveCtrlEnterInterleavesCommonPrompt(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("MARKCTRL plain prompt")

	updated, _ := m.Update(testInterleaveEnter(t, false))
	next := updated.(model)

	if got := next.composerValue(); got != "" {
		t.Fatalf("expected the interleave to consume the composer, got %q", got)
	}
	if next.hasQueuedMessage() {
		t.Fatalf("a plain prompt must not be enqueued, queue = %v", next.queuedMessages)
	}
}

func TestInterleaveEnterWithPermissionModalActsAsEnter(t *testing.T) {
	setup := func() model {
		m := newQueuedMessageTestModel(t)
		m.pending = true
		m.activeRunID = 1
		m.runID = 1
		m.pendingPermission = &pendingPermissionPrompt{}
		m.input.SetValue("/model gpt-4o")
		return m
	}

	plainUpdated, _ := setup().Update(testKey(tea.KeyEnter))
	plain := plainUpdated.(model)

	modUpdated, _ := setup().Update(testInterleaveEnter(t, true))
	mod := modUpdated.(model)

	if plain.pendingPermission != nil {
		t.Fatal("plain Enter must confirm the permission modal")
	}
	if mod.pendingPermission != nil {
		t.Fatal("Ctrl+Enter with a permission modal open must confirm the modal exactly like Enter")
	}
	if mod.composerValue() != plain.composerValue() {
		t.Fatalf("Ctrl+Enter kept composer %q, Enter kept %q — they must be identical", mod.composerValue(), plain.composerValue())
	}
	if len(mod.queuedMessages) != len(plain.queuedMessages) {
		t.Fatalf("Ctrl+Enter queued %d message(s), Enter queued %d — they must be identical: %#v vs %#v",
			len(mod.queuedMessages), len(plain.queuedMessages), mod.queuedMessages, plain.queuedMessages)
	}
}

func TestInterleaveEnterWithSpecReviewModalActsAsEnter(t *testing.T) {
	setup := func() model {
		m := newQueuedMessageTestModel(t)
		m.pending = true
		m.activeRunID = 1
		m.runID = 1
		m.pendingSpecReview = &pendingSpecReviewPrompt{SpecID: "spec-audit"}
		m.input.SetValue("should not be queued")
		return m
	}

	plainUpdated, _ := setup().Update(testKey(tea.KeyEnter))
	plain := plainUpdated.(model)

	modUpdated, _ := setup().Update(testInterleaveEnter(t, true))
	mod := modUpdated.(model)

	if len(mod.queuedMessages) != len(plain.queuedMessages) {
		t.Fatalf("Ctrl+Enter with a spec-review modal queued %d message(s), Enter queued %d — they must be identical: %#v vs %#v",
			len(mod.queuedMessages), len(plain.queuedMessages), mod.queuedMessages, plain.queuedMessages)
	}
	if mod.composerValue() != plain.composerValue() {
		t.Fatalf("Ctrl+Enter with a spec-review modal changed composer to %q, Enter kept %q", mod.composerValue(), plain.composerValue())
	}
	if mod.pendingSpecReview == nil {
		t.Fatal("Ctrl+Enter with a spec-review modal must not dismiss the modal")
	}
}

func TestInterleaveEnterInDetailedTranscriptActsAsEnter(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.transcriptDetailed = true
	m.input.SetValue("/transcript")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)
	if next.transcriptDetailed {
		t.Fatal("Ctrl+Enter on /transcript in the detailed transcript must toggle it off, exactly like Enter")
	}

	m = newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.transcriptDetailed = true
	m.input.SetValue("keep this draft")

	updated, _ = m.Update(testInterleaveEnter(t, true))
	next = updated.(model)
	if !next.transcriptDetailed {
		t.Fatal("Ctrl+Enter with free text in the detailed transcript must not leave detailed mode")
	}
	if got := next.composerValue(); got != "keep this draft" {
		t.Fatalf("Ctrl+Enter in the detailed transcript must leave the composer intact, got %q", got)
	}
}

func TestInterleaveEnterWithSlashCommandExecutesCommand(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.input.SetValue("/help")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if !transcriptContains(next.transcript, "Comandos") {
		t.Fatalf("expected Ctrl+Enter with a slash command to execute the command like Enter, got transcript %#v", next.transcript)
	}
	if got := next.composerValue(); got != "" {
		t.Fatalf("expected the command submission to consume the composer, got %q", got)
	}
	if next.interleave != nil && len(next.interleave.snapshot()) != 0 {
		t.Fatal("a slash command must never be staged as a raw interleave")
	}
}

func TestInterleaveEnterWithSpecialistMentionActsAsEnter(t *testing.T) {
	setup := func() model {
		m := newQueuedMessageTestModel(t)
		m.pending = true
		m.activeRunID = 1
		m.runID = 1
		m.agentOptions.Specialists = []agent.SpecialistInfo{{Name: "reviewer"}}
		m.input.SetValue("@reviewer fix this")
		return m
	}

	plainUpdated, _ := setup().Update(testKey(tea.KeyEnter))
	plain := plainUpdated.(model)

	modUpdated, _ := setup().Update(testInterleaveEnter(t, true))
	mod := modUpdated.(model)

	if len(mod.queuedMessages) != 1 || mod.queuedMessages[0] != "@reviewer fix this" {
		t.Fatalf("expected the specialist mention to ride the queue like Enter, got %#v", mod.queuedMessages)
	}
	if len(plain.queuedMessages) != 1 {
		t.Fatalf("plain Enter queued %#v, Ctrl+Enter must match", plain.queuedMessages)
	}
	if mod.interleave != nil && len(mod.interleave.snapshot()) != 0 {
		t.Fatal("a leading @specialist mention must not be interleaved unexpanded")
	}
}

func TestInterleaveEnterWithPendingAttachmentsKeepsComposer(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.pendingImages = []qgruntime.ImageBlock{previewImageBlock(t)}
	m.input.SetValue("analyze this")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if !transcriptContains(next.transcript, "Anexos não vão com Ctrl+Enter") {
		t.Fatalf("expected the short attachments notice, got transcript %#v", next.transcript)
	}
	if got := next.composerValue(); got != "analyze this" {
		t.Fatalf("expected the composer to stay intact, got %q", got)
	}
	if next.hasQueuedMessage() {
		t.Fatalf("attachments must not ride the queue either, got %#v", next.queuedMessages)
	}
	if next.interleave != nil && len(next.interleave.snapshot()) != 0 {
		t.Fatal("attachments must not interleave")
	}
	if len(next.pendingImages) != 1 {
		t.Fatal("the staged image must stay pending")
	}
}

func TestInterleaveEnterWithPendingDocumentKeepsComposer(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.pendingDocuments = []pendingDocument{{label: "report.pdf", text: "document text"}}
	m.input.SetValue("summarize this")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if !transcriptContains(next.transcript, "Anexos não vão com Ctrl+Enter") {
		t.Fatalf("expected the short attachments notice, got transcript %#v", next.transcript)
	}
	if got := next.composerValue(); got != "summarize this" {
		t.Fatalf("expected the composer to stay intact, got %q", got)
	}
	if len(next.pendingDocuments) != 1 {
		t.Fatal("the staged document must stay pending")
	}
}

func TestInterleaveEnterWithAttachmentsAndEmptyComposerSendsQueueHead(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.pendingImages = []qgruntime.ImageBlock{previewImageBlock(t)}
	m.queuedMessages = []string{"MARKHEAD queued followup"}

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if transcriptContains(next.transcript, "Anexos não vão com Ctrl+Enter") {
		t.Fatal("popping the queue head carries no attachments; the notice must not fire")
	}
	if len(next.queuedMessages) != 0 {
		t.Fatalf("expected the queue head interleaved, queue = %v", next.queuedMessages)
	}
	if len(next.pendingImages) != 1 {
		t.Fatal("the staged image must stay pending")
	}
}

// An open suggestion overlay does not claim Ctrl+Enter: the key is an
// explicit send-now gesture, so the literal composer text interleaves into
// the running turn and the suggestion is left for Enter/Tab.
func TestInterleaveEnterIntercalatesLiteralTextWithSuggestionOverlayOpen(t *testing.T) {
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
	doneA := make(chan tea.Msg, 1)
	go func() { doneA <- execCmd(cmdA) }()
	snapPath := filepath.Join(store.RootDir, m.activeSession.SessionID, sessions.ContinuationFile)
	waitForSnapshotCall(t, snapPath, "call-slow")

	m.input.SetValue("abra @arq MARKLITERAL texto")
	m.suggestions = []commandSuggestion{{Name: "@README.md", Desc: "arquivo"}}
	m.suggestionsAreFiles = true
	if !m.suggestionsActive() {
		t.Fatal("precondition: the file suggestion overlay must be open")
	}

	updated, _ = m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if got := m.composerValue(); got != "" {
		t.Fatalf("Ctrl+Enter must consume the composer literally, got %q", got)
	}
	if m.suggestionsActive() {
		t.Fatal("Ctrl+Enter must dismiss the overlay instead of accepting the suggestion")
	}

	close(release)
	late := <-doneA
	updated, _ = m.Update(late)
	m = updated.(model)

	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (turn + interleaved)", len(provider.requests))
	}
	found := false
	for _, message := range provider.requests[1].Messages {
		if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "abra @arq MARKLITERAL texto") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the literal composer text must reach the provider, got %#v", provider.requests[1].Messages)
	}
}

func TestCtrlCCancelReturnsQueueToComposerAboveDraftWithoutLaunching(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "hello"},
		{Type: qgruntime.StreamEventDone},
	}}
	m.provider = provider
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.queuedMessages = []string{"MARKFIRST queued", "MARKSECOND queued"}
	m.input.SetValue("in-progress draft")
	cancelled := false
	m.runCancel = func() { cancelled = true }

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)

	if !cancelled {
		t.Fatal("Ctrl+C must cancel the run")
	}
	if next.pending {
		t.Fatal("expected the run cancelled")
	}
	if next.hasQueuedMessage() {
		t.Fatalf("expected the queue returned to the composer, still queued: %v", next.queuedMessages)
	}
	if got := next.composerValue(); got != "MARKFIRST queued\nMARKSECOND queued\nin-progress draft" {
		t.Fatalf("expected the queue above the draft, got %q", got)
	}
	if provider := m.provider.(*fakeProvider); len(provider.requests) != 0 {
		t.Fatalf("nothing may launch on its own after the cancel, got %d request(s)", len(provider.requests))
	}
}

func TestGoalPauseCancelReturnsQueueToComposerWithoutLaunching(t *testing.T) {
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "hello"},
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.activeSession.Goal = &sessions.Goal{Objective: "Ship", Status: sessions.GoalStatusActive}
	m.queuedMessages = []string{"MARKFIRST queued", "MARKSECOND queued"}
	m.input.SetValue("/goal pause")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if next.pending {
		t.Fatal("expected /goal pause to cancel the running turn")
	}
	if next.hasQueuedMessage() {
		t.Fatalf("expected the queue returned to the composer, still queued: %v", next.queuedMessages)
	}
	if got := next.composerValue(); got != "MARKFIRST queued\nMARKSECOND queued" {
		t.Fatalf("expected the queue in the composer, got %q", got)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("nothing may launch on its own after the cancel, got %d request(s)", len(provider.requests))
	}
}
