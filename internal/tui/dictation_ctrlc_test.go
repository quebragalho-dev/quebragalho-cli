package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

type ctrlcStreamingRecorder struct {
	chunks chan []byte
	stopCh chan struct{}
}

func newCtrlcStreamingRecorder() *ctrlcStreamingRecorder {
	return &ctrlcStreamingRecorder{
		chunks: make(chan []byte),
		stopCh: make(chan struct{}, 1),
	}
}

func (r *ctrlcStreamingRecorder) Start() error { return nil }
func (r *ctrlcStreamingRecorder) Stop() ([]byte, error) {
	return nil, nil
}
func (r *ctrlcStreamingRecorder) StartStreaming() (<-chan []byte, func() error, error) {
	stop := func() error {
		select {
		case r.stopCh <- struct{}{}:
			close(r.chunks)
		default:
		}
		return nil
	}
	return r.chunks, stop, nil
}

// Ctrl+C during a bare-Space hold recording cancels dictation (even when
// pressing Ctrl first delivers a Ctrl+Space auto-repeat and Space stays down
// briefly afterward), discards dimmed live text without transcribing, restores
// the composer as it was before recording, and leaves outside-recording Ctrl+C
// unchanged.
func TestDictationCtrlCDuringSpaceHoldCancelsAndRestoresComposer(t *testing.T) {
	for _, releaseEvents := range []bool{false, true} {
		name := "without_release_events"
		if releaseEvents {
			name = "with_release_events"
		}
		t.Run(name, func(t *testing.T) {
			acceptCacheTrueColor(t)
			at := time.Unix(1_700_000_000, 0)
			m := holdTestModel(t, &at)
			m.dictation.eventTypesSupported = releaseEvents
			m.setComposerState(composerState{text: "rascunho", cursor: len([]rune("rascunho"))})

			holdToActivation(t, m, &at)
			updated, _ := m.Update(dictationStartedMsg{sessionID: m.dictation.sessionID})
			*m = updated.(model)
			rec := newCtrlcStreamingRecorder()
			m.dictation.streaming = true
			_, stopStream, _ := rec.StartStreaming()
			m.dictation.streamStop = stopStream
			cancelledSession := m.dictation.sessionID

			*m = m.handleDictationPartial(sttPartialMsg{sessionID: cancelledSession, text: "texto ao vivo"})
			dimStyle := qgTheme.faint.Inline(true)
			if !strings.Contains(m.composerLine(80), dimStyle.Render(" texto ao vivo")) {
				t.Fatalf("precondition: live partial should render dimmed, got %q", m.composerLine(80))
			}

			// Pressing Ctrl while bare Space is still held delivers an
			// auto-repeated Ctrl+Space just before C lands.
			at = at.Add(30 * time.Millisecond)
			pressKey(t, m, tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl})
			if m.dictation.phase != dictRecording {
				t.Fatalf("Ctrl+Space during Space-hold recording changed phase to %v, want recording", m.dictation.phase)
			}

			at = at.Add(30 * time.Millisecond)
			updated, _ = m.Update(testKeyCtrl('c'))
			*m = updated.(model)

			if m.dictation.phase != dictIdle {
				t.Fatalf("phase after Ctrl+C = %v, want idle", m.dictation.phase)
			}
			select {
			case <-rec.stopCh:
			default:
				t.Fatal("Ctrl+C must stop the streaming capture")
			}
			if got := m.currentComposerState(); got != (composerState{text: "rascunho", cursor: 8}) {
				t.Fatalf("composer state after Ctrl+C = %+v, want {rascunho 8}", got)
			}
			if strings.Contains(m.composerLine(80), dimStyle.Render("texto ao vivo")) {
				t.Fatalf("dimmed live text must be discarded on Ctrl+C: %q", m.composerLine(80))
			}

			// Remaining auto-repeats of the still-held Space must neither type
			// spaces nor start a new recording that refills live text.
			holdSpace(t, m, &at, repeatGaps(80, 30*time.Millisecond)...)
			if m.dictation.phase != dictIdle {
				t.Fatalf("held Space after Ctrl+C restarted dictation: phase = %v", m.dictation.phase)
			}
			if got := m.composerValue(); got != "rascunho" {
				t.Fatalf("held Space after Ctrl+C modified composer = %q, want %q", got, "rascunho")
			}

			// Late partial and final transcript from the cancelled session must drop.
			*m = m.handleDictationPartial(sttPartialMsg{sessionID: cancelledSession, text: "volta a preencher"})
			updated, _ = m.Update(dictationTranscribedMsg{sessionID: cancelledSession, text: "volta a preencher", streaming: true})
			*m = updated.(model)
			if got := m.composerValue(); got != "rascunho" {
				t.Fatalf("late streaming messages modified composer = %q, want %q", got, "rascunho")
			}

			// Outside recording, Ctrl+C keeps its normal behavior: first clears
			// the non-empty box, next arms exit, third quits.
			updated, _ = m.Update(testKeyCtrl('c'))
			*m = updated.(model)
			if got := m.composerValue(); got != "" || m.exitConfirmActive || m.exiting {
				t.Fatalf("first idle Ctrl+C should only clear composer: text=%q exitArm=%v exiting=%v", got, m.exitConfirmActive, m.exiting)
			}
			updated, _ = m.Update(testKeyCtrl('c'))
			*m = updated.(model)
			if !m.exitConfirmActive || m.exiting {
				t.Fatalf("second idle Ctrl+C should arm exit: exitArm=%v exiting=%v", m.exitConfirmActive, m.exiting)
			}
			updated, _ = m.Update(testKeyCtrl('c'))
			*m = updated.(model)
			if !m.exiting {
				t.Fatal("third idle Ctrl+C should quit")
			}
		})
	}
}

