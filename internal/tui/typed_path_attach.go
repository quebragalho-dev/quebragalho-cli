package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// A terminal without bracketed paste (Windows Terminal) delivers a paste as
// ordinary keypresses, one per character, so routePaste and its
// droppedAttachmentPath probe never run: a path copied in Explorer stayed as
// text in the composer until the user pressed Enter. A burst of keys that
// leaves the whole composer equal to an attachable path is a paste, not typing,
// and attaches the file right away.
const (
	// typedPathBurstGap is the widest pause between two keys of the same run. A
	// paste arrives in a couple of milliseconds; human typing of a path is far
	// slower, and even fast typing leaves a wider gap somewhere in a long path.
	typedPathBurstGap = 100 * time.Millisecond
	// typedPathBurstMinKeys is how many characters a run needs before it may be
	// a pasted path — shorter than the shortest useful path.
	typedPathBurstMinKeys = 3
	// typedPathCheckDelay is the quiet period after the last key of the run
	// before the deferred check runs, so the path is complete (and a burst that
	// is still streaming never converts halfway).
	typedPathCheckDelay = 150 * time.Millisecond
)

// typedPathCheckMsg is the deferred check that follows a burst of typed keys.
// seq must match m.typedPathSeq or the check is stale (another key arrived and
// rescheduled it).
type typedPathCheckMsg struct {
	seq int
}

// afterComposerKey folds one key into the burst detector once it has reached
// the composer. It returns the updated model plus a command to run alongside
// the key's own: the deferred check after a burst. Non-typing keys (movement,
// editing, modified keys) end the run and never schedule a check.
func (m model) afterComposerKey(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	if !typedPathKey(msg) {
		m.typedPathBurst = 0
		m.typedPathSeq++
		m.typedPathLastAt = time.Time{}
		return m, nil, false
	}
	// m.lastKeyTime was stamped by the keypress preamble: reusing it keeps the
	// clock calls per key at one, which the paste-detection timing depends on.
	now := m.lastKeyTime
	// A pause wider than the burst gap means a new run: the previous one ended.
	if m.typedPathLastAt.IsZero() || now.Sub(m.typedPathLastAt) > typedPathBurstGap {
		m.typedPathBurst = 0
	}
	m.typedPathLastAt = now
	m.typedPathBurst++
	if m.typedPathBurst < typedPathBurstMinKeys {
		return m, nil, false
	}
	// Only a composer that is already an attachable path gets a deferred check.
	// This is what keeps an ordinary burst (a command, a sentence) from
	// scheduling anything at all — and it is the same probe routePaste applies.
	if _, ok := droppedAttachmentPath(m.composerValue(), m.cwd); !ok {
		return m, nil, false
	}
	m.typedPathSeq++
	seq := m.typedPathSeq
	return m, tea.Tick(typedPathCheckDelay, func(time.Time) tea.Msg {
		return typedPathCheckMsg{seq: seq}
	}), true
}

// typedPathKey reports whether a key types a character into the composer: a
// plain printable key or Space, with no modifier. Alt+D/B/F are composer
// commands and Ctrl+<letter> are shortcuts, so neither can be part of a paste.
func typedPathKey(msg tea.KeyMsg) bool {
	if keyAlt(msg) || keyHasMod(msg, tea.ModCtrl) || keyHasMod(msg, tea.ModSuper) {
		return false
	}
	if keyIs(msg, tea.KeySpace) {
		return true
	}
	return keyPrintable(msg)
}

// typedPathCheck converts a pasted path into an attachment. It only fires when
// the burst was real (several keys, all recent) and the whole composer is a
// path droppedAttachmentPath accepts — the same probe routePaste applies to a
// bracketed paste, so images, videos and PDFs take the same path as Ctrl+V and
// drag-and-drop. Anything else is left exactly as typed: text pasted to be read
// or edited stays text.
//
// The seq guard alone decides staleness, deliberately without a second timing
// check: this runs typedPathCheckDelay after the run's last key, so comparing
// the clock here would always look "too late". The burst window is enforced
// where the keys arrive (afterComposerKey), and any later key — typing or not —
// bumps the seq, which is what cancels a pending check.
func (m model) typedPathCheck(msg typedPathCheckMsg) (tea.Model, tea.Cmd) {
	if msg.seq != m.typedPathSeq || m.typedPathBurst < typedPathBurstMinKeys {
		return m, nil
	}
	if !m.noBlockingModal() || m.transcriptDetailed || m.subchatReadOnly() || m.picker != nil || m.pendingAskUser != nil {
		return m, nil
	}
	text := m.composerValue()
	path, ok := droppedAttachmentPath(text, m.cwd)
	if !ok {
		return m, nil
	}
	// The path is complete and attachable: clear the typed text first, so the
	// attachment chip replaces it instead of sitting beside a stray path.
	m.typedPathBurst = 0
	m.typedPathSeq++
	m.clearComposer()
	m.clearSuggestions()
	return m.handleImageCommand(path)
}
