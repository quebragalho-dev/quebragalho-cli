package tui

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// fakeSendNowSignal stands in for the run's yieldnow.Signal: result=true
// wakes the (backgroundable) wait so messages deliver inside the running
// turn; result=false (or a nil signal on the model) interrupts the turn and
// relaunches everything as one new turn.
type fakeSendNowSignal struct {
	result bool
	calls  int
}

func (f *fakeSendNowSignal) Request() bool {
	f.calls++
	return f.result
}

// Text-streaming provider that gates mid-stream: the first request streams
// one delta and then parks until released; the second request returns at
// once. It never parks a backgroundable wait, so a send-now during the
// stream wakes nothing — the interrupt path.
type streamingGateProvider struct {
	started  chan struct{}
	release  chan struct{}
	requests []qgruntime.CompletionRequest
}

func (provider *streamingGateProvider) StreamCompletion(
	ctx context.Context,
	request qgruntime.CompletionRequest,
) (<-chan qgruntime.StreamEvent, error) {
	provider.requests = append(provider.requests, request)
	ch := make(chan qgruntime.StreamEvent, 4)
	if len(provider.requests) == 1 {
		ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventText, Content: "chunk "}
		close(provider.started)
		<-provider.release
	}
	ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventText, Content: "streamed answer"}
	ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventDone}
	close(ch)
	return ch, nil
}

// Ctrl+Enter during a text stream — nothing backgroundable is parked, so
// send-now interrupts and the batch launches in one new turn once the dead
// run drains.
func TestCtrlEnterDuringTextStreamingInterruptsAndLaunchesNewTurn(t *testing.T) {
	provider := &streamingGateProvider{started: make(chan struct{}), release: make(chan struct{})}
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.input.SetValue("run A")
	updated, cmdA := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	doneA := make(chan tea.Msg, 1)
	go func() { doneA <- execCmd(cmdA) }()

	// The run is mid-stream; nothing is parked on the run's Signal.
	select {
	case <-provider.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the provider stream never started")
	}

	m.input.SetValue("MARKSTREAM interrupt and relaunch")
	updated, _ = m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if len(m.pendingSendNow) != 1 {
		t.Fatalf("the batch parks until the dead run drains, got %v", m.pendingSendNow)
	}

	// The stream completes; the dead run drains and the batch launches.
	close(provider.release)
	late := <-doneA
	updated, flushCmd := m.Update(late)
	m = updated.(model)
	if !m.pending {
		t.Fatal("expected the send-now batch to launch the new turn")
	}
	if m.pendingSendNow != nil {
		t.Fatal("expected the parked batch consumed by the launch")
	}
	if flushCmd == nil {
		t.Fatal("expected the new turn's agent command")
	}
	_ = execCmd(flushCmd)
	providersLen := len(provider.requests)
	if providersLen < 2 {
		t.Fatalf("provider requests = %d, want at least 2 (original stream + relaunched turn)", providersLen)
	}
	last := provider.requests[len(provider.requests)-1]
	found := false
	for _, message := range last.Messages {
		if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "MARKSTREAM") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the new turn's wire must carry MARKSTREAM, got %#v", last.Messages)
	}
}

// Ctrl+Enter DURING a real exec_command yield wait wakes it at once; the
// operator message rides the SAME turn; the process keeps running as a
// background session.
func TestCtrlEnterDuringRealExecYieldDeliversInSameTurn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test command uses a POSIX shell")
	}
	dir := t.TempDir()
	manager := execution.NewProcessManager(execution.ProcessManagerOptions{})
	t.Cleanup(func() { manager.StopAll() })
	registry := tools.NewRegistry()
	registry.Register(tools.NewExecCommandTool(dir, manager))
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-exec", ToolName: "exec_command"},
			{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-exec", ArgumentsFragment: `{"cmd":"sleep 30","yield_time_ms":30000}`},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-exec"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := continuationHarness(t, testSessionStore(t), provider, registry)
	m.width = 96
	m.permissionMode = agent.PermissionModeUnsafe

	m.input.SetValue("run the sleeper")
	updated, cmdA := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmdA == nil {
		t.Fatal("expected an agent command for turn A")
	}
	doneA := make(chan tea.Msg, 1)
	go func() { doneA <- execCmd(cmdA) }()

	// The real process must exist with its yield wait parked on the run's
	// Signal before the send-now fires.
	for i := 0; ; i++ {
		if manager.Len() == 1 {
			break
		}
		if i > 1000 {
			t.Fatal("the exec_command never started its sleep")
		}
		time.Sleep(10 * time.Millisecond)
	}

	m.input.SetValue("MARKSAMETURN operator sends now")
	updated, _ = m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if got := m.composerValue(); got != "" {
		t.Fatalf("the send-now must consume the composer, got %q", got)
	}

	late := <-doneA
	updated, _ = m.Update(late)
	m = updated.(model)

	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (same-turn delivery)", len(provider.requests))
	}
	found := false
	for _, message := range provider.requests[1].Messages {
		if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "MARKSAMETURN") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the send-now message must reach the provider in the same turn, got %#v", provider.requests[1].Messages)
	}
	if m.pending {
		t.Fatal("the turn must have completed")
	}
	// The process kept running as a background session — the yield woke
	// early, the sleep was not killed.
	if got := manager.Len(); got != 1 {
		t.Fatalf("manager holds %d processes, want 1 (the still-running sleep)", got)
	}
}

func TestInterleaveBufferPopLastRemovesOnlyTheLastPending(t *testing.T) {
	buffer := &interleaveBuffer{}
	if _, ok := buffer.popLast(); ok {
		t.Fatal("popLast on an empty buffer must report false")
	}
	buffer.push("first")
	buffer.push("second")
	buffer.push("third")
	if text, ok := buffer.popLast(); !ok || text != "third" {
		t.Fatalf("popLast = %q, %v — want third, true", text, ok)
	}
	if got := buffer.pendingCount(); got != 2 {
		t.Fatalf("pendingCount = %d, want 2", got)
	}
	if messages := buffer.drain(); len(messages) != 2 || messages[0].Content != "first" || messages[1].Content != "second" {
		t.Fatalf("drain after popLast = %#v, want [first second] in order", messages)
	}
	if _, ok := buffer.popLast(); ok {
		t.Fatal("popLast after a drain must report false")
	}
}

