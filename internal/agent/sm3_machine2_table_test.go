package agent

import (
	"testing"
)

// SM3 Machine 2, commit B: table test fixing the whole phase x event space
// of the compaction machine. Every one of the 6 x 10 = 60 cells is either
// legal (with pinned next phase, field effects and notify) or refused
// (state byte-identical, no notify, no panic).

func sm3SeedPhase(t *testing.T, phase compactPhase) *compactionState {
	t.Helper()
	return &compactionState{
		enabled:           phase != phaseDisabled,
		threshold:         700,
		window:            1000,
		preserveLast:      2,
		phase:             phase,
		lowWaterMark:      111,
		reactiveAttempted: phase == phaseSpent,
	}
}

// sm3Snap captures the comparable state fields (the struct also holds func
// fields, so whole-struct comparison does not compile).
type sm3Snap struct {
	enabled           bool
	threshold         int
	window            int
	phase             compactPhase
	lowWaterMark      int
	reactiveAttempted bool
}

func sm3Snapshot(state *compactionState) sm3Snap {
	return sm3Snap{
		enabled:           state.enabled,
		threshold:         state.threshold,
		window:            state.window,
		phase:             state.phase,
		lowWaterMark:      state.lowWaterMark,
		reactiveAttempted: state.reactiveAttempted,
	}
}

func sm3ExpectLegal(phase compactPhase, kind compactEventKind) bool {
	if kind == evWindowUpdate {
		return true // legal in every phase (pins the pending product decision)
	}
	if phase == phaseDisabled {
		return false
	}
	if phase == phaseSpent && (kind == evReactivePaidShrink || kind == evReactivePaidFail) {
		return false // budget spent: paid reactive is refused (free prune still allowed)
	}
	return true
}

func sm3ExpectNext(phase compactPhase, kind compactEventKind) compactPhase {
	switch kind {
	case evPruneFits:
		return phasePruned
	case evPaidShrink, evPaidNoShrink:
		return phasePaid
	case evReactivePruneFits:
		return phaseRecovered
	case evReactivePaidShrink, evReactivePaidFail:
		return phaseSpent
	case evWindowUpdate:
		return phaseBelow
	default:
		return phase // below-threshold, paid-error and reactive-noop stay
	}
}

