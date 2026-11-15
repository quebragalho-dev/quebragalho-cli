package tui

// StreamPhase names what the TUI is currently transmitting.
//
//	Text diagram:
//	  Idle --> Text : text (clear tool triple, append, fade on, maybe ClearScreen)
//	  Idle --> Reasoning : reasoning (append, no tool/fade touch)
//	  Idle --> Tool : toolStart (fresh decoder, estimate muted)
//	  Text/Reasoning/Tool --> Text : text (tool tail dropped, never merged)
//	  Tool --> Tool : toolStart (replace decoder, old tail gone) / toolDelta (feed)
//	  Any --> ClearPending : clearThrottle (throttled newline, pending=1, flush scheduled once)
//	  ClearPending --> Text/Tool : text/reasoning/toolDelta (coalesced output)
//	  ClearPending --> * : clearFlush (window elapsed -> ClearScreen, pending=0)
//	  Any --> Idle : end (flush pending -> ClearScreen; zero every twin incl. estimate)
//	  Any --> Idle : cancel (pending dropped silently, no redraw; zero every twin)
//	  Any --> Idle : begin (fresh run zeroes every twin)
//	  Text/Reasoning/Tool/ClearPending --> * : retryDiscard (drop text/tool/fade;
//	    reasoning and pending survive, pre-machine parity)
//	Only streamTransition writes the stream twins (live text/reasoning, tool
//	triple, fade latch/ages/activity, pending clear, live estimate). Stale
//	runID messages are dropped by the Update guards before reaching it;
//	a toolDelta outside Tool is refused with an error, never a panic.

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type StreamPhase int

const (
	StreamIdle StreamPhase = iota
	StreamText
	StreamReasoning
	StreamTool
	StreamClearPending
)

func (p StreamPhase) String() string {
	switch p {
	case StreamIdle:
		return "idle"
	case StreamText:
		return "text"
	case StreamReasoning:
		return "reasoning"
	case StreamTool:
		return "tool"
	case StreamClearPending:
		return "clearPending"
	default:
		return "unknown"
	}
}

type StreamEv int

const (
	StreamEvBegin StreamEv = iota
	StreamEvText
	StreamEvReasoning
	StreamEvToolStart
	StreamEvToolDelta
	StreamEvClearThrottle
	StreamEvClearFlush
	StreamEvEnd
	StreamEvCancel
	StreamEvRetryDiscard
)

func (e StreamEv) String() string {
	switch e {
	case StreamEvBegin:
		return "begin"
	case StreamEvText:
		return "text"
	case StreamEvReasoning:
		return "reasoning"
	case StreamEvToolStart:
		return "toolStart"
	case StreamEvToolDelta:
		return "toolDelta"
	case StreamEvClearThrottle:
		return "clearThrottle"
	case StreamEvClearFlush:
		return "clearFlush"
	case StreamEvEnd:
		return "end"
	case StreamEvCancel:
		return "cancel"
	case StreamEvRetryDiscard:
		return "retryDiscard"
	default:
		return "unknown"
	}
}

// streamLegal is the single table of legal (phase x event) streaming
// transitions. Only toolDelta is restricted (a fragment needs a live tool
// call); every other event is accepted in every phase to preserve the exact
// pre-machine behavior (first delta from Idle, coalesced output, empty-turn
// end, cancel with nothing streamed).
var streamLegal = map[StreamPhase]map[StreamEv]bool{
	StreamIdle: {
		StreamEvBegin: true, StreamEvText: true, StreamEvReasoning: true,
		StreamEvToolStart: true, StreamEvClearThrottle: true, StreamEvClearFlush: true,
		StreamEvEnd: true, StreamEvCancel: true, StreamEvRetryDiscard: true,
	},
	StreamText: {
		StreamEvBegin: true, StreamEvText: true, StreamEvReasoning: true,
		StreamEvToolStart: true, StreamEvClearThrottle: true, StreamEvClearFlush: true,
		StreamEvEnd: true, StreamEvCancel: true, StreamEvRetryDiscard: true,
	},
	StreamReasoning: {
		StreamEvBegin: true, StreamEvText: true, StreamEvReasoning: true,
		StreamEvToolStart: true, StreamEvClearThrottle: true, StreamEvClearFlush: true,
		StreamEvEnd: true, StreamEvCancel: true, StreamEvRetryDiscard: true,
	},
	StreamTool: {
		StreamEvBegin: true, StreamEvText: true, StreamEvReasoning: true,
		StreamEvToolStart: true, StreamEvToolDelta: true, StreamEvClearThrottle: true,
		StreamEvClearFlush: true, StreamEvEnd: true, StreamEvCancel: true,
		StreamEvRetryDiscard: true,
	},
	StreamClearPending: {
		StreamEvBegin: true, StreamEvText: true, StreamEvReasoning: true,
		StreamEvToolStart: true, StreamEvClearThrottle: true, StreamEvClearFlush: true,
		StreamEvEnd: true, StreamEvCancel: true, StreamEvRetryDiscard: true,
	},
}

