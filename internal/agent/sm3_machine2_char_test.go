package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// SM3 Machine 2, commit A: characterization of the CURRENT compaction
// behavior. The cited defect (reactive raw no-op vs proactive calibrated+tools
// no-op giving different verdicts) did NOT reproduce: a proof attempt on a
// realistic history with calibration active (ratio 0.51) and tool defs present
// showed both verdicts agreeing (both shrink -> retry), and a 146,965-combo
// grid sweep found divergences ONLY at +/-1-token int-truncation edges, never
// at realistic margins. Machine 2 therefore enters as a PURE REFACTORING:
// these tests pass on untouched dev and must pass after WITHOUT edits.

// padToExactSize appends 'x' runs to msgs[idx] until estimateTokens hits
// target exactly (4 added non-space bytes = exactly 1 token).
func padToExactSize(msgs []qgruntime.Message, idx, target int) []qgruntime.Message {
	for estimateTokens(msgs) < target {
		msgs[idx].Content += "xxxx"
	}
	if estimateTokens(msgs) != target {
		panic("padToExactSize missed target")
	}
	return msgs
}

// dupReadHistory builds a history with two identical read_file results; the
// older one is prunable for free by pruneSupersededReadResults.
func dupReadHistory(body string) []qgruntime.Message {
	return []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		toolCallWithArgs("old", "read_file", `{"path":"a.go"}`),
		{Role: qgruntime.MessageRoleTool, ToolCallID: "old", Content: body},
		toolCallWithArgs("new", "read_file", `{"path":"a.go"}`),
		{Role: qgruntime.MessageRoleTool, ToolCallID: "new", Content: body},
	}
}

func newCharState(window int, notified *int) *compactionState {
	opts := Options{ContextWindow: window, CompactionPreserveLast: 2}
	if notified != nil {
		opts.OnCompaction = func() { *notified++ }
	}
	return newCompactionState(opts, nil)
}