func TestSm3CompactTransitionTable(t *testing.T) {
	phases := []compactPhase{phaseDisabled, phaseBelow, phasePruned, phasePaid, phaseRecovered, phaseSpent}
	kinds := []compactEventKind{
		evBelowThreshold, evPruneFits, evPaidShrink, evPaidNoShrink, evPaidError,
		evReactivePruneFits, evReactivePaidShrink, evReactivePaidFail, evReactiveNoop,
		evWindowUpdate,
	}
	for _, phase := range phases {
		for _, kind := range kinds {
			name := phase.String() + "+" + kind.String()
			t.Run(name, func(t *testing.T) {
				for _, rewrote := range []bool{false, true} {
					state := sm3SeedPhase(t, phase)
					before := sm3Snapshot(state)
					ev := compactEvent{kind: kind, size: 222, rewrote: rewrote, window: 2000}
					notify := state.transition(ev)

					if !sm3ExpectLegal(phase, kind) {
						if notify {
							t.Fatalf("illegal cell must not notify")
						}
						if sm3Snapshot(state) != before {
							t.Fatalf("illegal cell must leave state untouched: %+v -> %+v", before, sm3Snapshot(state))
						}
						continue
					}
					if want := sm3ExpectNext(phase, kind); state.phase != want {
						t.Fatalf("next phase = %s, want %s", state.phase, want)
					}
					// Twin-field effects per event kind.
					switch kind {
					case evBelowThreshold, evReactiveNoop:
						if state.lowWaterMark != before.lowWaterMark || state.reactiveAttempted != before.reactiveAttempted || state.enabled != before.enabled {
							t.Fatalf("noop event must not move twins: %+v", state)
						}
						if notify {
							t.Fatalf("noop event must not notify")
						}
					case evPruneFits, evPaidShrink, evReactivePruneFits:
						if state.lowWaterMark != 222 {
							t.Fatalf("watermark = %d, want event size 222", state.lowWaterMark)
						}
						if !notify {
							t.Fatalf("rewrite event must notify")
						}
						if state.reactiveAttempted != before.reactiveAttempted {
							t.Fatalf("proactive/free event must not consume budget: %+v", state)
						}
					case evPaidNoShrink:
						if state.lowWaterMark != 222 {
							t.Fatalf("watermark = %d, want event size 222", state.lowWaterMark)
						}
						if notify != rewrote {
							t.Fatalf("notify = %v, want rewrote=%v", notify, rewrote)
						}
					case evPaidError:
						if state.lowWaterMark != before.lowWaterMark || state.reactiveAttempted != before.reactiveAttempted {
							t.Fatalf("paid error must not move twins: %+v", state)
						}
						if notify != rewrote {
							t.Fatalf("notify = %v, want rewrote=%v", notify, rewrote)
						}
					case evReactivePaidShrink:
						if state.lowWaterMark != 222 || !state.reactiveAttempted || !notify {
							t.Fatalf("paid shrink must fix watermark, consume budget and notify: %+v", state)
						}
					case evReactivePaidFail:
						if !state.reactiveAttempted {
							t.Fatalf("paid fail must consume budget: %+v", state)
						}
						if state.lowWaterMark != before.lowWaterMark {
							t.Fatalf("paid fail must not move watermark: %+v", state)
						}
						if notify != rewrote {
							t.Fatalf("notify = %v, want rewrote=%v", notify, rewrote)
						}
					case evWindowUpdate:
						if !state.enabled || state.window != 2000 || state.threshold != compactionThreshold(2000) {
							t.Fatalf("window update must enable+re-derive: %+v", state)
						}
						if state.lowWaterMark != 0 {
							t.Fatalf("window update must clear watermark: %+v", state)
						}
						if notify {
							t.Fatalf("window update must not notify")
						}
					}
				}
			})
		}
	}
}

// Phase and event names are distinct and human-readable.
func TestSm3CompactNames(t *testing.T) {
	seenPhase := map[string]bool{}
	for _, p := range []compactPhase{phaseDisabled, phaseBelow, phasePruned, phasePaid, phaseRecovered, phaseSpent} {
		if p.String() == "" || seenPhase[p.String()] {
			t.Fatalf("bad phase name %q", p.String())
		}
		seenPhase[p.String()] = true
	}
	seenEvent := map[string]bool{}
	for _, k := range []compactEventKind{
		evBelowThreshold, evPruneFits, evPaidShrink, evPaidNoShrink, evPaidError,
		evReactivePruneFits, evReactivePaidShrink, evReactivePaidFail, evReactiveNoop, evWindowUpdate,
	} {
		if k.String() == "" || k.String() == "unknown" || seenEvent[k.String()] {
			t.Fatalf("bad event name %q", k.String())
		}
		seenEvent[k.String()] = true
	}
}

// Refusal on a zero-value state (disabled) never panics and changes nothing.
func TestSm3CompactIllegalNeverPanics(t *testing.T) {
	state := &compactionState{}
	for _, kind := range []compactEventKind{
		evBelowThreshold, evPruneFits, evPaidShrink, evPaidNoShrink, evPaidError,
		evReactivePruneFits, evReactivePaidShrink, evReactivePaidFail, evReactiveNoop,
	} {
		if notify := state.transition(compactEvent{kind: kind, size: 5, rewrote: true}); notify {
			t.Fatalf("disabled+ %s must not notify", kind)
		}
	}
	if state.enabled || state.phase != phaseDisabled || state.lowWaterMark != 0 || state.reactiveAttempted {
		t.Fatalf("disabled refusals must not move state: %+v", state)
	}
}
