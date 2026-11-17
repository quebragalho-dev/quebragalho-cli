package swarm

// taskPhase names the single internal state of a swarm task: the visible
// Status plus the invisible handoff claim bit.
//
//	Text diagram:
//	  pending -> running : launch (SetStatus)
//	  pending -> handingOffPending : begin
//	  running -> handingOffRunning : begin
//	  handingOffPending -> pending : abort (back to origin)
//	  handingOffRunning -> running : abort (back to origin)
//	  handingOffPending -> handedOff : commit (successor pending)
//	  handingOffRunning -> handedOff : commit (successor pending)
//	  pending/running -> done/failed : finish or SetStatus
//	  pending/running -> pending : reassign (new owner)
//	  handedOff/done/failed -> (terminal, no exit)
//	Only (*Coordinator).transitionLocked writes Status/handoff (the twins are
//	installed together from the target phase); handing-off phases project to
//	pending/running outside the package, so no output, event, or disk byte
//	ever shows "handing-off".

// taskPhase is the internal task state.
type taskPhase int

const (
	phasePending taskPhase = iota
	phaseRunning
	phaseHandingOffPending
	phaseHandingOffRunning
	phaseHandedOff
	phaseDone
	phaseFailed
)

// String reports the phase for debug/test output only. It is never used for
// coordinator output, events, or persistence, so the handing-off spellings
// here cannot leak into anything the user or the disk sees.
func (p taskPhase) String() string {
	switch p {
	case phasePending:
		return "pending"
	case phaseRunning:
		return "running"
	case phaseHandingOffPending:
		return "handing-off(pending)"
	case phaseHandingOffRunning:
		return "handing-off(running)"
	case phaseHandedOff:
		return "handed-off"
	case phaseDone:
		return "done"
	case phaseFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// status projects the phase to the externally visible Status. Handing-off
// phases project to their origin (pending/running), exactly as the code did
// before the machine was explicit.
func (p taskPhase) status() TaskStatus {
	switch p {
	case phasePending, phaseHandingOffPending:
		return StatusPending
	case phaseRunning, phaseHandingOffRunning:
		return StatusRunning
	case phaseHandedOff:
		return StatusHandedOff
	case phaseDone:
		return StatusDone
	case phaseFailed:
		return StatusFailed
	default:
		return ""
	}
}

// claimed reports whether the phase holds the invisible handoff claim.
func (p taskPhase) claimed() bool {
	return p == phaseHandingOffPending || p == phaseHandingOffRunning
}

// terminal reports whether the phase is final. It mirrors TaskStatus.terminal
// over the projection.
func (p taskPhase) terminal() bool {
	switch p {
	case phaseHandedOff, phaseDone, phaseFailed:
		return true
	default:
		return false
	}
}

// phaseOf derives the internal phase from the stored twins. It reports false
// for combinations the writers never produce (unknown status, or a handoff
// claim on a terminal status) so callers fail closed instead of reasoning
// about an undefined state.
func phaseOf(status TaskStatus, handoff bool) (taskPhase, bool) {
	switch status {
	case StatusPending:
		if handoff {
			return phaseHandingOffPending, true
		}
		return phasePending, true
	case StatusRunning:
		if handoff {
			return phaseHandingOffRunning, true
		}
		return phaseRunning, true
	case StatusHandedOff:
		if handoff {
			return phaseHandedOff, false
		}
		return phaseHandedOff, true
	case StatusDone:
		if handoff {
			return phaseDone, false
		}
		return phaseDone, true
	case StatusFailed:
		if handoff {
			return phaseFailed, false
		}
		return phaseFailed, true
	default:
		return phasePending, false
	}
}

// taskEvent names the operation attempting a task transition. The SetStatus
// targets are separate events so the table below fixes legality per
// (phase x event) pair with no extra dimension.
type taskEvent int

const (
	evSetPending taskEvent = iota
	evSetRunning
	evSetDone
	evSetFailed
	evSetHandedOff
	evBegin
	evAbort
	evReserve
	evCommit
	evFinishDone
	evFinishFailed
	evReassign
)

// String reports the event for debug/test output only.
func (e taskEvent) String() string {
	switch e {
	case evSetPending:
		return "setPending"
	case evSetRunning:
		return "setRunning"
	case evSetDone:
		return "setDone"
	case evSetFailed:
		return "setFailed"
	case evSetHandedOff:
		return "setHandedOff"
	case evBegin:
		return "begin"
	case evAbort:
		return "abort"
	case evReserve:
		return "reserve"
	case evCommit:
		return "commit"
	case evFinishDone:
		return "finishDone"
	case evFinishFailed:
		return "finishFailed"
	case evReassign:
		return "reassign"
	default:
		return "unknown"
	}
}

// allTaskPhases lists every internal phase for the exhaustive table test.
var allTaskPhases = []taskPhase{
	phasePending,
	phaseRunning,
	phaseHandingOffPending,
	phaseHandingOffRunning,
	phaseHandedOff,
	phaseDone,
	phaseFailed,
}

// allTaskEvents lists every event for the exhaustive table test.
var allTaskEvents = []taskEvent{
	evSetPending,
	evSetRunning,
	evSetDone,
	evSetFailed,
	evSetHandedOff,
	evBegin,
	evAbort,
	evReserve,
	evCommit,
	evFinishDone,
	evFinishFailed,
	evReassign,
}

// taskLegal is the single table of legal (phase x event) transitions. The
// value is the target phase; absence means the pair is refused (the task is
// left untouched and the caller reports its legacy error). evReserve and the
// idempotent self-sets map a phase to itself.
var taskLegal = map[taskPhase]map[taskEvent]taskPhase{
	phasePending: {
		evSetPending:   phasePending,
		evSetRunning:   phaseRunning,
		evSetDone:      phaseDone,
		evSetFailed:    phaseFailed,
		evSetHandedOff: phaseHandedOff,
		evBegin:        phaseHandingOffPending,
		evAbort:        phasePending,
		evFinishDone:   phaseDone,
		evFinishFailed: phaseFailed,
		evReassign:     phasePending,
	},
	phaseRunning: {
		evSetPending:   phasePending,
		evSetRunning:   phaseRunning,
		evSetDone:      phaseDone,
		evSetFailed:    phaseFailed,
		evSetHandedOff: phaseHandedOff,
		evBegin:        phaseHandingOffRunning,
		evAbort:        phaseRunning,
		evFinishDone:   phaseDone,
		evFinishFailed: phaseFailed,
		evReassign:     phasePending,
	},
	phaseHandingOffPending: {
		evAbort:   phasePending,
		evReserve: phaseHandingOffPending,
		evCommit:  phaseHandedOff,
	},
	phaseHandingOffRunning: {
		evAbort:   phaseRunning,
		evReserve: phaseHandingOffRunning,
		evCommit:  phaseHandedOff,
	},
	phaseHandedOff: {
		evAbort:        phaseHandedOff,
		evSetHandedOff: phaseHandedOff,
	},
	phaseDone: {
		evAbort:   phaseDone,
		evSetDone: phaseDone,
	},
	phaseFailed: {
		evAbort:     phaseFailed,
		evSetFailed: phaseFailed,
	},
}

// taskNext reports the target phase for a (phase x event) pair, or false when
// the pair is illegal. It is the only reader of taskLegal for transitions.
func taskNext(cur taskPhase, ev taskEvent) (taskPhase, bool) {
	next, ok := taskLegal[cur][ev]
	return next, ok
}

// setStatusEvent maps a SetStatus target to its table event.
func setStatusEvent(status TaskStatus) (taskEvent, bool) {
	switch status {
	case StatusPending:
		return evSetPending, true
	case StatusRunning:
		return evSetRunning, true
	case StatusDone:
		return evSetDone, true
	case StatusFailed:
		return evSetFailed, true
	case StatusHandedOff:
		return evSetHandedOff, true
	default:
		return evSetPending, false
	}
}
