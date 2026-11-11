package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/providers/openai"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providers/providerio"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/trace"
)

func testOpenAIProvider(t *testing.T, server *httptest.Server) *openai.Provider {
	t.Helper()
	provider, err := openai.New(openai.Options{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Model:      "test-model",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("openai.New: %v", err)
	}
	return provider
}

// A persistent 503 through a real provider and real HTTP: the loop budget is
// the ONLY budget — exactly 10 requests in total, never 10 x hidden retries.
func TestRunPersistent503MakesExactlyTenHTTPRequests(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	var waits []time.Duration
	var notices []string
	_, err := Run(context.Background(), "go", testOpenAIProvider(t, server), Options{
		Registry:    tools.NewRegistry(),
		RetrySleep:  instantRetrySleep(&waits),
		OnReasoning: func(s string) { notices = append(notices, s) },
	})
	if err == nil {
		t.Fatal("persistent 503 must surface an error")
	}
	if got := atomic.LoadInt32(&hits); got != 10 {
		t.Fatalf("want exactly 10 HTTP requests in total (one budget, no hidden layer), got %d", got)
	}
	if len(waits) != 9 {
		t.Fatalf("want 9 waits, got %d", len(waits))
	}
	if !strings.Contains(err.Error(), "10/10 tentativas") {
		t.Fatalf("exhausted error must carry the attempt count, got %q", err.Error())
	}
	if !strings.Contains(strings.Join(notices, ""), "503") {
		t.Fatalf("retry notices must name the failure, got %q", notices)
	}
}

// A 429 with Retry-After: 12 travels structured from the response header to
// the wait — 12s, not the 7.5s schedule.
func TestRun429RetryAfterHeaderDrivesWait(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	var waits []time.Duration
	_, err := Run(context.Background(), "go", testOpenAIProvider(t, server), Options{
		Registry:   tools.NewRegistry(),
		RetrySleep: instantRetrySleep(&waits),
	})
	if err == nil {
		t.Fatal("persistent 429 must surface an error")
	}
	if got := atomic.LoadInt32(&hits); got != 10 {
		t.Fatalf("want exactly 10 HTTP requests, got %d", got)
	}
	if len(waits) != 9 {
		t.Fatalf("want 9 waits, got %d", len(waits))
	}
	for i, wait := range waits {
		// Structured 12s base with the production ±10% jitter: every wait
		// must sit in [10.8s, 13.2s]. The 7.5s schedule (max 8.25s with
		// jitter) can never land here, so the band proves the structured
		// Retry-After drove the wait.
		if wait < 10800*time.Millisecond || wait > 13200*time.Millisecond {
			t.Fatalf("wait %d = %v, want the structured Retry-After 12s ±10%%", i, wait)
		}
	}
}

// The structured Retry-After wins over any text hint; errors.As recovers it
// through the wrap chain.
func TestWaitAfterErrorPrefersStructuredRetryAfter(t *testing.T) {
	var waits []time.Duration
	retrier := newModelCallRetrier(Options{RetrySleep: instantRetrySleep(&waits)})
	retrier.jitter = func(d time.Duration) time.Duration { return d }
	wrapped := fmt.Errorf("collect: %w", &providerio.StatusError{
		StatusCode: 429,
		RetryAfter: 45 * time.Second,
		Message:    "rate limit error: slow down, retry after 5",
	})
	proceed, err := retrier.waitAfterError(context.Background(), wrapped, false)
	if err != nil || !proceed {
		t.Fatalf("structured retryable must proceed, got proceed=%v err=%v", proceed, err)
	}
	if len(waits) != 1 || waits[0] != 45*time.Second {
		t.Fatalf("waits = %v, want [45s] from the structured field, not the text hint", waits)
	}
}

// Without a structured value the text hint still applies (fallback).
func TestWaitAfterErrorFallsBackToTextHint(t *testing.T) {
	var waits []time.Duration
	retrier := newModelCallRetrier(Options{RetrySleep: instantRetrySleep(&waits)})
	retrier.jitter = func(d time.Duration) time.Duration { return d }
	proceed, err := retrier.waitAfterError(context.Background(), errors.New("503 Service Unavailable, retry after 30"), false)
	if err != nil || !proceed {
		t.Fatalf("hinted retryable must proceed, got proceed=%v err=%v", proceed, err)
	}
	if len(waits) != 1 || waits[0] != 30*time.Second {
		t.Fatalf("waits = %v, want [30s] from the text fallback", waits)
	}
}

// Every model-call attempt stamps model_requests and every scheduled retry
// stamps reconnect_count, so traces and the turn bench keep reporting them.
func TestAutoRetryStampsTraceCounters(t *testing.T) {
	exhausted := &countingFailProvider{err: errors.New("503 Service Unavailable")}
	rec := trace.NewRecorder("s", "r", "t")
	var waits []time.Duration
	_, _ = Run(context.Background(), "go", exhausted, Options{
		Registry:   tools.NewRegistry(),
		RetrySleep: instantRetrySleep(&waits),
		Trace:      rec,
	})
	exhaustedTrace := rec.Finish()
	if got := exhaustedTrace.Counter(trace.CounterModelRequests); got != 10 {
		t.Fatalf("model_requests = %d, want 10 attempts", got)
	}
	if got := exhaustedTrace.Counter(trace.CounterReconnectCount); got != 9 {
		t.Fatalf("reconnect_count = %d, want 9 scheduled retries", got)
	}

	recovered := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{
			{Type: qgruntime.StreamEventText, Content: "done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	rec2 := trace.NewRecorder("s", "r", "t")
	var waits2 []time.Duration
	if _, err := Run(context.Background(), "go", recovered, Options{
		Registry:   tools.NewRegistry(),
		RetrySleep: instantRetrySleep(&waits2),
		Trace:      rec2,
	}); err != nil {
		t.Fatalf("recovery run: %v", err)
	}
	recoveredTrace := rec2.Finish()
	if got := recoveredTrace.Counter(trace.CounterModelRequests); got != 3 {
		t.Fatalf("model_requests = %d, want 3 attempts", got)
	}
	if got := recoveredTrace.Counter(trace.CounterReconnectCount); got != 2 {
		t.Fatalf("reconnect_count = %d, want 2 scheduled retries", got)
	}
}

// collected.Err carries the provider's typed error: classification and the
// wait both key off the status code, not re-parsed text.
func TestCollectedTypedErrorClassifiesByStatus(t *testing.T) {
	ctx := context.Background()
	if !isRetryableModelError(ctx, &providerio.StatusError{StatusCode: 503, Message: "Service Unavailable"}) {
		t.Error("typed 503 must retry")
	}
	if isRetryableModelError(ctx, &providerio.StatusError{StatusCode: 400, Message: "Bad Request"}) {
		t.Error("typed 400 must not retry")
	}
	if isRetryableModelError(ctx, &providerio.StatusError{StatusCode: 429, Message: "rate limit error: ...insufficient_quota..."}) {
		t.Error("billing exhaustion mapped to 429 must not retry")
	}
	if isRetryableModelError(ctx, &providerio.StatusError{StatusCode: 429, Message: "insufficient credits, top up"}) {
		t.Error("typed 429 with insufficient credits must not retry")
	}
	if isRetryableModelError(ctx, &providerio.StatusError{StatusCode: 429, Message: "saldo insuficiente para continuar"}) {
		t.Error("typed 429 with saldo insuficiente must not retry")
	}
	if isRetryableModelError(ctx, &providerio.StatusError{StatusCode: 402, Message: "payment required"}) {
		t.Error("typed 402 must not retry")
	}
}
