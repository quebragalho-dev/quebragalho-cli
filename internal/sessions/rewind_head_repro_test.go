package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Rewind/truncate repro tests for the concrete checkpoint defects (commit A):
// M5 (checkpoint + rewind). They run against the untouched dev code and the
// defect tests MUST FAIL here (see report); the characterization tests MUST
// PASS before and after the fix without edits.

// Defect (a): truncateEventsLocked does not recalculate LastEventType after
// cutting the log. After truncating back to the first event, metadata must
// describe the kept head, not the truncated tail.
func TestTruncateRecalculatesLastEventType(t *testing.T) {
	store, _ := newCkStore(t)
	first, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"i": 0}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent("s", AppendEventInput{Type: EventSessionCheckpoint, Payload: CheckpointPayload{Tool: "write_file"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent("s", AppendEventInput{Type: EventToolResult, Payload: map[string]any{"ok": true}}); err != nil {
		t.Fatal(err)
	}
	if err := store.TruncateEvents("s", first.Sequence); err != nil {
		t.Fatal(err)
	}
	meta, err := store.readMetadata("s")
	if err != nil {
		t.Fatal(err)
	}
	if meta.EventCount != 1 {
		t.Fatalf("EventCount = %d, want 1 (single kept event)", meta.EventCount)
	}
	if meta.LastEventType != EventMessage {
		t.Fatalf("LastEventType = %q, want %q (type of the kept head, not the truncated tail)", meta.LastEventType, EventMessage)
	}
}

// Defect (b): EventCount becomes a count, not the last kept Sequence.
// Reachability of a gapped log via a real production path: a crash (kill -9)
// between the truncate's events-file rewrite and its metadata rewrite leaves
// metadata.EventCount STALE-HIGH; the next append then derives its sequence
// from max(metadata+1, logTail+1) and leaves a benign gap. This is the exact
// scenario blessed by TestAppendWithStaleHighMetadataLeavesGapNotDuplicate
// ("e.g. an interrupted truncate").
func TestTruncateEventCountIsLastSequenceWithGap(t *testing.T) {
	store, _ := newCkStore(t)
	if _, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"i": 0}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"i": 1}}); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash: interrupted truncate left metadata high while the
	// log only has 1,2.
	meta, err := store.readMetadata("s")
	if err != nil {
		t.Fatal(err)
	}
	meta.EventCount = 5
	if err := store.writeMetadata(meta); err != nil {
		t.Fatal(err)
	}
	gapped, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"i": 2}})
	if err != nil {
		t.Fatal(err)
	}
	if gapped.Sequence != 6 {
		t.Fatalf("precondition: expected log-derived sequence 6 (gap 3-5), got %d", gapped.Sequence)
	}
	// Keep everything through the gapped head: 3 events kept, last Sequence 6.
	if err := store.TruncateEvents("s", gapped.Sequence); err != nil {
		t.Fatal(err)
	}
	after, err := store.readMetadata("s")
	if err != nil {
		t.Fatal(err)
	}
	if after.EventCount != gapped.Sequence {
		t.Fatalf("EventCount = %d, want last kept Sequence %d (count %d diverges on a gapped log)", after.EventCount, gapped.Sequence, 3)
	}
	if after.LastEventType != EventMessage {
		t.Fatalf("LastEventType = %q, want %q", after.LastEventType, EventMessage)
	}
}

// Defect (d): a CheckpointFile with no Blob/Absent/Skipped matches no restore
// branch and is a silent success (neither restored nor reported).
func TestRestoreReportsInvalidCheckpointFile(t *testing.T) {
	store, ws := newCkStore(t)
	target, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ws, "invalid.txt")
	mustWriteFile(t, path, "current")
	if _, err := store.AppendEvent("s", AppendEventInput{
		Type:    EventSessionCheckpoint,
		Payload: CheckpointPayload{Tool: "write_file", Files: []CheckpointFile{{Path: "invalid.txt"}}},
	}); err != nil {
		t.Fatal(err)
	}
	report, err := store.RestoreToSequence("s", ws, target.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, skipped := range report.Skipped {
		if skipped == "invalid.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("invalid CheckpointFile (no blob/absent/skipped) must be REPORTED, got %+v", report)
	}
	if report.FilesRestored != 0 || report.FilesDeleted != 0 {
		t.Fatalf("invalid entry must never restore nor delete, got %+v", report)
	}
	if got, _ := os.ReadFile(path); string(got) != "current" {
		t.Fatalf("invalid entry must leave the file untouched, got %q", got)
	}
}

