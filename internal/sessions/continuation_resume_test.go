package sessions

import (
	"encoding/json"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func jsonRaw(value map[string]any) json.RawMessage {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(raw)
}

func TestRepairOrphanInsertsInlineBeforeNextMessage(t *testing.T) {
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleAssistant, Content: "one", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "read_it", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: "ok", ToolCallID: "c1"},
		{Role: qgruntime.MessageRoleAssistant, Content: "two", ToolCalls: []qgruntime.ToolCall{{ID: "c2", Name: "write_file", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleAssistant, Content: "three"},
	}
	fixed := RepairOrphanToolCalls(msgs)
	if len(fixed) != len(msgs)+1 {
		t.Fatalf("len = %d, want %d", len(fixed), len(msgs)+1)
	}
	// The synthetic result for c2 must sit right after its owner (index 2),
	// before the following assistant message — not at the end of the list.
	synthetic := -1
	for i, m := range fixed {
		if m.Role == qgruntime.MessageRoleTool && m.ToolCallID == "c2" {
			synthetic = i
		}
	}
	if synthetic != 3 {
		t.Fatalf("synthetic result at index %d, want 3 (right after its owner)", synthetic)
	}
	if !fixed[synthetic].IsError {
		t.Fatal("synthetic result must be IsError")
	}
}

func TestReconstructContinuationMessagesFiltersAndMerges(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "recon_1"})
	if err != nil {
		t.Fatal(err)
	}
	append := func(typ EventType, payload map[string]any) {
		t.Helper()
		if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: typ, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	append(EventMessage, map[string]any{"role": "user", "content": "do it"})
	append(EventToolCall, map[string]any{"id": "c1", "name": "read_it", "arguments": "{}"})
	append(EventToolResult, map[string]any{"toolCallId": "c1", "name": "read_it", "status": "ok", "output": "contents"})
	append(EventToolCall, map[string]any{"id": "c2", "name": "bash", "arguments": "{}"})
	append(EventToolResult, map[string]any{"toolCallId": "c2", "name": "bash", "status": "error", "output": "boom"})
	append(EventMessage, map[string]any{"role": "assistant", "content": "done"})
	append(EventUsage, map[string]any{"inputTokens": 5})
	append(EventError, map[string]any{"message": "Run cancelled."})
	events, err := store.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	msgs := ReconstructContinuationMessages(events)
	if len(msgs) != 5 {
		t.Fatalf("len = %d, want 5 (user, merged assistant, tool, tool, assistant; usage/error skipped)", len(msgs))
	}
	if msgs[0].Role != qgruntime.MessageRoleUser || msgs[0].Content != "do it" {
		t.Fatalf("msg0 = %#v", msgs[0])
	}
	if msgs[1].Role != qgruntime.MessageRoleAssistant || len(msgs[1].ToolCalls) != 2 {
		t.Fatalf("tool calls must merge into one assistant message: %#v", msgs[1])
	}
	if msgs[2].ToolCallID != "c1" || msgs[2].Content != "contents" || msgs[2].IsError {
		t.Fatalf("tool result c1 wrong: %#v", msgs[2])
	}
	if msgs[3].ToolCallID != "c2" || !msgs[3].IsError {
		t.Fatalf("tool result c2 must be error: %#v", msgs[3])
	}
	if msgs[4].Content != "done" {
		t.Fatalf("final answer lost: %#v", msgs[4])
	}
}

func TestTurnMessagesToBackfillEventsMapping(t *testing.T) {
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "go"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "read_it", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: "out", ToolCallID: "c1", ChangedFiles: []string{"f.txt"}},
		{Role: qgruntime.MessageRoleUser, Content: "nudge (not logged live)"},
		{Role: qgruntime.MessageRoleAssistant, Content: "final"},
	}
	inputs := TurnMessagesToBackfillEvents(msgs, 2)
	if len(inputs) != 3 {
		t.Fatalf("len = %d, want 3 (tool_call, tool_result, final assistant; user nudge skipped)", len(inputs))
	}
	if inputs[0].Type != EventToolCall || inputs[1].Type != EventToolResult || inputs[2].Type != EventMessage {
		t.Fatalf("types = %v %v %v", inputs[0].Type, inputs[1].Type, inputs[2].Type)
	}
}

func TestBackfillTurnEventsIdempotent(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "backfill_1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "go"}}); err != nil {
		t.Fatal(err)
	}
	meta, _ := store.Get(session.SessionID)
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "go"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "read_it", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: "out", ToolCallID: "c1"},
	}
	snap := ContinuationSnapshot{SessionID: session.SessionID, ModelID: "m", EventSeq: meta.EventCount, TurnBase: 1, Messages: msgs}
	head1, err := store.BackfillTurnEvents(session.SessionID, &snap)
	if err != nil {
		t.Fatal(err)
	}
	if head1 != meta.EventCount+2 {
		t.Fatalf("head = %d, want %d", head1, meta.EventCount+2)
	}
	events, _ := store.ReadEvents(session.SessionID)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	head2, err := store.BackfillTurnEvents(session.SessionID, &snap)
	if err != nil {
		t.Fatal(err)
	}
	if head2 != head1 {
		t.Fatal("second backfill must be a no-op")
	}
	if events, _ := store.ReadEvents(session.SessionID); len(events) != 3 {
		t.Fatalf("events = %d after no-op, want 3", len(events))
	}
}

func TestDeltaCoveredBySnapshot(t *testing.T) {
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "go"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "read_it", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: "out", ToolCallID: "c1"},
	}
	snap := &ContinuationSnapshot{SessionID: "s", EventSeq: 4, TurnBase: 1, Messages: msgs}
	covered := []Event{
		{Sequence: 5, Type: EventToolCall, Payload: jsonRaw(map[string]any{"id": "c1", "name": "read_it", "arguments": "{}"})},
		{Sequence: 6, Type: EventToolResult, Payload: jsonRaw(map[string]any{"toolCallId": "c1", "name": "read_it", "status": "ok", "output": "out"})},
		{Sequence: 7, Type: EventUsage, Payload: jsonRaw(map[string]any{"inputTokens": 1})},
	}
	if !DeltaCoveredBySnapshot(covered, snap) {
		t.Fatal("matching delta must be covered")
	}
	foreign := append(append([]Event{}, covered...), Event{Sequence: 8, Type: EventMessage, Payload: jsonRaw(map[string]any{"role": "user", "content": "someone else"})})
	if DeltaCoveredBySnapshot(foreign, snap) {
		t.Fatal("foreign conversation event must not be covered")
	}
	if DeltaCoveredBySnapshot(covered, &ContinuationSnapshot{EventSeq: 4, TurnBase: 0, Messages: msgs}) {
		t.Fatal("unknown base must not be covered")
	}
}

func TestInferTurnBase(t *testing.T) {
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "old"},
		{Role: qgruntime.MessageRoleAssistant, Content: "old answer"},
		{Role: qgruntime.MessageRoleUser, Content: "new prompt"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{{ID: "c9", Name: "x", Arguments: "{}"}}},
	}
	if got := InferTurnBase(msgs, "new prompt"); got != 3 {
		t.Fatalf("base = %d, want 3", got)
	}
	if got := InferTurnBase(msgs, "missing"); got != -1 {
		t.Fatalf("base = %d, want -1", got)
	}
}
