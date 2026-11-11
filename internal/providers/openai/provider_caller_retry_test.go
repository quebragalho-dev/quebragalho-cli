package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/providers/providerio"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func flakyStatusServer(t *testing.T, failures int32, status int, headers map[string]string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&hits, 1) <= failures {
			for key, value := range headers {
				w.Header().Set(key, value)
			}
			w.WriteHeader(status)
			return
		}
		writeSSE(w, `{"choices":[{"delta":{"content":"hi"}}]}`)
		writeSSE(w, `[DONE]`)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func testProvider(t *testing.T, server *httptest.Server) *Provider {
	t.Helper()
	provider, err := New(Options{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "test-model",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return provider
}

// Outside the agent loop (no caller-retry marker) the provider keeps its
// internal transport/status retries: title calls and friends behave as today.
func TestStreamCompletionWithoutMarkerKeepsInternalRetry(t *testing.T) {
	server, hits := flakyStatusServer(t, 2, http.StatusServiceUnavailable, nil)
	stream, err := testProvider(t, server).StreamCompletion(context.Background(), qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	collected := qgruntime.CollectStream(context.Background(), stream)
	if collected.Error != "" {
		t.Fatalf("two 503s must be absorbed internally, got %q", collected.Error)
	}
	if collected.Text != "hi" {
		t.Fatalf("text = %q, want %q", collected.Text, "hi")
	}
	if got := atomic.LoadInt32(hits); got != 3 {
		t.Fatalf("hits = %d, want 3 (2 internal retries + success)", got)
	}
}

// Under the caller-retry marker the provider issues one attempt and hands the
// 503 back: the agent loop owns the wait and the budget.
func TestStreamCompletionWithMarkerMakesOneAttempt(t *testing.T) {
	server, hits := flakyStatusServer(t, 99, http.StatusServiceUnavailable, nil)
	ctx := providerio.WithCallerRetry(context.Background())
	stream, err := testProvider(t, server).StreamCompletion(ctx, qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	collected := qgruntime.CollectStream(ctx, stream)
	if collected.Error == "" {
		t.Fatal("want the 503 surfaced, got success")
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("hits = %d, want exactly 1", got)
	}
}

// The 429's Retry-After header travels structured on the error event so the
// agent waits the server's delay instead of re-parsing text.
func TestStreamCompletionErrorCarriesStructuredRetryAfter(t *testing.T) {
	server, _ := flakyStatusServer(t, 99, http.StatusTooManyRequests, map[string]string{"Retry-After": "12"})
	ctx := providerio.WithCallerRetry(context.Background())
	stream, err := testProvider(t, server).StreamCompletion(ctx, qgruntime.CompletionRequest{})
	if err != nil {
		t.Fatalf("StreamCompletion: %v", err)
	}
	events := readAll(stream)
	var statusErr *providerio.StatusError
	for _, event := range events {
		if event.Type == qgruntime.StreamEventError && errors.As(event.Err, &statusErr) {
			break
		}
	}
	if statusErr == nil {
		t.Fatalf("error event must carry a typed status error, got %#v", events)
	}
	if statusErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", statusErr.StatusCode)
	}
	if statusErr.RetryAfter != 12*time.Second {
		t.Fatalf("retryAfter = %v, want 12s", statusErr.RetryAfter)
	}
}
