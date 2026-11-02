package tui

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestSessionTitleUsesMinimalEffort(t *testing.T) {
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "Fix Login Bug"},
		{Type: qgruntime.StreamEventDone},
	}}
	_, err := generateSessionTitle(context.Background(), provider, "User: fix login bug")
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(provider.requests))
	}
	if got := provider.requests[0].ReasoningEffort; got != "minimal" {
		t.Fatalf("ReasoningEffort = %q, want minimal", got)
	}
}
