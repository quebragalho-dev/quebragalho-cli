package tui

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// writeFinalSnapshot mimics the turn-final snapshot command production queues:
// the continuation's wire with the post-flush event count.
func writeFinalSnapshot(t *testing.T, store *sessions.Store, m model, turnBase int) {
	t.Helper()
	if len(m.continuation.messages) == 0 {
		t.Fatal("no continuation to snapshot")
	}
	meta, err := store.Get(m.activeSession.SessionID)
	if err != nil || meta == nil {
		t.Fatalf("Get: %v", err)
	}
	snap := buildContinuationSnapshot(m.activeSession.SessionID, m.continuation.modelID, m.providerName, string(m.reasoningEffort), m.continuation.goalKey, meta.EventCount, turnBase, m.continuation.messages, m.continuation.loadedTools)
	if err := store.WriteContinuation(snap); err != nil {
		t.Fatalf("WriteContinuation: %v", err)
	}
}

func TestExactResumeSendsIdenticalWire(t *testing.T) {
	store := testSessionStore(t)
	registry := tools.NewRegistry()
	registry.Register(continuationEchoTool{})
	scripts := append([][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "read_it"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-1", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
		{Type: qgruntime.StreamEventDone},
	}}, textScripts("Should I continue?", "Yes, go on.", "third answer", "third answer")...)
	provider := &scriptedProvider{scripts: scripts}
	m := continuationHarness(t, store, provider, registry)

	m = runTextTurn(t, m, "read it")
	m = runTextTurn(t, m, "Sim")
	if len(provider.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(provider.requests))
	}
	// Expected history after turn 2: turn-2's request wire plus its answer.
	// Turn 3 must resend exactly this plus the new prompt (text-only turns add
	// no new tool ids, so the wire is fully deterministic).
	turn2Wire := provider.requests[2]
	writeFinalSnapshot(t, store, m, len(m.continuation.messages)-2)
	sessionID := m.activeSession.SessionID
	usageBefore := m.lastUsage

	// Simulate a close: fresh in-memory model on the same store.
	freshProvider := &scriptedProvider{scripts: textScripts("third answer")}
	fresh := continuationHarness(t, store, freshProvider, registry)
	before := len(freshProvider.requests)
	fresh, notice := fresh.handleResumeCommand(sessionID)
	if strings.TrimSpace(notice) != "" {
		t.Fatalf("resume notice = %q", notice)
	}
	if len(freshProvider.requests) != before {
		t.Fatal("resume must not call the model")
	}
	if !fresh.resumedExact || fresh.reconstructedResume {
		t.Fatal("expected exact resume, got reconstructed")
	}
	if fresh.lastUsage != usageBefore {
		t.Fatalf("meter changed across resume: %#v vs %#v", fresh.lastUsage, usageBefore)
	}
	fresh = runTextTurn(t, fresh, "and then?")
	if len(freshProvider.requests) != 1 {
		t.Fatalf("resumed requests = %d, want 1", len(freshProvider.requests))
	}
	got := freshProvider.requests[0]
	wantLen := len(turn2Wire.Messages) + 2 // turn-2 answer + new prompt
	if len(got.Messages) != wantLen {
		t.Fatalf("resumed wire = %d msgs, want %d (turn-2 wire + answer + prompt)", len(got.Messages), wantLen)
	}
	for i := range turn2Wire.Messages {
		a, b := got.Messages[i], turn2Wire.Messages[i]
		if a.Role != b.Role || a.Content != b.Content || len(a.ToolCalls) != len(b.ToolCalls) {
			t.Fatalf("history msg %d differs: %#v vs %#v", i, a, b)
		}
		for j := range a.ToolCalls {
			if a.ToolCalls[j] != b.ToolCalls[j] {
				t.Fatalf("history tool call %d/%d differs: %#v vs %#v", i, j, a.ToolCalls[j], b.ToolCalls[j])
			}
		}
	}
	// Turn-2's answer follows its request wire in the resumed history.
	if got.Messages[len(turn2Wire.Messages)].Content != "Yes, go on." {
		t.Fatalf("turn-2 answer missing: %#v", got.Messages[len(turn2Wire.Messages)])
	}
	if last := lastUserMessage(got); !strings.HasSuffix(last.Content, "and then?") {
		t.Fatalf("last user message must end with the prompt, got %q", last.Content)
	}
	if requestContains(got, "Previous session context") {
		t.Fatal("resumed turn must not use the textual summary")
	}
}

func TestOldSessionResumeWarnsAndReconstructs(t *testing.T) {
	store := testSessionStore(t)
	provider := &scriptedProvider{scripts: textScripts("first answer", "second answer")}
	m := continuationHarness(t, store, provider, nil)
	m = runTextTurn(t, m, "first question")
	sessionID := m.activeSession.SessionID
	// Simulate a pre-feature session: drop the snapshot file.
	if err := store.InvalidateContinuation(sessionID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadContinuation(sessionID); err != nil || got != nil {
		t.Fatalf("ReadContinuation = %#v, %v; want nil, nil", got, err)
	}

	freshProvider := &scriptedProvider{scripts: textScripts("x")}
	fresh := continuationHarness(t, store, freshProvider, nil)
	fresh, _ = fresh.handleResumeCommand(sessionID)
	if fresh.resumedExact || !fresh.reconstructedResume {
		t.Fatal("old session must reconstruct, not exact-resume")
	}
	found := false
	for _, row := range fresh.transcript {
		if strings.Contains(row.text, "Sessão antiga: retomada com o histórico salvo; imagens e raciocínio anteriores não voltam.") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing old-session warning line")
	}
	// The reconstructed continuation must still send messages as messages:
	// no textual summary on the next turn.
	if len(fresh.continuation.messages) == 0 {
		t.Fatal("reconstructed resume must set a continuation, never fall back to summary")
	}
	fresh = runTextTurn(t, fresh, "second question")
	if len(freshProvider.requests) != 1 {
		t.Fatalf("resumed requests = %d, want 1", len(freshProvider.requests))
	}
	req := freshProvider.requests[0]
	if requestContains(req, "Previous session context") || requestContains(req, "Continuing QG session") {
		t.Fatal("reconstructed turn must not use the textual summary")
	}
	if !requestContains(req, "first answer") {
		t.Fatal("reconstructed turn must carry the prior answer as a message")
	}
}

func TestStaleSnapshotFallsBackWithWarning(t *testing.T) {
	store := testSessionStore(t)
	provider := &scriptedProvider{scripts: textScripts("first answer", "second answer")}
	m := continuationHarness(t, store, provider, nil)
	m = runTextTurn(t, m, "first question")
	writeFinalSnapshot(t, store, m, len(m.continuation.messages)-2)
	sessionID := m.activeSession.SessionID

	// Another process appends conversation after the snapshot.
	if _, err := store.AppendEvent(sessionID, sessions.AppendEventInput{Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "later"}}); err != nil {
		t.Fatal(err)
	}
	fresh := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("x")}, nil)
	fresh, _ = fresh.handleResumeCommand(sessionID)
	if fresh.resumedExact || !fresh.reconstructedResume {
		t.Fatal("stale snapshot must fall back to reconstruction")
	}
}
