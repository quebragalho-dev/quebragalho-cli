package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/dictation"
)

// Voice mode's dictation gestures — two ways to record:
//
//  1. Hold bare Space for 2 seconds: the terminal delivers a held Space as a
//     stream of ordinary Space presses (keyboard auto-repeat) with no release
//     event, so the hold and its release are inferred from press timing on
//     every terminal (see voiceHold). Recording starts once the key has been
//     down for voiceHoldThreshold and stops when the key is released. A short
//     press still types a space. Manual tapping never starts dictation: the
//     run's average repeat rate past the initial delay must be
//     voiceHoldRapidGap or faster, and a slower average cannot use this
//     gesture (Ctrl+Space covers it). One physical hold activates at most once.
//  2. Ctrl+Space, unchanged, with its two terminal tiers: where the terminal
//     confirms key-release reporting (Ghostty/Kitty/WezTerm, …) a press starts
//     and the release stops (hold-to-record); where it does not, Ctrl+Space
//     falls back to press-to-toggle. Legacy NUL input is decoded as Ctrl+Space
//     by the terminal input layer, so the same matcher covers both tiers.
//     Not on Windows: a lone Ctrl, Shift or Alt reaches the console as that
//     same NUL, so the program event filter drops it there (see
//     loneModifierNUL) and the Space hold is the only gesture.
//
// Only active while voice mode is on; the rest of dispatch is built on
// KeyPressMsg and is untouched.

// handleKeyboardEnhancements records the terminal's confirmed keyboard
// capabilities, so voice mode knows whether hold-to-record is available and
// hints know whether Ctrl+Enter is distinguishable from Enter at all.
func (m model) handleKeyboardEnhancements(msg tea.KeyboardEnhancementsMsg) model {
	m.dictation.eventTypesKnown = true
	m.dictation.eventTypesSupported = msg.SupportsEventTypes()
	m.keyboardDisambiguation = msg.SupportsKeyDisambiguation()
	return m
}

func voiceCaptureKey(msg tea.KeyMsg) bool {
	mod := msg.Key().Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	return keyIs(msg, tea.KeySpace) && mod == tea.ModCtrl
}

// voiceHoldKey matches a bare Space press: no modifier other than
// CapsLock/NumLock. This is the Space hold-to-dictate gesture's key; Ctrl+Space
// keeps its own matcher above.
func voiceHoldKey(msg tea.KeyMsg) bool {
	mod := msg.Key().Mod &^ (tea.ModCapsLock | tea.ModNumLock)
	return keyIs(msg, tea.KeySpace) && mod == 0
}

// voiceHoldPreservesLatchKey reports a key press that keeps an already-spent
// Space hold latched: bare Space itself, Ctrl+Space, or a Ctrl modifier press.
func voiceHoldPreservesLatchKey(msg tea.KeyMsg, spent bool) bool {
	if voiceHoldKey(msg) {
		return true
	}
	if !spent {
		return false
	}
	return voiceCaptureKey(msg) || keyIs(msg, tea.KeyLeftCtrl) || keyIs(msg, tea.KeyRightCtrl)
}

// voiceCaptureReleaseKey matches either half of the held Ctrl+Space chord. A
// terminal may report Ctrl releasing first, or report the later Space release
// without ModCtrl because Ctrl is no longer down.
func voiceCaptureReleaseKey(msg tea.KeyMsg) bool {
	return keyIs(msg, tea.KeySpace) || keyIs(msg, tea.KeyLeftCtrl) || keyIs(msg, tea.KeyRightCtrl)
}

func (m model) voiceCaptureStatus() string {
	switch m.dictation.phase {
	case dictStarting, dictRecording:
		if m.dictation.spaceHeld {
			return "solte para parar"
		}
		return "pressione Ctrl+Space para parar"
	case dictTranscribing:
		return "transcrevendo…"
	default:
		if m.voiceHoldWarming() {
			return "continue segurando…"
		}
		return "segure Espaço para ditar"
	}
}

