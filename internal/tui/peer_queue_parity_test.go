package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/peermsg"
)

// Parity: queued peer expire/release removes the queue entry in EVERY prompt
// phase, with the same transcript line and receipt as before. These tests use
// only pre-machine APIs (no PromptPhase/promptTransition) so they compile and
// pass on untouched dev fee8c5c.
func modelInPhaseForPeerParity(t *testing.T, phase string, queued peermsg.InboundMessage) model {
	t.Helper()
	m := newModel(context.Background(), Options{})
	m.peerApprovalQueue = []peermsg.InboundMessage{queued}
	switch phase {
	case "none":
	case "permission":
		m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "tool-x"}}
	case "permissionTyping":
		m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "tool-x"}, typing: true, savedDraft: "draft"}
		m.input.SetValue("feedback")
	case "askUser":
		m.pendingAskUser = &pendingAskUserPrompt{request: agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q"}}}, states: newAskUserStates([]agent.AskUserQuestion{{Question: "q"}})}
	case "specReview":
		m.pendingSpecReview = &pendingSpecReviewPrompt{SpecID: "s"}
	case "peerApproval":
		pending := peermsg.InboundMessage{ID: "pending-peer", Body: "pending"}
		m.peerPendingApproval = &pending
		m.pendingPermission = &pendingPermissionPrompt{request: agent.PermissionRequest{ToolCallID: "peer-pending-peer", ToolName: peerPermissionToolName}}
	default:
		t.Fatalf("unknown phase %s", phase)
	}
	return m
}

func TestPeerQueuedExpireParityPerPhase(t *testing.T) {
	phases := []string{"none", "permission", "permissionTyping", "askUser", "specReview", "peerApproval"}
	for _, phase := range phases {
		queued := peermsg.InboundMessage{ID: "queued-expire-1", Body: "queued body", From: peermsg.Peer{Ref: "abcd"}}
		m := modelInPhaseForPeerParity(t, phase, queued)
		beforeTranscript := len(m.transcript)
		next, cmd := m.handlePeerApprovalExpired(queued.ID)
		for _, q := range next.peerApprovalQueue {
			if q.ID == queued.ID {
				t.Fatalf("phase %s: queued expire must remove entry", phase)
			}
		}
		if len(next.transcript) != beforeTranscript+1 {
			t.Fatalf("phase %s: expire must append exactly one transcript row", phase)
		}
		last := next.transcript[len(next.transcript)-1]
		if !strings.Contains(last.text, "expired without approval") {
			t.Fatalf("phase %s: transcript=%q", phase, last.text)
		}
		// Receipt parity: with nil service both old and new return nil cmd.
		if (cmd == nil) != (m.peerService == nil) {
			t.Fatalf("phase %s: receipt cmd nil=%v", phase, cmd == nil)
		}
	}
}

func TestPeerQueuedReleaseParityPerPhase(t *testing.T) {
	phases := []string{"none", "permission", "permissionTyping", "askUser", "specReview", "peerApproval"}
	for _, phase := range phases {
		queued := peermsg.InboundMessage{ID: "queued-release-1", Body: "release me"}
		m := modelInPhaseForPeerParity(t, phase, queued)
		released := queued
		released.RequiresApproval = false
		next, _ := m.handleReleasedPeerMessage(released)
		for _, q := range next.peerApprovalQueue {
			if q.ID == queued.ID {
				t.Fatalf("phase %s: queued release must remove entry", phase)
			}
		}
	}
}
