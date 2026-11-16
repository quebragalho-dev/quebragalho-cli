package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// holdTestModel builds a voice-mode model on a fake clock for the Space-hold
// tests. Callers advance at before each press; the clock starts at its
// current value.
func holdTestModel(t *testing.T, at *time.Time) *model {
	t.Helper()
	m := newModel(t.Context(), Options{})
	m.dictation = batchOnlyController()
	m.dictation.voiceModeEnabled = true
	m.now = func() time.Time { return *at }
	return &m
}

// holdSpace presses bare Space once per gap, advancing the fake clock by each
// gap first, and returns one command per press. Callers never execute the
// returned commands: an activation command would start a real capture tool.
func holdSpace(t *testing.T, m *model, at *time.Time, gaps ...time.Duration) []tea.Cmd {
	t.Helper()
	cmds := make([]tea.Cmd, 0, len(gaps))
	for _, gap := range gaps {
		*at = at.Add(gap)
		updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace}))
		*m = updated.(model)
		cmds = append(cmds, cmd)
	}
	return cmds
}

// holdToActivation runs a full 2s Space hold (t0, t0+400ms, then every 30ms)
// until a press activates dictation. It returns the activating command, which
// is nil when activation only posts a notice (engine downloading). The
// returned command is never executed.
func holdToActivation(t *testing.T, m *model, at *time.Time) tea.Cmd {
	t.Helper()
	holdSpace(t, m, at, 0)
	holdSpace(t, m, at, 400*time.Millisecond)
	for i := 0; i < 100; i++ {
		cmds := holdSpace(t, m, at, 30*time.Millisecond)
		if cmds[0] != nil {
			return cmds[0]
		}
		// Activation reset the run: count is zero again until the next press
		// starts a new one. (A downloading engine activates without a
		// command, leaving the phase idle, so the phase alone is not enough.)
		if m.dictation.phase != dictIdle || m.dictation.hold.count == 0 {
			return nil
		}
	}
	t.Fatal("Space hold never activated dictation")
	return nil
}

// pressKey delivers one key press at the current clock time.
func pressKey(t *testing.T, m *model, key tea.Key) tea.Cmd {
	t.Helper()
	updated, cmd := m.Update(tea.KeyPressMsg(key))
	*m = updated.(model)
	return cmd
}

func TestVoiceHoldStartsDictationAtThreshold(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	t0 := at
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	holdSpace(t, m, &at, 0, 400*time.Millisecond)
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase before threshold = %v, want idle", m.dictation.phase)
	}
	if got := m.composerValue(); got != "oi  " {
		t.Fatalf("composer before threshold = %q, want two typed spaces", got)
	}
	// Ten more repeats are still silence, not a hold: consumed, still idle.
	for _, cmd := range holdSpace(t, m, &at,
		30*time.Millisecond, 30*time.Millisecond, 30*time.Millisecond, 30*time.Millisecond, 30*time.Millisecond,
		30*time.Millisecond, 30*time.Millisecond, 30*time.Millisecond, 30*time.Millisecond, 30*time.Millisecond) {
		if cmd != nil {
			t.Fatal("repeat before threshold returned a command")
		}
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase mid-hold = %v, want idle", m.dictation.phase)
	}
	if got := m.composerValue(); got != "oi  " {
		t.Fatalf("composer mid-hold = %q, want the two typed spaces kept", got)
	}
	var cmd tea.Cmd
	for i := 0; i < 100; i++ {
		if cmds := holdSpace(t, m, &at, 30*time.Millisecond); cmds[0] != nil {
			cmd = cmds[0]
			break
		}
	}
	if cmd == nil {
		t.Fatal("Space hold never activated dictation")
	}
	if d := at.Sub(t0); d < voiceHoldThreshold || d >= voiceHoldThreshold+30*time.Millisecond {
		t.Fatalf("activated at %v after t0, want the first press at or past 2s", d)
	}
	if m.dictation.phase != dictStarting {
		t.Fatalf("phase after activation = %v, want starting", m.dictation.phase)
	}
	if !m.dictation.spaceHeld {
		t.Error("spaceHeld not set after hold activation")
	}
	if got := m.composerValue(); got != "oi" {
		t.Fatalf("composer after activation = %q, want the typed spaces removed", got)
	}
}

