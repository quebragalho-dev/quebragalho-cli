package tui

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
)

// A second permission or ask_user prompt arriving while one is already open
// must explicitly resolve the superseded callback (fail-closed Deny for
// permission, nil answers for ask_user) and restore any typing draft so the
// first caller never parks forever and the composer draft is never lost.
func TestPromptReplaceResolvesSupersededPrompt(t *testing.T) {
	m := permissionModel(t)
	m.input.SetValue("rascunho salvo")

	firstCh := make(chan agent.PermissionDecision, 1)
	updated, _ := m.Update(permissionRequestMsg{
		runID: 7,
		request: agent.PermissionRequest{
			ToolCallID: "old-tool",
			ToolName:   "bash",
			Action:     agent.PermissionActionPrompt,
			AvailableDecisions: []agent.PermissionDecisionAction{
				agent.PermissionDecisionAllow,
				agent.PermissionDecisionDeny,
				agent.PermissionDecisionCancel,
			},
		},
		decide: func(d agent.PermissionDecision) { firstCh <- d },
	})
	m = updated.(model)

	// Enter inline feedback typing to verify savedDraft is also preserved across replace.
	chosen, _ := m.choosePermissionOption(permissionDecisionCancel)
	m = chosen.(model)
	m.input.SetValue("feedback parcial")

	secondCh := make(chan agent.PermissionDecision, 1)
	updated, _ = m.Update(permissionRequestMsg{
		runID: 7,
		request: agent.PermissionRequest{
			ToolCallID: "new-tool",
			ToolName:   "write_file",
			Action:     agent.PermissionActionPrompt,
		},
		decide: func(d agent.PermissionDecision) { secondCh <- d },
	})
	m = updated.(model)

	select {
	case got := <-firstCh:
		if got.Action != agent.PermissionDecisionDeny {
			t.Fatalf("superseded permission action = %q, want %q", got.Action, agent.PermissionDecisionDeny)
		}
		if got.Reason == "" {
			t.Fatal("superseded permission decision must carry an explicit reason")
		}
	default:
		t.Fatal("first permission request was left unresolved when replaced by a second")
	}
	select {
	case d := <-secondCh:
		t.Fatalf("second permission request must stay pending for the user, got %#v", d)
	default:
	}
	if m.pendingPermission == nil || m.pendingPermission.request.ToolCallID != "new-tool" {
		t.Fatalf("live permission = %#v, want new-tool", m.pendingPermission)
	}
	if m.input.Value() != "rascunho salvo" {
		t.Fatalf("composer draft after replace = %q, want %q", m.input.Value(), "rascunho salvo")
	}

	// Replacing a live ask_user prompt (with another ask_user or a permission)
	// must also unblock the superseded ask callback.
	oldAnswers := 0
	m2 := permissionModel(t)
	areq := agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q1"}}}
	updated, _ = m2.Update(askUserRequestMsg{runID: 7, request: areq, answer: func([]string) { oldAnswers++ }})
	m2 = updated.(model)

	breq := agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q2"}}}
	updated, _ = m2.Update(askUserRequestMsg{runID: 7, request: breq, answer: func([]string) {}})
	m2 = updated.(model)
	if oldAnswers != 1 {
		t.Fatalf("superseded ask_user answer calls = %d, want 1", oldAnswers)
	}
	if m2.pendingAskUser == nil || m2.pendingAskUser.request.Questions[0].Question != "q2" {
		t.Fatalf("live ask_user = %#v, want q2", m2.pendingAskUser)
	}
}

// Parity: cancelRun drops ask WITHOUT answering; settleEnd answers nil.
func TestPromptCancelRunVsSettleEndAnswerParity(t *testing.T) {
	// cancelRun path: no answer call (pre-machine parity).
	answers := -1
	m := newModel(context.Background(), Options{})
	areq := agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q"}}}
	if err := m.promptTransition(PromptEvOpenAskUser, promptArgs{askReq: &areq, askAnswer: func(a []string) { answers = len(a) }}); err != nil {
		t.Fatal(err)
	}
	if err := m.promptTransition(PromptEvCancelRun, promptArgs{}); err != nil {
		t.Fatal(err)
	}
	if m.pendingAskUser != nil {
		t.Fatal("cancelRun must clear ask")
	}
	if answers != -1 {
		t.Fatalf("cancelRun must not answer (got len %d)", answers)
	}

	// settleEnd path (agentResponseMsg): answers nil once.
	answers2 := -2
	m2 := newModel(context.Background(), Options{})
	if err := m2.promptTransition(PromptEvOpenAskUser, promptArgs{askReq: &areq, askAnswer: func(a []string) { answers2 = len(a) }}); err != nil {
		t.Fatal(err)
	}
	if err := m2.promptTransition(PromptEvSettleEnd, promptArgs{}); err != nil {
		t.Fatal(err)
	}
	if answers2 != 0 {
		t.Fatalf("settleEnd must answer nil (len 0), got %d", answers2)
	}

	// cancelRun via the model method also preserves parity (no answer).
	answers3 := -1
	m3 := newModel(context.Background(), Options{})
	m3.pending = true
	m3.activeRunID = 9
	m3.runCancel = func() {}
	m3.pendingAskUser = &pendingAskUserPrompt{request: areq, answer: func(a []string) { answers3 = len(a) }}
	m3.cancelRun()
	if answers3 != -1 {
		t.Fatalf("model cancelRun must not answer ask, got %d", answers3)
	}
}