// Ctrl+C during a Ctrl+Space recording cancels dictation, discards dimmed live
// partials even when the cursor started mid-text with paste previews, restores
// the exact pre-recording composer state, and ignores trailing chord repeats
// and releases.
func TestDictationCtrlCDuringCtrlSpaceRestoresExactMidCursorComposer(t *testing.T) {
	acceptCacheTrueColor(t)
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.dictation.eventTypesSupported = true
	preState := composerState{text: "antes BLOCK depois", cursor: 5}
	prePreviews := []composerPastePreview{{active: true, start: 6, end: 11, label: "[Pasted #1]"}}
	m.setComposerState(preState)
	m.composerPastePreviews = append([]composerPastePreview(nil), prePreviews...)

	updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("Ctrl+Space should start dictation")
	}
	*m = updated.(model)
	updated, _ = m.Update(dictationStartedMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	rec := newCtrlcStreamingRecorder()
	m.dictation.streaming = true
	_, stopStream, _ := rec.StartStreaming()
	m.dictation.streamStop = stopStream
	cancelledSession := m.dictation.sessionID

	// Two cumulative partials arrive while the cursor was mid-text.
	*m = m.handleDictationPartial(sttPartialMsg{sessionID: cancelledSession, text: "um"})
	*m = m.handleDictationPartial(sttPartialMsg{sessionID: cancelledSession, text: "um dois tres"})

	at = at.Add(50 * time.Millisecond)
	updated, _ = m.Update(testKeyCtrl('c'))
	*m = updated.(model)

	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after Ctrl+C = %v, want idle", m.dictation.phase)
	}
	select {
	case <-rec.stopCh:
	default:
		t.Fatal("Ctrl+C must stop the streaming capture")
	}
	if got := m.currentComposerState(); got != preState {
		t.Fatalf("composer state after Ctrl+C = %+v, want exact pre-recording %+v", got, preState)
	}
	if len(m.composerPastePreviews) != 1 || m.composerPastePreviews[0] != prePreviews[0] {
		t.Fatalf("paste previews after Ctrl+C = %+v, want %+v", m.composerPastePreviews, prePreviews)
	}
	if m.dictation.regionActive {
		t.Fatal("live region must be inactive after Ctrl+C")
	}

	// Trailing Ctrl+Space repeat and key releases after Ctrl+C must be inert.
	at = at.Add(30 * time.Millisecond)
	pressKey(t, m, tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl})
	updated, _ = m.Update(tea.KeyReleaseMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl}))
	*m = updated.(model)
	updated, _ = m.Update(tea.KeyReleaseMsg(tea.Key{Code: tea.KeyLeftCtrl}))
	*m = updated.(model)
	if m.dictation.phase != dictIdle {
		t.Fatalf("trailing chord events changed phase to %v, want idle", m.dictation.phase)
	}
	if got := m.currentComposerState(); got != preState {
		t.Fatalf("trailing chord events changed composer to %+v, want %+v", got, preState)
	}
}
