package tui

import tea "charm.land/bubbletea/v2"

// hoverKind discriminates which clickable surface a hoverTarget refers to.
type hoverKind int

const (
	hoverNone hoverKind = iota
	// hoverTranscript: a specialist card or collapse/expand toggle header in the
	// main transcript body (or the subchat child view), identified by bodyY.
	hoverTranscript
)

// hoverTarget identifies the single clickable row (if any) currently under the
// mouse cursor with no button pressed.
type hoverTarget struct {
	kind  hoverKind
	bodyY int // hoverTranscript
}

// mouseHover reports whether msg is a plain cursor-movement event with NO button
// pressed — a hover, not a drag. Requires AllMotion mouse reporting (see View's
// wantsMouseCapture branch); CellMotion only reports motion while a button is held,
// so this predicate would never match under the old mouse mode.
func mouseHover(msg tea.MouseMsg) bool {
	if _, ok := msg.(tea.MouseMotionMsg); !ok {
		return false
	}
	return mouseEvent(msg).Button == tea.MouseNone
}

// updateHoverTarget resolves visible transcript targets for a hover motion.
// Context data lives in the run-details overlay; the former sidebar rail is not
// rendered and therefore cannot own a hover target.
func (m model) updateHoverTarget(msg tea.MouseMsg) model {
	if line, ok := m.transcriptLineAtMouse(msg); ok {
		// A permission option reuses its OWN existing keyboard-cursor highlight
		// (see hoverPermissionOption) rather than the m.hover mechanism, so there's
		// nothing further to set here.
		if line.permOption {
			return m.hoverPermissionOption(line.permChoice).withHover(hoverTarget{})
		}
		// Only a card or a collapse/expand toggle header is "clickable" here; plain
		// selectable text (e.g. a user/assistant message) isn't, so hovering over
		// it must not light up as if it were.
		if line.specialistCard || line.toggle {
			return m.withHover(hoverTarget{kind: hoverTranscript, bodyY: line.bodyY})
		}
	}
	return m.withHover(hoverTarget{})
}

// hoverPermissionOption moves the pending permission prompt's cursor to the
// hovered option, reusing its EXISTING keyboard-navigation highlight (the same
// one Tab/Shift+Tab/arrow keys move) instead of new styling — the popup already
// renders whichever option .cursor points at differently. A no-op when no
// permission prompt is pending or the choice isn't found (defensive; every
// permOption line's choice always comes from permissionOptions(request) in the
// first place).
func (m model) hoverPermissionOption(choice permissionDecision) model {
	if m.pendingPermission == nil {
		return m
	}
	for index, option := range permissionOptions(m.pendingPermission.request) {
		if option.choice == choice {
			m.pendingPermission.cursor = index
			return m
		}
	}
	return m
}

func (m model) withHover(t hoverTarget) model {
	m.hover = t
	return m
}

// clearHover drops any hover highlight. Called wherever the underlying content a
// hover target refers to may no longer mean the same thing on the next render — a
// wheel-scroll (the cursor's mapping to a bodyY is only recomputed on the NEXT
// motion event; without clearing, a stale bodyY could coincidentally match a
// DIFFERENT row after the viewport shifts, highlighting the wrong thing) or a
// subchat toggle (bodyY numbering is entirely different between the parent
// transcript and a child session).
func (m model) clearHover() model {
	if m.hover.kind == hoverNone {
		return m
	}
	m.hover = hoverTarget{}
	return m
}