func streamEventLegal(phase StreamPhase, ev StreamEv) bool {
	return streamLegal[phase][ev]
}

// streamPhase derives the current streaming phase from the live fields. The
// tool call wins while its decoder is live (fragments keep feeding it);
// otherwise a deferred clear outranks settled prose. The decoder — not the
// call ID — marks the live call: providers that omit the ID still feed
// fragments through the pre-machine runID+ID guard.
func (m model) streamPhase() StreamPhase {
	if m.streamCallDecoder != nil {
		return StreamTool
	}
	if m.pendingStreamClear {
		return StreamClearPending
	}
	if len(m.streamingText) > 0 {
		return StreamText
	}
	if m.streamingReasoning != "" {
		return StreamReasoning
	}
	return StreamIdle
}

// validStreamState reports whether the stream fields form a possible
// combination. Impossible states the type allows but the machine forbids:
//   - a tool ID or name without a decoder (the delta guard assumes the
//     triple is dropped atomically).
//   - a deferred ClearScreen without an active run (would redraw while idle).
//   - a live estimate without an active run (stale speed readout).
//
// A set fade latch without line ages is LEGAL by design: the spec-impl seed
// sets fadeActive before the first delta, and a terminal resize drops the
// per-line ages while the latch keeps the tick alive; the renderer falls
// back to solid ink until the next delta reseeds.
func (m model) validStreamState() bool {
	// A live tool call is the decoder, not the ID: providers that omit the
	// call ID still feed fragments through the pre-machine runID+ID guard,
	// so an empty ID with a decoder is a VALID state. The block view still
	// requires ID != "" and stays empty, while the live estimate and the
	// activity clock advance.
	if m.streamCallDecoder == nil {
		if m.streamCallID != "" || m.streamCallName != "" {
			return false
		}
	}
	if m.pendingStreamClear && (!m.pending || m.activeRunID == 0) {
		return false
	}
	if m.streamLive && (!m.pending || m.activeRunID == 0) {
		return false
	}
	return true
}

// streamArgs carries the optional payload for a streaming transition.
type streamArgs struct {
	delta    string
	toolID   string
	toolName string
	fragment string
}

// streamClearThrottled reports whether a newline arriving now would fall
// inside the stream-clear throttle window. The caller already checked the
// delta contains a newline and clears are enabled; the transition performs
// the matching half (immediate clear vs deferred) without re-deciding.
func (m model) streamClearThrottled() bool {
	return m.now().Sub(m.lastStreamClear) < streamClearThrottle
}

// zeroStreamTwins is the single place that zeroes the stream twin set.
// end/cancel/begin all go through it so the three settle paths zero exactly
// the same fields: live text/reasoning, the tool triple, the fade state, the
// deferred clear, and the live estimate. The throttle clock (lastStreamClear)
// and the disable flags are config, not twins, and are never touched.
func (m *model) zeroStreamTwins() {
	m.streamingText = nil
	m.streamingReasoning = ""
	m.streamingReasoningExpanded = false
	m.streamCallID = ""
	m.streamCallName = ""
	m.streamCallDecoder = nil
	m.fadeActive = false
	m.lineAges = nil
	m.lastStreamActivity = time.Time{}
	m.pendingStreamClear = false
	m.streamLive = false
	m.streamEstimateChars = 0
	m.streamEstimateStart = time.Time{}
}

// streamAppendText is the shared first half of the text/clearThrottle
// transitions: streaming text means any in-progress tool call has finished
// (its decoder tail is dropped, never merged into the prose), then the delta
// is appended, counted toward the live estimate, and recorded for the fade.
func (m *model) streamAppendText(delta string) {
	m.streamCallID = ""
	m.streamCallName = ""
	m.streamCallDecoder = nil
	m.streamingText = append(m.streamingText, delta...)
	m.countStreamEstimate(delta)
	m.recordStreamingDelta(delta)
}

