package tui

import (
	"errors"
	"testing"
	"time"
)

// Ctrl+C during a dictation cancels the dictation, exactly like Esc: the
// microphone is released, the transcript is discarded, and the composer, the
// model run and the exit confirmation are left alone.

// ctrlcFakeRecorder observes Stop so tests can prove the microphone is
// released. Stop is invoked from a goroutine, so the signal is buffered and
// the test waits for it with a channel instead of sleeping.
type ctrlcFakeRecorder struct {
	stopCh chan struct{}
}

func (r *ctrlcFakeRecorder) Start() error { return nil }

func (r *ctrlcFakeRecorder) Stop() ([]byte, error) {
	select {
	case r.stopCh <- struct{}{}:
	default:
	}
	return nil, nil
}

func (r *ctrlcFakeRecorder) StartStreaming() (<-chan []byte, func() error, error) {
	return nil, nil, errors.New("ctrlcFakeRecorder: streaming not supported")
}

func waitCtrlcRecorderStop(t *testing.T, rec *ctrlcFakeRecorder) {
	t.Helper()
	select {
	case <-rec.stopCh:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelDictation did not call recorder Stop")
	}
}

// ctrlcBatchRecordingModel builds a model with a batch dictation in the
// recording phase through the real start path (startDictation +
// dictationStartedMsg), then swaps in a fake recorder so Stop is observable.
// The returned start command is never executed: no real capture tool spawns.
func ctrlcBatchRecordingModel(t *testing.T, composerText string) (model, *ctrlcFakeRecorder) {
	t.Helper()
	m := newModel(t.Context(), Options{})
	m.dictation = batchOnlyController()
	m.setComposerState(composerState{text: composerText, cursor: len([]rune(composerText))})
	started, cmd := m.startDictation()
	if cmd == nil {
		t.Fatal("startDictation should return the async start command on the batch path")
	}
	m = started
	if m.dictation.phase != dictStarting {
		t.Fatalf("phase after start = %v, want starting", m.dictation.phase)
	}
	updated, _ := m.handleDictationStarted(dictationStartedMsg{sessionID: m.dictation.sessionID})
	m = updated
	if m.dictation.phase != dictRecording {
		t.Fatalf("phase after started = %v, want recording", m.dictation.phase)
	}
	rec := &ctrlcFakeRecorder{stopCh: make(chan struct{}, 1)}
	m.dictation.recorder = rec
	return m, rec
}

// A recording with text in the composer cancels without touching the text:
// before the fix Ctrl+C cleared the composer (and left the mic open).
func TestDictationCtrlCCancelsRecordingKeepsComposer(t *testing.T) {
	m, rec := ctrlcBatchRecordingModel(t, "abc")

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)

	if next.dictation.phase != dictIdle {
		t.Fatalf("phase after Ctrl+C = %v, want idle", next.dictation.phase)
	}
	if got := next.composerValue(); got != "abc" {
		t.Fatalf("composer after Ctrl+C = %q, want the pre-dictation text kept", got)
	}
	if next.transientNotice.text != "Ditado cancelado." {
		t.Fatalf("notice after Ctrl+C = %q, want the cancellation notice", next.transientNotice.text)
	}
	if next.exitConfirmActive {
		t.Error("Ctrl+C during a dictation must not arm the exit confirmation")
	}
	if next.exiting {
		t.Error("Ctrl+C during a dictation must not quit")
	}
	waitCtrlcRecorderStop(t, rec)
}

// A recording with an empty composer cancels without arming the exit: before
// the fix the first Ctrl+C armed "Pressione Ctrl+C de novo para sair".
func TestDictationCtrlCRecordingEmptyComposerDoesNotArmExit(t *testing.T) {
	m, rec := ctrlcBatchRecordingModel(t, "")

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)

	if next.dictation.phase != dictIdle {
		t.Fatalf("phase after Ctrl+C = %v, want idle", next.dictation.phase)
	}
	if got := next.composerValue(); got != "" {
		t.Fatalf("composer after Ctrl+C = %q, want empty", got)
	}
	if next.exitConfirmActive {
		t.Error("Ctrl+C during a dictation must not arm the exit confirmation")
	}
	if next.exiting {
		t.Error("Ctrl+C during a dictation must not quit")
	}
	waitCtrlcRecorderStop(t, rec)
}

// A streaming partial rendered into the composer is discarded: the composer
// returns to the text from before the dictation and the stream is stopped.
func TestDictationCtrlCStreamingDiscardsPartialTranscript(t *testing.T) {
	m := newModel(t.Context(), Options{})
	m.dictation = batchOnlyController()
	m.setComposerState(composerState{text: "abc", cursor: 3})
	m.dictation.phase = dictRecording
	m.dictation.streaming = true
	streamStopped := false
	m.dictation.streamStop = func() error { streamStopped = true; return nil }
	m = m.handleDictationPartial(sttPartialMsg{sessionID: m.dictation.sessionID, text: "half-formed transcript"})
	if got := m.composerValue(); got != "abc half-formed transcript" {
		t.Fatalf("precondition: composer with partial = %q", got)
	}

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)

	if next.dictation.phase != dictIdle {
		t.Fatalf("phase after Ctrl+C = %v, want idle", next.dictation.phase)
	}
	if got := next.composerValue(); got != "abc" {
		t.Fatalf("composer after Ctrl+C = %q, want the pre-dictation text kept", got)
	}
	if !streamStopped {
		t.Error("Ctrl+C must stop the streaming capture")
	}
	if next.exitConfirmActive {
		t.Error("Ctrl+C during a dictation must not arm the exit confirmation")
	}
}

