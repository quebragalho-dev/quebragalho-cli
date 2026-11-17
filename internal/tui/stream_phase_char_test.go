package tui

// Characterization of the live-streaming behavior BEFORE the explicit
// StreamPhase machine (SM7). Every test below passes on the untouched dev
// and must keep passing byte-for-byte after the machine lands without any
// edit: the machine is a pure refactoring except for the streamLive leak
// proven separately in stream_live_leak_repro_test.go.
//
// What is pinned: the observable stream state (live text/reasoning, tool
// triple, fade latch, pending clear, decoder tail) and the emitted commands
// (immediate ClearScreen, deferred flush schedule, end-of-turn flush) for
// the nine mandatory scenarios: text->end; reasoning->text->end;
// toolStart->delta->text; two toolStarts; stale runID text; retry with
// discard; cancel in each phase; end with pending clear; flush after end.

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// streamCharSnapshot is the observable streaming state plus command flags.
// streamLive/streamEstimate are deliberately NOT snapshotted here: their
// end-of-turn leak is the proven defect (see stream_live_leak_repro_test.go)
// and asserting today's leaked value would bake the bug into the
// characterization.
type streamCharSnapshot struct {
	text        string
	reasoning   string
	toolID      string
	toolName    string
	hasDecoder  bool
	decoderLen  int // completed content lines in the decoder tail
	decoderCur  string
	decoderPath string
	fadeActive  bool
	lineAges    int // -1 when nil
	pending     bool
	cleared     bool // a ClearScreen command was emitted alongside
}

func snapStreamChar(m model, cmdClear bool) streamCharSnapshot {
	s := streamCharSnapshot{
		text:       m.streamingTextString(),
		reasoning:  m.streamingReasoning,
		toolID:     m.streamCallID,
		toolName:   m.streamCallName,
		hasDecoder: m.streamCallDecoder != nil,
		fadeActive: m.fadeActive,
		pending:    m.pendingStreamClear,
		cleared:    cmdClear,
		lineAges:   -1,
	}
	if m.streamCallDecoder != nil {
		s.decoderLen = m.streamCallDecoder.lineCount
		s.decoderCur = string(m.streamCallDecoder.cur)
		s.decoderPath = m.streamCallDecoder.path
	}
	if m.lineAges != nil {
		s.lineAges = len(m.lineAges)
	}
	return s
}

func charModel(t *testing.T, now time.Time) model {
	t.Helper()
	m := newModel(context.Background(), Options{ModelName: "gpt-4.1"})
	m = m.beginRun(nil)
	withFrozenClock(&m, now)
	return m
}

func applyText(t *testing.T, m model, runID int, delta string) (model, bool) {
	t.Helper()
	updated, cmd := m.Update(agentTextMsg{runID: runID, delta: delta})
	return updated.(model), cmdIncludesClearScreen(cmd)
}

func TestStreamChar_TextToEnd(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := charModel(t, t0)
	rid := m.activeRunID

	m, _ = applyText(t, m, rid, "hello ")
	m, cleared := applyText(t, m, rid, "world")
	if cleared {
		t.Fatal("plain text without newline must not ClearScreen")
	}
	if got := snapStreamChar(m, false); got.text != "hello world" {
		t.Fatalf("live text = %q", got.text)
	}

	updated, cmd := m.Update(agentResponseMsg{runID: rid})
	m = updated.(model)
	if cmdIncludesClearScreen(cmd) {
		t.Fatal("end without pending clear must not ClearScreen")
	}
	if got := snapStreamChar(m, false); got.text != "" || got.fadeActive || got.lineAges != -1 {
		t.Fatalf("end must settle live text/fade: %+v", got)
	}
}

func TestStreamChar_ReasoningTextToEnd(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := charModel(t, t0)
	rid := m.activeRunID

	updated, _ := m.Update(agentReasoningMsg{runID: rid, delta: "thinking..."})
	m = updated.(model)
	m, _ = applyText(t, m, rid, "answer")
	// Reasoning survives text today: both accumulate side by side.
	if got := snapStreamChar(m, false); got.reasoning != "thinking..." || got.text != "answer" {
		t.Fatalf("reasoning must survive text: %+v", got)
	}

	updated, cmd := m.Update(agentResponseMsg{runID: rid})
	m = updated.(model)
	if cmdIncludesClearScreen(cmd) {
		t.Fatal("end without pending clear must not ClearScreen")
	}
	if got := snapStreamChar(m, false); got.reasoning != "" || got.text != "" {
		t.Fatalf("end must settle reasoning and text: %+v", got)
	}
}

