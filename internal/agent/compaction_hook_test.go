package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// OnCompaction fires only when the compactor actually rewrote the history, so
// surfaces can blame a later cache miss on it without false positives.
func TestOnCompactionFiresOnPruneRewrite(t *testing.T) {
	fired := 0
	state := newCompactionState(Options{
		ContextWindow:          200_000,
		CompactionPreserveLast: 2,
		OnCompaction:           func() { fired++ },
	}, nil)
	messages := bigToolHistory(5)

	if _, retried, err := state.recover(context.Background(), summaryProvider("SUMMARY"), messages, nil, "maximum context length exceeded"); err != nil || !retried {
		t.Fatalf("recover = retried %v, err %v, want a free prune retry", retried, err)
	}
	if fired != 1 {
		t.Fatalf("OnCompaction fired %d times, want exactly 1", fired)
	}
}

func TestOnCompactionSilentOnNoop(t *testing.T) {
	fired := 0
	state := newCompactionState(Options{
		ContextWindow:          200_000,
		CompactionPreserveLast: 2,
		OnCompaction:           func() { fired++ },
	}, nil)
	messages := continuationHistory("system congelado")

	if got := state.maybeCompact(context.Background(), summaryProvider("SUMMARY"), messages, nil); len(got) != len(messages) {
		t.Fatalf("short history must pass through untouched")
	}
	if fired != 0 {
		t.Fatalf("OnCompaction fired %d times on a no-op, want 0", fired)
	}
}

type failingSummarizerProvider struct{}

func (failingSummarizerProvider) StreamCompletion(context.Context, qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	return nil, errSummarizerDown
}

var errSummarizerDown = errors.New("summarizer down")

// The hook fires whenever the returned history differs from the received
// one, even when the paid summarizer fails after a free prune rewrite.
func TestOnCompactionFiresWhenSummarizerFailsAfterPrune(t *testing.T) {
	fired := 0
	state := newCompactionState(Options{
		ContextWindow:          50_000,
		CompactionPreserveLast: 2,
		OnCompaction:           func() { fired++ },
	}, nil)
	pruned := state.maybeCompact(context.Background(), failingSummarizerProvider{}, bigToolHistory(5), nil)
	if fired != 1 {
		t.Fatalf("OnCompaction fired %d times, want exactly 1 (prune rewrote the history)", fired)
	}
	if estimateTokens(pruned) >= estimateTokens(bigToolHistory(5)) {
		t.Fatal("expected the prune stage to shrink the history even without a summarizer")
	}
}

// A reactive prune that still leaves the history over the window falls
// through to the paid summarizer: when it fails, the pruned rewrite still
// counts and the hook fires while the caller retries with the pruned
// history and surfaces the summarizer error.
func TestOnCompactionFiresOnRecoverPruneWithFailingSummarizer(t *testing.T) {
	fired := 0
	state := newCompactionState(Options{
		ContextWindow:          40_000,
		CompactionPreserveLast: 2,
		OnCompaction:           func() { fired++ },
	}, nil)
	messages := bigToolHistory(5)
	got, retried, err := state.recover(context.Background(), failingSummarizerProvider{}, messages, nil, "maximum context length exceeded")
	if err == nil || !retried {
		t.Fatalf("recover = retried %v, err %v, want a retry surfacing the summarizer failure", retried, err)
	}
	if fired != 1 {
		t.Fatalf("OnCompaction fired %d times, want exactly 1 (prune rewrote the history)", fired)
	}
	if estimateTokens(got) >= estimateTokens(messages) {
		t.Fatal("expected the pruned history back even though the summarizer failed")
	}
}

// A prune-only rewrite that the summarizer cannot shrink further still
// counts: the proactive path returns the pruned history and fires once.
func TestOnCompactionFiresOnPruneWithoutShrink(t *testing.T) {
	fired := 0
	state := newCompactionState(Options{
		ContextWindow:          50_000,
		CompactionPreserveLast: 2,
		OnCompaction:           func() { fired++ },
	}, nil)
	messages := bigToolHistory(5)
	huge := strings.Repeat("y", 500_000)
	pruned := state.maybeCompact(context.Background(), summaryProvider(huge), messages, nil)
	if fired != 1 {
		t.Fatalf("OnCompaction fired %d times, want exactly 1 (prune rewrote the history)", fired)
	}
	if estimateTokens(pruned) >= estimateTokens(messages) {
		t.Fatal("expected the pruned history back even though compaction did not shrink it")
	}
}

// A recover that compacts to nothing smaller is a no-op the caller
// discards (retried=false keeps the original error): the hook must stay
// silent, since no listener ever sees the pruned rewrite.
func TestOnCompactionSilentOnRecoverNoopWithDiscardedPrune(t *testing.T) {
	fired := 0
	state := newCompactionState(Options{
		ContextWindow:          40_000,
		CompactionPreserveLast: 2,
		OnCompaction:           func() { fired++ },
	}, nil)
	messages := bigToolHistory(5)
	huge := strings.Repeat("y", 500_000)
	_, retried, err := state.recover(context.Background(), summaryProvider(huge), messages, nil, "maximum context length exceeded")
	if err != nil || retried {
		t.Fatalf("recover = retried %v, err %v, want a discarded no-op retry", retried, err)
	}
	if fired != 0 {
		t.Fatalf("OnCompaction fired %d times on a discarded no-op, want 0", fired)
	}
}
