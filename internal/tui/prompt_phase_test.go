package tui

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/peermsg"
)

// Table test: every (phase x event) fixes which transitions are legal.
// Mirrors promptLegal; any change to the machine must update this table.
func TestPromptPhaseTableAllCombinations(t *testing.T) {
	allPhases := []PromptPhase{PromptNone, PromptPermission, PromptPermissionTyping, PromptAskUser, PromptSpecReview, PromptPeerApproval}
	allEvents := []PromptEvent{
		PromptEvOpenPermission, PromptEvOpenAskUser, PromptEvOpenSpecReview, PromptEvOpenPeer,
		PromptEvReplacePermission, PromptEvReplaceAskUser,
		PromptEvEnterTyping, PromptEvCancelTyping, PromptEvResolvePermission,
		PromptEvResolveAskUser, PromptEvResolveSpecReview, PromptEvCancelRun, PromptEvSettleEnd,
		PromptEvExpirePeer, PromptEvReleasePeer,
	}
	expected := map[PromptPhase]map[PromptEvent]bool{
		PromptNone: {
			PromptEvOpenPermission: true, PromptEvOpenAskUser: true,
			PromptEvOpenSpecReview: true, PromptEvOpenPeer: true,
			PromptEvCancelRun: true, PromptEvSettleEnd: true,
			PromptEvExpirePeer: true, PromptEvReleasePeer: true,
		},
		PromptPermission: {
			PromptEvReplacePermission: true, PromptEvReplaceAskUser: true,
			PromptEvEnterTyping: true, PromptEvResolvePermission: true,
			PromptEvCancelRun: true, PromptEvSettleEnd: true,
			PromptEvExpirePeer: true, PromptEvReleasePeer: true,
		},
		PromptPermissionTyping: {
			PromptEvReplacePermission: true, PromptEvReplaceAskUser: true,
			PromptEvCancelTyping: true, PromptEvResolvePermission: true,
			PromptEvCancelRun: true, PromptEvSettleEnd: true,
			PromptEvExpirePeer: true, PromptEvReleasePeer: true,
		},
		PromptAskUser: {
			PromptEvReplacePermission: true, PromptEvReplaceAskUser: true,
			PromptEvResolveAskUser: true,
			PromptEvCancelRun:      true, PromptEvSettleEnd: true,
			PromptEvExpirePeer: true, PromptEvReleasePeer: true,
		},
		PromptSpecReview: {
			PromptEvReplacePermission: true, PromptEvReplaceAskUser: true,
			PromptEvResolveSpecReview: true,
			PromptEvCancelRun:         true, PromptEvSettleEnd: true,
			PromptEvExpirePeer: true, PromptEvReleasePeer: true,
		},
		PromptPeerApproval: {
			PromptEvReplacePermission: true, PromptEvReplaceAskUser: true,
			PromptEvResolvePermission: true,
			PromptEvCancelRun:         true, PromptEvSettleEnd: true,
			PromptEvExpirePeer: true, PromptEvReleasePeer: true,
		},
	}
	for _, phase := range allPhases {
		for _, ev := range allEvents {
			want := expected[phase][ev]
			if got := promptEventLegal(phase, ev); got != want {
				t.Errorf("phase %s event %s: legal=%v want %v", phase, ev, got, want)
			}
		}
	}
}

func modelInPromptPhase(t *testing.T, phase PromptPhase) model {
	t.Helper()
	m := newModel(context.Background(), Options{})
	switch phase {
	case PromptPermission:
		m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "t"}}
	case PromptPermissionTyping:
		m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "t"}, typing: true, savedDraft: "d"}
		m.input.SetValue("feedback")
	case PromptAskUser:
		m.pendingAskUser = &pendingAskUserPrompt{request: agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q"}}}, states: newAskUserStates([]agent.AskUserQuestion{{Question: "q"}})}
	case PromptSpecReview:
		m.pendingSpecReview = &pendingSpecReviewPrompt{SpecID: "s"}
	case PromptPeerApproval:
		msg := peermsg.InboundMessage{ID: "p1"}
		m.peerPendingApproval = &msg
		m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "peer-p1", ToolName: peerPermissionToolName}}
	}
	if got := m.promptPhase(); got != phase {
		t.Fatalf("setup phase=%s got %s", phase, got)
	}
	return m
}

