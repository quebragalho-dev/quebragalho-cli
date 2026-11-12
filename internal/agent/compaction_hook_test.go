package agent

import (
	"context"
	"errors"
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

// A9: o hook dispara sempre que o histórico devolvido difere do recebido,
// mesmo quando o sumarizador pago falha depois de uma poda gratuita.
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
