package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// /resume must be refused while a compaction is rewriting session state — the
// late compact result would otherwise overwrite the just-resumed session.
func TestResumeRefusedDuringCompaction(t *testing.T) {
	store := testSessionStore(t)
	active, err := store.Create(sessions.CreateInput{Title: "Active"})
	if err != nil {
		t.Fatalf("Create active returned error: %v", err)
	}
	other, err := store.Create(sessions.CreateInput{Title: "Other"})
	if err != nil {
		t.Fatalf("Create other returned error: %v", err)
	}
	m := newModel(context.Background(), Options{SessionStore: store})
	m.activeSession = active
	m.compactInFlight = true
	m.input.SetValue("/resume " + other.SessionID)

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if next.activeSession.SessionID != active.SessionID {
		t.Fatalf("expected /resume during compaction not to replace active session, got %q want %q",
			next.activeSession.SessionID, active.SessionID)
	}
	if !next.compactInFlight {
		t.Fatal("refused /resume must leave the compaction in flight")
	}
	if !transcriptContains(next.transcript, "Compactação em andamento") {
		t.Fatalf("expected compaction warning, got %#v", next.transcript)
	}
}

// The session picker funnels through handleResumeCommand, so it must be
// refused during compaction as well.
func TestSessionPickerResumeRefusedDuringCompaction(t *testing.T) {
	store := testSessionStore(t)
	active, err := store.Create(sessions.CreateInput{Title: "Active"})
	if err != nil {
		t.Fatalf("Create active returned error: %v", err)
	}
	other, err := store.Create(sessions.CreateInput{Title: "Other"})
	if err != nil {
		t.Fatalf("Create other returned error: %v", err)
	}
	m := newModel(context.Background(), Options{SessionStore: store})
	m.activeSession = active
	m.compactInFlight = true

	// This is exactly what the picker does at model.go pickerSession case.
	next, text := m.handleResumeCommand(other.SessionID)

	if next.activeSession.SessionID != active.SessionID {
		t.Fatalf("expected picker resume during compaction not to replace active session, got %q want %q",
			next.activeSession.SessionID, active.SessionID)
	}
	if !strings.Contains(text, "Compactação em andamento") {
		t.Fatalf("expected compaction warning, got %q", text)
	}
}

// /rewind must be refused while a compaction is rewriting the session log —
// the rewind would be undone or applied to a log about to be replaced.
func TestRewindRefusedDuringCompaction(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{Title: "Rewind me", Cwd: t.TempDir(), ModelID: "gpt-4.1", Provider: "openai"})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	appendTestEvent(t, store, session.SessionID, sessions.EventMessage, map[string]any{"role": "user", "content": "first request"})
	appendTestEvent(t, store, session.SessionID, sessions.EventSessionCheckpoint, map[string]any{"tool": "write_file", "files": []any{}})
	appendTestEvent(t, store, session.SessionID, sessions.EventMessage, map[string]any{"role": "assistant", "content": "after checkpoint"})

	m := newModel(context.Background(), Options{SessionStore: store})
	m.input.SetValue("/resume " + session.SessionID)
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	m.compactInFlight = true

	before, err := store.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatalf("ReadEvents returned error: %v", err)
	}

	next, out := m.handleRewindCommand("")
	if !strings.Contains(out, "Compactação em andamento") {
		t.Fatalf("expected compaction warning, got %q", out)
	}
	after, err := store.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatalf("ReadEvents returned error: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("refused rewind must not touch the session log, before=%d after=%d", len(before), len(after))
	}
	if !next.compactInFlight {
		t.Fatal("refused rewind must leave the compaction in flight")
	}
}

// End-to-end: a /resume attempted during compaction must not move off session
// A, so the late compact result for A cannot overwrite a resumed session.
func TestLateCompactResultCannotReplaceResumedSession(t *testing.T) {
	store := testSessionStore(t)
	active, err := store.Create(sessions.CreateInput{Title: "Active"})
	if err != nil {
		t.Fatalf("Create active returned error: %v", err)
	}
	other, err := store.Create(sessions.CreateInput{Title: "Other"})
	if err != nil {
		t.Fatalf("Create other returned error: %v", err)
	}
	m := newModel(context.Background(), Options{SessionStore: store})
	m.activeSession = active
	m.sessionEvents = append([]sessions.Event{}, m.sessionEvents...)
	m.transcript = append([]transcriptRow{}, m.transcript...)
	snapshotEvents := append([]sessions.Event{}, m.sessionEvents...)
	snapshotTranscript := append([]transcriptRow{}, m.transcript...)
	m.compactInFlight = true
	m.input.SetValue("/resume " + other.SessionID)

	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if m.activeSession.SessionID != active.SessionID {
		t.Fatalf("resume during compaction moved off session A to %q", m.activeSession.SessionID)
	}

	updated, _ = m.Update(compactResultMsg{
		result:             CompactResult{Compacted: true},
		activeSession:      active,
		sessionEvents:      snapshotEvents,
		transcript:         snapshotTranscript,
		hasSessionSnapshot: true,
	})
	m = updated.(model)
	if m.activeSession.SessionID != active.SessionID {
		t.Fatalf("late compact result left model on %q, want session A %q", m.activeSession.SessionID, active.SessionID)
	}
	if len(m.sessionEvents) != len(snapshotEvents) {
		t.Fatalf("late compact result changed events, got %d want %d", len(m.sessionEvents), len(snapshotEvents))
	}
	if transcriptContains(m.transcript, other.Title) {
		t.Fatalf("resumed session B leaked into transcript: %#v", m.transcript)
	}
}

// Listing sessions must keep working during compaction — only switching is
// refused.
func TestResumeListStillWorksDuringCompaction(t *testing.T) {
	store := testSessionStore(t)
	active, err := store.Create(sessions.CreateInput{Title: "Active"})
	if err != nil {
		t.Fatalf("Create active returned error: %v", err)
	}
	m := newModel(context.Background(), Options{SessionStore: store})
	m.activeSession = active
	m.compactInFlight = true

	next, text := m.handleResumeCommand("")
	if strings.Contains(text, "Compactação em andamento") {
		t.Fatalf("listing must not be blocked during compaction, got %q", text)
	}
	if !strings.Contains(text, "Active") && !strings.Contains(text, "Sessões") {
		t.Fatalf("expected session list during compaction, got %q", text)
	}
	if next.activeSession.SessionID != active.SessionID {
		t.Fatalf("listing must not switch session, got %q want %q", next.activeSession.SessionID, active.SessionID)
	}
}