// Proactive decisions for five histories: below/at threshold (noop), above
// with fitting prune (pruned, no summarizer), above paid (compacted), paid
// without shrink (noop, watermark still fixed).
func TestSm3CharProactiveTable(t *testing.T) {
	t.Run("below threshold noop", func(t *testing.T) {
		var notified int
		state := newCharState(1000, &notified)
		provider := &summarizeRecordingProvider{}
		history := []qgruntime.Message{
			{Role: qgruntime.MessageRoleSystem, Content: "sys"},
			{Role: qgruntime.MessageRoleUser, Content: "hi"},
			{Role: qgruntime.MessageRoleAssistant, Content: "ok"},
		}
		got := state.maybeCompact(context.Background(), provider, history, nil)
		if len(got) != len(history) || provider.summarizeCalls != 0 {
			t.Fatalf("below threshold must noop without summarizing, got len=%d calls=%d", len(got), provider.summarizeCalls)
		}
		if state.lowWaterMark != 0 || notified != 0 {
			t.Fatalf("noop must not touch watermark/notify, mark=%d notified=%d", state.lowWaterMark, notified)
		}
	})

	t.Run("at threshold noop", func(t *testing.T) {
		var notified int
		state := newCharState(1000, &notified) // threshold 700
		provider := &summarizeRecordingProvider{}
		history := padToExactSize([]qgruntime.Message{
			{Role: qgruntime.MessageRoleSystem, Content: "sys"},
			{Role: qgruntime.MessageRoleUser, Content: "seed"},
		}, 1, compactionThreshold(1000))
		got := state.maybeCompact(context.Background(), provider, history, nil)
		if len(got) != len(history) || provider.summarizeCalls != 0 {
			t.Fatalf("at threshold must noop, got len=%d calls=%d", len(got), provider.summarizeCalls)
		}
		if state.lowWaterMark != 0 || notified != 0 {
			t.Fatalf("noop must not touch watermark/notify, mark=%d notified=%d", state.lowWaterMark, notified)
		}
	})

	t.Run("above with prune that fits", func(t *testing.T) {
		var notified int
		body := strings.Repeat("same source line\n", 1000)
		history := dupReadHistory(body)
		pruned, reclaimed := pruneSupersededReadResults(history)
		if reclaimed <= 0 {
			t.Fatal("setup must reclaim superseded output")
		}
		// Pick a window whose 70% threshold sits strictly between the pruned
		// size (fits) and the raw size (trips).
		prunedSize := estimateTokens(pruned)
		rawSize := estimateTokens(history)
		window := 0
		for w := 100; w < 100000; w++ {
			if th := compactionThreshold(w); th > prunedSize && th < rawSize {
				window = w
				break
			}
		}
		if window == 0 {
			t.Fatalf("no window separates pruned=%d from raw=%d", prunedSize, rawSize)
		}
		state := newCharState(window, &notified)
		provider := &summarizeRecordingProvider{}
		got := state.maybeCompact(context.Background(), provider, history, nil)
		if provider.summarizeCalls != 0 {
			t.Fatalf("fitting prune must avoid the summarizer, got %d calls", provider.summarizeCalls)
		}
		if !strings.Contains(got[2].Content, "superseded identical") || got[4].Content != body {
			t.Fatalf("older duplicate must be pruned, newer kept verbatim: %#v", got)
		}
		if state.lowWaterMark != estimateTokens(got) {
			t.Fatalf("lowWaterMark = %d, want pruned size %d", state.lowWaterMark, estimateTokens(got))
		}
		if notified != 1 {
			t.Fatalf("prune rewrite must notify once, got %d", notified)
		}
	})

	t.Run("above paid compacts", func(t *testing.T) {
		var notified int
		state := newCharState(1000, &notified)
		provider := &summarizeRecordingProvider{}
		history := compactableHistory()
		got := state.maybeCompact(context.Background(), provider, history, nil)
		if provider.summarizeCalls != 1 {
			t.Fatalf("paid compaction must summarize once, got %d", provider.summarizeCalls)
		}
		if estimateTokens(got) >= estimateTokens(history) {
			t.Fatal("paid compaction must shrink the history")
		}
		if !strings.Contains(got[1].Content, summaryLabel) {
			t.Fatalf("summary label missing: %#v", got)
		}
		if state.lowWaterMark != estimateTokens(got) {
			t.Fatalf("lowWaterMark = %d, want compacted size %d", state.lowWaterMark, estimateTokens(got))
		}
		if state.reactiveAttempted || notified != 1 {
			t.Fatalf("paid proactive must not consume reactive budget; notified=%d", notified)
		}
	})

	t.Run("paid without shrink is noop but fixes watermark", func(t *testing.T) {
		var notified int
		state := newCharState(1000, &notified)
		big := &mockProvider{turns: [][]qgruntime.StreamEvent{{
			{Type: qgruntime.StreamEventText, Content: strings.Repeat("S", 30000)},
			{Type: qgruntime.StreamEventDone},
		}}}
		history := compactableHistory()
		before := estimateTokens(history)
		got := state.maybeCompact(context.Background(), big, history, nil)
		if len(big.requests) != 1 {
			t.Fatalf("summarizer must be attempted once, got %d requests", len(big.requests))
		}
		if len(got) != len(history) {
			t.Fatalf("no-shrink must leave messages untouched, got len=%d want %d", len(got), len(history))
		}
		for i := range got {
			if got[i].Content != history[i].Content {
				t.Fatalf("message %d changed without shrink", i)
			}
		}
		if state.lowWaterMark != before {
			t.Fatalf("no-shrink must still fix watermark at current size: mark=%d want %d", state.lowWaterMark, before)
		}
		if notified != 0 {
			t.Fatalf("pure no-shrink without prune must not notify, got %d", notified)
		}
	})
}

// Reactive sequence on ONE state: free-prune retry (budget intact), paid
// retry (budget consumed), second attempt (refused).
func TestSm3CharReactiveSequence(t *testing.T) {
	ctx := context.Background()
	var notified int
	state := newCharState(1000, &notified)
	provider := &summarizeRecordingProvider{}
	const ctxErr = "This model's maximum context length is 1000 tokens. Please reduce the length of the messages."

	smallBody := strings.Repeat("same line\n", 200)
	prunable := dupReadHistory(smallBody)
	got, retried, err := state.recover(ctx, provider, prunable, nil, ctxErr)
	if err != nil || !retried {
		t.Fatalf("recover with fitting prune must retry: retried=%v err=%v", retried, err)
	}
	if provider.summarizeCalls != 0 {
		t.Fatalf("free prune must not summarize, got %d", provider.summarizeCalls)
	}
	if state.reactiveAttempted {
		t.Fatal("free-prune retry must not consume the one-shot budget")
	}
	if state.lowWaterMark != estimateTokens(got) || notified != 1 {
		t.Fatalf("prune retry must fix watermark+notify: mark=%d notified=%d", state.lowWaterMark, notified)
	}

	paid, retried, err := state.recover(ctx, provider, compactableHistory(), nil, ctxErr)
	if err != nil || !retried {
		t.Fatalf("recover paid must retry: retried=%v err=%v", retried, err)
	}
	if provider.summarizeCalls != 1 {
		t.Fatalf("paid recover must summarize once, got %d", provider.summarizeCalls)
	}
	if !state.reactiveAttempted || notified != 2 {
		t.Fatalf("paid recover must consume budget+notify: attempted=%v notified=%d", state.reactiveAttempted, notified)
	}
	if estimateTokens(paid) >= estimateTokens(compactableHistory()) {
		t.Fatal("paid recover must shrink the history")
	}

	again, retried, err := state.recover(ctx, provider, compactableHistory(), nil, ctxErr)
	if err != nil || retried {
		t.Fatalf("second recover must be refused: retried=%v err=%v", retried, err)
	}
	if provider.summarizeCalls != 1 || notified != 2 {
		t.Fatalf("refused recover must not summarize/notify: calls=%d notified=%d", provider.summarizeCalls, notified)
	}
	if len(again) != len(compactableHistory()) {
		t.Fatalf("refused recover must keep history, got len=%d", len(again))
	}
}

