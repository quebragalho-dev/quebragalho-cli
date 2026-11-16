package tui

import (
	"strings"
	"testing"
)

// Live dictation renders dimmed until the transcript is final, then it
// becomes ordinary composer text.

// The live region is drawn dimmed while the untouched prefix keeps the
// normal ink colour.
func TestDimLiveRegionIsDimmed(t *testing.T) {
	acceptCacheTrueColor(t)
	m := newModel(t.Context(), Options{})
	m.setComposerState(composerState{text: "note:", cursor: 5})
	m.dictation.phase = dictRecording
	m.dictation.streaming = true
	m = m.handleDictationPartial(sttPartialMsg{sessionID: m.dictation.sessionID, text: "the quick"})

	dimStyle := qgTheme.faint.Inline(true)
	inkStyle := qgTheme.ink.Inline(true)
	line := m.composerLine(80)
	if !strings.Contains(line, dimStyle.Render(" the quick")) {
		t.Fatalf("live region should be dimmed, got %q", line)
	}
	if !strings.Contains(line, inkStyle.Render("note:")) {
		t.Fatalf("untouched prefix should stay ink, got %q", line)
	}
	if strings.Contains(line, inkStyle.Render("the quick")) {
		t.Fatalf("live region must not render as ink, got %q", line)
	}
}

// A newer partial moves the dim range to the refined transcript.
func TestDimRangeFollowsNewerPartial(t *testing.T) {
	acceptCacheTrueColor(t)
	m := newModel(t.Context(), Options{})
	m.setComposerState(composerState{text: "note:", cursor: 5})
	m.dictation.phase = dictRecording
	m.dictation.streaming = true
	m = m.handleDictationPartial(sttPartialMsg{sessionID: m.dictation.sessionID, text: "the quick"})
	m = m.handleDictationPartial(sttPartialMsg{sessionID: m.dictation.sessionID, text: "the quick brown fox"})

	dimStyle := qgTheme.faint.Inline(true)
	line := m.composerLine(80)
	if !strings.Contains(line, dimStyle.Render(" the quick brown fox")) {
		t.Fatalf("refined transcript should be dimmed, got %q", line)
	}
}

// The final transcript returns to the normal colour and stays editable.
func TestDimFinalTranscriptReturnsToInk(t *testing.T) {
	acceptCacheTrueColor(t)
	m := newModel(t.Context(), Options{})
	m.setComposerState(composerState{text: "note:", cursor: 5})
	m.dictation.phase = dictRecording
	m.dictation.streaming = true
	m = m.handleDictationPartial(sttPartialMsg{sessionID: m.dictation.sessionID, text: "the quick"})
	m = m.handleDictationPartial(sttPartialMsg{sessionID: m.dictation.sessionID, text: "the quick brown fox"})
	updated, _ := m.Update(dictationTranscribedMsg{sessionID: m.dictation.sessionID, text: "the quick brown fox", streaming: true})
	m = updated.(model)

	if got := m.currentComposerState().text; got != "note: the quick brown fox" {
		t.Fatalf("final composer text = %q, want %q", got, "note: the quick brown fox")
	}
	dimStyle := qgTheme.faint.Inline(true)
	inkStyle := qgTheme.ink.Inline(true)
	line := m.composerLine(80)
	if strings.Contains(line, dimStyle.Render("the quick brown fox")) {
		t.Fatalf("final transcript must not stay dimmed, got %q", line)
	}
	if !strings.Contains(line, inkStyle.Render("note: the quick brown fox")) {
		t.Fatalf("final text should render as ink, got %q", line)
	}
}

// Cancelling drops the live region entirely.
func TestDimCancelRemovesLiveRegion(t *testing.T) {
	acceptCacheTrueColor(t)
	m := newModel(t.Context(), Options{})
	m.setComposerState(composerState{text: "note:", cursor: 5})
	m.dictation.phase = dictRecording
	m.dictation.streaming = true
	m = m.handleDictationPartial(sttPartialMsg{sessionID: m.dictation.sessionID, text: "the quick"})
	m, _ = m.cancelDictation()

	if got := m.currentComposerState().text; got != "note:" {
		t.Fatalf("composer text after cancel = %q, want %q", got, "note:")
	}
	dimStyle := qgTheme.faint.Inline(true)
	if strings.Contains(m.composerLine(80), dimStyle.Render("the quick")) {
		t.Fatalf("cancelled transcript must not stay dimmed: %q", m.composerLine(80))
	}
}