func TestStreamChar_ToolDeltaThenTextDropsTail(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := charModel(t, t0)
	rid := m.activeRunID

	updated, _ := m.Update(toolCallStreamStartMsg{runID: rid, id: "c1", name: "write_file"})
	m = updated.(model)
	updated, _ = m.Update(toolCallStreamDeltaMsg{runID: rid, id: "c1", fragment: `{"path":"a.go","content":"line1\nline2`})
	m = updated.(model)
	before := snapStreamChar(m, false)
	if before.toolID != "c1" || !before.hasDecoder || before.decoderLen != 1 {
		t.Fatalf("tool tail should hold 1 completed line: %+v", before)
	}
	view := plainRender(t, m.streamingToolCallView(80))
	if !strings.Contains(view, "a.go") {
		t.Fatalf("live view should show the path, got %q", view)
	}

	// Streaming text means the tool call finished: the live "writing" block
	// (including the decoder tail) is dropped, never merged into the prose.
	m, _ = applyText(t, m, rid, "done prose")
	after := snapStreamChar(m, false)
	if after.toolID != "" || after.hasDecoder {
		t.Fatalf("text must drop the live tool block: %+v", after)
	}
	if after.text != "done prose" {
		t.Fatalf("prose must be exactly the new delta, got %q", after.text)
	}
	if out := m.streamingToolCallView(80); out != "" {
		t.Fatalf("tool view must be empty after text, got %q", out)
	}
}

func TestStreamChar_TwoToolStartsReplace(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := charModel(t, t0)
	rid := m.activeRunID

	updated, _ := m.Update(toolCallStreamStartMsg{runID: rid, id: "c1", name: "write_file"})
	m = updated.(model)
	updated, _ = m.Update(toolCallStreamDeltaMsg{runID: rid, id: "c1", fragment: `{"path":"old.go","content":"old`})
	m = updated.(model)

	// A second start overwrites the first decoder outright: the old tail is
	// gone, the new call starts with a fresh buffer.
	updated, _ = m.Update(toolCallStreamStartMsg{runID: rid, id: "c2", name: "edit_file"})
	m = updated.(model)
	if got := snapStreamChar(m, false); got.toolID != "c2" || got.toolName != "edit_file" {
		t.Fatalf("second start must replace the call: %+v", got)
	} else if got.decoderLen != 0 || got.decoderCur != "" || got.decoderPath != "" {
		t.Fatalf("second start must reset the decoder: %+v", got)
	}
	if m.streamingTextString() != "" {
		t.Fatalf("tool start must not touch live text, got %q", m.streamingTextString())
	}
}

func TestStreamChar_StaleRunIDDropped(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := charModel(t, t0)
	rid := m.activeRunID
	m, _ = applyText(t, m, rid, "live")
	before := snapStreamChar(m, false)

	stale := rid + 999
	for _, msg := range []tea.Msg{
		agentTextMsg{runID: stale, delta: "stale"},
		agentReasoningMsg{runID: stale, delta: "stale"},
		toolCallStreamStartMsg{runID: stale, id: "s", name: "write_file"},
		toolCallStreamDeltaMsg{runID: stale, id: "s", fragment: "stale"},
	} {
		updated, cmd := m.Update(msg)
		_ = cmd
		m = updated.(model)
	}
	if got := snapStreamChar(m, false); !reflect.DeepEqual(got, before) {
		t.Fatalf("stale runID must not move stream state:\nbefore=%+v\nafter =%+v", before, got)
	}
}

func TestStreamChar_RetryDiscardKeepsReasoningAndPending(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := charModel(t, t0)
	rid := m.activeRunID

	// Build a pending clear first: immediate clear, then a throttled newline.
	m, firstClear := applyText(t, m, rid, "line one\n")
	if !firstClear {
		t.Fatal("setup: first newline should clear immediately")
	}
	m, secondClear := applyText(t, m, rid, "line two\n")
	if secondClear || !m.pendingStreamClear {
		t.Fatal("setup: second newline should throttle into pending")
	}
	updated, _ := m.Update(toolCallStreamStartMsg{runID: rid, id: "c1", name: "write_file"})
	m = updated.(model)
	updated, _ = m.Update(agentReasoningMsg{runID: rid, delta: "keep me"})
	m = updated.(model)

	updated, _ = m.Update(agentRetryWaitMsg{runID: rid, attempt: 1, max: 3, wait: time.Minute, summary: "502", discarded: true})
	m = updated.(model)
	got := snapStreamChar(m, false)
	// Today's exact discard effect: the partial text and the tool block go,
	// the fade hard-stops, but reasoning AND the pending clear survive.
	if got.text != "" || got.toolID != "" || got.hasDecoder || got.fadeActive || got.lineAges != -1 {
		t.Fatalf("discard must drop text/tool/fade: %+v", got)
	}
	if got.reasoning != "keep me" {
		t.Fatalf("discard must keep reasoning, got %+v", got)
	}
	if !got.pending {
		t.Fatalf("discard must keep the pending clear: %+v", got)
	}
	if m.retryWait == nil {
		t.Fatal("discard must arm the retry wait")
	}
}