// handleVoiceCapturePress handles Ctrl+Space while voice mode is on.
func (m model) handleVoiceCapturePress(msg tea.KeyMsg) (model, tea.Cmd) {
	if m.dictation.spaceHeld && (m.dictation.phase == dictStarting || m.dictation.phase == dictRecording) {
		// A hold recording (bare Space or Ctrl+Space) is already running: holding
		// Ctrl on top of bare Space (e.g. while pressing Ctrl+C) delivers
		// auto-repeats as Ctrl+Space and must neither toggle-stop the recording
		// nor let the Space hold time out.
		m.dictation.hold.lastAt = m.currentTime()
		return m, nil
	}
	if m.dictation.hold.spent {
		now := m.currentTime()
		if now.Sub(m.dictation.hold.lastAt) <= voiceHoldRepeatGap {
			m.dictation.hold.lastAt = now
			return m, nil
		}
		m.dictation.hold.spent = false
	}
	if !m.dictation.eventTypesSupported {
		// Tier 2: no release events — press-to-toggle.
		return m.toggleDictation()
	}
	// Tier 1: hold-to-record. Ignore auto-repeat presses while the key is held.
	if msg.Key().IsRepeat {
		return m, nil
	}
	if m.dictation.phase == dictIdle {
		return m.startDictationWithVoice(true, false)
	}
	return m, nil // already recording; the release will stop it
}

// handleVoiceCaptureRelease stops a hold-to-record session when Ctrl+Space is released.
func (m model) handleVoiceCaptureRelease() (model, tea.Cmd) {
	if !m.dictation.spaceHeld {
		return m, nil
	}
	switch m.dictation.phase {
	case dictRecording:
		return m.stopDictation()
	case dictStarting:
		// Released before the recording finished starting; stop as soon as it does.
		_ = m.dictation.transition(DictEvVoiceDefer, dictArgs{})
		return m, nil
	}
	return m, nil
}

// sttMicCheckedMsg carries the outcome of the microphone check enableVoiceMode
// runs off the UI goroutine.
type sttMicCheckedMsg struct {
	check dictation.MicCheck
	err   error
}

// handleSTTMicChecked reports the microphone check without ever changing voice
// mode or touching a recording: a result that arrives after voice mode was
// turned off, or while a dictation is in progress, is dropped.
func (m model) handleSTTMicChecked(msg sttMicCheckedMsg) model {
	if !m.dictation.voiceModeEnabled || m.dictation.active() {
		return m
	}
	if msg.err != nil {
		return m.appendSystemNotice("Não foi possível testar o microfone: " + dictationErrorText(msg.err))
	}
	if msg.check.Silent {
		return m.appendSystemNotice(m.micSilentText())
	}
	return m.showTransientNoticeInline("Microfone funcionando. Segure Espaço e fale.", transientNoticeSuccess)
}

// micSilentText names the platform's microphone permission path when the
// system is not delivering audio.
func (m model) micSilentText() string {
	const lead = "O microfone não está entregando som. "
	switch m.dictation.platform {
	case dictation.PlatformDarwin:
		return lead + "Em Ajustes do Sistema > Privacidade e Segurança > Microfone, permita o seu terminal e rode /voice de novo. Se o macOS acabou de pedir a permissão, aceite e rode /voice de novo."
	case dictation.PlatformWindows:
		return lead + "Em Configurações > Privacidade e segurança > Microfone, ligue \"Permitir que aplicativos da área de trabalho acessem o microfone\" e rode /voice de novo."
	default:
		return lead + "Confira o microfone padrão do sistema e rode /voice de novo."
	}
}

// voiceHoldThreshold is how long bare Space must stay down before a hold
// recording starts.
const voiceHoldThreshold = 2 * time.Second

// voiceHoldFirstGap is the max pause between the 1st and 2nd press of a run
// (the keyboard's initial repeat delay).
const voiceHoldFirstGap = 1200 * time.Millisecond

// voiceHoldRepeatGap is the max pause between two later presses of a run.
const voiceHoldRepeatGap = 500 * time.Millisecond

// voiceHoldRapidGap is the max pause that still counts as auto-repeat: a
// press this close to the previous one did not come from typing.
const voiceHoldRapidGap = 120 * time.Millisecond

// voiceHoldPasteGuard is the minimum age of a run before a rapid press counts
// as auto-repeat; it protects pasted spaces on terminals without bracketed paste.
const voiceHoldPasteGuard = 200 * time.Millisecond

// voiceHoldReleaseFloor is the minimum silence that counts as releasing the key.
const voiceHoldReleaseFloor = 300 * time.Millisecond

// voiceHoldReleaseCeiling is the maximum silence that counts as releasing the key.
const voiceHoldReleaseCeiling = 1500 * time.Millisecond

// voiceHoldTickInterval is how often a hold recording checks for the release.
const voiceHoldTickInterval = 100 * time.Millisecond

