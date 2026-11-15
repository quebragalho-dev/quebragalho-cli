package tui

// A Ctrl+X chord armed before an async prompt must be cancelled when the
// prompt opens, so the follow-up key reaches the modal instead of resolving
// a stale leader slash behind it. Each case below fails on the branch without
// the promptTransition fix (the chord stays armed and the follow-up key opens
// the model picker behind the card) and passes with it.
//
// The last test pins the inverse: with no prompt in between, Ctrl+X followed
// by the same key keeps opening the model picker.
//
// Hermetic: QG_CRED_STORAGE=file, injected clock, no time.Sleep.

import (
	"context"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/peermsg"
)

func reproTestModel(t *testing.T, now time.Time) model {
	t.Helper()
	t.Setenv("QG_CRED_STORAGE", "file")
	m := newModel(context.Background(), Options{})
	m.now = func() time.Time { return now }
	return m
}

func armLeaderChord(t *testing.T, m model) (model, int) {
	t.Helper()
	updated, _ := m.Update(testKeyCtrl('x'))
	m = updated.(model)
	if !m.leaderPending {
		t.Fatal("Ctrl+X must arm leaderPending")
	}
	return m, m.leaderSeq
}

// A stale leader timeout (old seq) must be a no-op: the chord is already gone
// and the modal stays exactly as it was.
func assertStaleLeaderTickNoop(t *testing.T, m model, oldSeq int) model {
	t.Helper()
	updated, _ := m.Update(leaderExpiredMsg{seq: oldSeq})
	after := updated.(model)
	if after.leaderPending {
		t.Fatal("stale leader tick must not re-arm the chord")
	}
	return after
}

func TestLeaderChordCancelledByAsyncPermission(t *testing.T) {
	now := time.Now()
	m := reproTestModel(t, now)
	m, oldSeq := armLeaderChord(t, m)
	decideCalled := false
	m.activeRunID = 7
	updated, _ := m.Update(permissionRequestMsg{
		runID:   7,
		request: agent.PermissionRequest{ToolCallID: "tc1", ToolName: "bash", Action: agent.PermissionActionPrompt},
		decide:  func(agent.PermissionDecision) { decideCalled = true },
	})
	m = updated.(model)
	if m.pendingPermission == nil {
		t.Fatal("permission prompt must be live")
	}
	if m.leaderPending {
		t.Fatal("opening the permission prompt must cancel the armed leader chord")
	}
	updated, _ = m.Update(testKeyText("m"))
	after := updated.(model)
	if after.picker != nil {
		t.Fatalf("follow-up key must reach the permission card, not open a picker behind it (kind=%v)", after.picker.kind)
	}
	if after.pendingPermission == nil {
		t.Fatalf("permission modal must stay live (decideCalled=%v)", decideCalled)
	}
	if after.leaderPending {
		t.Fatal("leader must stay cleared after the follow-up key")
	}
	after = assertStaleLeaderTickNoop(t, after, oldSeq)
	if after.pendingPermission == nil {
		t.Fatal("stale leader tick must leave the permission modal alone")
	}
}

func TestLeaderChordCancelledByAsyncAskUser(t *testing.T) {
	now := time.Now()
	m := reproTestModel(t, now)
	m, oldSeq := armLeaderChord(t, m)
	answered := false
	m.activeRunID = 7
	req := agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "Pick one?"}}}
	updated, _ := m.Update(askUserRequestMsg{
		runID:   7,
		request: req,
		answer:  func([]string) { answered = true },
	})
	m = updated.(model)
	if m.pendingAskUser == nil {
		t.Fatal("ask-user prompt must be live")
	}
	if m.leaderPending {
		t.Fatal("opening the ask-user prompt must cancel the armed leader chord")
	}
	updated, _ = m.Update(testKeyText("m"))
	after := updated.(model)
	if after.picker != nil {
		t.Fatalf("follow-up key must reach the ask-user card, not open a picker behind it (kind=%v)", after.picker.kind)
	}
	if after.pendingAskUser == nil {
		t.Fatalf("ask-user modal must stay live (answered=%v)", answered)
	}
	if after.leaderPending {
		t.Fatal("leader must stay cleared after the follow-up key")
	}
	after = assertStaleLeaderTickNoop(t, after, oldSeq)
	if after.pendingAskUser == nil {
		t.Fatal("stale leader tick must leave the ask-user modal alone")
	}
}