// Selection keeps precedence over the live dim range.
func TestDimSelectionWinsOverDim(t *testing.T) {
	acceptCacheTrueColor(t)
	m := newModel(t.Context(), Options{})
	state := composerState{text: "note: the quick", cursor: len([]rune("note: the quick"))}
	dim := composerDimRange{active: true, start: 5, end: 15}
	selection := composerSelectionState{active: true, anchor: 5, cursor: 9}

	out := renderComposerInput(m.input, state, 80, false, selection, nil, dim)
	selStyle := qgTheme.selection
	dimStyle := qgTheme.faint.Inline(true)
	if !strings.Contains(out, selStyle.Render(" the")) {
		t.Fatalf("selected cells should use the selection style, got %q", out)
	}
	if !strings.Contains(out, dimStyle.Render(" quick")) {
		t.Fatalf("unselected region cells should stay dimmed, got %q", out)
	}
}

// A skill token inside the live region is dimmed, not accented.
func TestDimSkillTokenInsideRegionIsDimmed(t *testing.T) {
	acceptCacheTrueColor(t)
	m := newModel(t.Context(), Options{})
	text := "/reviewer hi"
	state := composerState{text: text, cursor: len([]rune(text))}
	dim := composerDimRange{active: true, start: 0, end: len([]rune(text))}
	spans := []skillTokenSpan{{name: "reviewer", start: 0, end: 9}}

	out := renderComposerInput(m.input, state, 80, false, composerSelectionState{}, spans, dim)
	dimStyle := qgTheme.faint.Inline(true)
	accentStyle := qgTheme.accent.Inline(true)
	if !strings.Contains(out, dimStyle.Render("/reviewer hi")) {
		t.Fatalf("skill token inside the live region should be dimmed, got %q", out)
	}
	if strings.Contains(out, accentStyle.Render("/reviewer")) {
		t.Fatalf("skill token inside the live region must not use the accent style, got %q", out)
	}
}

// A paste preview before the region shifts the dim range into display
// coordinates: the label stays ink and the transcript stays dimmed.
func TestDimPastePreviewBeforeRegion(t *testing.T) {
	acceptCacheTrueColor(t)
	m := newModel(t.Context(), Options{})
	m.setComposerState(composerState{text: "XXXXXnote:", cursor: 10})
	m.composerPastePreviews = []composerPastePreview{{active: true, start: 0, end: 5, label: "[PREVIEW]"}}
	m.dictation.phase = dictRecording
	m.dictation.streaming = true
	m = m.handleDictationPartial(sttPartialMsg{sessionID: m.dictation.sessionID, text: "the quick"})

	dimStyle := qgTheme.faint.Inline(true)
	inkStyle := qgTheme.ink.Inline(true)
	line := m.composerLine(80)
	if !strings.Contains(line, dimStyle.Render(" the quick")) {
		t.Fatalf("transcript after a preview should be dimmed, got %q", line)
	}
	if strings.Contains(line, dimStyle.Render("PREVIEW")) {
		t.Fatalf("preview label must not be dimmed, got %q", line)
	}
	if strings.Contains(line, inkStyle.Render("the quick")) || strings.Contains(line, inkStyle.Render(" the quick")) {
		t.Fatalf("no transcript cell may stay ink, got %q", line)
	}
}

// An inactive range changes nothing, even with non-zero bounds.
func TestDimInactiveRangeChangesNothing(t *testing.T) {
	acceptCacheTrueColor(t)
	m := newModel(t.Context(), Options{})
	state := composerState{text: "note: the quick", cursor: len([]rune("note: the quick"))}

	first := renderComposerInput(m.input, state, 80, false, composerSelectionState{}, nil, composerDimRange{})
	second := renderComposerInput(m.input, state, 80, false, composerSelectionState{}, nil, composerDimRange{start: 2, end: 5})
	if first != second {
		t.Fatalf("inactive range must not change rendering:\n%q\n%q", first, second)
	}
}

// A stale region (inactive but with non-zero bounds) must not render dimmed:
// composerLine respects regionActive.
func TestDimStaleInactiveRegionShowsNoDim(t *testing.T) {
	acceptCacheTrueColor(t)
	m := newModel(t.Context(), Options{})
	m.setComposerState(composerState{text: "note: hi", cursor: 8})
	m.dictation.regionActive = false
	m.dictation.regionStart = 5
	m.dictation.regionEnd = 8

	dimStyle := qgTheme.faint.Inline(true)
	inkStyle := qgTheme.ink.Inline(true)
	line := m.composerLine(80)
	if strings.Contains(line, dimStyle.Render(" hi")) {
		t.Fatalf("stale inactive region must not render dimmed, got %q", line)
	}
	if !strings.Contains(line, inkStyle.Render("note: hi")) {
		t.Fatalf("stale inactive region should render as ink, got %q", line)
	}
}
