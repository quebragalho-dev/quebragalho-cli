package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestContinuationWriteReadRoundTrip(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "cont_roundtrip", Cwd: "/repo", ModelID: "m1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "hi"}}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	meta, _ := store.Get(session.SessionID)
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "hi"},
		{Role: qgruntime.MessageRoleAssistant, Content: "ok", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "read_it", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: "file contents here", ToolCallID: "c1"},
	}
	snap := ContinuationSnapshot{
		SessionID: session.SessionID, ModelID: "m1", Provider: "test",
		ReasoningEffort: "medium", GoalKey: "", EventSeq: meta.EventCount,
		Messages: msgs, LoadedTools: []string{"read_it"},
	}
	if err := store.WriteContinuation(snap); err != nil {
		t.Fatalf("WriteContinuation: %v", err)
	}
	path := filepath.Join(store.RootDir, session.SessionID, ContinuationFile)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("snapshot perm = %o, want 600", perm)
	}
	loaded, err := store.ReadContinuation(session.SessionID)
	if err != nil {
		t.Fatalf("ReadContinuation: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected snapshot, got nil")
	}
	if len(loaded.Messages) != len(msgs) || loaded.ModelID != "m1" || loaded.EventSeq != meta.EventCount {
		t.Fatalf("round-trip mismatch: %#v", loaded)
	}
	if len(loaded.LoadedTools) != 1 || loaded.LoadedTools[0] != "read_it" {
		t.Fatalf("loadedTools mismatch: %#v", loaded.LoadedTools)
	}
}

func TestContinuationReadMissingReturnsNil(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "cont_missing"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.ReadContinuation(session.SessionID)
	if err != nil {
		t.Fatalf("ReadContinuation missing: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil snapshot, got %#v", got)
	}
}

func TestContinuationInvalidateDeletes(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "cont_inval"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	snap := ContinuationSnapshot{SessionID: session.SessionID, ModelID: "m", EventSeq: 0, Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: "s"}}}
	if err := store.WriteContinuation(snap); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := store.InvalidateContinuation(session.SessionID); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	got, err := store.ReadContinuation(session.SessionID)
	if err != nil || got != nil {
		t.Fatalf("expected nil after invalidate, got %#v err=%v", got, err)
	}
}

func TestContinuationFreshness(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "cont_fresh"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "hi"}}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	events, _ := store.ReadEvents(session.SessionID)
	snap := ContinuationSnapshot{SessionID: session.SessionID, ModelID: "m", EventSeq: events[len(events)-1].Sequence, Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: "s"}}}
	fresh, _, err := store.CheckContinuationFresh(session.SessionID, &snap)
	if err != nil || !fresh {
		t.Fatalf("expected fresh, got fresh=%v err=%v", fresh, err)
	}
	// Usage-only delta stays fresh.
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventUsage, Payload: map[string]any{"inputTokens": 1}}); err != nil {
		t.Fatalf("AppendEvent usage: %v", err)
	}
	fresh, _, err = store.CheckContinuationFresh(session.SessionID, &snap)
	if err != nil || !fresh {
		t.Fatalf("usage-only delta must stay fresh, got fresh=%v err=%v", fresh, err)
	}
	// New conversation event makes it stale.
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "later"}}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	fresh, _, err = store.CheckContinuationFresh(session.SessionID, &snap)
	if err != nil || fresh {
		t.Fatalf("expected stale after new message, got fresh=%v err=%v", fresh, err)
	}
}

func TestForkDoesNotCopyContinuation(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	parent, err := store.Create(CreateInput{SessionID: "cont_fork_parent", Cwd: "/repo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(parent.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "hi"}}); err != nil {
		t.Fatal(err)
	}
	snap := ContinuationSnapshot{SessionID: parent.SessionID, ModelID: "m", EventSeq: 1, Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: "s"}}}
	if err := store.WriteContinuation(snap); err != nil {
		t.Fatal(err)
	}
	fork, err := store.Fork(parent.SessionID, ForkInput{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadContinuation(fork.SessionID); err != nil || got != nil {
		t.Fatalf("fork must start without a snapshot, got %#v err=%v", got, err)
	}
	if got, err := store.ReadContinuation(parent.SessionID); err != nil || got == nil {
		t.Fatalf("fork must not remove the parent snapshot, got %#v err=%v", got, err)
	}
}