func TestPromptIllegalTransitionKeepsState(t *testing.T) {
	// Opening a second prompt while busy is the critical illegal case.
	m := modelInPromptPhase(t, PromptPermission)
	before := m.promptPhase()
	err := m.promptTransition(PromptEvOpenAskUser, promptArgs{askReq: &agent.AskUserRequest{}})
	if err == nil {
		t.Fatal("second open should be illegal")
	}
	if m.promptPhase() != before || m.pendingPermission == nil {
		t.Fatal("illegal transition must keep state")
	}
	// Expire of an unknown peer id is legal but a no-op.
	m2 := newModel(context.Background(), Options{})
	if err := m2.promptTransition(PromptEvExpirePeer, promptArgs{expireID: "nope"}); err != nil {
		t.Fatalf("expire unknown should be legal no-op: %v", err)
	}
}

func TestPromptDecideAnswerExactlyOnce(t *testing.T) {
	decides := 0
	m := newModel(context.Background(), Options{})
	req := agent.PermissionRequest{ToolCallID: "t1"}
	if err := m.promptTransition(PromptEvOpenPermission, promptArgs{permReq: &req, permDecide: func(agent.PermissionDecision) { decides++ }}); err != nil {
		t.Fatal(err)
	}
	if err := m.promptTransition(PromptEvResolvePermission, promptArgs{decision: permissionDecisionAllow, reason: "ok"}); err != nil {
		t.Fatal(err)
	}
	if decides != 1 {
		t.Fatalf("decide calls=%d want 1", decides)
	}
	// Second resolve has no prompt: illegal, no second call.
	if err := m.promptTransition(PromptEvResolvePermission, promptArgs{decision: permissionDecisionAllow}); err == nil {
		t.Fatal("second resolve should be illegal")
	}
	if decides != 1 {
		t.Fatalf("decide calls after double resolve=%d want 1", decides)
	}

	answers := 0
	m2 := newModel(context.Background(), Options{})
	areq := agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q"}}}
	if err := m2.promptTransition(PromptEvOpenAskUser, promptArgs{askReq: &areq, askAnswer: func([]string) { answers++ }}); err != nil {
		t.Fatal(err)
	}
	if err := m2.promptTransition(PromptEvResolveAskUser, promptArgs{answers: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if answers != 1 {
		t.Fatalf("answer calls=%d want 1", answers)
	}
	if err := m2.promptTransition(PromptEvResolveAskUser, promptArgs{}); err == nil {
		t.Fatal("second answer should be illegal")
	}
	if answers != 1 {
		t.Fatalf("answer calls after double=%d want 1", answers)
	}
}

// Spec review survives cancelRun by design: it is an idle modal, not part of
// the run. This test proves M1 item (3) is intentional, so no fix is applied.
func TestPromptSpecReviewSurvivesCancelRun(t *testing.T) {
	m := newModel(context.Background(), Options{})
	review := pendingSpecReviewPrompt{SpecID: "s1"}
	if err := m.promptTransition(PromptEvOpenSpecReview, promptArgs{spec: &review}); err != nil {
		t.Fatal(err)
	}
	if err := m.promptTransition(PromptEvCancelRun, promptArgs{}); err != nil {
		t.Fatal(err)
	}
	if m.promptPhase() != PromptSpecReview || m.pendingSpecReview == nil {
		t.Fatal("spec review must survive cancelRun")
	}
	if err := m.promptTransition(PromptEvSettleEnd, promptArgs{}); err != nil {
		t.Fatal(err)
	}
	if m.promptPhase() != PromptSpecReview || m.pendingSpecReview == nil {
		t.Fatal("spec review must survive settleEnd")
	}
	// And cancelRun (which routes through CancelAll) keeps it too.
	m.pending = false
	m.activeRunID = 0
	m.cancelRun()
	if m.pendingSpecReview == nil {
		t.Fatal("cancelRun must not clear a spec review")
	}
}

func TestPromptGatesConsultPhase(t *testing.T) {
	m := newModel(context.Background(), Options{})
	if m.promptPhase() != PromptNone || !m.noBlockingModal() {
		t.Fatal("idle should not block")
	}
	m.pendingAskUser = &pendingAskUserPrompt{request: agent.AskUserRequest{}}
	if m.promptPhase() != PromptAskUser || m.noBlockingModal() {
		t.Fatal("askUser must block via phase")
	}
	if m.loopBusy() == false {
		// loopBusy is true when idle? No: pending false, no queue, no compact,
		// but modal blocks -> busy true.
		t.Fatal("loopBusy must see the prompt phase")
	}
}
