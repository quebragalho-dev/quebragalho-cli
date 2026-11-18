package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// Characterization of the CURRENT compaction behavior (commit A). The cited
// defect (reactive raw no-op vs proactive calibrated+tools no-op giving
// different verdicts) did NOT reproduce: a proof attempt on a
// realistic history with calibration active (ratio 0.51) and tool defs present
// showed both verdicts agreeing (both shrink -> retry), and a 146,965-combo
// grid sweep found divergences ONLY at +/-1-token int-truncation edges, never
// at realistic margins. This machine therefore enters as a PURE REFACTORING:
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
func TestCompactionProactiveDecisions(t *testing.T) {
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
func TestCompactionReactiveSequence(t *testing.T) {
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

// updateWindow and switchModel on a run born with ContextWindow==0 (opt-out)
// must respect the opt-out and never turn compaction on mid-run.
func TestCompactionUpdateWindowKeepsDisabledRunDisabled(t *testing.T) {
	state := newCompactionState(Options{ContextWindow: 0}, nil)
	if state.enabled {
		t.Fatal("precondition: zero-window run starts disabled")
	}
	state.updateWindow(5000)
	if state.enabled || state.phase != phaseDisabled || state.window != 0 || state.threshold != 0 {
		t.Fatalf("updateWindow enabled an opt-out run: enabled=%v phase=%s window=%d threshold=%d",
			state.enabled, state.phase, state.window, state.threshold)
	}
	state.switchModel("big-model", 200_000)
	if state.enabled || state.phase != phaseDisabled {
		t.Fatalf("switchModel enabled an opt-out run: enabled=%v phase=%s", state.enabled, state.phase)
	}
	provider := &summarizeRecordingProvider{}
	history := compactableHistory()
	got := state.maybeCompact(context.Background(), provider, history, nil)
	if len(got) != len(history) || provider.summarizeCalls != 0 {
		t.Fatalf("opt-out run compacted after updateWindow: len=%d calls=%d", len(got), provider.summarizeCalls)
	}
}

func TestCompactionSwitchModelKeepsWindowOnUnknown(t *testing.T) {
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
func TestCompactionMeasureParity(t *testing.T) {
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
	// ~identical history) and out of realistic-compaction range. The RECOVER
	// verdict at this edge is the RAW comparison (it retries); see
	// TestRecoverOneTokenEdgeUsesRawVerdict, which fails if the no-op check is
	// ever switched to measuredSize.
	edge := newCharState(1000, nil)
	edge.calibrationRatio = 0.5
	if edge.calibratedTokens(100) != edge.calibratedTokens(101) {
		t.Fatalf("precondition: truncation edge must equate 100 and 101 at ratio 0.5")
	}
}

// TestRecoverOneTokenEdgeUsesRawVerdict pins the real behavior at the
// 1-token truncation edge: the input below shrinks by exactly 1 raw token
// (odd input size, ratio 0.5, no tools), so the calibrated+tools measure
// reads no-op while the raw comparison reads shrink. recover must follow the
// RAW verdict and retry. If the no-op check is switched back to measuredSize,
// this test fails (retried=false).
func TestRecoverOneTokenEdgeUsesRawVerdict(t *testing.T) {
	ctx := context.Background()
	const ctxErr = "This model's maximum context length is 1000 tokens. Please reduce the length of the messages."
	base := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "seed"},
		{Role: qgruntime.MessageRoleAssistant, Content: "a"},
		{Role: qgruntime.MessageRoleUser, Content: "u2"},
		{Role: qgruntime.MessageRoleAssistant, Content: "a2"},
		{Role: qgruntime.MessageRoleUser, Content: "u3"},
	}
	if estimateTokens(base)%2 == 0 {
		base[1].Content += "xxxx" // +1 token: the edge needs an odd input size
	}
	// Grow the summary until Compact shrinks the input by exactly 1 token.
	// +4 summary chars grow the output by exactly 1 token, so D hits 1.
	// Compact is pure and independent of the recover verdict under test.
	summaryLen := -1
	for l := 4; l < 4000; l += 4 {
		out, err := Compact(base, CompactionOptions{
			PreserveLast: 2,
			Summarize: func([]qgruntime.Message) (string, error) {
				return strings.Repeat("S", l), nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if estimateTokens(base)-estimateTokens(out) == 1 {
			summaryLen = l
			break
		}
	}
	if summaryLen < 0 {
		t.Fatal("setup must land Compact exactly 1 raw token below the input")
	}
	// The edge itself: 1-token raw shrink that the calibrated+tools measure
	// reads as no-op (odd input, ratio 0.5, no tools).
	edgeOut, err := Compact(base, CompactionOptions{
		PreserveLast: 2,
		Summarize: func([]qgruntime.Message) (string, error) {
			return strings.Repeat("S", summaryLen), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	estIn, estWant := estimateTokens(base), estimateTokens(edgeOut)
	if estIn-estWant != 1 {
		t.Fatalf("setup off the edge: in=%d out=%d", estIn, estWant)
	}
	edgeProbe := newCharState(1000, nil)
	edgeProbe.calibrationRatio = 0.5
	if unifiedNoop := edgeProbe.calibratedTokens(estWant) >= edgeProbe.calibratedTokens(estIn); !unifiedNoop {
		t.Fatalf("setup must read unified no-op at the edge: in=%d out=%d", estIn, estWant)
	}
	// Fresh run on the edge history: raw says shrink, unified says no-op.
	// The edge itself is established on the pure Compact output above, so a
	// verdict change in recover fails at the retried assertion below.
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: strings.Repeat("S", summaryLen)},
		{Type: qgruntime.StreamEventDone},
	}}}
	state := newCharState(1000, nil)
	state.calibrationRatio = 0.5
	_, retried, err := state.recover(ctx, provider, base, nil, ctxErr)
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("summarizer must be attempted once, got %d requests", len(provider.requests))
	}
	if !retried {
		t.Fatal("at the 1-token edge recover must follow the RAW verdict and retry (fails under measuredSize)")
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