// KNOWN UNDESIRED behavior, pending product decision (NOT a regression
// test for a fix): the rewind marker can receive the ID/Sequence of a
// truncated event. IDs are deterministic sess:seq (store.go
// appendPreparedEventsLocked), so after ApplyRewind to N the next append
// reuses sess:N+1. When the new event shares the old one's Type, the
// production matching rule (ID + Sequence + Type, cf. tui
// sessionEventsForRefs) resolves the STALE ref to the NEW event: same keys,
// different payload. Do NOT "fix" by changing the numbering or adding a
// field without a product decision; the options are described in the
// sm3-trilho report. This test pins the collision so any future change to
// the numbering is a deliberate, reviewed diff.
func TestRewindKnownSequenceIDReuse(t *testing.T) {
	store, _ := newCkStore(t)
	if _, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"v": "one"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"v": "two"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"v": "three"}}); err != nil {
		t.Fatal(err)
	}
	oldFourth, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"v": "old-four"}})
	if err != nil {
		t.Fatal(err)
	}
	oldRef := struct {
		ID       string
		Sequence int
		Type     EventType
	}{ID: oldFourth.ID, Sequence: oldFourth.Sequence, Type: oldFourth.Type}

	ws := t.TempDir()
	if _, err := store.ApplyRewind("s", ws, 2); err != nil {
		t.Fatal(err)
	}
	// The rewind marker itself occupies Sequence 3 (ID s:3) with a different
	// Type, so the first post-rewind message lands on Sequence 4 (ID s:4) —
	// the exact ID/Sequence of the truncated fourth event.
	newFourth, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"v": "new-four"}})
	if err != nil {
		t.Fatal(err)
	}
	if newFourth.ID != oldRef.ID || newFourth.Sequence != oldRef.Sequence || newFourth.Type != oldRef.Type {
		t.Fatalf("precondition: expected ID/Sequence/Type reuse, old=%+v new=%+v", oldRef, newFourth)
	}
	// Production matching rule: resolve by ID, then require Sequence+Type.
	events, err := store.ReadEvents("s")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Event{}
	for _, ev := range events {
		byID[ev.ID] = ev
	}
	matched, ok := byID[oldRef.ID]
	if !ok || matched.Sequence != oldRef.Sequence || matched.Type != oldRef.Type {
		t.Fatalf("production rule does not resolve the stale ref, matched=%+v ok=%v", matched, ok)
	}
	var payload map[string]any
	if err := json.Unmarshal(matched.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["v"] != "new-four" {
		t.Fatalf("stale ref resolved to payload %v, want the NEW event (collision proven)", payload)
	}
	if strings.Contains(oldRef.ID, ":") == false {
		t.Fatalf("unexpected ID shape %q", oldRef.ID)
	}
}

