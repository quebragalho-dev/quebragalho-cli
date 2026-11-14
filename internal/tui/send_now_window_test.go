package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
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
	// then died without handing over anything. The drain already logged
	// the EventMessage and emitted the live row (the session log writer
	// persists it synchronously) — simulated the same way.
	m, err := m.ensureActiveSession("seed")
	if err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	m, err = m.appendSessionEvent(sessions.EventMessage, map[string]any{
		"role":    "user",
		"content": "DRAINED-before-death",
	})
	if err != nil {
		t.Fatalf("seed session event: %v", err)
	}
	m.interleave.noteDelivered([]deliveredInterleave{{id: "interleave-7-1", text: "DRAINED-before-death"}})

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

// P1: when the dead run never hands over its continuation (the goroutine is
// stuck or the run is ineligible), the timeout path replays the delivered
// message via the tracker. Its row carries the ORIGINAL id, so the dead
// run's late flush (rows with interleave-<runID>-<seq>) dedupes against it:
// exactly once on the transcript, exactly once on the wire, exactly once in
// the session log.
func TestTimeoutThenLateFlushShowsDeliveredMessageOnce(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("ANSWER")}
	store := testSessionStore(t)
	m := continuationHarness(t, store, provider, nil)
	m.pending = true
	m.activeRunID = 7
	m.runID = 7
	m.width = 96
	m.runCancel = func() {}
	m.yieldNow = &fakeSendNowSignal{result: false}

	m.input.SetValue("QF-DRAFT")
	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)

	// The loop drained a message into the dying run's wire; the goroutine
	// then died without handing over anything. The drain already logged
	// the EventMessage and emitted the live row (the session log writer
	// persists it synchronously) — simulated the same way.
	m, err := m.ensureActiveSession("seed")
	if err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	m, err = m.appendSessionEvent(sessions.EventMessage, map[string]any{
		"role":    "user",
		"content": "DRAINED-x",
	})
	if err != nil {
		t.Fatalf("seed session event: %v", err)
	}
	m.interleave.noteDelivered([]deliveredInterleave{{id: "interleave-7-1", text: "DRAINED-x"}})

	// The drain timeout fires with no continuation available: the batch
	// launches, replaying the delivered message with its tracked id.
	updated, cmd := m.Update(sendNowDrainTimeoutMsg{seq: m.pendingSendNowSeq})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected the timeout to launch the parked batch")
	}
	_ = execCmd(cmd)

	// The dead run's late flush lands: rows carry interleave-7-1 too; the
	// transcript must show the message exactly once.
	updated, _ = m.Update(agentResponseMsg{
		runID: 7,
		rows:  []transcriptRow{{kind: rowUser, text: "DRAINED-x", runID: 7, id: "interleave-7-1"}},
	})
	m = updated.(model)
	count := 0
	for _, row := range m.transcript {
		count += strings.Count(row.text, "DRAINED-x")
	}
	if count != 1 {
		t.Fatalf("the delivered message appears %d times in the transcript, want exactly 1", count)
	}
	// The wire carries it exactly once (the prepend, the only place it can).
	count = 0
	for _, message := range provider.requests[len(provider.requests)-1].Messages {
		count += strings.Count(message.Content, "DRAINED-x")
	}
	if count != 1 {
		t.Fatalf("the delivered message appears %d times on the wire, want exactly 1", count)
	}
	// The session log holds it exactly once: the drain-time event I
	// seeded, and no second user EventMessage for the relaunch's tracked
	// message.
	userEvents := 0
	for _, event := range m.sessionEvents {
		if event.Type != sessions.EventMessage {
			continue
		}
		if strings.Contains(string(event.Payload), "DRAINED-x") {
			userEvents++
		}
	}
	if userEvents != 1 {
		t.Fatalf("the delivered message appears %d times in the session events, want exactly 1", userEvents)
	}
}

// P3, child/side/spec sessions: the wire cannot rebuild from the log
// (ineligible kind), so the tracker prepend is the only carrier. The
// relaunch tags the rows with the tracked ids — the late flush dedupes by
// kind+id instead of scope-id — and the message appears exactly once on the
// transcript, once on the wire.
func TestTimeoutThenLateFlush_InIneligibleSessionShowsMessageOnce(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("ANSWER", "ANSWER2")}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.runCancel = func() {}
	m.yieldNow = &fakeSendNowSignal{result: false}
	// Ineligible kind: the wire will not rebuild from the log, so the
	// transfer rides the tracker alone.
	m.activeSession.SessionKind = sessions.SessionKindChild
	m.interleave.noteDelivered([]deliveredInterleave{{id: "interleave-1-1", text: "DRAINED-x"}})

	m.input.SetValue("QF-DRAFT")
	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	updated, cmd := m.Update(sendNowDrainTimeoutMsg{seq: m.pendingSendNowSeq})
	m = updated.(model)
	_ = execCmd(cmd)

	foundID := false
	for _, row := range m.transcript {
		if strings.Contains(row.text, "DRAINED-x") && row.id == "interleave-1-1" {
			foundID = true
		}
	}
	if !foundID {
		t.Error("the relaunched rows must carry the tracked id")
	}

	updated, _ = m.Update(agentResponseMsg{
		runID: 1,
		rows:  []transcriptRow{{kind: rowUser, text: "DRAINED-x", runID: 1, id: "interleave-1-1"}},
	})
	m = updated.(model)
	count := 0
	for _, row := range m.transcript {
		count += strings.Count(row.text, "DRAINED-x")
	}
	if count != 1 {
		t.Fatalf("the delivered message appears %d times in the transcript, want exactly 1", count)
	}
	count = 0
	for _, message := range provider.requests[len(provider.requests)-1].Messages {
		count += strings.Count(message.Content, "DRAINED-x")
	}
	if count != 1 {
		t.Fatalf("the delivered message appears %d times on the wire, want exactly 1", count)
	}
}

