package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// The operator took the message with Cmd+Enter mid-turn: it must ride the
// wire to the very next provider request — after this step's tool result,
// before the model answers — without the turn being interrupted.
func TestRunInjectsInterleavedMessagesBeforeNextRequest(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(stepEchoTool{})
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "c1", ToolName: "step_echo"},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "c1"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	called := 0
	result, err := Run(context.Background(), "start", provider, Options{
		Registry: registry,
		Cwd:      t.TempDir(),
		InterleaveMessages: func() []qgruntime.Message {
			called++
			if called == 1 {
				return []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "operator interleaves now"}}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if called == 0 {
		t.Fatal("InterleaveMessages was never called")
	}
	found := false
	for _, message := range result.Messages {
		if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "operator interleaves now") {
			found = true
		}
	}
	if !found {
		t.Fatalf("interleaved message must ride the run's wire: %#v", result.Messages)
	}
	var withInterleave *qgruntime.CompletionRequest
	for index := range provider.requests {
		req := &provider.requests[index]
		for _, message := range req.Messages {
			if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "operator interleaves now") {
				withInterleave = req
				break
			}
		}
		if withInterleave != nil {
			break
		}
	}
	if withInterleave == nil {
		t.Fatal("the next request after the tool result must include the interleaved message")
	}
	// The wire shape stays valid: the interleaved message lands after the
	// tool result and before the final answer.
	roles := make([]qgruntime.MessageRole, 0, len(result.Messages))
	for _, message := range result.Messages {
		roles = append(roles, message.Role)
	}
	lastTool := -1
	interleaveIndex := -1
	for index, message := range result.Messages {
		switch message.Role {
		case qgruntime.MessageRoleTool:
			lastTool = index
		case qgruntime.MessageRoleUser:
			if strings.Contains(message.Content, "operator interleaves now") {
				interleaveIndex = index
			}
		}
	}
	if lastTool < 0 || interleaveIndex < lastTool {
		t.Fatalf("interleaved message must land after this step's tool result, got messages %#v", result.Messages)
	}
}
