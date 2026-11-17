package tui

// Repro for the SM7 streamLive leak: FAILS on the untouched dev.
//
// streamLive marks "a model call currently emitting output whose usage has
// not arrived yet" (model.go). It is set by every streamed delta via
// countStreamEstimate but neither the normal end-of-turn path
// (agentResponseMsg) nor cancelRun mutes it, so after the turn settles the
// flag stays true while no run is active: streamLive==true && !pending.
// Production path, no hand-built state: begin -> text -> end (or cancel).
//
// The leak is invisible today only because the single reader gates on the
// turn as well (statusline.go: `m.pending && m.streamLive`), but the state
// is impossible by construction and the next reader that trusts streamLive
// alone would show a live speed estimate while idle. The StreamPhase
// machine fixes it by muting the estimate twins on end/cancel/begin in the
// same place that zeroes every other stream twin — zero visible change.

import (
	"context"
	"testing"
)

func TestStreamLiveLeakAfterEnd(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	m = m.beginRun(nil)
	rid := m.activeRunID

	updated, _ := m.Update(agentTextMsg{runID: rid, delta: "hello"})
	m = updated.(model)
	if !m.streamLive {
		t.Fatal("setup: streamed text must mark the call live")
	}

	updated, _ = m.Update(agentResponseMsg{runID: rid})
	m = updated.(model)
	if m.pending || m.activeRunID != 0 {
		t.Fatalf("setup: turn should be settled, pending=%v activeRunID=%v", m.pending, m.activeRunID)
	}
	if m.streamLive {
		t.Fatalf("streamLive leaked past end of turn (pending=%v activeRunID=%v)", m.pending, m.activeRunID)
	}
	if m.streamEstimateChars != 0 || !m.streamEstimateStart.IsZero() {
		t.Fatalf("estimate twins leaked past end of turn: chars=%d start=%v",
			m.streamEstimateChars, m.streamEstimateStart)
	}
}

func TestStreamLiveLeakAfterCancel(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	m = m.beginRun(nil)
	rid := m.activeRunID

	updated, _ := m.Update(agentTextMsg{runID: rid, delta: "hello"})
	m = updated.(model)
	if !m.streamLive {
		t.Fatal("setup: streamed text must mark the call live")
	}

	m.cancelRun()
	if m.streamLive {
		t.Fatalf("streamLive leaked past cancel (pending=%v activeRunID=%v)", m.pending, m.activeRunID)
	}
	if m.streamEstimateChars != 0 || !m.streamEstimateStart.IsZero() {
		t.Fatalf("estimate twins leaked past cancel: chars=%d start=%v",
			m.streamEstimateChars, m.streamEstimateStart)
	}
}