// voiceHold tracks a run of bare Space presses that may be a held key. A
// terminal delivers a held Space as ordinary presses (keyboard auto-repeat)
// with no release event, so the hold and its release are inferred from timing.
// Manual tapping never starts dictation: past the initial delay, the run's
// average repeat rate must be voiceHoldRapidGap or faster (see autoRepeat); an
// average rate slower than that cannot use the Space hold. Ctrl+Space covers
// it. One physical hold activates at most once: after an activation the
// remaining auto-repeats of that hold are swallowed until the key goes up.
type voiceHold struct {
	startedAt  time.Time     // first Space of the run; zero when there is no run
	lastAt     time.Time     // latest Space press, of the run or of the recording it started
	count      int           // presses in the run
	typed      int           // presses of the run that reached the composer
	maxGap     time.Duration // widest pause between two auto-repeats of the current run; a recording keeps the value of the run that started it, and a new run starts from zero
	repeatFrom time.Time     // second press of the run: where its auto-repeat pauses start
	flagged    bool          // a press of the run carried IsRepeat: the terminal says the key is held
	spent      bool          // this hold already activated: its remaining auto-repeats are swallowed until the key goes up
	warmed     bool          // the run already asked for the microphone to be opened ahead of the recording
}

// endRun drops the run but keeps lastAt, maxGap and spent: a running hold
// recording still needs all three to detect the release. A new run resets
// maxGap when it starts (see the fresh-run branch); endRun must not do it, or
// a recording would lose the rate of the run that started it.
func (h *voiceHold) endRun() {
	h.startedAt = time.Time{}
	h.count = 0
	h.typed = 0
	h.repeatFrom = time.Time{}
	h.flagged = false
	h.warmed = false
}

// keyUp ends the run and clears the activation latch: the key went up (or
// another key ended the hold — the system only repeats the last key pressed),
// so the next Space starts a new physical hold.
func (h *voiceHold) keyUp() {
	h.endRun()
	h.spent = false
}

// autoRepeat reports whether the run looks like a held key rather than manual
// tapping: the terminal flagged a repeat, or the pauses after the initial
// repeat delay average voiceHoldRapidGap or less. The average, not each pause,
// is judged: jitter and a busy interface deliver a held key in bursts, which
// moves the pauses around without changing their sum.
func (h voiceHold) autoRepeat() bool {
	if h.flagged {
		return true
	}
	pauses := h.count - 2
	return pauses > 0 && h.lastAt.Sub(h.repeatFrom) <= time.Duration(pauses)*voiceHoldRapidGap
}

// releaseTimeout is how long a hold recording waits without a Space press
// before treating the key as released: three times the run's widest
// auto-repeat pause, so slow repeat rates are not mistaken for a release,
// clamped to a sane floor and ceiling.
func (h voiceHold) releaseTimeout() time.Duration {
	return min(max(3*h.maxGap, voiceHoldReleaseFloor), voiceHoldReleaseCeiling)
}

// handleVoiceHoldSpace folds one bare Space press into the hold detector. A
// consumed press (handled=true) must not reach the composer; a press that
// falls through (handled=false) types as today.
func (m model) handleVoiceHoldSpace(msg tea.KeyMsg, now time.Time) (model, tea.Cmd, bool) {
	hold := m.dictation.hold
	phase := m.dictation.phase
	if m.dictation.spaceHeld && (phase == dictStarting || phase == dictRecording) {
		// Rule 1: a hold recording is running; this press is the key still down.
		m.dictation.hold.lastAt = now
		return m, nil, true
	}
	if hold.spent {
		// The hold that already activated is still down (its recording could
		// not start, or ended on its own): swallow its auto-repeats so they
		// neither type nor start another recording.
		if now.Sub(hold.lastAt) <= voiceHoldRepeatGap {
			m.dictation.hold.lastAt = now
			return m, nil, true
		}
		m.dictation.hold.spent = false
	}
	if phase != dictIdle {
		// Rule 2: transcribing, or a Ctrl+Space toggle recording: the run ends
		// and the press types as today.
		m.dictation.hold.endRun()
		return m, nil, false
	}
	// Rule 3: idle. A run with no presses yet, or one whose pause outgrew the
	// repeat gap, restarts here; the other key between two runs already ended
	// the old one.
	limit := voiceHoldRepeatGap
	if hold.count == 1 {
		limit = voiceHoldFirstGap
	}
	if hold.count == 0 || now.Sub(hold.lastAt) > limit {
		m.dictation.hold.startedAt = now
		m.dictation.hold.lastAt = now
		m.dictation.hold.count = 1
		m.dictation.hold.typed = 1
		// A new run measures its own repeat rate from zero. maxGap belongs to
		// the previous run (and to the recording it may have started); without
		// the reset a single slow pause anywhere in the session would slow
		// every later release detection.
		m.dictation.hold.maxGap = 0
		return m, nil, false
	}
	pause := now.Sub(hold.lastAt)
	m.dictation.hold.count++
	m.dictation.hold.lastAt = now
	if m.dictation.hold.count == 2 {
		m.dictation.hold.repeatFrom = now
	}
	if msg.Key().IsRepeat {
		m.dictation.hold.flagged = true
	}
	if m.dictation.hold.count > 2 && pause > m.dictation.hold.maxGap {
		// The first pause is the keyboard's initial repeat delay, not the
		// auto-repeat rate, so only later pauses widen the release timeout.
		m.dictation.hold.maxGap = pause
	}
	if now.Sub(m.dictation.hold.startedAt) >= voiceHoldThreshold && m.dictation.hold.autoRepeat() {
		return m.activateVoiceHold(m.dictation.hold, now)
	}
	if msg.Key().IsRepeat || (m.dictation.hold.count >= 3 && pause <= voiceHoldRapidGap && now.Sub(m.dictation.hold.startedAt) >= voiceHoldPasteGuard) {
		// The key is being held: open the microphone now, so the recording
		// that starts at voiceHoldThreshold captures from its first instant
		// (and keeps what was said just before it).
		if !m.dictation.hold.warmed && m.dictation.warmMic != nil {
			m.dictation.hold.warmed = true
			go m.dictation.warmMic()
		}
		return m, nil, true
	}
	m.dictation.hold.typed++
	return m, nil, false
}