func TestLeaderChordCancelledByAsyncSpecReview(t *testing.T) {
	now := time.Now()
	m := reproTestModel(t, now)
	m, oldSeq := armLeaderChord(t, m)
	// Production opens spec review through the session store; the unit under
	// fix is promptTransition, the same seam the prompt machine tests use.
	review := pendingSpecReviewPrompt{SpecID: "s1", SpecTitle: "t", SpecFilePath: "p", RelativePath: "r", DraftSessionID: "d"}
	if err := m.promptTransition(PromptEvOpenSpecReview, promptArgs{spec: &review}); err != nil {
		t.Fatalf("open spec review: %v", err)
	}
	if m.pendingSpecReview == nil {
		t.Fatal("spec review must be live")
	}
	if m.leaderPending {
		t.Fatal("opening the spec review must cancel the armed leader chord")
	}
	updated, _ := m.Update(testKeyText("m"))
	after := updated.(model)
	if after.picker != nil {
		t.Fatalf("follow-up key must reach the spec review card, not open a picker behind it (kind=%v)", after.picker.kind)
	}
	if after.pendingSpecReview == nil {
		t.Fatal("spec review modal must stay live")
	}
	if after.leaderPending {
		t.Fatal("leader must stay cleared after the follow-up key")
	}
	after = assertStaleLeaderTickNoop(t, after, oldSeq)
	if after.pendingSpecReview == nil {
		t.Fatal("stale leader tick must leave the spec review modal alone")
	}
}

func TestLeaderChordCancelledByAsyncPeerApproval(t *testing.T) {
	now := time.Now()
	m := reproTestModel(t, now)
	m, oldSeq := armLeaderChord(t, m)
	message := peermsg.InboundMessage{
		ID:               "peer-1",
		From:             peermsg.Peer{Identity: peermsg.Identity{Name: "Peer"}, Ref: "11223344"},
		Body:             "Continue the task.",
		RequiresApproval: true,
		HoldCause:        peermsg.HoldCauseExplicit,
	}
	next, _ := m.handlePeerMessage(message)
	m = next
	if m.pendingPermission == nil || m.peerPendingApproval == nil {
		t.Fatal("peer approval card must be live")
	}
	if m.leaderPending {
		t.Fatal("opening the peer approval must cancel the armed leader chord")
	}
	updated, _ := m.Update(testKeyText("m"))
	after := updated.(model)
	if after.picker != nil {
		t.Fatalf("follow-up key must reach the peer card, not open a picker behind it (kind=%v)", after.picker.kind)
	}
	if after.pendingPermission == nil {
		t.Fatal("peer approval modal must stay live")
	}
	if after.leaderPending {
		t.Fatal("leader must stay cleared after the follow-up key")
	}
	after = assertStaleLeaderTickNoop(t, after, oldSeq)
	if after.pendingPermission == nil {
		t.Fatal("stale leader tick must leave the peer approval modal alone")
	}
}

// With no prompt arriving mid-chord, Ctrl+X followed by m keeps opening the
// model picker. The cancellation above must not break the plain chord path.
func TestLeaderChordSlashStillOpensModelPicker(t *testing.T) {
	now := time.Now()
	m := reproTestModel(t, now)
	m, _ = armLeaderChord(t, m)
	updated, _ := m.Update(testKeyText("m"))
	after := updated.(model)
	if after.leaderPending {
		t.Fatal("follow-up key must resolve the chord")
	}
	if after.picker == nil {
		t.Fatal("Ctrl+X m with no prompt in between must open the model picker")
	}
	if after.picker.kind != pickerModel {
		t.Fatalf("Ctrl+X m must open the model picker, got kind %v", after.picker.kind)
	}
}
