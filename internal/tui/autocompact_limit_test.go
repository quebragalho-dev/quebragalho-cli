package tui

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// A session limit far below the model's window triggers proactive compaction
// on a conversation the automatic threshold would leave alone; the same
// conversation without a limit, or with proactive compaction off, never calls
// the summarizer.
func TestAutoCompactLimitReachesRunOptions(t *testing.T) {
	for _, tc := range []struct {
		name      string
		window    int
		limit     int
		wantCalls int
	}{
		{"limit below the window compacts", 10_000_000, config.MinAutoCompactLimit, 1},
		{"automatic leaves the same conversation alone", 10_000_000, 0, 0},
		{"off leaves a small-window conversation alone", 24_000, config.AutoCompactOff, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tools.NewRegistry()
			registry.Register(continuationEchoTool{})
			big := strings.Repeat("x", 120_000)
			first := append([]qgruntime.StreamEvent{{Type: qgruntime.StreamEventText, Content: big}},
				acceptAttrToolCall("read_it", acceptAttrUsage("", 40_000, 39_000, 100))...)
			p := &acceptRoutingProvider{
				main:    [][]qgruntime.StreamEvent{first, acceptAttrText("feito", acceptAttrUsage("", 9_000, 300, 20))},
				summary: acceptAttrText("RESUMO", acceptAttrUsage("", 30_000, 0, 300)),
			}
			m := acceptAttrLiveModelWithLimit(t, p, registry, tc.window, agent.Options{CompactionPreserveLast: 2}, "", tc.limit)
			_ = runTextTurn(t, m, strings.Repeat("y", 40_000))
			if p.summarizeCalls != tc.wantCalls {
				t.Fatalf("summarizeCalls = %d, want %d (window %d, limit %d)", p.summarizeCalls, tc.wantCalls, tc.window, tc.limit)
			}
		})
	}
}
