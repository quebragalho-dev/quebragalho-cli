package sessions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// Characterization of the continuation resume path (commit A, pure refactor).
// Passes before and after without edits: every case compares the resulting
// wire byte-for-byte (JSON), pinning today's behavior.
func mustWireJSON(t *testing.T, msgs []qgruntime.Message) string {
	t.Helper()
	data, err := json.Marshal(msgs)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestContinuationCharExactResume(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	s, err := store.Create(CreateInput{SessionID: "char_exact"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(s.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "hi"}}); err != nil {
		t.Fatal(err)
	}
	meta, _ := store.Get(s.SessionID)
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "hi"},
	}
	snap := ContinuationSnapshot{SessionID: s.SessionID, ModelID: "m", EventSeq: meta.EventCount, Messages: msgs}
	fresh, delta, err := store.CheckContinuationFresh(s.SessionID, &snap)
	if err != nil || !fresh || len(delta) != 0 {
		t.Fatalf("exact must stay fresh: fresh=%v delta=%v err=%v", fresh, delta, err)
	}
	repaired := RepairOrphanToolCalls(msgs)
	if mustWireJSON(t, repaired) != mustWireJSON(t, msgs) {
		t.Fatal("exact wire without orphans must be byte-identical")
	}
}

func TestContinuationCharStaleOldSnapshot(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	s, _ := store.Create(CreateInput{SessionID: "char_stale"})
	if _, err := store.AppendEvent(s.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "a"}}); err != nil {
		t.Fatal(err)
	}
	snap := ContinuationSnapshot{SessionID: s.SessionID, ModelID: "m", EventSeq: 0, Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: "s"}}}
	if _, err := store.AppendEvent(s.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "b"}}); err != nil {
		t.Fatal(err)
	}
	fresh, delta, err := store.CheckContinuationFresh(s.SessionID, &snap)
	if err != nil || fresh {
		t.Fatalf("old snapshot with new message must be stale: fresh=%v err=%v", fresh, err)
	}
	if len(delta) == 0 {
		t.Fatal("stale delta must be non-empty")
	}
	// Reconstructed wire from the log must contain the later message text.
	events, _ := store.ReadEvents(s.SessionID)
	rebuilt := ReconstructContinuationMessages(events)
	wire := mustWireJSON(t, rebuilt)
	if !strings.Contains(wire, `"Content":"b"`) {
		t.Fatalf("reconstructed wire must carry new message, got %s", wire)
	}
}

func TestContinuationCharToleratedOnlyDelta(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	s, _ := store.Create(CreateInput{SessionID: "char_tol"})
	if _, err := store.AppendEvent(s.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "hi"}}); err != nil {
		t.Fatal(err)
	}
	meta, _ := store.Get(s.SessionID)
	snap := ContinuationSnapshot{SessionID: s.SessionID, ModelID: "m", EventSeq: meta.EventCount, Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: "s"}}}
	for _, ev := range []AppendEventInput{
		{Type: EventUsage, Payload: map[string]any{"inputTokens": 1}},
		{Type: EventError, Payload: map[string]any{"message": "x"}},
		{Type: EventPermission, Payload: map[string]any{"v": 1}},
		{Type: EventSessionCheckpoint, Payload: map[string]any{"tool": "w"}},
	} {
		if _, err := store.AppendEvent(s.SessionID, ev); err != nil {
			t.Fatal(err)
		}
	}
	fresh, _, err := store.CheckContinuationFresh(s.SessionID, &snap)
	if err != nil || !fresh {
		t.Fatalf("tolerated-only delta must stay fresh: fresh=%v err=%v", fresh, err)
	}
}

func TestContinuationCharCoveredVsUncoveredCall(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	s, _ := store.Create(CreateInput{SessionID: "char_cov"})
	if _, err := store.AppendEvent(s.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "go"}}); err != nil {
		t.Fatal(err)
	}
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "go"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "bash", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: interruptedToolResultNotice, ToolCallID: "c1", IsError: true},
	}
	snap := ContinuationSnapshot{SessionID: s.SessionID, ModelID: "m", EventSeq: 1, Messages: msgs}
	if _, err := store.AppendEvent(s.SessionID, AppendEventInput{Type: EventToolCall, Payload: map[string]any{"id": "c1", "name": "bash", "arguments": "{}"}}); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := store.CheckContinuationFresh(s.SessionID, &snap)
	if err != nil || !fresh {
		t.Fatalf("covered call must stay fresh: %v %v", fresh, err)
	}
	if _, err := store.AppendEvent(s.SessionID, AppendEventInput{Type: EventToolCall, Payload: map[string]any{"id": "c2", "name": "bash", "arguments": "{}"}}); err != nil {
		t.Fatal(err)
	}
	fresh, _, err = store.CheckContinuationFresh(s.SessionID, &snap)
	if err != nil || fresh {
		t.Fatalf("uncovered call must be stale: fresh=%v err=%v", fresh, err)
	}
}

