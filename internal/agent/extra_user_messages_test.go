package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// A turn launched with leftover queued messages seeds them as SEPARATE
// consecutive user messages after the first prompt, in order — never merged
// into one text and never dropped. OpenAI-compatible providers accept
// consecutive user messages.
func TestRunSeedsExtraUserMessagesAsSeparateConsecutiveUserMessages(t *testing.T) {
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventText, Content: "done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	result, err := Run(context.Background(), "first prompt", provider, Options{
		Cwd: t.TempDir(),
		ExtraUserMessages: []qgruntime.Message{
			{Role: qgruntime.MessageRoleUser, Content: "second prompt"},
			{Role: qgruntime.MessageRoleUser, Content: "third prompt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) == 0 {
		t.Fatal("expected a provider request")
	}
	messages := provider.requests[0].Messages
	tail := []string{}
	for _, message := range messages {
		if message.Role == qgruntime.MessageRoleUser {
			tail = append(tail, message.Content)
		}
	}
	want := []string{"first prompt", "second prompt", "third prompt"}
	if len(tail) != len(want) {
		t.Fatalf("user messages = %#v, want %#v", tail, want)
	}
	for i := range want {
		if tail[i] != want[i] {
			t.Fatalf("user message %d = %q, want %q (order); all user messages: %#v", i, tail[i], want[i], tail)
		}
	}
	for _, message := range result.Messages {
		if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "second\nthird") {
			t.Fatal("extra user messages must not be merged into one text")
		}
	}
}

// The continuation seed path ([...continuation, bare prompt]) appends the
// extra user messages after the replayed history and the bare prompt, in order.
func TestRunSeedsExtraUserMessagesAfterContinuation(t *testing.T) {
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventText, Content: "done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	continuation := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "frozen system"},
		{Role: qgruntime.MessageRoleUser, Content: "earlier question"},
		{Role: qgruntime.MessageRoleAssistant, Content: "earlier answer"},
	}
	_, err := Run(context.Background(), "first prompt", provider, Options{
		Cwd:          t.TempDir(),
		Continuation: continuation,
		ExtraUserMessages: []qgruntime.Message{
			{Role: qgruntime.MessageRoleUser, Content: "second prompt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := provider.requests[0].Messages
	var flat []string
	for _, message := range messages {
		flat = append(flat, string(message.Role)+":"+message.Content)
	}
	// The prompt-carrying user message may carry a [context update] drift
	// note (the fake frozen system lacks every section), so assert its
	// suffix rather than byte equality. The extra messages must follow it,
	// separate and in order.
	if len(messages) != 5 {
		t.Fatalf("seed has %d messages, want 5: %#v", len(messages), flat)
	}
	if messages[0].Role != qgruntime.MessageRoleSystem || messages[0].Content != "frozen system" {
		t.Fatalf("seed[0] = %#v, want the frozen system", messages[0])
	}
	if messages[1].Content != "earlier question" || messages[2].Content != "earlier answer" {
		t.Fatalf("replayed history = %#v / %#v, want earlier question/answer", messages[1].Content, messages[2].Content)
	}
	if messages[3].Role != qgruntime.MessageRoleUser || !strings.HasSuffix(messages[3].Content, "first prompt") {
		t.Fatalf("seed[3] = %#v, want the prompt-carrying user message", messages[3])
	}
	if messages[4].Role != qgruntime.MessageRoleUser || messages[4].Content != "second prompt" {
		t.Fatalf("seed[4] = %#v, want user:second prompt", messages[4])
	}
}

// Extra messages that are empty or not user-role are dropped, mirroring the
// interleave filter; the run seeds exactly what the wire can carry.
func TestRunSkipsEmptyAndNonUserExtraMessages(t *testing.T) {
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventText, Content: "done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	_, err := Run(context.Background(), "first prompt", provider, Options{
		Cwd: t.TempDir(),
		ExtraUserMessages: []qgruntime.Message{
			{Role: qgruntime.MessageRoleUser, Content: "   "},
			{Role: qgruntime.MessageRoleAssistant, Content: "not a user message"},
			{Role: qgruntime.MessageRoleUser, Content: "second prompt"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := provider.requests[0].Messages
	var userContents []string
	for _, message := range messages {
		if message.Role == qgruntime.MessageRoleUser {
			userContents = append(userContents, message.Content)
		}
	}
	if len(userContents) != 2 || userContents[0] != "first prompt" || userContents[1] != "second prompt" {
		t.Fatalf("user messages = %#v, want [first prompt second prompt]", userContents)
	}
}
