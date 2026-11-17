package tui

import (
	"testing"
)

// Repro for sm3 M1: cancel in Starting must fully reset the session twins.
// On untouched dev reset() leaves voiceStopPending/spaceHeld/regionStart/
// regionEnd/regionAnchor behind, so a release deferred in Starting leaks and
// the NEXT recording stops alone as soon as it opens.
func TestDictationCancelInStartingMustNotLeakVoiceStop(t *testing.T) {
	m := model{dictation: batchOnlyController()}
	m.dictation.sessionID = 7
	m.dictation.phase = dictStarting
	m.dictation.spaceHeld = true

	// Release during Starting defers the stop.
	next, _ := m.handleVoiceCaptureRelease()
	if !next.dictation.voiceStopPending {
		t.Fatal("precondition: release in Starting must defer the stop")
	}

	// Cancel while still Starting.
	next, _ = next.cancelDictation()
	if next.dictation.phase != dictIdle {
		t.Fatalf("cancel must return to idle, got %v", next.dictation.phase)
	}
	if next.dictation.voiceStopPending {
		t.Fatalf("BUG: cancel in Starting leaked voiceStopPending=true into session %d", next.dictation.sessionID)
	}
	if next.dictation.spaceHeld {
		t.Fatalf("BUG: cancel leaked spaceHeld=true into session %d", next.dictation.sessionID)
	}
	if next.dictation.regionStart != 0 || next.dictation.regionEnd != 0 || next.dictation.regionAnchor != "" {
		t.Fatalf("BUG: cancel leaked region twins start=%d end=%d anchor=%q",
			next.dictation.regionStart, next.dictation.regionEnd, next.dictation.regionAnchor)
	}

	// Start again: the new recording must keep recording.
	restarted, _ := next.startDictation()
	sid := restarted.dictation.sessionID
	after, _ := restarted.handleDictationStarted(dictationStartedMsg{sessionID: sid})
	if after.dictation.phase != dictRecording {
		t.Fatalf("BUG: next recording stopped alone on open: phase=%v (voiceStopPending leaked)", after.dictation.phase)
	}
}

// Same reset hole for the live region twins: a cancel must drop the region
// coordinates, not just deactivate the region.
func TestDictationCancelMustDropRegionTwins(t *testing.T) {
	m := model{dictation: batchOnlyController()}
	m.setComposerState(composerState{text: "hello world", cursor: 5})
	m.dictation.sessionID = 11
	m.dictation.phase = dictRecording
	m.dictation.streaming = true
	m.dictation.regionActive = true
	m.dictation.regionStart = 0
	m.dictation.regionEnd = 5
	m.dictation.regionAnchor = "hello"
	m.dictation.regionPrefix = " "

	next, _ := m.cancelDictation()
	if next.dictation.regionActive {
		t.Fatal("cancel must deactivate the live region")
	}
	if next.dictation.regionStart != 0 || next.dictation.regionEnd != 0 ||
		next.dictation.regionAnchor != "" || next.dictation.regionPrefix != "" {
		t.Fatalf("BUG: cancel leaked region twins start=%d end=%d anchor=%q prefix=%q",
			next.dictation.regionStart, next.dictation.regionEnd,
			next.dictation.regionAnchor, next.dictation.regionPrefix)
	}
}
