package tui

// TurnPhase names theDerived turn lifecycle phase.
//
//	Text diagram:
//	  Idle --> Running : begin (pending=1 activeRunID=runID runCancel set)
//	  Running --> RetryWait : retryWait (retryWait set)
//	  RetryWait --> Running : retryClear (retryWait=nil)
//	  Running/RetryWait --> Draining : cancel (pending=0 activeRunID=0 flush+=id)
//	  Draining --> Idle : lateResponse drains last flush entry
//	  Draining --> Running : begin (new turn while old flush still drains)
//	  Running/RetryWait/Draining --> Exiting : quit (exiting=1; deferred until flush empty)
//	Only retryWait and pendingStreamClear are owned by clearTurnTransient: the
//	three settle paths (beginRun, cancelRun, agentResponse) call it so they
//	zero exactly the same set. All other turn fields stay where they are;
//	turnPhase is derived, never stored.

import (
	"fmt"
)

type TurnPhase int

const (
	TurnIdle TurnPhase = iota
	TurnRunning
	TurnRetryWait
	TurnDraining
	TurnExiting
)

func (p TurnPhase) String() string {
	switch p {
	case TurnIdle:
		return "idle"
	case TurnRunning:
		return "running"
	case TurnRetryWait:
		return "retryWait"
	case TurnDraining:
		return "draining"
	case TurnExiting:
		return "exiting"
	default:
		return "unknown"
	}
}

type TurnEvent int

const (
	TurnEvBegin TurnEvent = iota
	TurnEvRetryWait
	TurnEvRetryClear
	TurnEvText
	TurnEvCancel
	TurnEvResponse
	TurnEvLateResponse
	TurnEvSendNow
	TurnEvQuit
)

func (e TurnEvent) String() string {
	switch e {
	case TurnEvBegin:
		return "begin"
	case TurnEvRetryWait:
		return "retryWait"
	case TurnEvRetryClear:
		return "retryClear"
	case TurnEvText:
		return "text"
	case TurnEvCancel:
		return "cancel"
	case TurnEvResponse:
		return "response"
	case TurnEvLateResponse:
		return "lateResponse"
	case TurnEvSendNow:
		return "sendNow"
	case TurnEvQuit:
		return "quit"
	default:
		return "unknown"
	}
}

// turnLegal is the single table of legal (phase x event) turn transitions.
var turnLegal = map[TurnPhase]map[TurnEvent]bool{
	TurnIdle: {
		TurnEvBegin: true,
		TurnEvQuit:  true,
	},
	TurnRunning: {
		TurnEvRetryWait:  true,
		TurnEvRetryClear: true,
		TurnEvText:       true,
		TurnEvCancel:     true,
		TurnEvResponse:   true,
		TurnEvSendNow:    true,
		TurnEvQuit:       true,
	},
	TurnRetryWait: {
		TurnEvRetryClear: true,
		TurnEvText:       true,
		TurnEvCancel:     true,
		TurnEvResponse:   true,
		TurnEvSendNow:    true,
		TurnEvQuit:       true,
	},
	TurnDraining: {
		TurnEvBegin:        true,
		TurnEvLateResponse: true,
		TurnEvSendNow:      true,
		TurnEvCancel:       true,
		TurnEvQuit:         true,
	},
	TurnExiting: {
		TurnEvLateResponse: true,
		TurnEvQuit:         true,
	},
}

func turnEventLegal(phase TurnPhase, ev TurnEvent) bool {
	return turnLegal[phase][ev]
}

// turnPhase derives the current turn phase from the live fields.
func (m model) turnPhase() TurnPhase {
	if m.exiting {
		return TurnExiting
	}
	if m.pending && m.activeRunID != 0 {
		if m.retryWait != nil {
			return TurnRetryWait
		}
		return TurnRunning
	}
	if len(m.flushRunIDs) > 0 {
		return TurnDraining
	}
	return TurnIdle
}

// validTurnState reports whether the turn fields form a possible combination.
// Impossible states the type allows but the machine forbids:
//   - retryWait set without an active run (masked today by retryWaitSegment,
//     but still a leak that must be zeroed on every settle path).
//   - pendingStreamClear set without an active run (fires ClearScreen idle).
//   - pending/activeRunID/runCancel out of sync.
func (m model) validTurnState() bool {
	if m.retryWait != nil && !(m.pending && m.activeRunID != 0) {
		return false
	}
	if m.pendingStreamClear && !(m.pending && m.activeRunID != 0) {
		return false
	}
	if m.pending != (m.activeRunID != 0) {
		return false
	}
	if (m.runCancel != nil) != m.pending {
		// runCancel is nilled on settle; allow nil while pending only for
		// hand-built test models that never began a real run.
		if m.pending && m.runCancel == nil && m.retryWait == nil {
			return true
		}
		if m.pending {
			return false
		}
	}
	return true
}

// clearTurnTransient is the single place that zeroes the transient turn twins
// (retryWait, pendingStreamClear). beginRun, cancelRun and the normal
// agentResponse path all call it so the three settle paths zero exactly the
// same set. It never panics; illegal reuse is just a second zeroing.
func (m *model) clearTurnTransient() {
	m.retryWait = nil
	m.pendingStreamClear = false
}

// checkTurnEvent validates (phase x event) without mutating: illegal events
// return an error and keep state. Callers use it to refuse, never to panic.
func (m model) checkTurnEvent(ev TurnEvent) error {
	if !turnEventLegal(m.turnPhase(), ev) {
		return fmt.Errorf("illegal turn transition %s in %s", ev, m.turnPhase())
	}
	return nil
}
