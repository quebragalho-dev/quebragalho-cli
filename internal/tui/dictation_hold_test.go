package tui

import (
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

func TestVoiceHoldSlowRepeatRateIsNotARelease(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	m := holdTestModel(t, &at)
	m.setComposerState(composerState{text: "oi", cursor: 2})

	holdSpace(t, m, &at, 0, 500*time.Millisecond)
	var cmd tea.Cmd
	for i := 0; i < 20; i++ {
		if cmds := holdSpace(t, m, &at, 400*time.Millisecond); cmds[0] != nil {
			cmd = cmds[0]
			break
		}
	}
	if cmd == nil {
		t.Fatal("slow hold never activated dictation")
	}
	if m.dictation.phase != dictStarting {
		t.Fatalf("phase after slow hold = %v, want starting", m.dictation.phase)
	}
	if got := m.composerValue(); got != "oi" {
		t.Fatalf("composer after slow hold = %q, want every typed space removed", got)
	}
	updated, _ := m.Update(dictationStartedMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	lastPress := at

	at = lastPress.Add(700 * time.Millisecond)
	updated, _ = m.Update(voiceHoldTickMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictRecording {
		t.Fatalf("phase 700ms after a 400ms-rate press = %v, want recording", m.dictation.phase)
	}

	at = lastPress.Add(1300 * time.Millisecond)
	updated, _ = m.Update(voiceHoldTickMsg{sessionID: m.dictation.sessionID})
	*m = updated.(model)
	if m.dictation.phase != dictTranscribing {
		t.Fatalf("phase 1300ms after a 400ms-rate press = %v, want transcribing", m.dictation.phase)
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