// streamTransition is the single point that writes the stream twins. It
// validates (phase x event) against streamLegal and refuses illegal
// transitions with an error while keeping state, never panicking. Commands
// preserve the exact pre-machine cadence: an immediate ClearScreen, a
// one-shot deferred flush, or the fade's self-perpetuating tick.
func (m *model) streamTransition(ev StreamEv, a streamArgs) (tea.Cmd, error) {
	phase := m.streamPhase()
	if !streamEventLegal(phase, ev) {
		return nil, fmt.Errorf("illegal stream transition %s in %s", ev, phase)
	}
	switch ev {
	case StreamEvBegin, StreamEvCancel:
		// A cancelled turn never redraws: the pending clear is dropped, not
		// flushed. A fresh run never inherits the previous turn's twins.
		m.zeroStreamTwins()
		return nil, nil
	case StreamEvEnd:
		hadPending := m.pendingStreamClear
		m.zeroStreamTwins()
		// A throttled newline that turned out to be the turn's last delta
		// still owes its caret repair: flush it here so the ghost caret is
		// not left behind at stream end.
		if hadPending {
			return tea.ClearScreen, nil
		}
		return nil, nil
	case StreamEvText:
		m.streamAppendText(a.delta)
		var cmds []tea.Cmd
		if strings.Contains(a.delta, "\n") && !m.streamClearDisabled {
			now := m.now()
			m.lastStreamClear = now
			m.pendingStreamClear = false
			cmds = append(cmds, tea.ClearScreen)
		}
		if !m.fadeDisabled {
			startTick := !m.fadeActive
			m.fadeActive = true
			if startTick {
				cmds = append(cmds, streamingFadeTick())
			}
		}
		return tea.Batch(cmds...), nil
	case StreamEvClearThrottle:
		m.streamAppendText(a.delta)
		var cmds []tea.Cmd
		// The redraw the throttled newline owes is deferred, not dropped: a
		// one-shot flush is scheduled, coalesced while one is pending. The
		// clamp mirrors the immediate-clear branch for a clock that crosses
		// the throttle edge between the Update predicate and this write.
		if !m.pendingStreamClear {
			now := m.now()
			m.pendingStreamClear = true
			if d := streamClearThrottle - now.Sub(m.lastStreamClear); d > 0 {
				cmds = append(cmds, scheduleStreamClearFlush(d))
			} else {
				m.lastStreamClear = now
				m.pendingStreamClear = false
				cmds = append(cmds, tea.ClearScreen)
			}
		}
		if !m.fadeDisabled {
			startTick := !m.fadeActive
			m.fadeActive = true
			if startTick {
				cmds = append(cmds, streamingFadeTick())
			}
		}
		return tea.Batch(cmds...), nil
	case StreamEvReasoning:
		m.streamingReasoning += a.delta
		m.countStreamEstimate(a.delta)
		if a.delta != "" {
			m.lastStreamActivity = m.now()
		}
		return nil, nil
	case StreamEvToolStart:
		m.streamCallID = a.toolID
		m.streamCallName = a.toolName
		m.streamCallDecoder = newStreamingDecoder()
		m.muteStreamLive()
		return nil, nil
	case StreamEvToolDelta:
		// The table already restricted this event to Tool, but a fragment
		// for a superseded call (or a nil decoder) is still dropped, never
		// fed: same observable as the pre-machine guard.
		if a.toolID != m.streamCallID || m.streamCallDecoder == nil {
			return nil, nil
		}
		m.streamCallDecoder.feed(a.fragment)
		m.countStreamEstimate(a.fragment)
		m.lastStreamActivity = m.now()
		return nil, nil
	case StreamEvClearFlush:
		now := m.now()
		if m.pendingStreamClear && now.Sub(m.lastStreamClear) >= streamClearThrottle {
			m.lastStreamClear = now
			m.pendingStreamClear = false
			return tea.ClearScreen, nil
		}
		return nil, nil
	case StreamEvRetryDiscard:
		// The failed attempt's live text vanishes (the retry re-sends clean
		// context); the tool block and the fade go with it. Reasoning and a
		// pending clear survive, pre-machine parity.
		m.streamingText = nil
		m.streamCallID = ""
		m.streamCallName = ""
		m.streamCallDecoder = nil
		m.fadeActive = false
		m.lineAges = nil
		m.lastStreamActivity = time.Time{}
		return nil, nil
	default:
		return nil, fmt.Errorf("unhandled stream event %s", ev)
	}
}
