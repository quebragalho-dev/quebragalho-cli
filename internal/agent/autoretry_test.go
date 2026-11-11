package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestIsRetryableModelErrorClassification(t *testing.T) {
	ctx := context.Background()
	retryable := []string{
		"unexpected EOF",
		"connection reset by peer",
		"connection refused",
		"broken pipe",
		"connection closed",
		"i/o timeout",
		"server closed the connection",
		"temporarily unavailable",
		"no such host",
		"TLS handshake timeout",
		"provider stream error: no output for 6m (the model produced nothing)",
		"provider stream error: idle timeout after 5m0s (upstream stopped sending data)",
		"stream stalled (upstream kept the connection alive but produced no output)",
		"HTTP 408 Request Timeout",
		"HTTP 425 Too Early",
		"429 Too Many Requests",
		"rate limit error: slow down",
		"500 Internal Server Error",
		"502 Bad Gateway",
		"503 Service Unavailable",
		"504 Gateway Timeout",
		"529 overloaded",
		"provider stream error: upstream 502",
		"provider request error: 503 Service Unavailable",
		"upstream unreachable: the model server could not connect to host (connection refused)",
		"429 Too Many Requests, request denied",
		"502 Bad Gateway: upstream cancelled the request",
	}
	for _, msg := range retryable {
		if !isRetryableModelError(ctx, errors.New(msg)) {
			t.Errorf("expected retry for %q", msg)
		}
	}
	notRetryable := []string{
		"",
		"400 Bad Request: invalid model id",
		"401 unauthorized",
		"auth error: your API key is missing or invalid",
		"403 Forbidden",
		"402 Payment Required",
		"insufficient balance: top up to continue",
		"404 model not found",
		"model_not_allowed for this key",
		"context length exceeded",
		"HTTP 400: image content is not supported by this model",
		// Stops never retry — but only by type/sentinel now (see
		// TestIsRetryableModelErrorSentinels): these plain strings fail
		// closed through the default, never through a broad needle that
		// would also catch "upstream cancelled the request".
		"permission approval cancelled",
		"run cancelled",
		"cancelled in TUI",
		"context canceled",
		"permission denied",
	}
	for _, msg := range notRetryable {
		if isRetryableModelError(ctx, errors.New(msg)) {
			t.Errorf("did NOT expect retry for %q", msg)
		}
	}
	if isRetryableModelError(ctx, nil) {
		t.Error("nil error must not be retryable")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if isRetryableModelError(cancelled, errors.New("connection reset by peer")) {
		t.Error("cancelled context must not retry, even for transport errors")
	}
}

// Stops retry only by type/sentinel — never by a broad substring that would
// also catch a retryable "upstream cancelled the request" or a 429 "request
// denied" (those are covered by the classification table above and must
// retry).
func TestIsRetryableModelErrorSentinels(t *testing.T) {
	ctx := context.Background()
	stops := []error{
		fmt.Errorf("stream: %w", context.Canceled),
		fmt.Errorf("stream: %w", context.DeadlineExceeded),
		fmt.Errorf("ask_user: %w", ErrPermissionApprovalCanceled),
		context.Canceled,
		context.DeadlineExceeded,
		ErrPermissionApprovalCanceled,
	}
	for _, err := range stops {
		if isRetryableModelError(ctx, err) {
			t.Errorf("stop signal must not retry: %v", err)
		}
	}
}

func TestAutoRetryDelaySchedule(t *testing.T) {
	want := []time.Duration{
		7500 * time.Millisecond,
		15 * time.Second,
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		4 * time.Minute,
		5 * time.Minute,
		5 * time.Minute,
		5 * time.Minute,
	}
	for i, w := range want {
		if got := autoRetryDelay(i+1, 0); got != w {
			t.Errorf("autoRetryDelay(%d) = %v, want %v", i+1, got, w)
		}
	}
}

func TestAutoRetryDelayHonorsRetryAfterWithCap(t *testing.T) {
	if got := autoRetryDelay(1, 45*time.Second); got != 45*time.Second {
		t.Fatalf("Retry-After 45s must win over the schedule, got %v", got)
	}
	if got := autoRetryDelay(1, 10*time.Minute); got != 5*time.Minute {
		t.Fatalf("hostile Retry-After must cap at 5m, got %v", got)
	}
	if got := autoRetryDelay(9, 0); got != 5*time.Minute {
		t.Fatalf("late schedule must cap at 5m, got %v", got)
	}
}

func TestParseRetryAfterHint(t *testing.T) {
	cases := map[string]time.Duration{
		"429 Too Many Requests, retry after 30":     30 * time.Second,
		"rate limited; Retry-After: 12":             12 * time.Second,
		"please retry in 45s":                       45 * time.Second,
		"nothing here":                              0,
		"retry after 600":                           5 * time.Minute,
		"error 4290 in request id 14015, slow down": 0,
	}
	for msg, want := range cases {
		if got := parseRetryAfterHint(msg); got != want {
			t.Errorf("parseRetryAfterHint(%q) = %v, want %v", msg, got, want)
		}
	}
}

type countingFailProvider struct {
	calls int32
	err   error
}

func (p *countingFailProvider) StreamCompletion(_ context.Context, _ qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	atomic.AddInt32(&p.calls, 1)
	return nil, p.err
}

func instantRetrySleep(record *[]time.Duration) func(context.Context, time.Duration) error {
	return func(_ context.Context, d time.Duration) error {
		*record = append(*record, d)
		return nil
	}
}

func TestRunExhaustsSingleBudgetOfTen(t *testing.T) {
	p := &countingFailProvider{err: errors.New("503 Service Unavailable")}
	var waits []time.Duration
	_, err := Run(context.Background(), "go", p, Options{
		Registry:   tools.NewRegistry(),
		RetrySleep: instantRetrySleep(&waits),
	})
	if err == nil {
		t.Fatal("persistent failure must surface an error after exhausting retries")
	}
	if got := atomic.LoadInt32(&p.calls); got != 10 {
		t.Fatalf("want exactly 10 model calls in one budget, got %d", got)
	}
	if len(waits) != 9 {
		t.Fatalf("want 9 waits between 10 attempts, got %d", len(waits))
	}
	if !strings.Contains(err.Error(), "10/10 tentativas") {
		t.Fatalf("exhausted error must carry the attempt count, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("exhausted error must keep the original message, got %q", err.Error())
	}
}

func TestRunRetryBudgetResetsAfterSuccess(t *testing.T) {
	p := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{{Type: qgruntime.StreamEventError, Error: "500 Internal Server Error"}},
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "ping"},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
			{Type: qgruntime.StreamEventDone},
		},
		{{Type: qgruntime.StreamEventError, Error: "502 Bad Gateway"}},
		{{Type: qgruntime.StreamEventError, Error: "502 Bad Gateway"}},
		{
			{Type: qgruntime.StreamEventText, Content: "done"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	registry := tools.NewRegistry()
	registry.Register(&countingPingTool{})
	var waits []time.Duration
	result, err := Run(context.Background(), "go", p, Options{
		Registry:       registry,
		PermissionMode: PermissionModeAuto,
		RetrySleep:     instantRetrySleep(&waits),
	})
	if err != nil {
		t.Fatalf("both turns should recover within their own budget, got %v", err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want %q", result.FinalAnswer, "done")
	}
	if len(p.requests) != 12 {
		t.Fatalf("want 12 model calls (9 + 3: a per-call budget reset), got %d", len(p.requests))
	}
}

type countingPingTool struct {
	calls int32
}

func (tool *countingPingTool) Name() string        { return "ping" }
func (tool *countingPingTool) Description() string { return "test ping tool" }
func (tool *countingPingTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object"}
}
func (tool *countingPingTool) Safety() tools.Safety {
	return tools.Safety{Permission: tools.PermissionAllow, AdvertiseInAuto: true}
}
func (tool *countingPingTool) Run(_ context.Context, _ map[string]any) tools.Result {
	atomic.AddInt32(&tool.calls, 1)
	return tools.Result{Status: tools.StatusOK, Output: "pong"}
}

func TestRunMidStreamFailureDiscardsPartialAndRunsToolOnce(t *testing.T) {
	tool := &countingPingTool{}
	registry := tools.NewRegistry()
	registry.Register(tool)
	p := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventText, Content: "partial-upstream-text"},
			{Type: qgruntime.StreamEventError, Error: "provider stream error: upstream 502"},
		},
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "ping"},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "final"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	var notices []string
	var waits []time.Duration
	result, err := Run(context.Background(), "go", p, Options{
		Registry:       registry,
		PermissionMode: PermissionModeAuto,
		RetrySleep:     instantRetrySleep(&waits),
		OnReasoning:    func(s string) { notices = append(notices, s) },
	})
	if err != nil {
		t.Fatalf("mid-stream retryable failure should recover, got %v", err)
	}
	if result.FinalAnswer != "final" {
		t.Fatalf("final answer = %q, want %q", result.FinalAnswer, "final")
	}
	if got := atomic.LoadInt32(&tool.calls); got != 1 {
		t.Fatalf("tool must execute exactly once, got %d", got)
	}
	for _, message := range result.Messages {
		if strings.Contains(message.Content, "partial-upstream-text") {
			t.Fatalf("partial text must not reach the history sent to the model: %#v", message)
		}
	}
	joined := strings.Join(notices, "")
	if !strings.Contains(joined, "descartad") {
		t.Fatalf("retry notice must mark the discarded partial output, got %q", joined)
	}
}

func TestRunCancelsDuringRetryWait(t *testing.T) {
	p := &countingFailProvider{err: errors.New("connection reset by peer")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Run(ctx, "go", p, Options{
		Registry: tools.NewRegistry(),
		RetrySleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel during the wait must surface cancellation, got %v", err)
	}
	if got := atomic.LoadInt32(&p.calls); got != 1 {
		t.Fatalf("no further attempt may start after cancel, got %d calls", got)
	}
}
