package mcp

// MCP connection liveness as an explicit state machine.
//
//	stateDiagram-v2
//	[*] --> alive: newMCPServerConn (fresh transport)
//	alive --> dead: markDead (post-write transport failure, same generation)
//	dead --> alive: swap (fresh transport heals, clears the latch)
//	alive --> alive: swap (relist/reconnect generation change)
//	dead --> dead: markDead (stale failure for another generation, ignored)
//	alive --> closed: Close (terminal)
//	dead --> closed: Close (terminal)
//	closed --> closed: refused (never dials, never reuses)
//
//	A swap never closes a generation with calls in flight: the old client is
//	retired and closed when its last user releases it. Close terminates
//	immediately and closes every generation at once, so no server process or
//	fd survives it. A stuck call on an old generation is still bounded by the
//	package's own tool timeout (withToolTimeout), so retaining never waits
//	unboundedly and never leaks forever.
type connState int

const (
	connAlive connState = iota
	connDead
	connClosed
)

// String renders the connection liveness with its stable name.
func (s connState) String() string {
	switch s {
	case connAlive:
		return "alive"
	case connDead:
		return "dead"
	case connClosed:
		return "closed"
	default:
		return "unknown"
	}
}

type connEvent int

const (
	connEventAcquire connEvent = iota
	connEventRelease
	connEventMarkDead
	connEventSwap
	connEventClose
)

// String renders the connection event for tables and errors.
func (e connEvent) String() string {
	switch e {
	case connEventAcquire:
		return "acquire"
	case connEventRelease:
		return "release"
	case connEventMarkDead:
		return "markDead"
	case connEventSwap:
		return "swap"
	case connEventClose:
		return "close"
	default:
		return "unknown"
	}
}

// legalConnTransition is the single table of legal liveness transitions.
// It returns the projected state after the event; ok=false refuses and the
// caller must neither mutate nor leak (a refused swap closes the fresh
// client it already dialed).
func legalConnTransition(from connState, event connEvent) (to connState, ok bool) {
	switch from {
	case connAlive:
		switch event {
		case connEventAcquire, connEventRelease:
			return connAlive, true
		case connEventMarkDead:
			return connDead, true
		case connEventSwap:
			return connAlive, true
		case connEventClose:
			return connClosed, true
		}
		return from, false
	case connDead:
		switch event {
		case connEventAcquire, connEventRelease:
			return connDead, true
		case connEventMarkDead:
			return connDead, true
		case connEventSwap:
			return connAlive, true
		case connEventClose:
			return connClosed, true
		}
		return from, false
	case connClosed:
		// Releases still flow through so a call racing Close can drop its
		// reference without a second close; everything else is refused.
		if event == connEventRelease {
			return connClosed, true
		}
		return from, false
	}
	return from, false
}
