package tui

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/peermsg"
)

// Repro for M1 bugs: these tests FAIL on untouched dev (fee8c5c).
// They capture the concrete buggy behavior before the PromptPhase machine lands.

func TestReproCancelRunLosesPermissionDraft(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.pending = true
	m.activeRunID = 3
	m.runCancel = func() {}
	m.pendingPermission = &pendingPermissionPrompt{
		request:    agent.PermissionRequest{ToolCallID: "tool-1", ToolName: "exec_command"},
		typing:     true,
		savedDraft: "meu rascunho",
	}
	m.input.SetValue("feedback de negacao")
	m.cancelRun()
	if got := m.input.Value(); got != "meu rascunho" {
		t.Fatalf("cancelRun lost composer draft: input=%q want %q", got, "meu rascunho")
	}
	if m.pendingPermission != nil {
		t.Fatalf("cancelRun should clear pendingPermission")
	}
}

func TestReproPeerExpireKillsForeignPermission(t *testing.T) {
	m := newModel(context.Background(), Options{})
	held := peermsg.InboundMessage{
		ID:   "peer-1",
		Body: "held peer",
		From: peermsg.Peer{Ref: "aaaa"},
	}
	m.peerPendingApproval = &held
	m.pendingPermission = &pendingPermissionPrompt{
		request: agent.PermissionRequest{ToolCallID: "tool-foreign", ToolName: "exec_command"},
	}
	next, _ := m.handlePeerApprovalExpired("peer-1")
	if next.pendingPermission == nil {
		t.Fatalf("peer expire cleared an unrelated tool permission prompt")
	}
	if next.peerPendingApproval != nil {
		t.Fatalf("peerPendingApproval should be cleared for the expired message")
	}
}
