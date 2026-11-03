package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// A provider that sends events + usage + [DONE] but keeps the HTTP body open
// must not hang the turn: [DONE] ends the stream without waiting for EOF.
func TestStreamCompletionEndsOnDoneWithoutClose(t *testing.T) {
	t.Parallel()
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"hello "}}]}`)
		writeSSE(w, `{"choices":[{"delta":{"content":"world"}}],"usage":{"prompt_tokens":12,"completion_tokens":5}}`)
		writeSSE(w, `[DONE]`)
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer server.Close()
	defer close(released)

	provider, err := New(Options{BaseURL: server.URL + "/", Model: "gpt-test", StreamIdleTimeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := provider.StreamCompletion(ctx, qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	done := make(chan []qgruntime.StreamEvent, 1)
	go func() { done <- readAll(stream) }()
	var events []qgruntime.StreamEvent
	select {
	case events = <-done:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("stream did not end after [DONE] without EOF")
	}

	var text string
	var usage qgruntime.Usage
	var sawUsage, sawDone, sawError bool
	for _, e := range events {
		switch e.Type {
		case qgruntime.StreamEventText:
			text += e.Content
		case qgruntime.StreamEventUsage:
			sawUsage = true
			usage = e.Usage
		case qgruntime.StreamEventDone:
			sawDone = true
		case qgruntime.StreamEventError:
			sawError = true
		}
	}
	if text != "hello world" {
		t.Fatalf("text = %q, want %q", text, "hello world")
	}
	if !sawUsage || usage.PromptTokens != 12 || usage.CompletionTokens != 5 {
		t.Fatalf("usage lost: saw=%v %+v", sawUsage, usage)
	}
	if !sawDone {
		t.Fatalf("no done event: %+v", events)
	}
	if sawError {
		t.Fatalf("unexpected error: %+v", events)
	}
}
