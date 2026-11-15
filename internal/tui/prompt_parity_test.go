package tui

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
)

// Parity with pre-machine behavior: a new tool/ask request overwrote the live
// prompt without resolving the old callback. Registered as an open item.
func TestPromptReplaceKeepsOldCallbackUnresolved(t *testing.T) {
	oldDecides := 0
	m := newModel(context.Background(), Options{})
	oldReq := agent.PermissionRequest{ToolCallID: "old-tool"}
	if err := m.promptTransition(PromptEvOpenPermission, promptArgs{permReq: &oldReq, permDecide: func(agent.PermissionDecision) { oldDecides++ }}); err != nil {
		t.Fatal(err)
	}
	newReq := agent.PermissionRequest{ToolCallID: "new-tool"}
	if err := m.promptTransition(PromptEvReplacePermission, promptArgs{permReq: &newReq, permDecide: func(agent.PermissionDecision) {}}); err != nil {
		t.Fatalf("replace must be legal: %v", err)
	}
	if oldDecides != 0 {
		t.Fatalf("old decide calls=%d want 0 (leak preserved for parity)", oldDecides)
	}
	if m.pendingPermission == nil || m.pendingPermission.request.ToolCallID != "new-tool" {
		t.Fatal("new permission must replace the live one")
	}

	oldAnswers := 0
	m2 := newModel(context.Background(), Options{})
	areq := agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q"}}}
	if err := m2.promptTransition(PromptEvOpenAskUser, promptArgs{askReq: &areq, askAnswer: func([]string) { oldAnswers++ }}); err != nil {
		t.Fatal(err)
	}
	breq := agent.AskUserRequest{Questions: []agent.AskUserQuestion{{Question: "q2"}}}
	if err := m2.promptTransition(PromptEvReplaceAskUser, promptArgs{askReq: &breq, askAnswer: func([]string) {}}); err != nil {
		t.Fatalf("replace ask must be legal: %v", err)
	}
	if oldAnswers != 0 {
		t.Fatalf("old answer calls=%d want 0", oldAnswers)
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
