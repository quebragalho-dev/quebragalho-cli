package daemon

// Session lifecycle as an explicit state machine.
//
//	stateDiagram-v2
//	[*] --> queued: newSession
//	queued --> running: Started (lease acquired)
//	queued --> done: finish(nil) (failure before the lease)
//	queued --> failed: finish(err) (failure before the lease)
//	running --> done: finish(nil)
//	running --> failed: finish(err)
//	running --> running: Started (idempotent, still running)
//	done --> done: finish (idempotent, never re-closes)
//	failed --> failed: finish (idempotent, never re-closes)
//	done --> running: refused
//	failed --> running: refused
//
//	transition is the single writer of state, finished, exitCode, err and the
//	done close: the twins are fixed together and the channels close at most
//	once, so timeouts, Drain-kill and retries can race the normal path.
type sessEvent int

const (
	sessEventStart sessEvent = iota
	sessEventFinishDone
	sessEventFinishFailed
)

// String renders the session event for tables and errors.
func (e sessEvent) String() string {
	switch e {
	case sessEventStart:
		return "start"
	case sessEventFinishDone:
		return "finishDone"
	case sessEventFinishFailed:
		return "finishFailed"
	default:
		return "unknown"
	}
}

// String renders the session state with its stable daemon name.
func (s SessionState) String() string {
	return string(s)
}

// legalSessTransition is the single table of legal session transitions.
// It returns the projected state; ok=false refuses without mutating.
func legalSessTransition(from SessionState, event sessEvent) (to SessionState, ok bool) {
	switch from {
	case SessionQueued:
		switch event {
		case sessEventStart:
			return SessionRunning, true
		case sessEventFinishDone:
			return SessionDone, true
		case sessEventFinishFailed:
			return SessionFailed, true
		}
		return from, false
	case SessionRunning:
		switch event {
		case sessEventStart:
			return SessionRunning, true
		case sessEventFinishDone:
			return SessionDone, true
		case sessEventFinishFailed:
			return SessionFailed, true
		}
		return from, false
	case SessionDone:
		switch event {
		case sessEventFinishDone, sessEventFinishFailed:
			return SessionDone, true
		}
		return from, false
	case SessionFailed:
		switch event {
		case sessEventFinishDone, sessEventFinishFailed:
			return SessionFailed, true
		}
		return from, false
	}
	return from, false
}
