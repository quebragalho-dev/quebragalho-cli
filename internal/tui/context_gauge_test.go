package tui

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/usage"
)

// The context-fill gauge is empty before any request, then shows
// used/window · pct from the last turn's input tokens against the model window.
func TestSidebarContextGaugeUsesAPIWindow(t *testing.T) {
	m := batchThreeLiveModel(t)
	window := int64(1000000)
	m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &window
	if got := plainRender(t, m.sidebarTokenLine(40)); strings.Contains(got, "%") {
		t.Fatalf("expected empty gauge before any request, got %q", got)
	}
	// 161k latest-step tokens against the 1M window = 16%.
	if _, err := m.usageTracker.Record(usage.RecordInput{
		ModelID: "muse-spark-1.3-contributor",
		Usage:   qgruntime.Usage{InputTokens: 160_000, OutputTokens: 1000},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := plainRender(t, m.sidebarTokenLine(40))
	if !strings.Contains(got, "161") || !strings.Contains(got, "16%") {
		t.Fatalf("gauge = %q, want latest tokens and 16%%", got)
	}
}

func TestStatusLineDoesNotInferContextFromRegistry(t *testing.T) {
	m := batchThreeLiveModel(t)
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: 160000, OutputTokens: 1000}, transcriptRow{})
	status := plainRender(t, m.statusLine(110))
	assertContains(t, status, "ctx 1.000.000 (fallback/docs)")
	assertNotContains(t, status, "1M")
	if got := plainRender(t, m.sidebarTokenLine(40)); strings.Contains(got, "%") {
		t.Fatalf("null API window must not produce a gauge: %q", got)
	}
}