// Cancelling while transcribing leaves the composer alone, and the late
// transcription result of the cancelled session inserts nothing.
func TestDictationCtrlCTranscribingDropsLateTranscript(t *testing.T) {
	m := newModel(t.Context(), Options{})
	m.dictation = batchOnlyController()
	m.setComposerState(composerState{text: "abc", cursor: 3})
	m.dictation.phase = dictTranscribing
	cancelledSession := m.dictation.sessionID

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)

	if next.dictation.phase != dictIdle {
		t.Fatalf("phase after Ctrl+C = %v, want idle", next.dictation.phase)
	}
	if got := next.composerValue(); got != "abc" {
		t.Fatalf("composer after Ctrl+C = %q, want it unchanged", got)
	}

	updated, _ = next.Update(dictationTranscribedMsg{sessionID: cancelledSession, text: "hello world"})
	after := updated.(model)
	if after.dictation.phase != dictIdle {
		t.Fatalf("phase after the late transcript = %v, want idle", after.dictation.phase)
	}
	if got := after.composerValue(); got != "abc" {
		t.Fatalf("composer after the late transcript = %q, want it unchanged", got)
	}
}

// Cancelling while starting leaves the composer alone, and the late "started"
// message of the cancelled session does not begin a recording.
func TestDictationCtrlCStartingDropsLateStart(t *testing.T) {
	m := newModel(t.Context(), Options{})
	m.dictation = batchOnlyController()
	m.setComposerState(composerState{text: "abc", cursor: 3})
	started, cmd := m.startDictation()
	if cmd == nil {
		t.Fatal("startDictation should return the async start command on the batch path")
	}
	m = started
	cancelledSession := m.dictation.sessionID

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)

	if next.dictation.phase != dictIdle {
		t.Fatalf("phase after Ctrl+C = %v, want idle", next.dictation.phase)
	}
	if got := next.composerValue(); got != "abc" {
		t.Fatalf("composer after Ctrl+C = %q, want it unchanged", got)
	}

	updated, _ = next.Update(dictationStartedMsg{sessionID: cancelledSession})
	after := updated.(model)
	if after.dictation.phase != dictIdle {
		t.Fatalf("phase after the late start = %v, want idle (no recording began)", after.dictation.phase)
	}
	if got := after.composerValue(); got != "abc" {
		t.Fatalf("composer after the late start = %q, want it unchanged", got)
	}
}

// A model run in flight survives the dictation cancel: its cancel func is not
// called and the exit is not armed. The next Ctrl+C behaves as before: it
// cancels the run and arms the exit.
func TestDictationCtrlCRunInProgressSurvives(t *testing.T) {
	m, rec := ctrlcBatchRecordingModel(t, "abc")
	runCancelled := false
	m.pending = true
	m.runCancel = func() { runCancelled = true }

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)

	if next.dictation.phase != dictIdle {
		t.Fatalf("phase after Ctrl+C = %v, want idle", next.dictation.phase)
	}
	if runCancelled {
		t.Fatal("Ctrl+C during a dictation must not cancel the model run")
	}
	if !next.pending {
		t.Fatal("the model run must still be in flight after the dictation cancel")
	}
	if next.exitConfirmActive {
		t.Error("Ctrl+C during a dictation must not arm the exit confirmation")
	}
	waitCtrlcRecorderStop(t, rec)

	updated, _ = next.Update(testKeyCtrl('c'))
	after := updated.(model)
	if !runCancelled {
		t.Fatal("the second Ctrl+C (dictation idle) must cancel the model run")
	}
	if after.pending {
		t.Fatal("the run must be cancelled after the second Ctrl+C")
	}
	if !after.exitConfirmActive {
		t.Error("the second Ctrl+C must arm the exit confirmation, as before")
	}
	if after.exiting {
		t.Error("the second Ctrl+C arms the exit; it must not quit yet")
	}
}

// Inside /btw the dictation cancels but the conversation stays open: before
// the fix Ctrl+C left /btw (empty composer) or cleared the side draft.
func TestDictationCtrlCInsideBTWKeepsConversation(t *testing.T) {
	m, rec := ctrlcBatchRecordingModel(t, "")
	parent := model{}
	m.btw = btwState{active: true, parent: &parent}

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)

	if next.dictation.phase != dictIdle {
		t.Fatalf("phase after Ctrl+C = %v, want idle", next.dictation.phase)
	}
	if !next.btw.active {
		t.Fatal("Ctrl+C must cancel the dictation without leaving /btw")
	}
	if next.transientNotice.text != "Ditado cancelado." {
		t.Fatalf("notice after Ctrl+C = %q, want the cancellation notice", next.transientNotice.text)
	}
	waitCtrlcRecorderStop(t, rec)
}

// An armed exit confirmation does not turn the dictation cancel into a quit.
func TestDictationCtrlCWithExitArmedDoesNotQuit(t *testing.T) {
	m, rec := ctrlcBatchRecordingModel(t, "abc")
	m.exitConfirmActive = true

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)

	if next.dictation.phase != dictIdle {
		t.Fatalf("phase after Ctrl+C = %v, want idle", next.dictation.phase)
	}
	if got := next.composerValue(); got != "abc" {
		t.Fatalf("composer after Ctrl+C = %q, want the pre-dictation text kept", got)
	}
	if next.exiting {
		t.Fatal("Ctrl+C during a dictation must not quit, even with the exit armed")
	}
	waitCtrlcRecorderStop(t, rec)
}
