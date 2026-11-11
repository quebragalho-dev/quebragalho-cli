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
