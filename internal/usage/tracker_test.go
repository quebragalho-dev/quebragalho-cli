package usage

import (
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestTrackerNormalizesUsageAndComputesModelCost(t *testing.T) {
	tracker := NewTracker(TrackerOptions{Now: fixedUsageClock("2026-06-04T13:00:00Z")})

	record, err := tracker.Record(RecordInput{
		ModelID: "muse-spark-1.3-contributor",
		Source:  "exec",
		Usage: qgruntime.Usage{
			PromptTokens:      1_000,
			CompletionTokens:  250,
			CachedInputTokens: 200,
		},
	})
	if err != nil {
		t.Fatalf("Record returned error: %v", err)
	}
	if record.Sequence != 1 || record.ID != "qg_usage_1" || record.CreatedAt != "2026-06-04T13:00:00Z" {
		t.Fatalf("unexpected record identity: %#v", record)
	}
	if record.Usage.InputTokens != 1_000 || record.Usage.OutputTokens != 250 || record.Usage.TotalTokens != 1_250 {
		t.Fatalf("usage not normalized: %#v", record.Usage)
	}
	if record.Cost.TotalCost <= 0 || record.Cost.ModelID != "muse-spark-1.3-contributor" {
		t.Fatalf("cost not computed: %#v", record.Cost)
	}

	summary := tracker.Summary()
	if summary.RecordCount != 1 || summary.TotalTokens != 1_250 || summary.ByModel[0].ModelID != "muse-spark-1.3-contributor" {
		t.Fatalf("unexpected summary: %#v", summary)
	}
	if FormatSummary(summary) != "1 request, 1,250 tokens, "+summary.FormattedTotalCost {
		t.Fatalf("unexpected formatted summary: %q", FormatSummary(summary))
	}
}

func TestTrackerRejectsInvalidUsageAndUnknownModels(t *testing.T) {
	tracker := NewTracker(TrackerOptions{})
	if _, err := tracker.Record(RecordInput{ModelID: "muse-spark-1.3-contributor", Usage: qgruntime.Usage{InputTokens: -1}}); err == nil {
		t.Fatal("expected invalid usage error")
	}
}

func TestTrackerRecordsUnknownModelWithUnknownCost(t *testing.T) {
	// A model served by the gateway but missing from the curated catalog must
	// not break usage reporting: tokens are counted, cost stays unknown (zero).
	tracker := NewTracker(TrackerOptions{Now: fixedUsageClock("2026-06-04T13:00:00Z")})
	record, err := tracker.Record(RecordInput{
		ModelID: "future-model-9",
		Source:  "exec",
		Usage:   qgruntime.Usage{InputTokens: 500, OutputTokens: 100},
	})
	if err != nil {
		t.Fatalf("Record returned error for uncatalogued model: %v", err)
	}
	if record.ModelID != "future-model-9" {
		t.Fatalf("record model = %q, want future-model-9", record.ModelID)
	}
	if record.Usage.TotalTokens != 600 {
		t.Fatalf("usage not normalized: %#v", record.Usage)
	}
	if record.Cost.TotalCost != 0 {
		t.Fatalf("unknown-model cost = %v, want zero (unknown)", record.Cost.TotalCost)
	}
	summary := tracker.Summary()
	if summary.RecordCount != 0 || summary.TotalTokens != 0 {
		t.Fatalf("unknown model must not join the priced tally, got %#v", summary)
	}
	if got := tracker.Records(); len(got) != 0 {
		t.Fatalf("unknown model must not be stored, got %#v", got)
	}
}

func TestTrackerTreatsReasoningAsOutputBreakdown(t *testing.T) {
	tracker := NewTracker(TrackerOptions{})
	record, err := tracker.Record(RecordInput{
		ModelID: "muse-spark-1.3-contributor",
		Usage: qgruntime.Usage{
			InputTokens:     100,
			OutputTokens:    40,
			ReasoningTokens: 10,
		},
	})
	if err != nil {
		t.Fatalf("Record returned error: %v", err)
	}
	if record.Usage.TotalTokens != 140 {
		t.Fatalf("total tokens = %d, want 140", record.Usage.TotalTokens)
	}
}

func TestTrackerResetClearsRecords(t *testing.T) {
	tracker := NewTracker(TrackerOptions{})
	if _, err := tracker.Record(RecordInput{ModelID: "muse-spark-1.3-contributor", Usage: qgruntime.Usage{InputTokens: 1, OutputTokens: 1}}); err != nil {
		t.Fatalf("Record returned error: %v", err)
	}
	tracker.Reset()
	if summary := tracker.Summary(); summary.RecordCount != 0 || len(summary.ByModel) != 0 {
		t.Fatalf("Reset did not clear tracker: %#v", summary)
	}
}

func fixedUsageClock(value string) func() time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return func() time.Time { return parsed }
}