func TestVoiceHoldShortPressOnlyTypes(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	gaps := []time.Duration{0, 400 * time.Millisecond}
	for i := 0; i < 20; i++ {
		gaps = append(gaps, 30*time.Millisecond)
	}
	for i, cmd := range holdSpace(t, m, &at, gaps...) {
		if cmd != nil {
			t.Fatalf("press %d of a short hold returned a command", i)
		}
	}
	if cmd := pressKey(t, m, tea.Key{Code: 'a', Text: "a"}); cmd != nil {
		t.Fatalf("typing after a short hold returned a command")
	}
	if got := m.composerValue(); got != "oi  a" {
		t.Fatalf("composer after short hold = %q, want %q", got, "oi  a")
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after short hold = %v, want idle", m.dictation.phase)
	}
}

func TestVoiceHoldSlowTapsNeverStart(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	gaps := []time.Duration{0}
	for i := 0; i < 5; i++ {
		gaps = append(gaps, 700*time.Millisecond)
	}
	presses := len(gaps)
	for i, cmd := range holdSpace(t, m, &at, gaps...) {
		if cmd != nil {
			t.Fatalf("slow tap %d returned a command", i)
		}
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after slow taps = %v, want idle", m.dictation.phase)
	}
	if got, want := m.composerValue(), "oi"+strings.Repeat(" ", presses); got != want {
		t.Fatalf("composer after slow taps = %q, want every tap typed", got)
	}
}

func TestVoiceHoldPastedSpacesAreLeftAlone(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	gaps := []time.Duration{0}
	for i := 0; i < 7; i++ {
		gaps = append(gaps, time.Millisecond)
	}
	for i, cmd := range holdSpace(t, m, &at, gaps...) {
		if cmd != nil {
			t.Fatalf("pasted space %d returned a command", i)
		}
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after pasted spaces = %v, want idle", m.dictation.phase)
	}
	if got, want := m.composerValue(), "oi"+strings.Repeat(" ", len(gaps)); got != want {
		t.Fatalf("composer after pasted spaces = %q, want all 8 typed", got)
	}
}

func TestVoiceHoldOtherKeyEndsRun(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	t0 := at
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	gaps := []time.Duration{0, 400 * time.Millisecond}
	for i := 0; i < 36; i++ {
		gaps = append(gaps, 30*time.Millisecond)
	}
	holdSpace(t, m, &at, gaps...)
	pressKey(t, m, tea.Key{Code: 'x', Text: "x"})
	// A fresh run from here would need 2s; pressing only to t0+2.5s stays idle.
	at = t0.Add(1600 * time.Millisecond)
	gaps = nil
	for i := 0; i < 30; i++ {
		gaps = append(gaps, 30*time.Millisecond)
	}
	for i, cmd := range holdSpace(t, m, &at, gaps...) {
		if cmd != nil {
			t.Fatalf("press %d after another key returned a command", i)
		}
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after interrupted hold = %v, want idle", m.dictation.phase)
	}
}