// activateVoiceHold starts the hold recording once bare Space has stayed down
// for voiceHoldThreshold. The spaces the run typed are pulled back out of the
// composer first, so dictation starts from the text as it was before the hold.
func (m model) activateVoiceHold(hold voiceHold, now time.Time) (model, tea.Cmd, bool) {
	state := m.currentComposerState()
	runes := []rune(state.text)
	remove := 0
	for i := state.cursor - 1; i >= 0 && remove < hold.typed; i-- {
		if runes[i] != ' ' {
			break
		}
		remove++
	}
	if remove > 0 {
		nextState, nextPreviews := deleteComposerRangeWithPastePreviews(state, m.composerPastePreviews, state.cursor-remove, state.cursor)
		m.setComposerState(nextState)
		m.composerPastePreviews = nextPreviews
	}
	// The run is over; keep lastAt and maxGap so the recording can tell a
	// slow repeat rate from the key going up. Latch spent in every path: this
	// physical hold already had its activation, and its remaining auto-repeats
	// are swallowed until the key goes up.
	m.dictation.hold.endRun()
	m.dictation.hold.lastAt = now
	m.dictation.hold.spent = true
	if m.dictation.downloading {
		return m.appendDictationNotice("downloading", "Ainda configurando o motor de ditado — um momento."), nil, true
	}
	next, vcmd := m.startDictationWithVoice(true, false)
	if !next.dictation.active() {
		return next, vcmd, true
	}
	return next, tea.Batch(vcmd, voiceHoldTickCmd(next.dictation.sessionID)), true
}

// voiceHoldTickMsg re-checks whether the held Space is still down. It carries
// the session so a tick outliving its recording ends the chain silently.
type voiceHoldTickMsg struct{ sessionID int64 }

func voiceHoldTickCmd(sessionID int64) tea.Cmd {
	return tea.Tick(voiceHoldTickInterval, func(time.Time) tea.Msg {
		return voiceHoldTickMsg{sessionID: sessionID}
	})
}

func (m model) handleVoiceHoldTick(msg voiceHoldTickMsg) (model, tea.Cmd) {
	if msg.sessionID != m.dictation.sessionID || !m.dictation.spaceHeld {
		return m, nil
	}
	if m.now().Sub(m.dictation.hold.lastAt) > m.dictation.hold.releaseTimeout() {
		// The key went up: the latch is over, then stop as usual.
		m.dictation.hold.keyUp()
		return m.handleVoiceCaptureRelease()
	}
	return m, voiceHoldTickCmd(m.dictation.sessionID)
}

// voiceHoldWarming reports a run that looks like a hold in progress: idle with
// at least three rapid presses and no sign of the key going up yet. The count
// is checked first so the clock is never read when there is no run (many
// tests build a model with a nil clock).
func (m model) voiceHoldWarming() bool {
	if m.dictation.phase != dictIdle || m.dictation.hold.count < 3 {
		return false
	}
	return m.now().Sub(m.dictation.hold.lastAt) <= m.dictation.hold.releaseTimeout()
}
