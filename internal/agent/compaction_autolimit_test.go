package agent

import (
	"context"
	"testing"
)

// autoCompactThreshold maps a context window and a user limit to the
// proactive trigger: a positive limit below the automatic threshold wins,
// anything else falls back to the automatic threshold.
func TestAutoCompactThreshold(t *testing.T) {
	cases := []struct {
		window int
		limit  int
		want   int
	}{
		{window: 1000, limit: 0, want: 700},
		{window: 1000, limit: 500, want: 500},
		{window: 1000, limit: 700, want: 700},
		{window: 1000, limit: 900, want: 700},
		{window: 1000, limit: -1, want: 700},
		{window: 0, limit: 500, want: 0},
	}
	for _, tc := range cases {
		if got := autoCompactThreshold(tc.window, tc.limit); got != tc.want {
			t.Errorf("autoCompactThreshold(%d, %d) = %d, want %d", tc.window, tc.limit, got, tc.want)
		}
	}
}

// A positive limit triggers proactive compaction far below the automatic
// threshold of a huge window, while the same history with no limit is
// returned untouched.
func TestAutoCompactLimitTriggersEarlier(t *testing.T) {
	history := compactableHistory()

	limited := summaryProvider("LIMITED SUMMARY")
	limitedState := newCompactionState(Options{
		ContextWindow:          10_000_000,
		AutoCompactLimit:       500,
		CompactionPreserveLast: 2,
	}, nil)
	compacted := limitedState.maybeCompact(context.Background(), limited, history, nil)
	if estimateTokens(compacted) >= estimateTokens(history) {
		t.Fatal("positive limit must compact a history above it even when far below the automatic threshold")
	}
	if len(limited.requests) == 0 {
		t.Fatal("positive limit must reach the summarizer")
	}

	automatic := summaryProvider("AUTOMATIC SUMMARY")
	automaticState := newCompactionState(Options{
		ContextWindow:          10_000_000,
		CompactionPreserveLast: 2,
	}, nil)
	untouched := automaticState.maybeCompact(context.Background(), automatic, history, nil)
	if len(untouched) != len(history) {
		t.Fatal("automatic mode must return the same history untouched below its threshold")
	}
	if len(automatic.requests) != 0 {
		t.Fatalf("automatic mode must not call the summarizer, got %d requests", len(automatic.requests))
	}
}

// A negative limit turns proactive compaction off (no measurement, no
// transition) while the reactive recovery after a context-limit error keeps
// working on the same kind of state.
func TestAutoCompactOffDisablesProactiveOnly(t *testing.T) {
	history := compactableHistory()
	provider := summaryProvider("SUMMARY")
	state := newCompactionState(Options{
		ContextWindow:          1000,
		AutoCompactLimit:       -1,
		CompactionPreserveLast: 2,
	}, nil)

	got := state.maybeCompact(context.Background(), provider, history, nil)
	if len(got) != len(history) || &got[0] != &history[0] {
		t.Fatal("off must return the same slice without compacting")
	}
	if len(provider.requests) != 0 {
		t.Fatalf("off must never call the summarizer proactively, got %d requests", len(provider.requests))
	}
	if state.phase != phaseBelow {
		t.Fatalf("off must perform no transition, phase = %v", state.phase)
	}

	recovered, retried, err := state.recover(context.Background(), provider, history, nil, "context_length_exceeded")
	if err != nil {
		t.Fatal(err)
	}
	if !retried {
		t.Fatal("off must still recover reactively after a context-limit error")
	}
	if len(provider.requests) == 0 {
		t.Fatal("reactive recovery must reach the summarizer")
	}
	if estimateTokens(recovered) >= estimateTokens(history) {
		t.Fatal("reactive recovery did not shrink the history")
	}
}

// The user limit survives window changes: a switch to a smaller window
// clamps the trigger to its automatic threshold, and switching back
// restores the limit.
func TestAutoCompactLimitSurvivesWindowChange(t *testing.T) {
	state := newCompactionState(Options{
		ContextWindow:          1_000_000,
		AutoCompactLimit:       400_000,
		CompactionPreserveLast: 2,
	}, nil)
	if state.threshold != 400_000 {
		t.Fatalf("threshold = %d, want the 400000 user limit", state.threshold)
	}

	state.switchModel("m2", 200_000)
	if want := compactionThreshold(200_000); state.threshold != want {
		t.Fatalf("threshold after shrinking the window = %d, want the clamped automatic %d", state.threshold, want)
	}
	if state.limit != 400_000 {
		t.Fatalf("a window change must keep the user limit, got %d", state.limit)
	}

	state.switchModel("m3", 1_000_000)
	if state.threshold != 400_000 {
		t.Fatalf("threshold after restoring the window = %d, want the 400000 user limit", state.threshold)
	}
}

// A limit the window cannot hold is clamped to the automatic threshold.
func TestAutoCompactLimitClampedToWindow(t *testing.T) {
	state := newCompactionState(Options{
		ContextWindow:          200_000,
		AutoCompactLimit:       400_000,
		CompactionPreserveLast: 2,
	}, nil)
	if want := compactionThreshold(200_000); state.threshold != want {
		t.Fatalf("threshold = %d, want the clamped automatic %d", state.threshold, want)
	}
}
