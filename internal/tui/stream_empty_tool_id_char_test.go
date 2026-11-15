package tui

// Characterization of tool-call streaming WITHOUT a call ID (providers that
// omit the id at the start of the stream). Passes on the untouched dev and
// must keep passing after the StreamPhase machine: the old runID+ID guard
// (`msg.id != m.streamCallID`) passes for id "" once the start created the
// decoder, so the fragment is fed, the live estimate counts it and the
// activity clock is stamped — while the block view stays empty (it requires
// ID != ""). Self-contained on purpose: it also runs on the base commit,
// which predates the machine helpers.

import (
	"context"
	"testing"
	"time"
)

func TestStreamChar_EmptyToolIDStillFeedsDecoder(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	m = m.beginRun(nil)
	rid := m.activeRunID
	t0 := time.Unix(1_700_000_000, 0)
	withFrozenClock(&m, t0)

	fragment := `{"path":"a.go","content":"line1\nline2`
	updated, _ := m.Update(toolCallStreamStartMsg{runID: rid, id: "", name: "write_file"})
	m = updated.(model)
	updated, _ = m.Update(toolCallStreamDeltaMsg{runID: rid, id: "", fragment: fragment})
	m = updated.(model)

	// The decoder is fed despite the empty ID: the start created it and the
	// delta guard passes.
	if m.streamCallDecoder == nil {
		t.Fatal("empty-ID start must still create the decoder")
	}
	if m.streamCallDecoder.lineCount != 1 || string(m.streamCallDecoder.cur) != "line2" {
		t.Fatalf("decoder tail = %d lines cur=%q, want 1 line cur=%q",
			m.streamCallDecoder.lineCount, m.streamCallDecoder.cur, "line2")
	}
	// The live estimate counts the fragment and the activity clock advances:
	// this is what keeps the "still generating…" watchdog quiet during a
	// long id-less write.
	if m.streamEstimateChars != len([]rune(fragment)) {
		t.Fatalf("estimate chars = %d, want %d", m.streamEstimateChars, len([]rune(fragment)))
	}
	if !m.streamLive {
		t.Fatal("fragment must mark the call live")
	}
	if !m.lastStreamActivity.Equal(t0) {
		t.Fatalf("activity = %v, want frozen %v", m.lastStreamActivity, t0)
	}
	// The block view still requires a non-empty ID, so nothing renders.
	if out := m.streamingToolCallView(80); out != "" {
		t.Fatalf("empty-ID tool must render no block, got %q", out)
	}
}
