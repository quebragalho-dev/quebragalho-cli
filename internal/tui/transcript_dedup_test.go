package tui

import "testing"

// appendTranscriptRowsDedup must produce byte-identical output to repeated
// appendTranscriptRow (the O(n²) form it replaces), including keyed-row dedup and
// always-append for unkeyed rows.
func TestAppendTranscriptRowsDedupMatchesPerRow(t *testing.T) {
	newRows := []transcriptRow{
		{kind: rowToolCall, runID: 1, id: "a"},
		{kind: rowSystem, text: "note"},
		{kind: rowToolCall, runID: 1, id: "a"}, // duplicate keyed row -> skipped
		{kind: rowToolResult, runID: 1, id: "b"},
		{kind: rowSystem, text: "note"},        // unkeyed duplicate -> still appended
		{kind: rowToolCall, runID: 2, id: "a"}, // same id, different run -> distinct key
	}
	base := initialTranscript()

	want := append([]transcriptRow{}, base...)
	for _, r := range newRows {
		want = appendTranscriptRow(want, r)
	}
	got := appendTranscriptRowsDedup(append([]transcriptRow{}, base...), newRows)

	if len(got) != len(want) {
		t.Fatalf("length mismatch: bulk=%d per-row=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].kind != want[i].kind || got[i].id != want[i].id || got[i].runID != want[i].runID || got[i].text != want[i].text {
			t.Fatalf("row %d differs:\n bulk=%+v\n per =%+v", i, got[i], want[i])
		}
	}
}

// An interleaved operator message reaches the transcript twice for one run
// (once live via the sink, once in the end-of-turn rows): both rows share a
// single run-scoped dedup identity, so exactly one copy stays. The identity
// is run-scoped on purpose: the same text submitted in two different turns
// stays visible twice, as do two distinct interleaves inside one run.
func TestInterleaveRowDedupsByRunAndSequence(t *testing.T) {
	rows := initialTranscript()
	live := transcriptRow{kind: rowUser, runID: 7, id: "interleave-7-1", text: "operator interleaves now"}
	rows = appendTranscriptRow(rows, live)
	rows = appendTranscriptRow(rows, live) // end-of-turn row carries the same identity
	count := 0
	for _, row := range rows {
		if row.kind == rowUser && row.text == "operator interleaves now" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("interleaved row appears %d times, want exactly 1 (live + end-of-turn must dedupe)", count)
	}

	// Same text, next turn: a different run means a different identity.
	next := transcriptRow{kind: rowUser, runID: 8, id: "interleave-8-1", text: "operator interleaves now"}
	rows = appendTranscriptRow(rows, next)
	count = 0
	for _, row := range rows {
		if row.kind == rowUser && row.text == "operator interleaves now" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("same text in two turns appears %d times, want 2 (run scope must not merge turns)", count)
	}

	// Same run, second interleave: a different sequence stays visible too.
	other := transcriptRow{kind: rowUser, runID: 8, id: "interleave-8-2", text: "and another thing"}
	rows = appendTranscriptRow(rows, other)
	rows = appendTranscriptRow(rows, other)
	found := 0
	for _, row := range rows {
		if row.kind == rowUser && row.text == "and another thing" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("second interleave of the same run appears %d times, want exactly 1", found)
	}
}