// P2: compaction in flight blocks the parked batch from launching — the
// same run/c compactResultMsg rewrites transcript/sessionEvents wholesale;
// the batch stays parked, visible and pending, and launches when the
// compact finishes, on both the flush path and the timeout path.
func TestParkedBatchDefersDuringCompaction_OnFlushPath(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("ANSWER", "ANSWER2")}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.runCancel = func() {}
	m.yieldNow = &fakeSendNowSignal{result: false}

	m.input.SetValue("QF-DRAFT")
	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)
	m.compactInFlight = true

	updated, cmd := m.Update(agentResponseMsg{runID: 1})
	m = updated.(model)
	if cmd != nil {
		t.Fatal("the parked batch must not launch during compaction")
	}
	if got := len(provider.requests); got != 0 {
		t.Fatalf("no request may fire while compaction is in flight, got %d", got)
	}
	if got := len(m.pendingSendNow); got != 1 || m.pendingSendNow[0] != "QF-DRAFT" {
		t.Fatalf("the batch stays parked during compaction, got %v", m.pendingSendNow)
	}
	if !m.pending {
		t.Fatal("the parked window must stay pending during compaction")
	}

	// Compaction finishes (error branch): the batch launches then.
	updated, cmd = m.Update(compactResultMsg{err: errors.New("boom")})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("the parked batch must launch when compaction ends")
	}
	_ = execCmd(cmd)
	if got := len(provider.requests); got != 1 {
		t.Fatalf("expected the deferred batch to launch after compaction, got %d request(s)", got)
	}
	if len(m.pendingSendNow) != 0 {
		t.Fatal("the batch must be consumed when it launches")
	}
}

func TestParkedBatchDefersDuringCompaction_OnTimeoutPath(t *testing.T) {
	provider := &scriptedProvider{scripts: textScripts("ANSWER", "ANSWER2")}
	m := newQueuedMessageTestModel(t)
	m.provider = provider
	m.pending = true
	m.activeRunID = 7
	m.runID = 7
	m.width = 96
	m.runCancel = func() {}
	m.yieldNow = &fakeSendNowSignal{result: false}
	m.compactInFlight = true

	m.interleave.noteDelivered([]deliveredInterleave{{id: "interleave-7-1", text: "DRAINED-x"}})
	// The session must exist and the drain must have logged the EventMessage
	// (the real drain writes through synchronously) for the rebuilt wire to
	// carry the delivered message.
	var err error
	m, err = m.ensureActiveSession("seed")
	if err != nil {
		t.Fatalf("ensure session: %v", err)
	}
	m, err = m.appendSessionEvent(sessions.EventMessage, map[string]any{
		"role":    "user",
		"content": "DRAINED-x",
	})
	if err != nil {
		t.Fatalf("seed session event: %v", err)
	}
	m.input.SetValue("QF-DRAFT")
	updated, _ := m.Update(testInterleaveEnter(t, true))
	m = updated.(model)

	updated, cmd := m.Update(sendNowDrainTimeoutMsg{seq: m.pendingSendNowSeq})
	m = updated.(model)
	if cmd != nil {
		t.Fatal("the timeout must not launch during compaction either")
	}
	if got := len(provider.requests); got != 0 {
		t.Fatalf("no request may fire while compaction is in flight, got %d", got)
	}
	if len(m.pendingSendNow) == 0 {
		t.Fatal("the batch must remain parked during compaction")
	}
	if !m.pending {
		t.Fatal("the parked window must stay pending during compaction")
	}

	// Compaction finishes (success branch): the batch launches, replaying
	// the delivered message exactly once.
	updated, cmd = m.Update(compactResultMsg{})
	m = updated.(model)
	if cmd == nil {
		t.Fatal("the parked batch must launch when compaction ends")
	}
	_ = execCmd(cmd)
	if got := len(provider.requests); got != 1 {
		t.Fatalf("expected the deferred batch to launch after compaction, got %d request(s)", got)
	}
	count := 0
	for _, message := range provider.requests[0].Messages {
		count += strings.Count(message.Content, "DRAINED-x")
	}
	if count != 1 {
		t.Fatalf("the delivered message rides the deferred launch exactly once, got %d", count)
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
