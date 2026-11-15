package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// Characterization for M2: event sequences for well-formed streams. Must pass
// before and after the fix without edits.
func TestStreamWellFormedSequencesUnchanged(t *testing.T) {
	eventTypes := func(events []qgruntime.StreamEvent) []qgruntime.StreamEventType {
		types := make([]qgruntime.StreamEventType, 0, len(events))
		for _, e := range events {
			types = append(types, e.Type)
		}
		return types
	}
	equalTypes := func(a, b []qgruntime.StreamEventType) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	t.Run("normal", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeSSE(w, `{"choices":[{"delta":{"content":"hello"}}]}`)
			writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			writeSSE(w, `[DONE]`)
		}))
		t.Cleanup(server.Close)
		events := readAll(mustStream(t, server))
		want := []qgruntime.StreamEventType{qgruntime.StreamEventText, qgruntime.StreamEventDone}
		if got := eventTypes(events); !equalTypes(got, want) {
			t.Fatalf("normal sequence = %v, want %v (%#v)", got, want, events)
		}
	})

	t.Run("toolcalls", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":""}}]}}]}`)
			writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a.go\"}"}}]}}]}`)
			writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
			writeSSE(w, `[DONE]`)
		}))
		t.Cleanup(server.Close)
		events := readAll(mustStream(t, server))
		want := []qgruntime.StreamEventType{
			qgruntime.StreamEventToolCallStart,
			qgruntime.StreamEventToolCallDelta,
			qgruntime.StreamEventToolCallEnd,
			qgruntime.StreamEventDone,
		}
		if got := eventTypes(events); !equalTypes(got, want) {
			t.Fatalf("toolcalls sequence = %v, want %v (%#v)", got, want, events)
		}
	})

	t.Run("length", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeSSE(w, `{"choices":[{"delta":{"content":"cut"}}]}`)
			writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"length"}]}`)
			writeSSE(w, `[DONE]`)
		}))
		t.Cleanup(server.Close)
		events := readAll(mustStream(t, server))
		want := []qgruntime.StreamEventType{qgruntime.StreamEventText, qgruntime.StreamEventDone}
		if got := eventTypes(events); !equalTypes(got, want) {
			t.Fatalf("length sequence = %v, want %v (%#v)", got, want, events)
		}
		var reason string
		for _, e := range events {
			if e.Type == qgruntime.StreamEventDone {
				reason = e.FinishReason
			}
		}
		if reason != qgruntime.FinishReasonLength {
			t.Fatalf("length Done FinishReason = %q, want %q", reason, qgruntime.FinishReasonLength)
		}
	})

	t.Run("length followed by stop keeps length", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeSSE(w, `{"choices":[{"delta":{"content":"cut"}}]}`)
			writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"length"}]}`)
			writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			writeSSE(w, `[DONE]`)
		}))
		t.Cleanup(server.Close)
		events := readAll(mustStream(t, server))
		want := []qgruntime.StreamEventType{qgruntime.StreamEventText, qgruntime.StreamEventDone}
		if got := eventTypes(events); !equalTypes(got, want) {
			t.Fatalf("length-then-stop sequence = %v, want %v (%#v)", got, want, events)
		}
		var reason string
		for _, e := range events {
			if e.Type == qgruntime.StreamEventDone {
				reason = e.FinishReason
			}
		}
		if reason != qgruntime.FinishReasonLength {
			t.Fatalf("length-then-stop Done FinishReason = %q, want %q", reason, qgruntime.FinishReasonLength)
		}
	})

	t.Run("content_filter followed by stop keeps filter", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeSSE(w, `{"choices":[{"delta":{"content":"partial"}}]}`)
			writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"content_filter"}]}`)
			writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
			writeSSE(w, `[DONE]`)
		}))
		t.Cleanup(server.Close)
		events := readAll(mustStream(t, server))
		want := []qgruntime.StreamEventType{qgruntime.StreamEventText, qgruntime.StreamEventDone}
		if got := eventTypes(events); !equalTypes(got, want) {
			t.Fatalf("filter-then-stop sequence = %v, want %v (%#v)", got, want, events)
		}
		var reason string
		for _, e := range events {
			if e.Type == qgruntime.StreamEventDone {
				reason = e.FinishReason
			}
		}
		if reason != qgruntime.FinishReasonContentFilter {
			t.Fatalf("filter-then-stop Done FinishReason = %q, want %q", reason, qgruntime.FinishReasonContentFilter)
		}
	})

	t.Run("eof after finish without done", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeSSE(w, `{"choices":[{"delta":{"content":"complete"}}]}`)
			writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
		}))
		t.Cleanup(server.Close)
		events := readAll(mustStream(t, server))
		want := []qgruntime.StreamEventType{qgruntime.StreamEventText, qgruntime.StreamEventDone}
		if got := eventTypes(events); !equalTypes(got, want) {
			t.Fatalf("eof-after-finish sequence = %v, want %v (%#v)", got, want, events)
		}
		if n := len(eventsOfType(events, qgruntime.StreamEventError)); n != 0 {
			t.Fatalf("eof after finish must not error, got %#v", events)
		}
	})
}

func mustStream(t *testing.T, server *httptest.Server) <-chan qgruntime.StreamEvent {
	t.Helper()
	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	return stream
}
