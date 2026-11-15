package tui

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/usage"
)

// The run-details token readout is empty before any request, then shows the
// latest-step tokens against the model window.
func TestSidebarContextGaugeUsesAPIWindow(t *testing.T) {
	m := batchThreeLiveModel(t)
	window := int64(1000000)
	m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &window
	if got := plainRender(t, m.sidebarTokenText()); got != "" {
		t.Fatalf("expected no token readout before any request, got %q", got)
	}
	// 161k latest-step tokens against the 1M window.
	if _, err := m.usageTracker.Record(usage.RecordInput{
		ModelID: "muse-spark-1.3-contributor",
		Usage:   qgruntime.Usage{InputTokens: 160_000, OutputTokens: 1000},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := plainRender(t, m.sidebarTokenText())
	if got == "" {
		t.Fatal("expected a token readout after recording usage")
	}
	assertContains(t, got, "161")
}

func TestStatusLineDoesNotInferContextFromRegistry(t *testing.T) {
	m := batchThreeLiveModel(t)
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: 160000, OutputTokens: 1000}, transcriptRow{})
	status := plainRender(t, m.statusLine(110))
	// Used tokens without a window render bare, never the old guess.
	assertContains(t, status, "160k")
	assertNotContains(t, status, "ctx")
	if got := plainRender(t, m.sidebarTokenText()); got == "" {
		t.Fatal("expected a token readout from recorded usage")
	}
}
