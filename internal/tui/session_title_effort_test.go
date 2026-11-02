package tui

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func titleEffort(t *testing.T, modelID string) string {
	t.Helper()
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "Some Title"},
		{Type: qgruntime.StreamEventDone},
	}}
	if _, err := generateSessionTitle(context.Background(), provider, modelID, "User: fix login bug"); err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(provider.requests))
	}
	return provider.requests[0].ReasoningEffort
}

// The title call sends reasoning effort only when the model supports it, and
// then the smallest supported tier: muse-spark has no effort list (Sonda:
// minimal changes nothing there), deepseek advertises low/high/max,
// glm-5.2 and gpt-6-luna measured minimal as accepted and cheapest.
func TestSessionTitleEffortFollowsModelSupport(t *testing.T) {
	cases := []struct {
		model string
		want  string
	}{
		{"muse-spark-1.3-contributor", ""},
		{"muse-spark", ""},
		{"deepseek-v4.1-flash", "low"},
		{"glm-5.2", "minimal"},
		{"gpt-6-luna", "minimal"},
	}
	for _, tc := range cases {
		if got := titleEffort(t, tc.model); got != tc.want {
			t.Errorf("ReasoningEffort for %s = %q, want %q", tc.model, got, tc.want)
		}
	}
}

// A silent title failure must not retry forever: two attempts per session,
// then the gate stays shut.
func TestAutoTitleStopsAfterTwoFailures(t *testing.T) {
	store := testSessionStore(t)
	session := createAutoTitledSession(t, store, "how do I add a fetch call", "Use the fetch tool.")

	newAttemptModel := func() model {
		m := newModel(context.Background(), Options{
			SessionStore: store,
			Provider:     titleProvider(""),
		})
		m.activeSession = session
		events, err := store.ReadEvents(session.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		m.sessionEvents = events
		return m
	}

	fail := func(m model) model {
		next, cmd := m.maybeAutoTitleActiveSession()
		if cmd == nil {
			t.Fatal("expected a title attempt to be scheduled")
		}
		msg, ok := execCmd(cmd).(sessionTitleGeneratedMsg)
		if !ok {
			t.Fatalf("expected sessionTitleGeneratedMsg, got %#v", msg)
		}
		// Empty title from the fake provider counts as a silent failure.
		updated, _ := next.handleSessionTitleGenerated(sessionTitleGeneratedMsg{sessionID: session.SessionID, err: msg.err, title: msg.title})
		return updated
	}

	m := newAttemptModel()
	m = fail(m)
	m = fail(m)
	if _, cmd := m.maybeAutoTitleActiveSession(); cmd != nil {
		t.Fatal("after two failed attempts no further title attempt may be scheduled")
	}
	// The gate stays shut on later turns too.
	if _, cmd := m.maybeAutoTitleActiveSession(); cmd != nil {
		t.Fatal("exhausted title attempts must stay exhausted")
	}
}
