package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// The window between an interrupting send-now and the dead run's drain must
// behave like a turn that is still running: never two concurrent runs, the
// parked batch launches first, and whatever the user types in the window
// rides the FIFO behind it — nothing fires around the batch.
func TestParkedSendNowBlocksNewRunsAndLaunchesFirst(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("ANSWER-ONE", "ANSWER-TWO")}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	cancelled := false
	m.runCancel = func() { cancelled = true }
	m.yieldNow = &fakeSendNowSignal{result: false}
	m.input.SetValue("QF-DRAFT")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if !cancelled {
		t.Fatal("precondition: the interrupt path cancels the running turn")
	}
	if !m.pending {
		t.Fatal("the parked window must look like a running turn to every launch gate")
	}
	if len(provider.requests) != 0 {
		t.Fatalf("no request may fire before the batch launches, got %d", len(provider.requests))
	}

	// Plain Enter during the window rides the FIFO behind the batch —
	// never a second run.
	m.input.SetValue("QF-WINDOW")
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("Enter during a parked send-now window must not launch a run")
	}
	if got := m.queuedMessages; len(got) != 1 || got[0] != "QF-WINDOW" {
		t.Fatalf("the window's prompt rides the FIFO behind the batch, got %v", got)
	}

	// Ctrl+Enter during the window joins the parked batch, in order.
	m.input.SetValue("QF-WINDOW-CTRL")
	updated, _ = m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if got := m.pendingSendNow; len(got) != 2 || got[0] != "QF-DRAFT" || got[1] != "QF-WINDOW-CTRL" {
		t.Fatalf("expected the batch joined in order, got %v", got)
	}
	if got := len(provider.requests); got != 0 {
		t.Fatalf("still no request may fire, got %d", got)
	}

	// /agora during the window joins the parked batch too.
	m.input.SetValue("/agora QF-WINDOW-AGORA")
	updated, _ = m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if got := m.pendingSendNow; len(got) != 3 || got[2] != "QF-WINDOW-AGORA" {
		t.Fatalf("expected /agora to join the batch, got %v", got)
	}

	// The dead run drains; the batch launches with everything in order.
	updated, cmd = m.Update(agentResponseMsg{runID: 1})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected the parked batch to launch when the dead run drains")
	}
	_ = execCmd(cmd)
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 (the batch)", len(provider.requests))
	}
	var userMessages []string
	for _, message := range provider.requests[0].Messages {
		if message.Role == qgruntime.MessageRoleUser {
			userMessages = append(userMessages, message.Content)
		}
	}
	want := []string{"QF-DRAFT", "QF-WINDOW-CTRL", "QF-WINDOW-AGORA"}
	if strings.Join(userMessages, "|") != strings.Join(want, "|") {
		t.Fatalf("batch wire = %#v, want %#v in order", userMessages, want)
	}
	if got := m.queuedMessages; len(got) != 1 || got[0] != "QF-WINDOW" {
		t.Fatalf("QF-WINDOW stays queued for the next turn, got %v", got)
	}

	// The batch turn ends; only then does QF-WINDOW launch.
	updated, cmd = m.Update(agentResponseMsg{runID: m.activeRunID})
	m = updated.(model)
	m.titledSessions = map[string]bool{m.activeSession.SessionID: true}
	if cmd == nil {
		t.Fatal("expected QF-WINDOW to launch after the batch turn")
	}
	_ = execCmd(cmd)
	last := provider.requests[len(provider.requests)-1]
	if content := last.Messages[len(last.Messages)-1].Content; !strings.Contains(content, "QF-WINDOW") {
		t.Fatalf("the FIFO head launches after the batch, got %#v", last.Messages)
	}
}