func TestEnterDuringTurnStagesInterleaveWithoutStartingRun(t *testing.T) {
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
	if len(next.queuedMessages) != 0 {
		t.Fatalf("a plain prompt during a run must not enter the FIFO, queue = %v", next.queuedMessages)
	}
	if next.interleave == nil || next.interleave.pendingCount() != 1 {
		t.Fatalf("expected the plain prompt interleaved into the running turn, got %+v", next.interleave)
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
	if strings.Contains(view, "enviando após a ação atual…") {
		t.Fatalf("the interleave notice must be gone, got:\n%s", view)
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
	if strings.Contains(view, "enviando após a ação atual…") {
		t.Fatalf("the interleave notice must be gone, got:\n%s", view)
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
	m.queuedMessages = []string{"queued followup"}

	view := viewString(m.View())
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
	m.queuedMessages = []string{"summarize the failing test output"}

	view := plainRender(t, viewString(m.View()))
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

func TestEscCancelsAndReturnsPendingInterleaveToComposerWithoutLaunching(t *testing.T) {
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
	if next.interleave == nil || next.interleave.pendingCount() != 1 {
		t.Fatalf("expected the plain prompt interleaved while the run is active, got %+v", next.interleave)
	}

	// First Esc only arms the cancel confirmation; the pending message stays.
	updated, _ = next.Update(testKey(tea.KeyEsc))
	next = updated.(model)

	if cancelled {
		t.Fatal("first Esc must only arm the cancel confirmation")
	}
	if next.interleave == nil || next.interleave.pendingCount() != 1 {
		t.Fatal("first Esc must not drop the pending message")
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
		t.Fatalf("expected no FIFO entry left behind, still queued: %v", next.queuedMessages)
	}
	if got := next.composerValue(); got != "queued followup\nin-progress draft" {
		t.Fatalf("expected the pending interleave above the draft, got %q", got)
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

func TestUpArrowPopsPendingInterleaveIntoComposer(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("interleaved followup")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	updated, _ = next.Update(testKey(tea.KeyUp))
	next = updated.(model)

	if next.interleave != nil && next.interleave.pendingCount() != 0 {
		t.Fatalf("expected ↑ to pop the pending interleave, still pending: %v", next.interleave.snapshot())
	}
	if got := next.composerValue(); got != "interleaved followup" {
		t.Fatalf("expected the interleaved message back in the composer, got %q", got)
	}
}

func TestUpArrowPutsInterleaveAboveDraft(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.input.SetValue("first message")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	next.input.SetValue("in-progress draft")

	updated, _ = next.Update(testKey(tea.KeyUp))
	next = updated.(model)

	if next.interleave != nil && next.interleave.pendingCount() != 0 {
		t.Fatalf("expected ↑ to pop the pending interleave, still pending: %v", next.interleave.snapshot())
	}
	if got := next.composerValue(); got != "first message\nin-progress draft" {
		t.Fatalf("expected the interleaved text above the draft, got %q", got)
	}
}

// With both an interleave and a FIFO pending, ↑ pops the most recently
// pushed one first (the interleave), then the FIFO tail.
func TestUpArrowPrefersInterleaveOverQueueTail(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.queuedMessages = []string{"fifo tail"}
	m.interleave = &interleaveBuffer{}
	m.interleave.push("interleaved newer")

	updated, _ := m.Update(testKey(tea.KeyUp))
	next := updated.(model)
	if got := next.composerValue(); got != "interleaved newer" {
		t.Fatalf("expected ↑ to pop the interleave first, got %q", got)
	}
	if got := next.queuedMessages; len(got) != 1 || got[0] != "fifo tail" {
		t.Fatalf("the FIFO must stay untouched while the interleave is pending, got %v", got)
	}

	updated, _ = next.Update(testKey(tea.KeyUp))
	next = updated.(model)
	if got := next.queuedMessages; len(got) != 0 {
		t.Fatalf("expected the FIFO tail popped next, queue = %v", got)
	}
	if got := next.composerValue(); got != "fifo tail\ninterleaved newer" {
		t.Fatalf("expected the FIFO tail above the earlier pop, got %q", got)
	}
}

func TestComposerHintShowsAndClearsWithPendingMessage(t *testing.T) {
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

// Plain prompts entered during a run interleave in order: the loop drains
// them in the same turn they were typed.
func TestSecondInterleaveStacksBehindFirstInBuffer(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.input.SetValue("first prompt")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	next.input.SetValue("second prompt")

	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)

	if next.interleave == nil {
		t.Fatal("expected both plain prompts interleaved into the running turn")
	}
	got := next.interleave.snapshot()
	if len(got) != 2 || got[0] != "first prompt" || got[1] != "second prompt" {
		t.Fatalf("expected the interleave buffer [first prompt second prompt], got %v", got)
	}
	if len(next.queuedMessages) != 0 {
		t.Fatalf("plain prompts must not enter the FIFO, got %v", next.queuedMessages)
	}
}

// The FIFO keeps its own order: messages enqueued through the queue path
// (here @specialist mentions, which never interleave unexpanded) stack in
// order of arrival.
func TestSecondQueuedMessageStacksUnderFirst(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.agentOptions.Specialists = []agent.SpecialistInfo{{Name: "reviewer"}}
	m.input.SetValue("@reviewer first queued")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	next.input.SetValue("@reviewer second queued")

	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)

	if got := next.queuedMessages; len(got) != 2 || got[0] != "@reviewer first queued" || got[1] != "@reviewer second queued" {
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
	// beginRun resets the run's signal slot, so install the fake after the
	// turn is live — same as the wiring when it lands: beginRun creates
	// the run's Signal and the agent side wakes it.
	signal := &fakeSendNowSignal{result: true}
	m.yieldNow = signal
	updated, _ = m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if got := m.composerValue(); got != "" {
		t.Fatalf("expected the send-now to consume the composer, got %q", got)
	}
	if signal.calls != 1 {
		t.Fatalf("expected Request() called exactly once, got %d", signal.calls)
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

// Send-now carries only common messages: with an empty composer and a queue
// pending, Ctrl+Enter leaves the FIFO in place — it drains one per turn
// after the running turn, as always.
func TestInterleaveEnterWithEmptyComposerKeepsQueue(t *testing.T) {
	m := continuationHarness(t, testSessionStore(t), &scriptedProvider{scripts: textScripts("answer")}, nil)
	m.width = 96
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.yieldNow = &fakeSendNowSignal{result: true}
	m.queuedMessages = []string{"MARKHEAD queued followup"}

	updated, cmd := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)
	if cmd != nil {
		t.Fatal("send-now with no common work must not launch or interrupt anything")
	}
	if got := next.queuedMessages; len(got) != 1 || got[0] != "MARKHEAD queued followup" {
		t.Fatalf("the FIFO must stay untouched, got %v", got)
	}
	if next.interleave != nil && next.interleave.pendingCount() != 0 {
		t.Fatalf("nothing may be staged for the running turn, got %v", next.interleave.snapshot())
	}
	if !next.pending {
		t.Fatal("the running turn must stay pending")
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

// A mid-turn plain prompt that never sees a tool batch (text-only answer)
// would strand in the buffer; at the end of the turn the leftovers launch
// TOGETHER as separate user messages, in order — never concatenated, never
// requeued silently.
func TestInterleaveStrandedAfterTextOnlyTurnLaunchesSobrasTogetherInOrder(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("answer one", "answer two")}
	m := continuationHarness(t, testSessionStore(t), provider, nil)
	m.width = 96
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.interleave = &interleaveBuffer{}

	for _, text := range []string{"first leftover", "second leftover"} {
		m.input.SetValue(text)
		updated, _ := m.Update(testKey(tea.KeyEnter))
		m = updated.(model)
	}
	if got := m.interleave.pendingCount(); got != 2 {
		t.Fatalf("expected both plain prompts interleaved, got %d", got)
	}

	// The turn ends with a text-only answer; no tool batch ever drains the
	// interleave. The stranded messages launch as ONE new turn, as separate
	// user messages, in order.
	updated, cmd := m.Update(agentResponseMsg{runID: 1})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected the stranded sobras to launch as the next turn")
	}
	if !m.pending {
		t.Fatal("expected the stranded sobras to start a new turn")
	}
	if m.interleave != nil && m.interleave.pendingCount() != 0 {
		t.Fatal("expected the interleave buffer drained")
	}
	if len(m.queuedMessages) != 0 {
		t.Fatalf("sobras must not ride the FIFO, got %v", m.queuedMessages)
	}
	_ = execCmd(cmd)
	req := provider.requests[len(provider.requests)-1]
	var userMessages []string
	for _, message := range req.Messages {
		if message.Role == qgruntime.MessageRoleUser {
			userMessages = append(userMessages, message.Content)
		}
	}
	want := []string{"first leftover", "second leftover"}
	if len(userMessages) != len(want) {
		t.Fatalf("user messages = %#v, want %#v", userMessages, want)
	}
	for i := range want {
		if userMessages[i] != want[i] {
			t.Fatalf("user message %d = %q, want %q (order); all: %#v", i, userMessages[i], want[i], userMessages)
		}
	}
	if strings.Contains(strings.Join(userMessages, "|"), "first leftover\nsecond leftover") {
		t.Fatal("sobras must not be concatenated into one message")
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
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if m.interleave == nil || m.interleave.pendingCount() != 1 {
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

func TestQueuedHintFallsBackToLeaderChordUntilTerminalConfirmsDisambiguation(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.queuedMessages = []string{"queued followup"}

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "Ctrl+X Ctrl+S envia agora") {
		t.Fatalf("expected the hint to offer Ctrl+X Ctrl+S before the terminal confirms kitty disambiguation, got:\n%s", view)
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
	m.queuedMessages = []string{"queued followup"}
	updated, _ := m.Update(tea.KeyboardEnhancementsMsg{Flags: ansi.KittyDisambiguateEscapeCodes})
	next := updated.(model)

	view := plainRender(t, viewString(next.View()))
	if !strings.Contains(view, "Ctrl+Enter envia agora") {
		t.Fatalf("expected the hint to advertise Ctrl+Enter once the terminal confirms kitty disambiguation, got:\n%s", view)
	}
	if strings.Contains(view, "Ctrl+X Ctrl+S envia agora") {
		t.Fatalf("hint must not fall back to the leader chord after the terminal confirms, got:\n%s", view)
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

	if !transcriptContains(next.transcript, "Anexos não vão com Ctrl+X Ctrl+S") {
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

// The attachments refusal names the send chord the user can ACTUALLY
// press: Ctrl+X Ctrl+S before the terminal confirms kitty disambiguation,
// Ctrl+Enter after.
func TestAttachmentsNoticeNamesTheTerminalSafeSendChord(t *testing.T) {
	newModelWithAttachments := func(disambiguation bool) model {
		m := newQueuedMessageTestModel(t)
		m.pending = true
		m.activeRunID = 1
		m.runID = 1
		m.keyboardDisambiguation = disambiguation
		m.pendingImages = []qgruntime.ImageBlock{previewImageBlock(t)}
		m.input.SetValue("analyze this")
		return m
	}

	plain := newModelWithAttachments(false)
	updated, _ := plain.Update(testInterleaveEnter(t, true))
	next := updated.(model)
	if !transcriptContains(next.transcript, "Anexos não vão com Ctrl+X Ctrl+S") {
		t.Fatalf("expected the notice to name Ctrl+X Ctrl+S, got transcript %#v", next.transcript)
	}
	if transcriptContains(next.transcript, "Anexos não vão com Ctrl+Enter") {
		t.Fatalf("the notice must not name an unsendable chord, got transcript %#v", next.transcript)
	}

	dup := newModelWithAttachments(true)
	updated, _ = dup.Update(testInterleaveEnter(t, true))
	next = updated.(model)
	if !transcriptContains(next.transcript, "Anexos não vão com Ctrl+Enter") {
		t.Fatalf("expected the notice to name Ctrl+Enter once disambiguation is confirmed, got transcript %#v", next.transcript)
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

	if !transcriptContains(next.transcript, "Anexos não vão com Ctrl+X Ctrl+S") {
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
	m.yieldNow = &fakeSendNowSignal{result: true}

	updated, cmd := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if transcriptContains(next.transcript, "Anexos não vão com Ctrl+X Ctrl+S") {
		t.Fatal("an empty composer carries no attachments; the notice must not fire")
	}
	if cmd != nil {
		t.Fatal("send-now with no common work must not launch or interrupt anything")
	}
	if len(next.queuedMessages) != 1 {
		t.Fatalf("the FIFO must stay untouched, queue = %v", next.queuedMessages)
	}
	if len(next.pendingImages) != 1 {
		t.Fatal("the staged image must stay pending")
	}
}

// Send-now must never carry FIFO items: the FIFO only holds what WAITS for
// the end of the turn (slash commands, @specialist mentions, messages with
// attachments). Sending them now would hit the model as raw text — "/model x"
// as a user message instead of a command, a mention without expansion. The
// FIFO stays untouched and drains one per turn afterwards, as always.
func TestSendNowKeepsSpecialistQueueUntilTurnEnds(t *testing.T) {
	setup := func(signal *fakeSendNowSignal) (model, *scriptedProvider) {
		m := continuationHarness(t, testSessionStore(t), &scriptedProvider{scripts: textScripts("FRESH answer", "ANSWER ONE", "ANSWER TWO")}, nil)
		m.width = 96
		m.pending = true
		m.activeRunID = 1
		m.runID = 1
		cancelled := false
		m.runCancel = func() { cancelled = true }
		m.yieldNow = signal
		m.queuedMessages = []string{"/model glm-5.3", "@reviewer check this"}
		m.input.SetValue("QF-DRAFT")
		_ = cancelled
		return m, m.provider.(*scriptedProvider)
	}

	for _, testCase := range []struct {
		name   string
		signal *fakeSendNowSignal
	}{
		{"true (wake the same turn)", &fakeSendNowSignal{result: true}},
		{"false (interrupt and relaunch)", &fakeSendNowSignal{result: false}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			m, provider := setup(testCase.signal)
			updated, _ := m.Update(testInterleaveEnter(t, true))
			next := updated.(model)

			if got := next.queuedMessages; len(got) != 2 || got[0] != "/model glm-5.3" || got[1] != "@reviewer check this" {
				t.Fatalf("the FIFO must stay untouched by send-now, got %v", got)
			}
			if got := next.composerValue(); got != "" {
				t.Fatalf("the draft must be consumed, got %q", got)
			}

			if testCase.signal.result {
				// Same-turn delivery: the draft rides the running turn; the
				// FIFO waits.
				if got := next.interleave.snapshot(); len(got) != 1 || got[0] != "QF-DRAFT" {
					t.Fatalf("expected only the draft in the buffer, got %v", got)
				}
				return
			}

			// Interrupt path: the batch parks until the cancelled run
			// drains, then launches as the new turn.
			flushed, flushCmd := next.Update(agentResponseMsg{runID: 1})
			next = flushed.(model)
			if flushCmd == nil {
				t.Fatal("expected the parked batch to launch when the cancelled run drains")
			}
			_ = execCmd(flushCmd)
			for i, req := range provider.requests {
				for _, message := range req.Messages {
					if message.Role != qgruntime.MessageRoleUser {
						continue
					}
					if strings.Contains(message.Content, "/model glm-5.3") {
						t.Fatalf("request %d carries the slash command as a user message: %#v", i, message.Content)
					}
					if strings.Contains(message.Content, "@reviewer check this") {
						t.Fatalf("request %d carries the unexpanded mention: %#v", i, message.Content)
					}
				}
			}
			// The FIFO drains afterwards, one per turn, in order.
			if len(next.queuedMessages) != 2 {
				t.Fatalf("the FIFO must survive the interrupt, got %v", next.queuedMessages)
			}
		})
	}
}

// FIFO items launch after the send-now turn, one per turn, in order — the
// pre-existing one-per-turn drain, unchanged.
func TestSendNowKeepsSlashCommandsInQueueUntilTheyRunInOrder(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("FRESH answer", "ANSWER ONE", "ANSWER TWO")}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.yieldNow = &fakeSendNowSignal{result: false}
	m.queuedMessages = []string{"/model glm-5.3", "@reviewer check this"}
	m.input.SetValue("QF-DRAFT")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if got := m.queuedMessages; len(got) != 2 {
		t.Fatalf("the FIFO must survive the interrupt, got %v", got)
	}
	// The dying goroutine drains; the parked batch launches as a new turn.
	updated, cmd := m.Update(agentResponseMsg{runID: 1})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected the parked batch to launch when the cancelled run drains")
	}
	_ = execCmd(cmd)
	if got := m.queuedMessages; len(got) != 2 {
		t.Fatalf("the FIFO must survive the interrupt untouched, got %v", got)
	}
	for i, req := range provider.requests {
		for _, message := range req.Messages {
			if message.Role != qgruntime.MessageRoleUser {
				continue
			}
			if strings.Contains(message.Content, "/model glm-5.3") {
				t.Fatalf("request %d carries the slash command as a user message: %#v", i, message.Content)
			}
			if strings.Contains(message.Content, "@reviewer check this") {
				t.Fatalf("request %d carries the unexpanded mention: %#v", i, message.Content)
			}
		}
	}

	// Turn ends: the first FIFO item launches alone.
	updated, cmd = m.Update(agentResponseMsg{runID: m.activeRunID})
	m = updated.(model)
	m.titledSessions = map[string]bool{m.activeSession.SessionID: true}
	if cmd == nil {
		t.Fatal("expected the first FIFO item to launch after the send-now turn")
	}
	if got := m.queuedMessages; len(got) != 1 || got[0] != "@reviewer check this" {
		t.Fatalf("expected the head consumed in order, queue = %v", got)
	}
	_ = execCmd(cmd)

	// The second item launches next.
	updated, cmd = m.Update(agentResponseMsg{runID: m.activeRunID})
	m = updated.(model)
	m.titledSessions[m.activeSession.SessionID] = true
	if cmd == nil {
		t.Fatal("expected the second FIFO item to launch")
	}
	if len(m.queuedMessages) != 0 {
		t.Fatalf("expected the FIFO drained, got %v", m.queuedMessages)
	}
	_ = execCmd(cmd)
}

// The loop drained a mid-turn message and the turn was cancelled right
// after: the drained message reaches the new turn's wire exactly once (via
// the cancelled run's continuation) and the transcript exactly once.
func TestSendNowInterruptKeepsDrainedMessageExactlyOnce(t *testing.T) {
	store := testSessionStore(t)
	// The session must exist for the cancelled run's continuation to gate
	// in — a turn through the real submit path creates it.
	m := runTextTurn(t, continuationHarness(t, store, &scriptedProvider{scripts: textScripts("first answer")}, nil), "run A")
	sessID := m.activeSession.SessionID
	m.width = 96
	m.pending = true
	m.activeRunID = 7
	m.runID = 7
	cancelled := false
	m.runCancel = func() { cancelled = true }
	m.yieldNow = &fakeSendNowSignal{result: false}

	// The user sends a mid-turn message; Ctrl+Enter interrupts with a draft.
	m.input.SetValue("QF-DRAFT")
	updated, cmd := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)
	if !cancelled {
		t.Fatal("expected the interrupt path to cancel the running turn")
	}
	// The key dispatch itself always yields a batch cmd (focus, blink,
	// transcript settle); what must NOT happen is an inline launch. The
	// batch is parked until the cancelled run drains.
	_ = cmd
	if next.pending {
		t.Fatal("the cancelled turn must not keep running inline")
	}
	if len(next.pendingSendNow) != 1 || next.pendingSendNow[0] != "QF-DRAFT" {
		t.Fatalf("expected the send-now batch parked for the flush drain, got %v", next.pendingSendNow)
	}

	// The dying goroutine returns its accumulated message with the drained
	// interleave already on its wire — as a real run would.
	deadRunMessages := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "run A"},
		{Role: qgruntime.MessageRoleUser, Content: "DRAINED-mid-turn"},
		{Role: qgruntime.MessageRoleAssistant, Content: "partial answer"},
	}
	deadRow := transcriptRow{kind: rowUser, text: "DRAINED-mid-turn", runID: 7, id: "interleave-7-1"}
	updated, flushCmd := next.Update(agentResponseMsg{
		runID: 7,
		rows:  []transcriptRow{deadRow},
		continuation: &turnContinuation{
			sessionID: sessID,
			modelID:   "test-model",
			runID:     7,
			messages:  deadRunMessages,
		},
	})
	done := updated.(model)
	if flushCmd == nil {
		t.Fatal("expected the parked send-now batch to launch when the cancelled run drains")
	}
	if !done.pending {
		t.Fatal("expected the new turn to start after the drain")
	}
	_ = execCmd(flushCmd)

	// The drained message appears EXACTLY ONCE in the new turn's wire,
	// replayed from the cancelled run's continuation. provider.requests[0]
	// belongs to the first turn this test ran to create the session; the new
	// turn's request is the last one.
	provider := done.provider.(*scriptedProvider)
	if len(provider.requests) < 2 {
		t.Fatalf("provider requests = %d, want at least 2 (first turn + relaunch)", len(provider.requests))
	}
	count := 0
	for _, message := range provider.requests[len(provider.requests)-1].Messages {
		if strings.Contains(message.Content, "DRAINED-mid-turn") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("drained message appears %d times in the new turn's request, want exactly 1: %#v", count, provider.requests[0].Messages)
	}

	// And exactly once in the transcript.
	transcriptCount := 0
	for _, row := range done.transcript {
		if strings.Contains(row.text, "DRAINED-mid-turn") {
			transcriptCount++
		}
	}
	if transcriptCount != 1 {
		t.Fatalf("drained message appears %d times in the transcript, want exactly 1", transcriptCount)
	}
}

// /retry after a multi-message turn resends the SAME messages, separate and
// in order — never one concatenated text.
func TestRetryResendsMultiMessageTurnAsSeparateMessages(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	provider := &scriptedProvider{scripts: textScripts("first", "second", "retried")}
	m.provider = provider

	m, cmd := m.launchPrompts([]string{"RETRY-ONE", "RETRY-TWO", "RETRY-THREE"})
	_ = execCmd(cmd)
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(provider.requests))
	}
	m.pending = false

	m.input.SetValue("/retry")
	updated, retryCmd := m.Update(testKey(tea.KeyEnter))
	_ = updated
	if retryCmd == nil {
		t.Fatal("expected /retry to relaunch the turn")
	}
	_ = execCmd(retryCmd)
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (original + retry)", len(provider.requests))
	}
	var userMessages []string
	for _, message := range provider.requests[1].Messages {
		if message.Role == qgruntime.MessageRoleUser {
			userMessages = append(userMessages, message.Content)
		}
	}
	for _, want := range []string{"RETRY-ONE", "RETRY-TWO", "RETRY-THREE"} {
		found := false
		for _, content := range userMessages {
			if content == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("retry request lost %q; user messages = %#v", want, userMessages)
		}
	}
	if strings.Join(userMessages, "|") == "" {
		t.Fatal("retry produced no user messages")
	}
	for _, content := range userMessages {
		if strings.Contains(content, "RETRY-ONE\nRETRY-TWO") {
			t.Fatalf("retry concatenated the messages into one text: %q", content)
		}
	}
}

// Send-now with a confirming signal: the undelivered interleaves (oldest)
// and the draft go into the buffer for the running turn to absorb; the
// FIFO waits untouched. Nothing cancels, nothing relaunches, the composer
// is consumed.
func TestSendNowWithConfirmingSignalPushesCommonMessagesIntoBufferInOrder(t *testing.T) {
	m := continuationHarness(t, testSessionStore(t), &scriptedProvider{scripts: textScripts("answer")}, nil)
	m.width = 96
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	cancelled := false
	m.runCancel = func() { cancelled = true }
	m.yieldNow = &fakeSendNowSignal{result: true}
	m.interleave = &interleaveBuffer{}
	m.interleave.push("OLDEST leftover")
	m.queuedMessages = []string{"QF-UM", "QF-DOIS"}
	m.input.SetValue("QF-DRAFT")

	updated, cmd := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("a confirming signal must not interrupt the turn")
	}
	if cancelled {
		t.Fatal("a confirming signal must not cancel the running turn")
	}
	if !next.pending {
		t.Fatal("the running turn must stay pending")
	}
	if got := next.queuedMessages; len(got) != 2 || got[0] != "QF-UM" || got[1] != "QF-DOIS" {
		t.Fatalf("the FIFO must stay untouched, got %v", got)
	}
	got := next.interleave.snapshot()
	want := []string{"OLDEST leftover", "QF-DRAFT"}
	if len(got) != len(want) {
		t.Fatalf("interleave = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interleave[%d] = %q, want %q; all: %v", i, got[i], want[i], got)
		}
	}
	if got := next.composerValue(); got != "" {
		t.Fatalf("the draft must be consumed, got %q", got)
	}
}

// Send-now with nothing to wake the wait (nil signal, the interim default):
// the turn is interrupted and the draft relaunches as a message in one new
// turn — nothing returns to the composer, and the FIFO stays queued for
// the turns after it.
func TestSendNowWithoutSignalInterruptsAndLaunchesDraftSeparately(t *testing.T) {
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider: &fakeProvider{events: []qgruntime.StreamEvent{
			{Type: qgruntime.StreamEventText, Content: "fresh answer"},
			{Type: qgruntime.StreamEventDone},
		}},
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	provider := m.provider.(*fakeProvider)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	cancelled := false
	m.runCancel = func() { cancelled = true }
	m.queuedMessages = []string{"QF-UM", "QF-DOIS"}
	m.input.SetValue("QF-DRAFT")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if !cancelled {
		t.Fatal("with nothing to wake, send-now must interrupt the running turn")
	}
	if next.pending {
		t.Fatal("the cancelled turn must stop; the new turn launches when it drains")
	}
	if got := next.composerValue(); got != "" {
		t.Fatalf("nothing may return to the composer, got %q", got)
	}
	if got := next.queuedMessages; len(got) != 2 || got[0] != "QF-UM" || got[1] != "QF-DOIS" {
		t.Fatalf("the FIFO must survive the interrupt untouched, got %v", got)
	}
	if len(next.pendingSendNow) != 1 || next.pendingSendNow[0] != "QF-DRAFT" {
		t.Fatalf("the batch parks until the cancelled run drains, got %v", next.pendingSendNow)
	}
	// The dying goroutine returns; the parked batch launches as a new turn.
	updated, cmd := next.Update(agentResponseMsg{runID: 1})
	next = updated.(model)
	if cmd == nil {
		t.Fatal("expected the parked batch to launch when the cancelled run drains")
	}
	if !next.pending {
		t.Fatal("send-now must start the new turn")
	}
	_ = execCmd(cmd)
	if len(provider.requests) == 0 {
		t.Fatal("expected a provider request from the new turn")
	}
	var userMessages []string
	for _, message := range provider.requests[0].Messages {
		if message.Role == qgruntime.MessageRoleUser {
			userMessages = append(userMessages, message.Content)
		}
	}
	want := []string{"QF-DRAFT"}
	if len(userMessages) != len(want) {
		t.Fatalf("user messages = %#v, want exactly %#v", userMessages, want)
	}
	for i := range want {
		if userMessages[i] != want[i] {
			t.Fatalf("user message %d = %q, want %q; all: %#v", i, userMessages[i], want[i], userMessages)
		}
	}
}

// /agora with an argument while pending IS the send-now path: the argument
// goes out now as a common message. The FIFO waits for the turn to end.
func TestAgoraWithArgumentRidesBehindTheCommonMessages(t *testing.T) {
	m := continuationHarness(t, testSessionStore(t), &scriptedProvider{scripts: textScripts("answer")}, nil)
	m.width = 96
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.yieldNow = &fakeSendNowSignal{result: true}
	m.queuedMessages = []string{"QF-UM"}
	m.input.SetValue("/agora QF-AGORA")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if got := next.composerValue(); got != "" {
		t.Fatalf("the /agora argument must consume the composer, got %q", got)
	}
	got := next.interleave.snapshot()
	want := []string{"QF-AGORA"}
	if len(got) != len(want) {
		t.Fatalf("interleave = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("interleave[%d] = %q, want %q; all: %v", i, got[i], want[i], got)
		}
	}
	if got := next.queuedMessages; len(got) != 1 || got[0] != "QF-UM" {
		t.Fatalf("the FIFO must stay untouched, got %v", got)
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
	m.yieldNow = &fakeSendNowSignal{result: true}

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

func TestAltScreenRendersSussurroBlockOnce(t *testing.T) {
	m := newModel(context.Background(), Options{AltScreen: true,
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     &fakeProvider{events: []qgruntime.StreamEvent{{Type: qgruntime.StreamEventDone}}},
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	m = updated.(model)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.queuedMessages = []string{"MARKONE first queued", "MARKSECOND second queued"}
	m.interleave.push("MARKINTER pending interleave")
	m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowUser, text: "run the thing"})
	next, _ := m.settleTranscript()
	m = next

	view := plainRender(t, viewString(m.View()))
	if count := strings.Count(view, "na fila (2)"); count != 1 {
		t.Fatalf("expected the queue notice exactly once in the alt-screen frame, got %d:\n%s", count, view)
	}
	if count := strings.Count(view, "MARKONE first queued"); count != 1 {
		t.Fatalf("expected MARKONE rendered exactly once, got %d:\n%s", count, view)
	}
	if count := strings.Count(view, "MARKSECOND second queued"); count != 1 {
		t.Fatalf("expected MARKSECOND rendered exactly once, got %d:\n%s", count, view)
	}
	if strings.Contains(view, "enviando após a ação atual…") {
		t.Fatalf("the interleave notice must be gone from the alt-screen frame, got:\n%s", view)
	}
	if count := strings.Count(view, "MARKINTER pending interleave"); count != 1 {
		t.Fatalf("expected the pending interleave exactly once, got %d:\n%s", count, view)
	}
}

func TestEnterWhilePendingAndCompactingInterleavesPrompt(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.compactInFlight = true
	m.input.SetValue("MARKCOMPACTING prompt")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	// A plain prompt during a turn interleaves into the running turn even
	// while a compaction is in flight: the same-turn path never starts a
	// second run, so it does not race the compact rewrite.
	if len(next.queuedMessages) != 0 {
		t.Fatalf("a plain prompt must not enter the FIFO, got %v", next.queuedMessages)
	}
	if next.interleave == nil || next.interleave.pendingCount() != 1 {
		t.Fatalf("expected the prompt interleaved while compacting, got %+v", next.interleave)
	}
	if transcriptContains(next.transcript, "Compactação em andamento") {
		t.Fatalf("expected no compaction warning, got transcript %#v", next.transcript)
	}
	if got := next.composerValue(); got != "" {
		t.Fatalf("expected the composer consumed by the interleave, got %q", got)
	}
}

// Full Ctrl+C sequence with a real slow-tool run: an active turn plus one
// queued message, first Ctrl+C cancels (queue returns to the composer), the
// cancelled run's late agentResponseMsg lands, second Ctrl+C clears the
// composer, and a further stale duplicate changes nothing. A late run must
// never resurrect text the operator already dismissed.
func TestSecondCtrlCClearsComposerDespiteLateCancelledRun(t *testing.T) {
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
			{Type: qgruntime.StreamEventText, Content: "late answer"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := continuationHarness(t, store, provider, registry)
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

	m.input.SetValue("queued while running")
	updated, _ = m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	// A plain prompt now interleaves into the running turn; the Ctrl+C
	// sequence below returns that pending interleave to the composer.
	if m.interleave == nil || m.interleave.pendingCount() != 1 {
		t.Fatalf("expected the prompt interleaved behind the running turn, got %+v", m.interleave)
	}

	// First Ctrl+C cancels; the queue returns to the composer.
	updated, _ = m.Update(testKeyCtrl('c'))
	m = updated.(model)
	if m.pending {
		t.Fatal("expected the first Ctrl+C to cancel the running turn")
	}
	if got := m.composerValue(); got != "queued while running" {
		t.Fatalf("expected the queue back in the composer, got %q", got)
	}

	// The cancelled run finishes late; its message lands between the presses.
	close(release)
	late := <-doneA
	updated, _ = m.Update(late)
	m = updated.(model)
	if got := m.composerValue(); got != "queued while running" {
		t.Fatalf("late cancelled-run message must not touch the composer, got %q", got)
	}

	// Second Ctrl+C clears the composer — first with the exit confirmation
	// still armed (fast harness), then again after it expired (slow E2E:
	// the 3s ctrlCExitConfirmDuration lapsed before the second press).
	// Either way the composer ends empty and stays empty.
	updated, _ = m.Update(testKeyCtrl('c'))
	m = updated.(model)
	if got := m.composerValue(); got != "" {
		t.Fatalf("expected the second Ctrl+C to clear the composer, got %q", got)
	}

	// A further stale duplicate of the same late message changes nothing.
	updated, _ = m.Update(late)
	m = updated.(model)
	if got := m.composerValue(); got != "" {
		t.Fatalf("stale duplicate must leave the cleared composer alone, got %q", got)
	}
	if m.pending {
		t.Fatal("no run may start on its own after the clears")
	}
}

// Same sequence, but the exit confirmation lapses before the second press
// (the E2E harness was slower than the 3s confirm window): the composer
// must still clear instead of re-arming a quit.
func TestSecondCtrlCClearsComposerAfterConfirmExpired(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.queuedMessages = []string{"queued while running"}
	cancelled := false
	m.runCancel = func() { cancelled = true }

	updated, _ := m.Update(testKeyCtrl('c'))
	m = updated.(model)
	if !cancelled || m.pending {
		t.Fatal("expected the first Ctrl+C to cancel the running turn")
	}
	if got := m.composerValue(); got != "queued while running" {
		t.Fatalf("expected the queue back in the composer, got %q", got)
	}

	updated, _ = m.Update(exitConfirmExpiredMsg{seq: m.exitConfirmSeq})
	m = updated.(model)
	if m.exitConfirmActive {
		t.Fatal("expected the expired confirm to disarm")
	}

	updated, _ = m.Update(testKeyCtrl('c'))
	m = updated.(model)
	if got := m.composerValue(); got != "" {
		t.Fatalf("expected Ctrl+C after the lapsed confirm to clear the composer, got %q", got)
	}
	if m.exiting {
		t.Fatal("clearing the composer must not quit the app")
	}
}

// End of turn with leftovers: everything the turn never delivered launches
// TOGETHER as separate user messages in ONE new turn, in order — then the
// FIFO drains one per turn afterwards, head first. Each provider request
// carries only its own messages; the notice counts the FIFO down.
func TestEndOfTurnSobrasLaunchTogetherSeparatelyAndInOrder(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("answer UM", "answer DOIS", "answer TRES", "answer CAUDA")}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.queuedMessages = []string{"QF-TAIL"}

	for _, leftover := range []string{"QF-UM", "QF-DOIS", "QF-TRES"} {
		m.input.SetValue(leftover)
		updated, cmd := m.Update(testKey(tea.KeyEnter))
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("leftover prompt %q must not launch while the turn is active", leftover)
		}
	}
	if got := m.interleave.pendingCount(); got != 3 {
		t.Fatalf("expected the three leftovers interleaved, got %d", got)
	}
	if got := m.queuedMessages; len(got) != 1 || got[0] != "QF-TAIL" {
		t.Fatalf("the FIFO must stay untouched while sobras are pending, got %#v", got)
	}

	// Turn A ends: the three sobras launch as ONE new turn, as separate
	// user messages, in order; the FIFO head waits for the turn after.
	before := len(provider.requests)
	updated, cmd := m.Update(agentResponseMsg{runID: m.activeRunID})
	m = updated.(model)
	if m.titledSessions == nil {
		m.titledSessions = map[string]bool{}
	}
	m.titledSessions[m.activeSession.SessionID] = true
	if cmd == nil {
		t.Fatal("expected the end-of-turn sobras to launch as the next turn")
	}
	if !m.pending {
		t.Fatal("expected the end-of-turn sobras turn to start")
	}
	if got := m.queuedMessages; len(got) != 1 || got[0] != "QF-TAIL" {
		t.Fatalf("the FIFO drains after the sobras turn, got %#v", got)
	}
	if got := m.interleave.pendingCount(); got != 0 {
		t.Fatalf("expected the interleave buffer drained, got %d", got)
	}
	_ = execCmd(cmd)
	if got := len(provider.requests) - before; got != 1 {
		t.Fatalf("expected the sobras to make exactly one provider request, got %d", got)
	}
	req := provider.requests[len(provider.requests)-1]
	var userMessages []string
	for _, message := range req.Messages {
		if message.Role == qgruntime.MessageRoleUser {
			userMessages = append(userMessages, message.Content)
		}
	}
	want := []string{"QF-UM", "QF-DOIS", "QF-TRES"}
	if len(userMessages) != len(want) {
		t.Fatalf("user messages = %#v, want exactly %#v", userMessages, want)
	}
	for i := range want {
		if userMessages[i] != want[i] {
			t.Fatalf("user message %d = %q, want %q (order); all: %#v", i, userMessages[i], want[i], userMessages)
		}
	}

	// The sobras turn ends: now the FIFO head launches, one per turn.
	updated, cmd = m.Update(agentResponseMsg{runID: m.activeRunID})
	m = updated.(model)
	m.titledSessions[m.activeSession.SessionID] = true
	if cmd == nil {
		t.Fatal("expected the FIFO head to launch after the sobras turn")
	}
	_ = execCmd(cmd)
	req = provider.requests[len(provider.requests)-1]
	last := req.Messages[len(req.Messages)-1]
	if !strings.Contains(last.Content, "QF-TAIL") {
		t.Fatalf("expected the provider's last message to be QF-TAIL, got %#v", last)
	}
	// The sobras ride the history replay exactly once (they are real
	// user turns), never re-seeded as fresh messages behind QF-TAIL.
	counts := map[string]int{}
	for _, message := range req.Messages {
		for _, leftover := range []string{"QF-UM", "QF-DOIS", "QF-TRES"} {
			if strings.Contains(message.Content, leftover) {
				counts[leftover]++
			}
		}
	}
	for _, leftover := range []string{"QF-UM", "QF-DOIS", "QF-TRES"} {
		if counts[leftover] != 1 {
			t.Fatalf("sobras %q appears %d times in the FIFO turn's request, want exactly 1 (history replay): %#v", leftover, counts[leftover], req.Messages)
		}
	}
	if len(m.queuedMessages) != 0 {
		t.Fatalf("expected an empty FIFO, got %#v", m.queuedMessages)
	}
}

// The FIFO drain chain: three prompts enqueued while a turn is active
// drain one per turn, head first. Each turn's end launches only the head;
// the provider request carries that head and none of the later messages;
// the notice counts down until the queue is empty and silent. (The FIFO
// cannot be filled by plain prompts anymore — those interleave into the
// running turn — so the test seeds it through the queue path directly.)
func TestQueuedMessagesDrainOnePerTurnHeadFirst(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("answer UM", "answer DOIS", "answer TRES")}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96

	for _, queued := range []string{"QF-UM", "QF-DOIS", "QF-TRES"} {
		m.queuedMessages = append(m.queuedMessages, queued)
	}
	if got := m.queuedMessages; len(got) != 3 || got[0] != "QF-UM" || got[1] != "QF-DOIS" || got[2] != "QF-TRES" {
		t.Fatalf("expected the FIFO [QF-UM QF-DOIS QF-TRES], got %#v", got)
	}

	// End the active turn: only the head launches, and its provider request
	// carries the head but none of the messages still queued behind it.
	endTurnLaunching := func(head string, rest []string) {
		t.Helper()
		before := len(provider.requests)
		updated, cmd := m.Update(agentResponseMsg{runID: m.activeRunID})
		m = updated.(model)
		// Auto-title would spend a provider request of its own on later
		// turns; shut its one-shot gate with the real session id so every
		// request in the window below belongs to the queue chain.
		if m.titledSessions == nil {
			m.titledSessions = map[string]bool{}
		}
		m.titledSessions[m.activeSession.SessionID] = true
		if cmd == nil {
			t.Fatalf("expected the head %q to launch, got no command", head)
		}
		if !m.pending {
			t.Fatalf("expected the head %q to start a new turn", head)
		}
		if got := m.queuedMessages; len(got) != len(rest) {
			t.Fatalf("after launching %q, expected queue %#v, got %#v", head, rest, got)
		} else {
			for i := range rest {
				if got[i] != rest[i] {
					t.Fatalf("after launching %q, expected queue %#v, got %#v", head, rest, got)
				}
			}
		}
		_ = execCmd(cmd) // run the launched turn against the provider
		if got := len(provider.requests) - before; got != 1 {
			t.Fatalf("expected the head %q to make exactly one provider request, got %d", head, got)
		}
		req := provider.requests[len(provider.requests)-1]
		last := req.Messages[len(req.Messages)-1]
		if !strings.Contains(last.Content, head) {
			t.Fatalf("expected the provider's last message to be %q, got %#v", head, last)
		}
		for _, message := range req.Messages {
			for _, later := range rest {
				if strings.Contains(message.Content, later) {
					t.Fatalf("request for %q must not contain %q yet: %#v", head, later, req.Messages)
				}
			}
		}
	}

	// Turn A ends: only QF-UM launches; the notice counts the two remaining.
	endTurnLaunching("QF-UM", []string{"QF-DOIS", "QF-TRES"})
	if view := plainRender(t, viewString(m.View())); !strings.Contains(view, "na fila (2)") {
		t.Fatalf("expected the notice to count the two remaining, got:\n%s", view)
	}

	// QF-UM's turn ends: only QF-DOIS launches; one remains.
	endTurnLaunching("QF-DOIS", []string{"QF-TRES"})
	if view := plainRender(t, viewString(m.View())); !strings.Contains(view, "na fila (1)") {
		t.Fatalf("expected the notice to count the last remaining, got:\n%s", view)
	}

	// QF-DOIS's turn ends: only QF-TRES launches; the queue is empty.
	endTurnLaunching("QF-TRES", nil)

	// QF-TRES's turn ends: nothing launches, the queue stays empty and silent.
	updated, cmd := m.Update(agentResponseMsg{runID: m.activeRunID})
	m = updated.(model)
	if cmd != nil {
		t.Fatal("expected nothing to launch once the queue is empty")
	}
	if m.pending {
		t.Fatal("expected no new turn once the queue is empty")
	}
	if len(m.queuedMessages) != 0 {
		t.Fatalf("expected an empty queue, got %#v", m.queuedMessages)
	}
	if view := plainRender(t, viewString(m.View())); strings.Contains(view, "na fila") {
		t.Fatalf("expected no queue notice once the queue is empty, got:\n%s", view)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected exactly 3 provider requests, got %d", len(provider.requests))
	}
}
