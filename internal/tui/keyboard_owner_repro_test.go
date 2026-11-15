package tui

// A Ctrl+X chord armed before an async prompt must be cancelled when the
// prompt opens, so the follow-up key reaches the modal.
// This test FAILS on the branch without the promptTransition fix
// (the chord stays armed and the follow-up key opens the model picker
// behind the card) and PASSES with it.
//
// Hermetic: QG_CRED_STORAGE=file, injected clock, no time.Sleep.

import (
	"context"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
)

func reproTestModel(t *testing.T, now time.Time) model {
	t.Helper()
	t.Setenv("QG_CRED_STORAGE", "file")
	m := newModel(context.Background(), Options{})
	m.now = func() time.Time { return now }
	return m
}

// Arming Ctrl+X and then receiving an async permission prompt must cancel
// the chord, so the follow-up key goes to the permission modal — never to a
// leader slash (here: "m" must NOT open the /model picker behind the card).
func TestReproLeaderCancelledByAsyncPermission(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	m := reproTestModel(t, now)
	updated, _ := m.Update(testKeyCtrl('x'))
	m = updated.(model)
	if !m.leaderPending {
		t.Fatal("Ctrl+X must arm leaderPending")
	}
	// Async agent prompt arriving between the chord keys (production path).
	decideCalled := false
	m.activeRunID = 7
	updated, _ = m.Update(permissionRequestMsg{
		runID:   7,
		request: agent.PermissionRequest{ToolCallID: "tc1", ToolName: "bash", Action: agent.PermissionActionPrompt},
		decide:  func(agent.PermissionDecision) { decideCalled = true },
	})
	m = updated.(model)
	if m.pendingPermission == nil {
		t.Fatal("permission prompt must be live")
	}
	if m.leaderPending {
		t.Logf("note: leaderPending still armed with a permission modal open (part of the defect)")
	}
	updated, _ = m.Update(testKeyText("m"))
	after := updated.(model)
	if after.picker != nil {
		t.Fatalf("leader chord opened a picker behind the permission modal (kind=%v)", after.picker.kind)
	}
	if after.pendingPermission == nil {
		t.Fatalf("permission modal lost (decideCalled=%v)", decideCalled)
	}
	if after.leaderPending {
		t.Fatal("leader still pending after the follow-up key")
	}
}
