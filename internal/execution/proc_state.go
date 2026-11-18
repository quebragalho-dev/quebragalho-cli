package execution

// procState is the explicit lifecycle of an interactive process.
//
// Text diagram:
//
//	running --Wait returned--> reaped --markDone--> done
//	removed = leaving the map (no state; List and Len both report !done)
//
// running: Wait has not returned yet (reaped open, done open, exitCode nil).
// reaped:  Wait returned (reaped closed, done open, exitCode still nil).
// done:    result published (reaped closed, done closed, exitCode set).
// "removed" is not a state: it is a map delete (Remove/expiry).
type procState int

const (
	procRunning procState = iota
	procReaped
	procDone
)

func (s procState) String() string {
	switch s {
	case procRunning:
		return "running"
	case procReaped:
		return "reaped"
	case procDone:
		return "done"
	default:
		return "unknown"
	}
}

// procEvent are the only events that move the machine.
type procEvent int

const (
	// procEvWaitReturned: exec Wait() returned (Start goroutine, after the old close).
	procEvWaitReturned procEvent = iota
	// procEvPublished: result ready to publish (markDone).
	procEvPublished
)

func (e procEvent) String() string {
	switch e {
	case procEvWaitReturned:
		return "waitReturned"
	case procEvPublished:
		return "published"
	default:
		return "unknown"
	}
}

// procNext is the SINGLE table of legal transitions (state x event).
func procNext(cur procState, ev procEvent) (procState, bool) {
	switch cur {
	case procRunning:
		if ev == procEvWaitReturned {
			return procReaped, true
		}
	case procReaped:
		if ev == procEvPublished {
			return procDone, true
		}
	case procDone:
		// Terminal: nenhuma saida.
	}
	return cur, false
}

// procDonePayload carries the twins published together on reaped->done.
type procDonePayload struct {
	waitErr   error
	exitCode  int
	report    AdapterReport
	reportErr error
	changes   []Change
}

// transition is the SINGLE place that writes state (state + twins) and the only
// one that closes reaped/done. It never panics: an illegal transition or a nil
// payload is refused without writing anything and returns false.
func (process *managedProcess) transition(ev procEvent, payload *procDonePayload) bool {
	process.mu.Lock()
	defer process.mu.Unlock()
	next, ok := procNext(process.state, ev)
	if !ok {
		return false
	}
	switch ev {
	case procEvWaitReturned:
		process.state = next
		if process.reaped != nil {
			select {
			case <-process.reaped:
				// Already closed (legacy manual literal); do not close again.
			default:
				close(process.reaped)
			}
		}
		return true
	case procEvPublished:
		if payload == nil {
			return false
		}
		process.waitErr = payload.waitErr
		value := payload.exitCode
		process.exitCode = &value
		process.resultReport = payload.report
		process.reportErr = payload.reportErr
		process.changes = append([]Change(nil), payload.changes...)
		process.state = next
		if process.done != nil {
			process.doneOnce.Do(func() { close(process.done) })
		}
		return true
	default:
		return false
	}
}
