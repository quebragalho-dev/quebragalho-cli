package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Durability contract for the normal AppendEvent path: at most 1 fsync per
// event (the events.jsonl one). metadata.json is no longer rewritten on every
// event; its derived fields (EventCount, UpdatedAt, LastEventType) are
// reconciled from events.jsonl on Get/List instead. This keeps the crash
// recovery guarantee — every acknowledged event is in the fsync'd log, and the
// hot path never touches metadata.json (so it can never surface torn after a
// power loss) — without changing the on-disk file format.

// Only the first append (the 0->1 transition) may rewrite metadata.json, with
// full durability; later appends must not touch it: the only fsync on the
// steady-state path is the events.jsonl one.
func TestAppendEventLeavesMetadataFileUntouched(t *testing.T) {
	store := seqTestStore(t)
	session, err := store.Create(CreateInput{SessionID: "qg_fsync_1", Title: "t", Cwd: "/repo", ModelID: "m", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"content": "first"}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Sequence != 1 {
		t.Fatalf("first sequence: want 1, got %d", first.Sequence)
	}
	metaPath := filepath.Join(store.RootDir, session.SessionID, MetadataFile)
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"content": "ok"}}); err != nil {
			t.Fatal(err)
		}
	}

	after, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("steady-state AppendEvent rewrote %s: want byte-identical after the 0->1 transition", MetadataFile)
	}

	events, err := store.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("want 6 events in the log, got %d", len(events))
	}
}

// The 0->1 transition persists EventCount/UpdatedAt/LastEventType to disk with
// full durability. The on-disk EventCount > 0 is what tells resume paths that
// the session is populated, so a missing events.jsonl is rejected instead of
// silently resumed as empty (see TestACPResumeRejectsMissingPopulatedEventLog).
func TestFirstAppendPersistsPopulatedMarkerToDisk(t *testing.T) {
	store := seqTestStore(t)
	session, err := store.Create(CreateInput{SessionID: "qg_fsync_5", Title: "t", Cwd: "/repo", ModelID: "m", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	event, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"content": "first"}})
	if err != nil {
		t.Fatal(err)
	}
	// Read the raw file, bypassing the read-path reconciliation: the marker
	// must be durable on disk, not just derived.
	raw, err := store.readMetadata(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.EventCount != 1 {
		t.Fatalf("on-disk EventCount after first append: want 1, got %d", raw.EventCount)
	}
	if raw.UpdatedAt != event.CreatedAt {
		t.Fatalf("on-disk UpdatedAt: want %q, got %q", event.CreatedAt, raw.UpdatedAt)
	}
	if raw.LastEventType != event.Type {
		t.Fatalf("on-disk LastEventType: want %q, got %q", event.Type, raw.LastEventType)
	}
}

// Get must reconcile the derived counters from the log when metadata.json is
// stale (the normal state after the fix, or the crash window between the
// append and the metadata write in the old layout).
func TestGetReconcilesDerivedCountersFromLog(t *testing.T) {
	store := seqTestStore(t)
	session, err := store.Create(CreateInput{SessionID: "qg_fsync_2", Title: "titulo", Cwd: "/repo", ModelID: "m", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	var last Event
	for i := 0; i < 3; i++ {
		last, err = store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"content": "ok"}})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Simulate stale metadata: zeroed EventCount and creation-time UpdatedAt.
	meta, err := store.readMetadata(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	meta.EventCount = 0
	meta.UpdatedAt = meta.CreatedAt
	meta.LastEventType = ""
	if err := store.writeMetadata(meta); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("session vanished after reconciliation")
	}
	if loaded.EventCount != 3 {
		t.Fatalf("reconciled EventCount: want 3, got %d", loaded.EventCount)
	}
	if loaded.UpdatedAt != last.CreatedAt {
		t.Fatalf("reconciled UpdatedAt: want %q, got %q", last.CreatedAt, loaded.UpdatedAt)
	}
	if loaded.LastEventType != last.Type {
		t.Fatalf("reconciled LastEventType: want %q, got %q", last.Type, loaded.LastEventType)
	}
	if loaded.Title != "titulo" {
		t.Fatalf("reconciliation must not change non-derived fields: Title=%q", loaded.Title)
	}
	if loaded.SessionID != session.SessionID || loaded.CreatedAt != session.CreatedAt {
		t.Fatalf("reconciliation changed the session identity: %+v", loaded)
	}
}

// List must order by the activity derived from the log, not by the UpdatedAt
// frozen in metadata.json.
func TestListReflectsDerivedActivity(t *testing.T) {
	base, err := time.Parse(time.RFC3339, "2026-06-04T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	current := base
	store := NewStore(StoreOptions{
		RootDir: t.TempDir(),
		Now: func() time.Time {
			current = current.Add(time.Second)
			return current
		},
	})
	old, err := store.Create(CreateInput{SessionID: "qg_fsync_old", Title: "antiga", Cwd: "/repo", ModelID: "m", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(CreateInput{SessionID: "qg_fsync_new", Title: "nova", Cwd: "/repo", ModelID: "m", Provider: "p"}); err != nil {
		t.Fatal(err)
	}
	appendN(t, store, old.SessionID, 5)

	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}
	if sessions[0].SessionID != old.SessionID {
		t.Fatalf("List must order by log activity: first=%q, want %q", sessions[0].SessionID, old.SessionID)
	}
	if sessions[0].EventCount != 5 {
		t.Fatalf("derived EventCount in list: want 5, got %d", sessions[0].EventCount)
	}
}

// Appends create no new files in the session directory (format compatibility:
// old versions keep opening sessions written by new versions).
func TestSessionDirLayoutStableAfterAppends(t *testing.T) {
	store := seqTestStore(t)
	session, err := store.Create(CreateInput{SessionID: "qg_fsync_3", Title: "t", Cwd: "/repo", ModelID: "m", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	appendN(t, store, session.SessionID, 5)

	entries, err := os.ReadDir(filepath.Join(store.RootDir, session.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	want := []string{EventsFile, MetadataFile, "session.lock"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("session dir changed after appends: got %v, want %v", names, want)
	}
}

// The file format does not change: metadata.json keeps the known keys and
// every events.jsonl line carries the required fields.
func TestSessionFileFormatUnchanged(t *testing.T) {
	store := seqTestStore(t)
	session, err := store.Create(CreateInput{SessionID: "qg_fsync_4", Title: "t", Cwd: "/repo", ModelID: "m", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventToolCall, Payload: map[string]any{"name": "read"}}); err != nil {
		t.Fatal(err)
	}

	rawMeta, err := os.ReadFile(filepath.Join(store.RootDir, session.SessionID, MetadataFile))
	if err != nil {
		t.Fatal(err)
	}
	var metaMap map[string]any
	if err := json.Unmarshal(rawMeta, &metaMap); err != nil {
		t.Fatalf("metadata.json unreadable: %v", err)
	}
	for _, key := range []string{"sessionId", "title", "createdAt", "updatedAt", "eventCount"} {
		if _, ok := metaMap[key]; !ok {
			t.Fatalf("metadata.json lost key %q", key)
		}
	}

	rawEvents, err := os.ReadFile(filepath.Join(store.RootDir, session.SessionID, EventsFile))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(rawEvents)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 line in events.jsonl, got %d", len(lines))
	}
	var lineMap map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &lineMap); err != nil {
		t.Fatalf("events.jsonl line unreadable: %v", err)
	}
	for _, key := range []string{"id", "sessionId", "sequence", "type", "createdAt"} {
		if _, ok := lineMap[key]; !ok {
			t.Fatalf("event lost field %q", key)
		}
	}
}
