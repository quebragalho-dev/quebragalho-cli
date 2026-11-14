package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// A stream that ends cleanly (EOF) after partial text, WITHOUT [DONE] and
// without any finish_reason, is a truncated transport — not a complete
// answer. It must surface a retryable error and never a Done.
func TestStreamCleanEOFAfterPartialIsTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"PARCIAL"}}]}`)
	}))
	t.Cleanup(server.Close)

	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	var sawError, sawDone bool
	var errText string
	for _, event := range readAll(stream) {
		switch event.Type {
		case qgruntime.StreamEventError:
			sawError = true
			errText = event.Error
		case qgruntime.StreamEventDone:
			sawDone = true
		}
	}
	if sawDone {
		t.Fatal("clean EOF after partial text must not emit Done")
	}
	if !sawError {
		t.Fatal("clean EOF after partial text must emit a transport error")
	}
	if !strings.Contains(strings.ToLower(errText), "eof") {
		t.Fatalf("error must read as a truncated transport failure, got %q", errText)
	}
}

// EOF before any byte is the same truncation, with nothing streamed yet.
func TestStreamEOFBeforeAnyByteIsTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)

	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	var sawError, sawDone bool
	for _, event := range readAll(stream) {
		switch event.Type {
		case qgruntime.StreamEventError:
			sawError = true
		case qgruntime.StreamEventDone:
			sawDone = true
		}
	}
	if sawDone || !sawError {
		t.Fatalf("empty EOF must error, got done=%v err=%v", sawDone, sawError)
	}
}

// A finish_reason of length without [DONE]: the model stopped on the token
// budget — a legitimate, complete (if model-truncated) answer. No error.
func TestStreamLengthFinishWithoutDoneSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"cut by budget"},"finish_reason":"length"}]}`)
	}))
	t.Cleanup(server.Close)

	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	collected := qgruntime.CollectStream(context.Background(), stream)
	if collected.Error != "" {
		t.Fatalf("length finish without [DONE] must not error (no false retry), got %q", collected.Error)
	}
	if collected.Text != "cut by budget" {
		t.Fatalf("text = %q", collected.Text)
	}
}

// A finish_reason of content_filter without [DONE]: same legitimate end.
func TestStreamContentFilterFinishWithoutDoneSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"partial"},"finish_reason":"content_filter"}]}`)
	}))
	t.Cleanup(server.Close)

	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	collected := qgruntime.CollectStream(context.Background(), stream)
	if collected.Error != "" {
		t.Fatalf("content_filter finish without [DONE] must not error, got %q", collected.Error)
	}
}

// A finish_reason of tool_calls without [DONE]: a complete tool-call turn ends
// legitimately; the tool call closes exactly once and no error fires.
func TestStreamToolCallsFinishWithoutDoneSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":""}}]}}]}`)
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"a.go\"}"}}]}}]}`)
		writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`)
	}))
	t.Cleanup(server.Close)

	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	events := readAll(stream)
	if n := len(eventsOfType(events, qgruntime.StreamEventError)); n != 0 {
		t.Fatalf("tool_calls finish without [DONE] must not error, got %d errors: %#v", n, events)
	}
	if n := len(eventsOfType(events, qgruntime.StreamEventToolCallEnd)); n != 1 {
		t.Fatalf("complete tool call must close exactly once, got %d", n)
	}
	if n := len(eventsOfType(events, qgruntime.StreamEventDone)); n != 1 {
		t.Fatalf("tool_calls finish must emit Done, got %d", n)
	}
}

// A tool-call stream cut by clean EOF mid-arguments, WITHOUT finish_reason and
// WITHOUT [DONE]: truncated. The partial call is closed (so nothing dangles),
// an error ends the stream, and Done never fires — so the agent retries the
// whole turn instead of executing a half-written tool call.
func TestStreamToolCallTruncatedByCleanEOFErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"path\":"}}]}}]}`)
	}))
	t.Cleanup(server.Close)

	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	events := readAll(stream)
	if n := len(eventsOfType(events, qgruntime.StreamEventDone)); n != 0 {
		t.Fatalf("truncated tool call must not emit Done, got %d", n)
	}
	if n := len(eventsOfType(events, qgruntime.StreamEventError)); n != 1 {
		t.Fatalf("truncated tool call must end with one transport error, got %d: %#v", n, events)
	}
	if n := len(eventsOfType(events, qgruntime.StreamEventToolCallEnd)); n != 1 {
		t.Fatalf("truncated tool call must be closed exactly once (no dangling start), got %d", n)
	}
}

// A finish_reason without [DONE] is a legitimate end: no error, Done emitted.
func TestStreamFinishReasonWithoutDoneSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"complete"}}]}`)
		writeSSE(w, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(server.Close)

	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	collected := qgruntime.CollectStream(context.Background(), stream)
	if collected.Error != "" {
		t.Fatalf("finish_reason end must not error, got %q", collected.Error)
	}
	if collected.Text != "complete" {
		t.Fatalf("text = %q, want %q", collected.Text, "complete")
	}
}

// [DONE] without finish_reason is a legitimate end: no error, Done emitted.
func TestStreamDoneWithoutFinishReasonSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeSSE(w, `{"choices":[{"delta":{"content":"complete"}}]}`)
		writeSSE(w, `[DONE]`)
	}))
	t.Cleanup(server.Close)

	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	collected := qgruntime.CollectStream(context.Background(), stream)
	if collected.Error != "" {
		t.Fatalf("[DONE] end must not error, got %q", collected.Error)
	}
	if collected.Text != "complete" {
		t.Fatalf("text = %q, want %q", collected.Text, "complete")
	}
}