// updateWindow on a run born with ContextWindow==0 enables compaction today.
// Fixed as characterization: changing it is a pending PRODUCT decision, and
// the explicit machine must preserve it byte-for-behavior.
func TestSm3CharUpdateWindowEnablesDisabledRun(t *testing.T) {
	state := newCompactionState(Options{ContextWindow: 0}, nil)
	if state.enabled {
		t.Fatal("precondition: zero-window run starts disabled")
	}
	state.updateWindow(5000)
	if !state.enabled || state.window != 5000 || state.threshold != compactionThreshold(5000) {
		t.Fatalf("updateWindow enables the run today: enabled=%v window=%d threshold=%d",
			state.enabled, state.window, state.threshold)
	}
	if state.lowWaterMark != 0 {
		t.Fatalf("updateWindow resets watermark: mark=%d", state.lowWaterMark)
	}
}

func TestSm3CharSwitchModelKeepsWindowOnUnknown(t *testing.T) {
	state := newCompactionState(Options{ContextWindow: 1000, Model: "m1"}, nil)
	state.switchModel("m2", 0)
	if state.window != 1000 || state.threshold != compactionThreshold(1000) {
		t.Fatalf("unknown window must keep the old one: window=%d threshold=%d", state.window, state.threshold)
	}
	if state.mainModel != "m2" || state.summarizerResolved {
		t.Fatalf("model must switch and summarizer reset: model=%q resolved=%v", state.mainModel, state.summarizerResolved)
	}
	state.switchModel("m3", 2000)
	if state.window != 2000 || state.threshold != compactionThreshold(2000) || state.lowWaterMark != 0 {
		t.Fatalf("known window must re-derive: window=%d threshold=%d mark=%d",
			state.window, state.threshold, state.lowWaterMark)
	}
}

// Raw (estimateTokens only) vs unified (calibrated + tool tokens) no-op
// verdicts agree at every realistic margin; the sole mathematical divergence
// is a +/-1-token int-truncation edge with ratio != 1, pinned below.
func TestSm3CharMeasureParity(t *testing.T) {
	pairs := [][2][]qgruntime.Message{
		{compactableHistory(), mustCompactForParity(t, compactableHistory())},
	}
	dup := dupReadHistory(strings.Repeat("same source line\n", 1000))
	pruned, _ := pruneSupersededReadResults(dup)
	pairs = append(pairs, [2][]qgruntime.Message{dup, pruned})

	toolSets := [][]qgruntime.ToolDefinition{
		nil,
		{{Name: "read_file", Description: strings.Repeat("d", 4000)}},
	}
	for _, ratio := range []float64{0.5, 0.85, 1.0, 1.7} {
		state := newCharState(1000, nil)
		state.calibrationRatio = ratio
		for _, tools := range toolSets {
			for _, pair := range pairs {
				rawNoop := estimateTokens(pair[1]) >= estimateTokens(pair[0])
				uniNoop := state.calibratedTokens(estimateTokens(pair[1])+estimateToolDefTokens(tools)) >=
					state.calibratedTokens(estimateTokens(pair[0])+estimateToolDefTokens(tools))
				if rawNoop != uniNoop {
					t.Fatalf("verdicts disagree at realistic margin (ratio=%v tools=%d): raw=%v unified=%v",
						ratio, len(tools), rawNoop, uniNoop)
				}
			}
		}
	}

	// The sole edge: int truncation can equate sizes 1 token apart, so a
	// 1-token raw shrink reads as unified no-op. Harmless (a retry of a
	// ~identical history) and out of realistic-compaction range.
	edge := newCharState(1000, nil)
	edge.calibrationRatio = 0.5
	if edge.calibratedTokens(100) != edge.calibratedTokens(101) {
		t.Fatalf("precondition: truncation edge must equate 100 and 101 at ratio 0.5")
	}
}

func mustCompactForParity(t *testing.T, history []qgruntime.Message) []qgruntime.Message {
	t.Helper()
	provider := &summarizeRecordingProvider{}
	got := newCharState(1000, nil).maybeCompact(context.Background(), provider, history, nil)
	if provider.summarizeCalls != 1 || estimateTokens(got) >= estimateTokens(history) {
		t.Fatal("parity setup must produce a real shrink")
	}
	return got
}
