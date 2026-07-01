package agent

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Gitlawb/zero/internal/tools"
	"github.com/Gitlawb/zero/internal/zeroruntime"
)

// stallProvider connects successfully (HTTP 200) but the stream emits a stall/idle
// timeout error for the first stallBefore calls, then succeeds with "done". When
// partialText is set it emits that text BEFORE the stall, simulating a stall after
// partial output (which must NOT be retried — re-issuing would duplicate it).
type stallProvider struct {
	calls         int32
	stallBefore   int32
	partialText   string
	reasoningText string
}

func (p *stallProvider) StreamCompletion(_ context.Context, _ zeroruntime.CompletionRequest) (<-chan zeroruntime.StreamEvent, error) {
	n := atomic.AddInt32(&p.calls, 1)
	ch := make(chan zeroruntime.StreamEvent, 3)
	if n <= p.stallBefore {
		if p.partialText != "" {
			ch <- zeroruntime.StreamEvent{Type: zeroruntime.StreamEventText, Content: p.partialText}
		}
		if p.reasoningText != "" {
			ch <- zeroruntime.StreamEvent{Type: zeroruntime.StreamEventReasoning, Content: p.reasoningText}
		}
		ch <- zeroruntime.StreamEvent{
			Type:  zeroruntime.StreamEventError,
			Error: "provider stream error: no output for 10m (the model produced nothing)",
		}
		close(ch)
		return ch, nil
	}
	ch <- zeroruntime.StreamEvent{Type: zeroruntime.StreamEventText, Content: "done"}
	ch <- zeroruntime.StreamEvent{Type: zeroruntime.StreamEventDone}
	close(ch)
	return ch, nil
}

// A no-output stall is re-issued on a fresh connection and recovers — this is the
// macOS stale-pooled-connection hang turned into an automatic recovery.
func TestRunRetriesStreamStallWithNoOutput(t *testing.T) {
	p := &stallProvider{stallBefore: 1}
	result, err := Run(context.Background(), "go", p, Options{Registry: tools.NewRegistry()})
	if err != nil {
		t.Fatalf("a no-output stall should retry to success, got %v", err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want %q", result.FinalAnswer, "done")
	}
	if got := atomic.LoadInt32(&p.calls); got != 2 {
		t.Fatalf("want 2 calls (1 stall + 1 retry), got %d", got)
	}
}

// A stall AFTER partial output must NOT be retried — re-issuing would duplicate
// the already-streamed text. It surfaces the error instead.
func TestRunDoesNotRetryStallAfterPartialOutput(t *testing.T) {
	p := &stallProvider{stallBefore: 1, partialText: "partial"}
	_, err := Run(context.Background(), "go", p, Options{Registry: tools.NewRegistry()})
	if err == nil {
		t.Fatal("a stall after partial output must NOT be retried; want an error")
	}
	if got := atomic.LoadInt32(&p.calls); got != 1 {
		t.Fatalf("partial-then-stall must not retry, got %d calls", got)
	}
}

// A stall after only REASONING was forwarded (no text/tool-calls) must NOT be
// retried: the user already saw the reasoning, so re-issuing with callbacks
// re-enabled would duplicate it. collected.Text is empty here, so this exercises
// the turn-level forwarded-output gate specifically, not the text check.
func TestRunDoesNotRetryStallAfterForwardedReasoning(t *testing.T) {
	p := &stallProvider{stallBefore: 1, reasoningText: "thinking hard"}
	_, err := Run(context.Background(), "go", p, Options{
		Registry:    tools.NewRegistry(),
		OnReasoning: func(string) {},
	})
	if err == nil {
		t.Fatal("a stall after forwarded reasoning must NOT be retried; want an error")
	}
	if got := atomic.LoadInt32(&p.calls); got != 1 {
		t.Fatalf("reasoning-then-stall must not retry, got %d calls", got)
	}
}

// A persistent stall surfaces an error after exhausting the capped retries.
func TestRunGivesUpAfterMaxStallRetries(t *testing.T) {
	p := &stallProvider{stallBefore: 99}
	_, err := Run(context.Background(), "go", p, Options{Registry: tools.NewRegistry()})
	if err == nil {
		t.Fatal("a persistent stall must surface an error after exhausting retries")
	}
	if got := atomic.LoadInt32(&p.calls); got != int32(1+maxStreamStallRetries) {
		t.Fatalf("want %d calls (1 + %d retries), got %d", 1+maxStreamStallRetries, maxStreamStallRetries, got)
	}
}

func TestIsStreamTimeoutError(t *testing.T) {
	timeouts := []string{
		"provider stream error: no output for 10m (the model produced nothing)",
		"provider stream error: idle timeout after 5m0s (upstream stopped sending data)",
		"stream stalled (upstream kept the connection alive but produced no output)",
	}
	for _, m := range timeouts {
		if !isStreamTimeoutError(m) {
			t.Fatalf("want timeout-classified: %q", m)
		}
	}
	notTimeouts := []string{"", "context length exceeded", "rate limit error: slow down", "model not found"}
	for _, m := range notTimeouts {
		if isStreamTimeoutError(m) {
			t.Fatalf("must NOT classify as timeout: %q", m)
		}
	}
}