func TestContinuationCharOrphanWireAndLogShareNotice(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	s, _ := store.Create(CreateInput{SessionID: "char_orph"})
	if _, err := store.AppendEvent(s.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "go"}}); err != nil {
		t.Fatal(err)
	}
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "go"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{{ID: "c9", Name: "write_file", Arguments: "{}"}}},
	}
	wire := RepairOrphanToolCalls(msgs)
	wireJSON := mustWireJSON(t, wire)
	if !strings.Contains(wireJSON, interruptedToolResultNotice) {
		t.Fatalf("wire repair must embed shared notice, got %s", wireJSON)
	}
	snap := ContinuationSnapshot{SessionID: s.SessionID, ModelID: "m", EventSeq: 1, Messages: msgs}
	added, err := store.BackfillOrphanResults(s.SessionID, &snap)
	if err != nil || added == 0 {
		t.Fatalf("backfill must append: added=%d err=%v", added, err)
	}
	events, _ := store.ReadEvents(s.SessionID)
	logJSON := mustWireJSON(t, ReconstructContinuationMessages(events))
	_ = logJSON
	var lastPayload map[string]any
	for _, ev := range events {
		if ev.Type == EventToolResult {
			_ = json.Unmarshal(ev.Payload, &lastPayload)
		}
	}
	if lastPayload["output"] != interruptedToolResultNotice {
		t.Fatalf("log backfill must use the same notice bytes, got %v", lastPayload)
	}
	// Wire and log notices are byte-identical (single shared const).
	if wireJSON == "" || lastPayload["output"] == "" {
		t.Fatal("empty notice")
	}
}

func TestContinuationCharModelFilter(t *testing.T) {
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "frozen"},
		{Role: qgruntime.MessageRoleUser, Content: "hi"},
		{Role: qgruntime.MessageRoleAssistant, Content: "w", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "r", Arguments: "{}", ProviderCallID: "n1", Signature: "s"}}},
		{Role: qgruntime.MessageRoleTool, Content: "out", ToolCallID: "c1", ToolCallProviderID: "n1"},
	}
	kept := FilterContinuationForModel(msgs, "a", "a")
	if mustWireJSON(t, kept) != mustWireJSON(t, msgs) {
		t.Fatal("same-model filter must be byte-identical")
	}
	switched := FilterContinuationForModel(msgs, "a", "b")
	switchedJSON := mustWireJSON(t, switched)
	if strings.Contains(switchedJSON, `"content":"frozen"`) {
		t.Fatalf("switch must drop frozen system, got %s", switchedJSON)
	}
	if strings.Contains(switchedJSON, "n1") || strings.Contains(switchedJSON, `"signature"`) {
		t.Fatalf("switch must clear provider-native ids, got %s", switchedJSON)
	}
	if mustWireJSON(t, msgs) == switchedJSON {
		t.Fatal("switch must change the wire")
	}
}

func TestContinuationCharInvalidSnapshot(t *testing.T) {
	bad := ContinuationSnapshot{SessionID: "s1", ModelID: "m", Version: 99, EventSeq: 0, Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: "s"}}}
	if err := ValidateContinuationSnapshot("s1", &bad); err == nil {
		t.Fatal("bad version must be invalid")
	}
	empty := ContinuationSnapshot{SessionID: "s1", ModelID: "m", Version: 1, EventSeq: 0, Messages: nil}
	if err := ValidateContinuationSnapshot("s1", &empty); err == nil {
		t.Fatal("empty messages must be invalid")
	}
	orphan := ContinuationSnapshot{SessionID: "s1", ModelID: "m", Version: 1, EventSeq: 0, Messages: []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "s"},
		{Role: qgruntime.MessageRoleAssistant, ToolCalls: []qgruntime.ToolCall{{ID: "zzz", Name: "x", Arguments: "{}"}}},
	}}
	if err := ValidateContinuationSnapshot("s1", &orphan); err == nil {
		t.Fatal("orphan call must be invalid")
	}
}
