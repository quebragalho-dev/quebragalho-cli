package agent

import (
	"context"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// sessionProvider adapts a TurnSession back to the Provider interface so the
// loop's existing Provider-shaped call sites (streamWithReconnect, the
// compaction summarizer, finalAnswerAfterMaxTurns) route their stream I/O
// through the session without any signature change. For the default adapter
// this reduces to the wrapped provider's StreamCompletion, so behavior is
// byte-identical; an optimized session (PR8) takes effect here transparently.
type sessionProvider struct {
	session qgruntime.TurnSession
}

func (s sessionProvider) StreamCompletion(ctx context.Context, request qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	return s.session.Stream(ctx, request)
}