// Characterization (valid checkpoints): rewind must never delete/restore a
// file differently than today for valid entries. Passes before and after the
// fix WITHOUT edits: blob restores, absent deletes, skipped leaves alone,
// outside-workspace is confined, mode 0 preserves the existing mode.
func TestRestoreValidVariantsCharacterization(t *testing.T) {
	cases := []struct {
		name        string
		setup       func(t *testing.T, store *Store, ws string, target Event)
		path        string
		wantContent string // "" means file must not exist
		wantReport  func(t *testing.T, r RestoreReport)
	}{
		{
			name: "blob restores content",
			setup: func(t *testing.T, store *Store, ws string, target Event) {
				mustWriteFile(t, filepath.Join(ws, "a.txt"), "original")
				mustCapture(t, store, ws, "write_file", "a.txt")
				mustWriteFile(t, filepath.Join(ws, "a.txt"), "edited")
			},
			path:        "a.txt",
			wantContent: "original",
			wantReport: func(t *testing.T, r RestoreReport) {
				if r.FilesRestored != 1 {
					t.Fatalf("FilesRestored = %d, want 1", r.FilesRestored)
				}
			},
		},
		{
			name: "absent deletes file",
			setup: func(t *testing.T, store *Store, ws string, target Event) {
				mustCapture(t, store, ws, "write_file", "gone.txt")
				mustWriteFile(t, filepath.Join(ws, "gone.txt"), "created")
			},
			path:        "gone.txt",
			wantContent: "",
			wantReport: func(t *testing.T, r RestoreReport) {
				if r.FilesDeleted != 1 {
					t.Fatalf("FilesDeleted = %d, want 1", r.FilesDeleted)
				}
			},
		},
		{
			name: "skipped leaves file alone",
			setup: func(t *testing.T, store *Store, ws string, target Event) {
				mustWriteFile(t, filepath.Join(ws, "big.txt"), "current")
				if _, err := store.AppendEvent("s", AppendEventInput{
					Type:    EventSessionCheckpoint,
					Payload: CheckpointPayload{Tool: "write_file", Files: []CheckpointFile{{Path: "big.txt", Skipped: true}}},
				}); err != nil {
					t.Fatal(err)
				}
			},
			path:        "big.txt",
			wantContent: "current",
			wantReport: func(t *testing.T, r RestoreReport) {
				if r.FilesRestored != 0 || len(r.Skipped) != 1 {
					t.Fatalf("skipped must not restore and must be reported, got %+v", r)
				}
			},
		},
		{
			name: "outside workspace confined",
			setup: func(t *testing.T, store *Store, ws string, target Event) {
				mustWriteFile(t, filepath.Join(ws, "inner.txt"), "current")
				if _, err := store.AppendEvent("s", AppendEventInput{
					Type:    EventSessionCheckpoint,
					Payload: CheckpointPayload{Tool: "write_file", Files: []CheckpointFile{{Path: "../escape.txt", Skipped: true}}},
				}); err != nil {
					t.Fatal(err)
				}
			},
			path:        "inner.txt",
			wantContent: "current",
			wantReport: func(t *testing.T, r RestoreReport) {
				if r.FilesRestored != 0 {
					t.Fatalf("FilesRestored = %d, want 0", r.FilesRestored)
				}
			},
		},
		{
			name: "mode 0 preserves existing mode",
			setup: func(t *testing.T, store *Store, ws string, target Event) {
				p := filepath.Join(ws, "run.sh")
				mustWriteFile(t, p, "original")
				if err := os.Chmod(p, 0o640); err != nil {
					t.Fatal(err)
				}
				hash, err := store.writeBlob("s", []byte("original"))
				if err != nil {
					t.Fatal(err)
				}
				// Old checkpoint without Mode: Blob set, Mode 0.
				if _, err := store.AppendEvent("s", AppendEventInput{
					Type:    EventSessionCheckpoint,
					Payload: CheckpointPayload{Tool: "write_file", Files: []CheckpointFile{{Path: "run.sh", Blob: hash}}},
				}); err != nil {
					t.Fatal(err)
				}
				mustWriteFile(t, p, "edited")
				if err := os.Chmod(p, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			path:        "run.sh",
			wantContent: "original",
			wantReport: func(t *testing.T, r RestoreReport) {
				if r.FilesRestored != 1 {
					t.Fatalf("FilesRestored = %d, want 1", r.FilesRestored)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, ws := newCkStore(t)
			target, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, store, ws, target)
			report, err := store.RestoreToSequence("s", ws, target.Sequence)
			if err != nil {
				t.Fatal(err)
			}
			tc.wantReport(t, report)
			got, readErr := os.ReadFile(filepath.Join(ws, tc.path))
			if tc.wantContent == "" {
				if !os.IsNotExist(readErr) {
					t.Fatalf("expected %s to not exist (deleted), content=%q err=%v", tc.path, got, readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(got) != tc.wantContent {
				t.Fatalf("content = %q, want %q", got, tc.wantContent)
			}
			if tc.name == "mode 0 preserves existing mode" {
				info, err := os.Stat(filepath.Join(ws, tc.path))
				if err != nil {
					t.Fatal(err)
				}
				// Current behavior: mode unknown -> preserve the file's mode
				// at restore time (0o600 as mutated above), no signal.
				if info.Mode().Perm() != 0o600 {
					t.Fatalf("mode = %v, want 0o600 (existing preserved)", info.Mode().Perm())
				}
			}
		})
	}
}
