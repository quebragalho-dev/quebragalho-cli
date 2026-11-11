package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The queue notice must sit BELOW the last pending row of the whole
// Sussurro block — interleave, parked batch and FIFO — carrying the total
// count of visible pending messages. Before the fix the interleave-only
// case rendered no notice at all, and a mixed block showed a notice below
// the FIFO rows with a count that ignored the interleaves.

func TestInterleaveOnlyShowsNotice_N1(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.interleave.push("MARKINTER one")

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "na fila (1) — Ctrl+X Ctrl+S envia agora · ↑ edita a última") {
		t.Fatalf("expected the single notice below the pending interleave, got:\n%s", view)
	}
	if count := strings.Count(view, "na fila ("); count != 1 {
		t.Fatalf("expected exactly one occurrence of the notice shape, got %d", count)
	}
}

func TestInterleaveOnlyShowsNotice_N2(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.interleave.push("MARKINTER one")
	m.interleave.push("MARKINTER two")

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "na fila (2) — Ctrl+X Ctrl+S envia agora · ↑ edita a última") {
		t.Fatalf("expected the notice to count both interleaves, got:\n%s", view)
	}
	if count := strings.Count(view, "na fila ("); count != 1 {
		t.Fatalf("expected exactly one occurrence of the notice shape, got %d", count)
	}
}

func TestMixedInterleaveAndFIFOSingleNoticeBelowBothBlocks(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.interleave.push("MARKINTER one")
	m.queuedMessages = []string{"MARKONE queued"}

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "na fila (2) — Ctrl+X Ctrl+S envia agora · ↑ edita a última") {
		t.Fatalf("expected one notice counting interleave + FIFO, got:\n%s", view)
	}
	idxInterleave := strings.Index(view, "MARKINTER one")
	idxQueued := strings.Index(view, "MARKONE queued")
	idxNotice := strings.Index(view, "na fila (2)")
	if idxInterleave == -1 || idxQueued == -1 || idxNotice == -1 {
		t.Fatalf("all three rows must render, got:\n%s", view)
	}
	if !(idxInterleave < idxQueued && idxQueued < idxNotice) {
		t.Fatalf("notice must sit below the FIFO rows (which sit below the interleave rows): interleave=%d fifo=%d notice=%d\n%s", idxInterleave, idxQueued, idxNotice, view)
	}
}

func TestParkedBatchCountsInTheSingleNotice(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.pendingSendNow = []string{"MARKPARKED one", "MARKPARKED two"}
	m.queuedMessages = []string{"/tools"}

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "na fila (3) — Ctrl+X Ctrl+S envia agora · ↑ edita a última") {
		t.Fatalf("expected the notice to count parked batch + FIFO (3), got:\n%s", view)
	}
	if count := strings.Count(view, "na fila ("); count != 1 {
		t.Fatalf("expected exactly one occurrence of the notice shape, got %d", count)
	}
}

func TestNoticeHintKitty(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.keyboardDisambiguation = true
	m.queuedMessages = []string{"MARKONE queued"}

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "na fila (1) — Ctrl+Enter envia agora · ↑ edita a última") {
		t.Fatalf("expected the kitty hint to name Ctrl+Enter, got:\n%s", view)
	}
}

func TestNoticeHintLegacy(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.queuedMessages = []string{"MARKONE queued"}

	view := plainRender(t, viewString(m.View()))
	if !strings.Contains(view, "na fila (1) — Ctrl+X Ctrl+S envia agora · ↑ edita a última") {
		t.Fatalf("expected the legacy hint to name Ctrl+X Ctrl+S, got:\n%s", view)
	}
}

func TestUpArrowMixedPopsTheLastDrawnFirst(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.interleave = &interleaveBuffer{}
	m.interleave.push("INTERLEAVE A")
	m.interleave.push("INTERLEAVE B")
	m.queuedMessages = []string{"FIFO tail"}
	// Note: the second ↑ happens while the composer is single-line; a
	// multi-line composer consumes ↑ for cursor movement first, so deeper
	// multi-pop chains are only reachable with the caret at the top line.

	updated, _ := m.Update(testKey(tea.KeyUp))
	m = updated.(model)
	if got := m.composerValue(); got != "FIFO tail" {
		t.Fatalf("expected ↑ to pop the FIFO tail (last drawn), got %q", got)
	}
	if got := len(m.queuedMessages); got != 0 {
		t.Fatalf("expected the FIFO emptied, queue = %v", m.queuedMessages)
	}

	updated, _ = m.Update(testKey(tea.KeyUp))
	m = updated.(model)
	if got := m.composerValue(); got != "INTERLEAVE B\nFIFO tail" {
		t.Fatalf("expected the newest interleave next, composer = %q", got)
	}
	if got := m.interleave.snapshot(); len(got) != 1 || got[0] != "INTERLEAVE A" {
		t.Fatalf("expected only INTERLEAVE A left, got %v", got)
	}
}

func TestUpArrowInterleaveOnlyPopsLast(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.interleave = &interleaveBuffer{}
	m.interleave.push("INTERLEAVE A")
	m.interleave.push("INTERLEAVE B")

	updated, _ := m.Update(testKey(tea.KeyUp))
	m = updated.(model)
	if got := m.composerValue(); got != "INTERLEAVE B" {
		t.Fatalf("expected ↑ to pop the last interleave, got %q", got)
	}

	updated, _ = m.Update(testKey(tea.KeyUp))
	m = updated.(model)
	if got := m.composerValue(); got != "INTERLEAVE A\nINTERLEAVE B" {
		t.Fatalf("expected the older interleave above it, composer = %q", got)
	}
	if m.interleave != nil && len(m.interleave.snapshot()) != 0 {
		t.Fatalf("expected the interleave buffer drained, got %v", m.interleave.snapshot())
	}
}

func TestUpArrowFIFOOnlyPopsTail(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.queuedMessages = []string{"FIRST queued", "LAST queued"}

	updated, _ := m.Update(testKey(tea.KeyUp))
	m = updated.(model)
	if got := m.composerValue(); got != "LAST queued" {
		t.Fatalf("expected ↑ to pop the FIFO tail, got %q", got)
	}
	if got := m.queuedMessages; len(got) != 1 || got[0] != "FIRST queued" {
		t.Fatalf("expected the head to remain, queue = %v", got)
	}
}

func TestUpArrowParkedThenFIFO(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.width = 96
	m.pendingSendNow = []string{"PARKED"}
	m.queuedMessages = []string{"FIFO tail"}

	updated, _ := m.Update(testKey(tea.KeyUp))
	m = updated.(model)
	if got := m.composerValue(); got != "FIFO tail" {
		t.Fatalf("expected ↑ to pop the drawn-last FIFO tail first, got %q", got)
	}
	if got := m.pendingSendNow; len(got) != 1 || got[0] != "PARKED" {
		t.Fatalf("the parked batch must not be popped while the FIFO is pending, got %v", got)
	}

	updated, _ = m.Update(testKey(tea.KeyUp))
	m = updated.(model)
	if got := m.composerValue(); got != "PARKED\nFIFO tail" {
		t.Fatalf("expected the parked batch popped next, composer = %q", got)
	}
	if len(m.pendingSendNow) != 0 {
		t.Fatalf("expected the parked batch emptied, got %v", m.pendingSendNow)
	}
}
