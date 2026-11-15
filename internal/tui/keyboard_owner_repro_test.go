package tui

// Failing reproduction for the two proven M3 suspects (S1, S2).
// These tests FAIL on the untouched dev branch (087136d6) via production
// paths and must PASS after the minimal fix. See the report for the exact
// failure output before the fix.
//
// S1: setup.visible is outside noBlockingModal/loopBusy, so a due /loop
// fires a turn behind the onboarding wizard.
// S2: a Ctrl+X chord armed before an async permission prompt resolves inside
// the wrong modal (opens the /model picker behind the permission card).
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

// S1: a due loop must NOT fire while the setup wizard covers the screen.
func TestReproLoopBlockedBehindSetup(t *testing.T) {
	now := time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)
	m := reproTestModel(t, now)
	m, _ = m.startLoop(loopCommand{action: loopActionStart, mode: loopModeFixed, prompt: "poll", interval: time.Minute})
	m.setup.visible = true
	if m.noBlockingModal() {
		t.Logf("note: noBlockingModal=true while setup.visible (part of the defect)")
	}
	got, _ := m.fireDueLoopIfIdle()
	if got.activeLoopID != "" {
		t.Fatalf("S1 REPRO: due loop fired behind setup.visible (activeLoopID=%q)", got.activeLoopID)
	}
	if got.loops[0].nextRunAt.IsZero() {
		t.Fatal("S1 REPRO: skipped loop must stay scheduled")
	}
}

// S2: arming Ctrl+X and then receiving an async permission prompt must cancel
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
		t.Fatalf("S2 REPRO: leader chord opened a picker behind the permission modal (kind=%v)", after.picker.kind)
	}
	if after.pendingPermission == nil {
		t.Fatalf("S2 REPRO: permission modal lost (decideCalled=%v)", decideCalled)
	}
	if after.leaderPending {
		t.Fatal("S2 REPRO: leader still pending after the follow-up key")
	}
}
