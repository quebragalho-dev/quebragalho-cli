package tui

import (
	"context"
	"testing"
	"time"
)

// Table test: every (phase x event) fixes which stream transitions are legal.
// Only toolDelta is restricted (a fragment needs a live tool call); every
// other event is accepted in every phase, preserving the exact pre-machine
// behavior (first delta from Idle, coalesced output, empty-turn end, cancel
// with nothing streamed).
func TestStreamPhaseTableAllCombinations(t *testing.T) {
	allPhases := []StreamPhase{StreamIdle, StreamText, StreamReasoning, StreamTool, StreamClearPending}
	allEvents := []StreamEv{
		StreamEvBegin, StreamEvText, StreamEvReasoning, StreamEvToolStart, StreamEvToolDelta,
		StreamEvClearThrottle, StreamEvClearFlush, StreamEvEnd, StreamEvCancel, StreamEvRetryDiscard,
	}
	legalEverywhere := map[StreamEv]bool{
		StreamEvBegin: true, StreamEvText: true, StreamEvReasoning: true,
		StreamEvToolStart: true, StreamEvClearThrottle: true, StreamEvClearFlush: true,
		StreamEvEnd: true, StreamEvCancel: true, StreamEvRetryDiscard: true,
	}
	for _, phase := range allPhases {
		for _, ev := range allEvents {
			want := legalEverywhere[ev]
			if phase == StreamTool && ev == StreamEvToolDelta {
				want = true
			}
			if got := streamEventLegal(phase, ev); got != want {
				t.Errorf("stream phase %s event %s: legal=%v want %v", phase, ev, got, want)
			}
		}
	}
	// String() must name every phase/event (padrão item 1).
	for _, p := range allPhases {
		if p.String() == "unknown" {
			t.Errorf("phase %d has no String", int(p))
		}
	}
	for _, e := range allEvents {
		if e.String() == "unknown" {
			t.Errorf("event %d has no String", int(e))
		}
	}
}