// The parked batch never leaves the screen: it renders as pending rows next
// to the queue notice, and ↑ edits the last parked message back into the
// composer.
func TestParkedSendNowStaysVisibleAndEditable(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.runCancel = func() {}
	m.yieldNow = &fakeSendNowSignal{result: false}
	m.input.SetValue("QF-DRAFT")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "❯ QF-DRAFT") {
		t.Fatalf("the parked batch must render as pending rows, got:\n%s", view)
	}
	if !strings.Contains(view, "na fila (1)") {
		t.Fatalf("the queue notice must count the parked batch, got:\n%s", view)
	}

	// ↑ edits the parked draft back into the composer.
	updated, _ = m.Update(testKey(tea.KeyUp))
	m = updated.(model)
	if got := m.composerValue(); got != "QF-DRAFT" {
		t.Fatalf("expected ↑ to return the parked draft, got %q", got)
	}
	if len(m.pendingSendNow) != 0 {
		t.Fatalf("expected the batch emptied by ↑, got %v", m.pendingSendNow)
	}
	if m.pending {
		t.Fatal("the window ends once the parked batch is fully edited back")
	}

	// A drain with nothing parked launches nothing.
	updated, cmd := m.Update(agentResponseMsg{runID: 1})
	m = updated.(model)
	if cmd != nil {
		t.Fatal("no turn may launch without a parked batch")
	}
}

// A dead run whose goroutine never returns must not strand the batch
// forever: after the drain timeout the batch launches anyway. A message the
// loop already drained into the dead run's wire is replayed from the
// tracker, without the dead run's continuation.
func TestParkedSendNowLaunchesAfterDrainTimeout(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("ANSWER-ONE", "ANSWER-TWO")}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 7
	m.runID = 7
	m.width = 96
	m.runCancel = func() {}
	m.yieldNow = &fakeSendNowSignal{result: false}

	// The loop drained a message into the dying run's wire; the goroutine
	// then died without handing over anything.
	m.interleave.noteDelivered([]string{"DRAINED-before-death"})

	m.input.SetValue("QF-DRAFT")
	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	if len(m.pendingSendNow) != 1 || m.pendingSendNow[0] != "QF-DRAFT" {
		t.Fatalf("expected the batch parked, got %v", m.pendingSendNow)
	}

	// The drain timeout fires; the batch launches anyway.
	updated, cmd := m.Update(sendNowDrainTimeoutMsg{seq: m.pendingSendNowSeq})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("the drain timeout must launch the parked batch")
	}
	if len(m.pendingSendNow) != 0 {
		t.Fatal("the timeout must consume the parked batch")
	}
	_ = execCmd(cmd)
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(provider.requests))
	}
	count := map[string]int{}
	for _, message := range provider.requests[0].Messages {
		count[message.Content]++
	}
	if count["DRAINED-before-death"] != 1 {
		t.Fatalf("the drained message must be replayed exactly once, got %d: %#v", count["DRAINED-before-death"], provider.requests[0].Messages)
	}
	if count["QF-DRAFT"] != 1 {
		t.Fatalf("the draft must ride the relaunched batch exactly once, got %d: %#v", count["QF-DRAFT"], provider.requests[0].Messages)
	}
}

// A session boundary during the window must not drop the parked batch:
// /new, /resume and /clear return everything undelivered to the composer,
// like a cancellation.
func TestStartNewSessionReturnsParkedSendNowToComposer(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.runCancel = func() {}
	m.yieldNow = &fakeSendNowSignal{result: false}
	m.queuedMessages = []string{"/tools"}
	m.input.SetValue("QF-DRAFT")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)

	fresh := m.startNewSession()
	if got := fresh.composerValue(); !strings.Contains(got, "QF-DRAFT") || !strings.Contains(got, "/tools") {
		t.Fatalf("expected the parked batch and the queue back in the composer, got %q", got)
	}
	if got := fresh.composerValue(); strings.Index(got, "QF-DRAFT") > strings.Index(got, "/tools") {
		t.Fatalf("the parked batch must stay ahead of the queue, got %q", got)
	}
	if fresh.pendingSendNow != nil {
		t.Fatal("the session boundary must consume the parked batch into the composer")
	}
	if len(fresh.queuedMessages) != 0 {
		t.Fatal("the session boundary must return the queue to the composer too")
	}
}
