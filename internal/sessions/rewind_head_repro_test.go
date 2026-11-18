package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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

// Regression: ApplyRewind must keep Sequence and deterministic event IDs
// (sess:seq) strictly monotonic above any truncated event, without changing
// the on-disk JSON schema. Stale pre-rewind EventRefs (ID + Sequence + Type)
// must never resolve to post-rewind events.
func TestRewindDoesNotReuseSequenceOrID(t *testing.T) {
	store, _ := newCkStore(t)
	if _, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"v": "one"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"v": "two"}}); err != nil {
		t.Fatal(err)
	}
	oldThird, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"v": "old-three"}})
	if err != nil {
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
	nextMsg, err := store.AppendEvent("s", AppendEventInput{Type: EventMessage, Payload: map[string]any{"v": "new-after-rewind"}})
	if err != nil {
		t.Fatal(err)
	}
	if nextMsg.Sequence <= oldFourth.Sequence || nextMsg.ID == oldThird.ID || nextMsg.ID == oldRef.ID {
		t.Fatalf("post-rewind event reused truncated ID/Sequence: old3=%s/%d old4=%s/%d got=%s/%d",
			oldThird.ID, oldThird.Sequence, oldRef.ID, oldRef.Sequence, nextMsg.ID, nextMsg.Sequence)
	}

	events, err := store.ReadEvents("s")
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Event{}
	for _, ev := range events {
		byID[ev.ID] = ev
	}
	if matched, ok := byID[oldThird.ID]; ok {
		t.Fatalf("truncated event ID %q reappeared after rewind: %+v", oldThird.ID, matched)
	}
	if matched, ok := byID[oldRef.ID]; ok {
		t.Fatalf("stale pre-rewind ref %q resolved after rewind: %+v", oldRef.ID, matched)
	}
	if len(events) != 4 || events[2].Type != EventSessionRewind || events[2].Sequence != 5 || nextMsg.Sequence != 6 {
		t.Fatalf("unexpected post-rewind sequence layout: events=%+v nextMsg=%+v", events, nextMsg)
	}

	// Second rewind across an already-gapped log must stay monotonic too.
	if _, err := store.ApplyRewind("s", ws, 1); err != nil {
		t.Fatal(err)
	}
	events2, err := store.ReadEvents("s")
	if err != nil {
		t.Fatal(err)
	}
	if len(events2) != 2 || events2[1].Type != EventSessionRewind || events2[1].Sequence != 7 || events2[1].ID != "s:7" {
		t.Fatalf("second rewind did not advance monotonically past %d: %+v", nextMsg.Sequence, events2)
	}
	rawEvents, err := os.ReadFile(store.eventsPath("s"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(rawEvents)), "\n") {
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) != 6 {
			t.Fatalf("on-disk event JSON schema changed: keys=%v", raw)
		}
	}
	if !strings.Contains(oldRef.ID, ":") {
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
				// Unix permission bits do not exist on Windows (os.Chmod only
				// maps the owner-write bit to read-only there), so the mode
				// assertion is Unix-only. Content restoration above still
				// asserts everywhere.
				if runtime.GOOS == "windows" {
					return
				}
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