func TestStreamChar_CancelInEachPhase(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	setup := map[string]func(t *testing.T, m model, rid int) model{
		"idleTurn": func(t *testing.T, m model, rid int) model { return m },
		"text": func(t *testing.T, m model, rid int) model {
			m, _ = applyText(t, m, rid, "partial")
			return m
		},
		"reasoning": func(t *testing.T, m model, rid int) model {
			updated, _ := m.Update(agentReasoningMsg{runID: rid, delta: "hmm"})
			return updated.(model)
		},
		"tool": func(t *testing.T, m model, rid int) model {
			updated, _ := m.Update(toolCallStreamStartMsg{runID: rid, id: "c1", name: "write_file"})
			m = updated.(model)
			updated, _ = m.Update(toolCallStreamDeltaMsg{runID: rid, id: "c1", fragment: `{"path":"a.go","content":"x`})
			return updated.(model)
		},
		"clearPending": func(t *testing.T, m model, rid int) model {
			m, _ = applyText(t, m, rid, "line one\n")
			m, _ = applyText(t, m, rid, "line two\n")
			if !m.pendingStreamClear {
				t.Fatal("setup: expected a pending clear")
			}
			return m
		},
	}
	for name, build := range setup {
		t.Run(name, func(t *testing.T) {
			m := charModel(t, t0)
			m = build(t, m, m.activeRunID)
			m.cancelRun()
			got := snapStreamChar(m, false)
			if got.text != "" || got.reasoning != "" || got.toolID != "" ||
				got.toolName != "" || got.hasDecoder || got.fadeActive ||
				got.lineAges != -1 || got.pending {
				t.Fatalf("cancel must settle every stream twin, got %+v", got)
			}
			// A cancelled turn never redraws: the pending clear is dropped,
			// and a flush arriving after the cancel is a no-op.
			updated, cmd := m.Update(streamClearFlushMsg{})
			m = updated.(model)
			if cmdIncludesClearScreen(cmd) {
				t.Fatal("flush after cancel must not ClearScreen")
			}
			if m.pendingStreamClear {
				t.Fatal("flush after cancel must leave no pending clear")
			}
		})
	}
}

func TestStreamChar_EndWithPendingClearFlushes(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := charModel(t, t0)
	rid := m.activeRunID

	m, _ = applyText(t, m, rid, "line one\n")
	m, _ = applyText(t, m, rid, "line two\n")
	if !m.pendingStreamClear {
		t.Fatal("setup: expected a pending clear")
	}
	updated, cmd := m.Update(agentResponseMsg{runID: rid})
	m = updated.(model)
	if !cmdIncludesClearScreen(cmd) {
		t.Fatal("end must flush a pending stream-clear rather than dropping it")
	}
	if m.pendingStreamClear {
		t.Fatal("end should clear the pending flag once flushed")
	}
}

func TestStreamChar_FlushAfterEndNoops(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := charModel(t, t0)
	rid := m.activeRunID

	m, _ = applyText(t, m, rid, "line one\n")
	updated, _ := m.Update(agentResponseMsg{runID: rid})
	m = updated.(model)
	withFrozenClock(&m, t0.Add(10*time.Second))
	updated, cmd := m.Update(streamClearFlushMsg{})
	m = updated.(model)
	if cmdIncludesClearScreen(cmd) {
		t.Fatal("flush after end must not ClearScreen")
	}
	if cmd != nil {
		t.Fatalf("flush after end must be a no-op, got %T", cmd)
	}
}

func TestStreamChar_ResizeKeepsFadeLatchWithoutAges(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	m := charModel(t, t0)
	rid := m.activeRunID
	m.fadeDisabled = false

	m, _ = applyText(t, m, rid, "hello")
	if !m.fadeActive || m.lineAges == nil {
		t.Fatal("setup: expected active fade with ages")
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(model)
	// Benign by design: the latch stays (the tick keeps running) while the
	// per-line ages are dropped (width re-wraps); the renderer falls back to
	// solid ink until the next delta reseeds.
	if !m.fadeActive || m.lineAges != nil {
		t.Fatalf("resize must keep latch and drop ages, fade=%v ages=%v", m.fadeActive, m.lineAges)
	}
}