func TestVoiceHoldRepeatConsumedWhileRecording(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	holdToActivation(t, m, &at)
	updated, _ := m.Update(dictationStartedMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictRecording {
		t.Fatalf("phase after start = %v, want recording", m.dictation.phase)
	}
	for _, cmd := range holdSpace(t, m, &at, 30*time.Millisecond, 30*time.Millisecond, 30*time.Millisecond) {
		if cmd != nil {
			t.Fatal("auto-repeat while recording returned a command")
		}
	}
	if m.dictation.phase != dictRecording {
		t.Fatalf("phase after repeats = %v, want recording", m.dictation.phase)
	}
	if got := m.composerValue(); got != "oi" {
		t.Fatalf("composer after repeats = %q, want repeats consumed", got)
	}
	if !m.dictation.hold.lastAt.Equal(at) {
		t.Fatalf("hold.lastAt = %v, want the last press time %v", m.dictation.hold.lastAt, at)
	}
}

func TestVoiceHoldReleaseStopsRecording(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	holdToActivation(t, m, &at)
	updated, _ := m.Update(dictationStartedMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	lastPress := at

	at = lastPress.Add(100 * time.Millisecond)
	updated, cmd := m.Update(voiceHoldTickMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictRecording {
		t.Fatalf("phase 100ms after press = %v, want recording", m.dictation.phase)
	}
	if cmd == nil {
		t.Fatal("tick while held returned no re-arm command")
	}

	at = lastPress.Add(301 * time.Millisecond)
	updated, _ = m.Update(voiceHoldTickMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictTranscribing {
		t.Fatalf("phase 301ms after press = %v, want transcribing", m.dictation.phase)
	}
}

func TestVoiceHoldSlowRepeatNeverStarts(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	// A repeat rate of 400ms looks like slow manual tapping, not auto-repeat:
	// nothing may activate no matter how long the key stays down.
	gaps := []time.Duration{0, 500 * time.Millisecond}
	for i := 0; i < 8; i++ {
		gaps = append(gaps, 400*time.Millisecond)
	}
	presses := len(gaps)
	for i, cmd := range holdSpace(t, m, &at, gaps...) {
		if cmd != nil {
			t.Fatalf("slow repeat %d returned a command", i)
		}
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after slow repeats = %v, want idle", m.dictation.phase)
	}
	if got, want := m.composerValue(), "oi"+strings.Repeat(" ", presses); got != want {
		t.Fatalf("composer after slow repeats = %q, want every press typed", got)
	}
}

func TestVoiceHoldJitterTolerated(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	t0 := at
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	// Mostly 30ms repeats with every 10th pause at 300ms (jitter): the run
	// still looks like auto-repeat and activates at 2s.
	holdSpace(t, m, &at, 0, 400*time.Millisecond)
	var cmd tea.Cmd
	pauses := 0
	for i := 0; i < 100; i++ {
		pauses++
		gap := 30 * time.Millisecond
		if pauses%10 == 0 {
			gap = 300 * time.Millisecond
		}
		if cmds := holdSpace(t, m, &at, gap); cmds[0] != nil {
			cmd = cmds[0]
			break
		}
	}
	if cmd == nil {
		t.Fatal("jittered hold never activated dictation")
	}
	if d := at.Sub(t0); d < voiceHoldThreshold {
		t.Fatalf("activated at %v after t0, want at or past 2s", d)
	}
	if m.dictation.phase != dictStarting {
		t.Fatalf("phase after jittered hold = %v, want starting", m.dictation.phase)
	}
	if got := m.composerValue(); got != "oi" {
		t.Fatalf("composer after jittered hold = %q, want every typed space removed", got)
	}
	updated, _ := m.Update(dictationStartedMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	lastPress := at

	// The widest pause was 300ms, so the release timeout is 3 * 300ms.
	at = lastPress.Add(700 * time.Millisecond)
	updated, _ = m.Update(voiceHoldTickMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictRecording {
		t.Fatalf("phase 700ms after a 300ms pause = %v, want recording", m.dictation.phase)
	}

	at = lastPress.Add(901 * time.Millisecond)
	updated, _ = m.Update(voiceHoldTickMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictTranscribing {
		t.Fatalf("phase 901ms after a 300ms pause = %v, want transcribing", m.dictation.phase)
	}
}

func TestVoiceHoldStaleTickIsIgnored(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	holdToActivation(t, m, &at)
	before := m.dictation.hold
	updated, cmd := m.Update(voiceHoldTickMsg{sessionID: m.dictation.sessionID - 1})
	*m = updated.(model)
	if cmd != nil {
		t.Fatal("stale tick returned a command")
	}
	if m.dictation.phase != dictStarting || !m.dictation.spaceHeld {
		t.Fatalf("stale tick changed the session: phase=%v held=%v", m.dictation.phase, m.dictation.spaceHeld)
	}
	if m.dictation.hold != before {
		t.Fatalf("stale tick changed the run: %+v, want %+v", m.dictation.hold, before)
	}
}

func TestVoiceHoldReleaseWhileStartingDefersStop(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	holdToActivation(t, m, &at)
	at = m.dictation.hold.lastAt.Add(time.Second)
	updated, _ := m.Update(voiceHoldTickMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictStarting {
		t.Fatalf("phase after release while starting = %v, want starting", m.dictation.phase)
	}
	if !m.dictation.voiceStopPending {
		t.Fatal("release while starting did not defer the stop")
	}
	updated, _ = m.Update(dictationStartedMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictTranscribing {
		t.Fatalf("phase after deferred start = %v, want transcribing", m.dictation.phase)
	}
}

func TestVoiceHoldModeOffTypesEverything(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := newModel(t.Context(), Options{})
	m.dictation = batchOnlyController()
	m.now = func() time.Time { return at }
	m.setComposerState(composerState{text: "oi", cursor: 2})

	gaps := []time.Duration{0, 400 * time.Millisecond}
	for i := 0; i < 87; i++ {
		gaps = append(gaps, 30*time.Millisecond)
	}
	cmds := holdSpace(t, &m, &at, gaps...)
	for i, cmd := range cmds {
		if cmd != nil {
			t.Fatalf("press %d with voice mode off returned a command", i)
		}
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase with voice mode off = %v, want idle", m.dictation.phase)
	}
	if got, want := m.composerValue(), "oi"+strings.Repeat(" ", len(gaps)); got != want {
		t.Fatalf("composer with voice mode off holds %d spaces, want every press typed", strings.Count(got, " ")-strings.Count(want, " ")+len(gaps))
	}
}

func TestVoiceHoldBehindModalStaysIdle(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.autoCompactPrompt = &autoCompactPromptState{}

	gaps := []time.Duration{0, 400 * time.Millisecond}
	for i := 0; i < 54; i++ {
		gaps = append(gaps, 30*time.Millisecond)
	}
	holdSpace(t, m, &at, gaps...)
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase behind a modal = %v, want idle", m.dictation.phase)
	}
}

func TestVoiceHoldStatusTexts(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)

	if got := m.voiceModeIndicator(); !strings.Contains(got, "segure Espaço para ditar") {
		t.Fatalf("idle indicator = %q, want the hold hint", got)
	}
	m.setComposerState(composerState{text: "oi", cursor: 2})
	holdSpace(t, m, &at, 0, 30*time.Millisecond, 30*time.Millisecond)
	if got := m.voiceModeIndicator(); !strings.Contains(got, "continue segurando") {
		t.Fatalf("warming indicator = %q, want the keep-holding hint", got)
	}
	holdToActivation(t, m, &at)
	if got := m.dictationStatusChip(); !strings.Contains(got, "solte para parar") {
		t.Fatalf("recording chip = %q, want the release hint", got)
	}
}

func TestVoiceHoldDisablesSilenceAutoStop(t *testing.T) {
	d := batchOnlyController()
	if d.silenceAutoStop(true) {
		t.Error("silenceAutoStop(hold=true) must be false")
	}
	if !d.silenceAutoStop(false) {
		t.Error("silenceAutoStop(hold=false) must follow the config default")
	}
	off := false
	d.cfg.SilenceAutoStop = &off
	if d.silenceAutoStop(true) || d.silenceAutoStop(false) {
		t.Error("silenceAutoStop must be false when the config disables it")
	}
}

func TestVoiceHoldWhileDownloadingShowsNotice(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.dictation.downloading = true
	m.setComposerState(composerState{text: "oi", cursor: 2})

	holdToActivation(t, m, &at)
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase while downloading = %v, want idle", m.dictation.phase)
	}
	if got := m.composerValue(); got != "oi" {
		t.Fatalf("composer while downloading = %q, want the typed spaces removed", got)
	}
	if !transcriptHasText(*m, "Ainda configurando o motor de ditado") {
		t.Fatal("missing the downloading notice in the transcript")
	}
}

func TestVoiceHoldManualTappingNeverStarts(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	// Tapped by hand every 180ms for 4s: evenly spaced but too slow to be
	// auto-repeat, so nothing may activate.
	gaps := []time.Duration{0}
	for i := 0; i < 22; i++ {
		gaps = append(gaps, 180*time.Millisecond)
	}
	presses := len(gaps)
	for i, cmd := range holdSpace(t, m, &at, gaps...) {
		if cmd != nil {
			t.Fatalf("tap %d returned a command", i)
		}
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after manual tapping = %v, want idle", m.dictation.phase)
	}
	if got, want := m.composerValue(), "oi"+strings.Repeat(" ", presses); got != want {
		t.Fatalf("composer after manual tapping = %q, want every tap typed", got)
	}
}

func TestVoiceHoldSpentSwallowsAfterFailedStart(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.dictation.downloading = true
	m.setComposerState(composerState{text: "oi", cursor: 2})

	holdToActivation(t, m, &at)
	// 100 more auto-repeats (3s, enough for a second activation): one physical
	// hold activates once, so all of them are swallowed.
	for i, cmd := range holdSpace(t, m, &at, repeatGaps(100, 30*time.Millisecond)...) {
		if cmd != nil {
			t.Fatalf("swallowed repeat %d returned a command", i)
		}
	}
	if got := m.composerValue(); got != "oi" {
		t.Fatalf("composer after swallowed repeats = %q, want the original text", got)
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after swallowed repeats = %v, want idle", m.dictation.phase)
	}
	if m.dictation.hold.count != 0 {
		t.Fatalf("hold.count after swallowed repeats = %d, want no new run", m.dictation.hold.count)
	}
	found := 0
	for _, row := range m.transcript {
		if strings.Contains(row.text, "Ainda configurando o motor de ditado") {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("downloading notice appears %d times, want exactly once", found)
	}
}

func TestVoiceHoldEndedRecordingDoesNotRestart(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	holdToActivation(t, m, &at)
	// The recording fails to start (missing capture tool): back to idle with
	// the hold still down and latched.
	updated, cmd := m.Update(dictationStartedMsg{sessionID: m.dictation.sessionID, err: errors.New("mic busy")})
	*m = updated.(model)
	if cmd != nil {
		t.Fatal("a failed start returned a command")
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after failed start = %v, want idle", m.dictation.phase)
	}
	for i, cmd := range holdSpace(t, m, &at, repeatGaps(100, 30*time.Millisecond)...) {
		if cmd != nil {
			t.Fatalf("repeat %d after failed start returned a command", i)
		}
	}
	if got := m.composerValue(); got != "oi" {
		t.Fatalf("composer after failed start = %q, want it unchanged", got)
	}
	if m.dictation.phase != dictIdle {
		t.Fatalf("phase after failed start repeats = %v, want idle", m.dictation.phase)
	}
}

func TestVoiceHoldReleaseEndsLatch(t *testing.T) {
	// Latch, then a long pause and one more press: the key went up in between,
	// so the press types and the latch is gone.
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.dictation.downloading = true
	m.setComposerState(composerState{text: "oi", cursor: 2})
	holdToActivation(t, m, &at)
	holdSpace(t, m, &at, repeatGaps(100, 30*time.Millisecond)...)
	at = at.Add(600 * time.Millisecond)
	if cmd := pressKey(t, m, tea.Key{Code: tea.KeySpace}); cmd != nil {
		t.Fatal("the press after the pause returned a command")
	}
	if got := m.composerValue(); got != "oi " {
		t.Fatalf("composer after the pause = %q, want one typed space", got)
	}
	if m.dictation.hold.spent {
		t.Error("hold.spent still set after the key went up")
	}

	// A tick past the release timeout clears the latch too.
	at = time.Unix(1_700_000_000, 0)
	m = holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})
	holdToActivation(t, m, &at)
	updated, _ := m.Update(dictationStartedMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	at = m.dictation.hold.lastAt.Add(time.Second)
	updated, _ = m.Update(voiceHoldTickMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictTranscribing {
		t.Fatalf("phase after release tick = %v, want transcribing", m.dictation.phase)
	}
	if m.dictation.hold.spent {
		t.Error("hold.spent still set after the release tick")
	}

	// A real Space release event clears the latch.
	m.dictation.hold.spent = true
	updated, _ = m.Update(tea.KeyReleaseMsg(tea.Key{Code: tea.KeySpace}))
	*m = updated.(model)
	if m.dictation.hold.spent {
		t.Error("hold.spent still set after a Space release event")
	}
}

func TestVoiceHoldAutoRepeatBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		hold voiceHold
		want bool
	}{
		{name: "two presses have no pause to judge", hold: voiceHold{count: 2}, want: false},
		{name: "exactly eighty percent", hold: voiceHold{count: 12, rapid: 8}, want: true},
		{name: "below eighty percent", hold: voiceHold{count: 12, rapid: 7}, want: false},
		{name: "single rapid pause", hold: voiceHold{count: 3, rapid: 1}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.hold.autoRepeat(); got != test.want {
				t.Fatalf("autoRepeat() = %v, want %v", got, test.want)
			}
		})
	}
}

// repeatGaps returns n identical gaps for holdSpace.
func repeatGaps(n int, gap time.Duration) []time.Duration {
	gaps := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		gaps = append(gaps, gap)
	}
	return gaps
}