// Invariant walk: begin -> text -> reasoning -> toolStart -> toolDelta ->
// clearThrottle -> clearFlush -> retryDiscard -> end -> begin, plus cancel
// paths. After each step the fields must map to a valid phase and never to
// an impossible mix (half tool triple, clear without run, live without run).
func TestStreamInvariantsAcrossEventSequence(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	withFrozenClock(&m, t0)
	if got := m.streamPhase(); got != StreamIdle {
		t.Fatalf("initial phase=%s want idle", got)
	}
	if !m.validStreamState() {
		t.Fatal("initial state must be valid")
	}

	// begin: fresh run zeroes every twin, phase stays Idle until output.
	m = m.beginRun(nil)
	withFrozenClock(&m, t0)
	if got := m.streamPhase(); got != StreamIdle {
		t.Fatalf("after begin phase=%s want idle", got)
	}
	if !m.validStreamState() {
		t.Fatal("after begin state must be valid")
	}
	rid := m.activeRunID

	// text -> Text.
	cmd, err := m.streamTransition(StreamEvText, streamArgs{delta: "hello"})
	if err != nil {
		t.Fatalf("text in idle: %v", err)
	}
	if cmd != nil {
		t.Fatalf("plain text must emit no command, got %T", cmd)
	}
	if got := m.streamPhase(); got != StreamText {
		t.Fatalf("after text phase=%s want text", got)
	}
	if !m.validStreamState() {
		t.Fatal("after text state must be valid")
	}

	// reasoning accumulates alongside text without moving the phase.
	if _, err := m.streamTransition(StreamEvReasoning, streamArgs{delta: "hmm"}); err != nil {
		t.Fatalf("reasoning in text: %v", err)
	}
	if m.streamingReasoning != "hmm" || m.streamingTextString() != "hello" {
		t.Fatalf("reasoning must accumulate beside text: %q/%q", m.streamingReasoning, m.streamingTextString())
	}
	if !m.validStreamState() {
		t.Fatal("after reasoning state must be valid")
	}

	// toolStart -> Tool; live text survives the start (pre-machine parity).
	if _, err := m.streamTransition(StreamEvToolStart, streamArgs{toolID: "c1", toolName: "write_file"}); err != nil {
		t.Fatalf("toolStart in text: %v", err)
	}
	if got := m.streamPhase(); got != StreamTool {
		t.Fatalf("after toolStart phase=%s want tool", got)
	}
	if m.streamingTextString() != "hello" {
		t.Fatalf("toolStart must keep live text, got %q", m.streamingTextString())
	}
	if !m.validStreamState() {
		t.Fatal("after toolStart state must be valid")
	}

	// toolDelta with a superseded id is dropped, never fed, no error.
	before := m.streamCallDecoder
	if cmd, err := m.streamTransition(StreamEvToolDelta, streamArgs{toolID: "old", fragment: "x"}); err != nil || cmd != nil {
		t.Fatalf("superseded delta must drop silently, cmd=%v err=%v", cmd, err)
	}
	if m.streamCallDecoder != before {
		t.Fatal("superseded delta must not touch the decoder")
	}

	// Matching delta feeds the decoder and marks the call live.
	if _, err := m.streamTransition(StreamEvToolDelta, streamArgs{toolID: "c1", fragment: `{"path":"a.go","content":"x`}); err != nil {
		t.Fatalf("toolDelta in tool: %v", err)
	}
	if !m.streamLive {
		t.Fatal("tool delta must mark the call live")
	}
	if !m.validStreamState() {
		t.Fatal("after toolDelta state must be valid")
	}

	// text after tool drops the decoder tail, never merges it.
	if _, err := m.streamTransition(StreamEvText, streamArgs{delta: "prose"}); err != nil {
		t.Fatalf("text in tool: %v", err)
	}
	if got := m.streamPhase(); got != StreamText {
		t.Fatalf("after text phase=%s want text", got)
	}
	if m.streamCallID != "" || m.streamCallDecoder != nil {
		t.Fatal("text must drop the live tool block")
	}
	if !m.validStreamState() {
		t.Fatal("after tool->text state must be valid")
	}

	// Throttled newline -> ClearPending; flush inside the window is a no-op.
	if _, err := m.streamTransition(StreamEvText, streamArgs{delta: "one\n"}); err != nil {
		t.Fatalf("newline text: %v", err)
	}
	if _, err := m.streamTransition(StreamEvClearThrottle, streamArgs{delta: "two\n"}); err != nil {
		t.Fatalf("clearThrottle in text: %v", err)
	}
	if got := m.streamPhase(); got != StreamClearPending {
		t.Fatalf("after clearThrottle phase=%s want clearPending", got)
	}
	if !m.validStreamState() {
		t.Fatal("throttled state must be valid")
	}
	if cmd, err := m.streamTransition(StreamEvClearFlush, streamArgs{}); err != nil || cmd != nil {
		t.Fatalf("flush inside window must no-op, cmd=%v err=%v", cmd, err)
	}
	if got := m.streamPhase(); got != StreamClearPending {
		t.Fatalf("no-op flush must keep phase, got %s", got)
	}

	// Flush past the window fires ClearScreen and returns to Text.
	withFrozenClock(&m, t0.Add(streamClearThrottle))
	cmd, err = m.streamTransition(StreamEvClearFlush, streamArgs{})
	if err != nil {
		t.Fatalf("clearFlush: %v", err)
	}
	if !cmdIncludesClearScreen(cmd) {
		t.Fatal("flush past the window must ClearScreen")
	}
	if got := m.streamPhase(); got != StreamText {
		t.Fatalf("after flush phase=%s want text", got)
	}
	if !m.validStreamState() {
		t.Fatal("after flush state must be valid")
	}
	_ = rid

	// retryDiscard drops text/tool/fade; reasoning survives.
	if _, err := m.streamTransition(StreamEvRetryDiscard, streamArgs{}); err != nil {
		t.Fatalf("retryDiscard: %v", err)
	}
	if m.streamingTextString() != "" || m.streamingReasoning != "hmm" {
		t.Fatalf("discard must drop text and keep reasoning: %q/%q",
			m.streamingTextString(), m.streamingReasoning)
	}
	if got := m.streamPhase(); got != StreamReasoning {
		t.Fatalf("after discard phase=%s want reasoning", got)
	}
	if !m.validStreamState() {
		t.Fatal("after discard state must be valid")
	}

	// end settles everything including the live estimate (the proven leak).
	cmd, err = m.streamTransition(StreamEvEnd, streamArgs{})
	if err != nil {
		t.Fatalf("end: %v", err)
	}
	if cmd != nil {
		t.Fatalf("end without pending must emit nothing, got %T", cmd)
	}
	if got := m.streamPhase(); got != StreamIdle {
		t.Fatalf("after end phase=%s want idle", got)
	}
	if m.streamLive || m.streamEstimateChars != 0 || !m.streamEstimateStart.IsZero() {
		t.Fatal("end must mute the live estimate")
	}
	if !m.validStreamState() {
		t.Fatal("after end state must be valid")
	}

	// end with pending flushes the ClearScreen at stream end.
	m2 := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	m2 = m2.beginRun(nil)
	withFrozenClock(&m2, t0)
	if _, err := m2.streamTransition(StreamEvText, streamArgs{delta: "one\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.streamTransition(StreamEvClearThrottle, streamArgs{delta: "two\n"}); err != nil {
		t.Fatal(err)
	}
	cmd, err = m2.streamTransition(StreamEvEnd, streamArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if !cmdIncludesClearScreen(cmd) {
		t.Fatal("end with pending must flush ClearScreen")
	}
	if !m2.validStreamState() {
		t.Fatal("after end-with-pending state must be valid")
	}

	// cancel drops a pending clear silently and settles everything.
	m3 := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	m3 = m3.beginRun(nil)
	withFrozenClock(&m3, t0)
	if _, err := m3.streamTransition(StreamEvText, streamArgs{delta: "one\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m3.streamTransition(StreamEvClearThrottle, streamArgs{delta: "two\n"}); err != nil {
		t.Fatal(err)
	}
	cmd, err = m3.streamTransition(StreamEvCancel, streamArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if cmd != nil {
		t.Fatalf("cancel must emit nothing, got %T", cmd)
	}
	if got := m3.streamPhase(); got != StreamIdle {
		t.Fatalf("after cancel phase=%s want idle", got)
	}
	if m3.pendingStreamClear || m3.streamLive || m3.streamingTextString() != "" {
		t.Fatal("cancel must settle pending/live/text")
	}
	if !m3.validStreamState() {
		t.Fatal("after cancel state must be valid")
	}
}

// A toolDelta outside Tool is refused with an error while keeping state:
// feeding it would dereference a nil decoder.
func TestStreamIllegalToolDeltaRefused(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	for _, setup := range []struct {
		name  string
		build func(t *testing.T, m *model)
	}{
		{"idle", nil},
		{"text", func(t *testing.T, m *model) {
			t.Helper()
			if _, err := m.streamTransition(StreamEvText, streamArgs{delta: "hi"}); err != nil {
				t.Fatalf("setup text: %v", err)
			}
		}},
		{"reasoning", func(t *testing.T, m *model) {
			t.Helper()
			if _, err := m.streamTransition(StreamEvReasoning, streamArgs{delta: "hmm"}); err != nil {
				t.Fatalf("setup reasoning: %v", err)
			}
		}},
		{"clearPending", func(t *testing.T, m *model) {
			t.Helper()
			// Direct transition calls bypass the Update throttle predicate,
			// so stamp the clock with an immediate clear first.
			if _, err := m.streamTransition(StreamEvText, streamArgs{delta: "one\n"}); err != nil {
				t.Fatalf("setup text: %v", err)
			}
			if _, err := m.streamTransition(StreamEvClearThrottle, streamArgs{delta: "x\n"}); err != nil {
				t.Fatalf("setup throttle: %v", err)
			}
			if m.streamPhase() != StreamClearPending {
				t.Fatalf("setup phase=%s want clearPending", m.streamPhase())
			}
		}},
	} {
		t.Run(setup.name, func(t *testing.T) {
			m := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
			m = m.beginRun(nil)
			withFrozenClock(&m, t0)
			if setup.build != nil {
				setup.build(t, &m)
			}
			beforeText, beforeReasoning := m.streamingTextString(), m.streamingReasoning
			cmd, err := m.streamTransition(StreamEvToolDelta, streamArgs{toolID: "c1", fragment: "x"})
			if err == nil {
				t.Fatalf("toolDelta in %s must be refused", m.streamPhase())
			}
			if cmd != nil {
				t.Fatalf("refused transition must emit nothing, got %T", cmd)
			}
			if m.streamingTextString() != beforeText || m.streamingReasoning != beforeReasoning {
				t.Fatal("refused transition must keep state")
			}
			if !m.validStreamState() {
				t.Fatal("state must stay valid after refusal")
			}
		})
	}
}

// validStreamState pins the impossible mixes and the one benign exception:
// a set fade latch without line ages (spec-impl seed, terminal resize) is
// legal — the renderer falls back to solid ink until the next delta reseeds.
func TestStreamValidState(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	if !m.validStreamState() {
		t.Fatal("fresh model must be valid")
	}

	half := m
	half.streamCallID = "c1"
	if half.validStreamState() {
		t.Fatal("tool ID without decoder must be invalid")
	}
	halfName := m
	halfName.streamCallName = "write_file"
	if halfName.validStreamState() {
		t.Fatal("tool name without decoder must be invalid")
	}
	emptyID := m
	emptyID.streamCallDecoder = newStreamingDecoder()
	emptyID.streamCallName = "write_file"
	if !emptyID.validStreamState() {
		t.Fatal("empty ID with decoder must be valid (id-less providers)")
	}
	if emptyID.streamPhase() != StreamTool {
		t.Fatalf("empty ID with decoder must derive Tool, got %s", emptyID.streamPhase())
	}

	orphan := m
	orphan.pendingStreamClear = true
	if orphan.validStreamState() {
		t.Fatal("deferred clear without a run must be invalid")
	}

	live := m
	live.streamLive = true
	if live.validStreamState() {
		t.Fatal("live estimate without a run must be invalid")
	}

	seeded := m
	seeded.fadeActive = true
	if !seeded.validStreamState() {
		t.Fatal("fade latch without ages must stay legal (seed/resize)")
	}
}
