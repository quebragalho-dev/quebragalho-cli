package tui

// StatusPhase names the external statusline command lifecycle.
//
//	Text diagram:
//	  Off -> Idle : reconfig (command activated)
//	  Idle -> Flying : start/apply-consume/tick-due (InFlight=1 Seq++ Cancel/Done new)
//	  Flying -> FlyingRefreshDue : start force during flight (RefreshDue=1)
//	  Flying/FlyingRefreshDue -> Idle : apply seq==Seq (stale seq discarded)
//	  FlyingRefreshDue -> Flying : apply consumes RefreshDue (next run starts)
//	  Any -> Idle/Off : cancel/reconfig (kill tree, drop RefreshDue, bump Seq/Gen)
//	Stale completions (seq mismatch) and old ticks (gen mismatch) are refused
//	keeping state, never panicking in production.
//
// The live fields are the twins: InFlight pairs with Cancel/Done, Seq matches
// runs to results, RefreshDue queues one turn-end refresh, StartedAt gates the
// throttle window, TickGen versions the idle chain. statusTransition is the
// single writer that fixes those twins together.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type StatusPhase int

const (
	StatusOff StatusPhase = iota
	StatusIdle
	StatusFlying
	StatusFlyingRefreshDue
)

func (p StatusPhase) String() string {
	switch p {
	case StatusOff:
		return "off"
	case StatusIdle:
		return "idle"
	case StatusFlying:
		return "flying"
	case StatusFlyingRefreshDue:
		return "flyingRefreshDue"
	default:
		return "unknown"
	}
}

type StatusEvent int

const (
	StatusEvStart StatusEvent = iota
	StatusEvApply
	StatusEvCancel
	StatusEvTick
	StatusEvReconfig
)

func (e StatusEvent) String() string {
	switch e {
	case StatusEvStart:
		return "start"
	case StatusEvApply:
		return "apply"
	case StatusEvCancel:
		return "cancel"
	case StatusEvTick:
		return "tick"
	case StatusEvReconfig:
		return "reconfig"
	default:
		return "unknown"
	}
}

// statusLegal is the single table of legal (phase x event) statusline
// transitions. Start in Off is illegal (callers guard on active); stale
// seq/gen are refused by the transition even when the cell is legal.
var statusLegal = map[StatusPhase]map[StatusEvent]bool{
	StatusOff: {
		StatusEvApply:    true,
		StatusEvCancel:   true,
		StatusEvTick:     true,
		StatusEvReconfig: true,
	},
	StatusIdle: {
		StatusEvStart:    true,
		StatusEvApply:    true,
		StatusEvCancel:   true,
		StatusEvTick:     true,
		StatusEvReconfig: true,
	},
	StatusFlying: {
		StatusEvStart:    true,
		StatusEvApply:    true,
		StatusEvCancel:   true,
		StatusEvTick:     true,
		StatusEvReconfig: true,
	},
	StatusFlyingRefreshDue: {
		StatusEvStart:    true,
		StatusEvApply:    true,
		StatusEvCancel:   true,
		StatusEvTick:     true,
		StatusEvReconfig: true,
	},
}

func statusEventLegal(phase StatusPhase, ev StatusEvent) bool {
	return statusLegal[phase][ev]
}

// statusPhase derives the current command phase from the live fields.
func (m model) statusPhase() StatusPhase {
	if !m.statuslineCommandActive() {
		return StatusOff
	}
	if m.statuslineCmdInFlight {
		if m.statuslineRefreshDue {
			return StatusFlyingRefreshDue
		}
		return StatusFlying
	}
	return StatusIdle
}

// statusArgs carries the optional payload for a statusline transition.
type statusArgs struct {
	force   bool
	seq     int
	lines   []string
	errNote string
	gen     int
}

// statusTransition is the single point that writes the command lifecycle
// fields (InFlight/Cancel/Done/Seq/StartedAt/RefreshDue/TickGen). Display
// cache (Output/Err) and config stay with the callers. Illegal transitions
// and stale seq/gen are refused with an error while keeping state.
func (m *model) statusTransition(ev StatusEvent, a statusArgs) (tea.Cmd, error) {
	phase := m.statusPhase()
	if !statusEventLegal(phase, ev) {
		return nil, fmt.Errorf("illegal statusline transition %s in %s", ev, phase)
	}
	switch ev {
	case StatusEvStart:
		return m.statusStart(a.force), nil
	case StatusEvTick:
		if a.gen != m.statuslineTickGen {
			return nil, fmt.Errorf("stale statusline tick gen %d in %d", a.gen, m.statuslineTickGen)
		}
		if !m.statuslineCommandActive() {
			return nil, nil
		}
		return m.statusStart(false), nil
	case StatusEvApply:
		if a.seq != m.statuslineCmdSeq {
			return nil, fmt.Errorf("stale statusline seq %d in %d", a.seq, m.statuslineCmdSeq)
		}
		m.statuslineCmdInFlight = false
		m.statuslineCmdCancel = nil
		if a.errNote != "" {
			m.statuslineCmdErr = a.errNote
		} else {
			m.statuslineCmdOutput = strings.Join(a.lines, "\n")
			m.statuslineCmdErr = ""
		}
		if m.statuslineRefreshDue && m.statuslineCommandActive() {
			m.statuslineRefreshDue = false
			return m.statusStart(true), nil
		}
		return nil, nil
	case StatusEvCancel:
		if m.statuslineCmdCancel != nil {
			m.statuslineCmdCancel()
			m.statuslineCmdCancel = nil
		}
		m.statuslineCmdInFlight = false
		m.statuslineRefreshDue = false
		return nil, nil
	case StatusEvReconfig:
		if m.statuslineCmdCancel != nil {
			m.statuslineCmdCancel()
			m.statuslineCmdCancel = nil
		}
		m.statuslineCmdInFlight = false
		m.statuslineRefreshDue = false
		m.statuslineCmdStartedAt = time.Time{}
		m.statuslineCmdSeq++
		m.statuslineTickGen++
		return nil, nil
	}
	return nil, fmt.Errorf("unknown statusline event %s", ev)
}

// statusStart arms one run when due (forced or past the refresh window).
// At most one run flies: a forced trigger during a flight only sets
// RefreshDue. Returns the runner command or nil. Caller decides the phase
// legality; Off must never reach here.
func (m *model) statusStart(force bool) tea.Cmd {
	if m.statuslineCmdInFlight {
		if force {
			m.statuslineRefreshDue = true
		}
		return nil
	}
	refresh := time.Duration(m.statuslineCfg.EffectiveRefreshMs()) * time.Millisecond
	if !force && !m.statuslineCmdStartedAt.IsZero() && m.now().Sub(m.statuslineCmdStartedAt) < refresh {
		return nil
	}
	m.statuslineCmdInFlight = true
	m.statuslineCmdStartedAt = m.now()
	m.statuslineCmdSeq++
	seq := m.statuslineCmdSeq
	cfg := m.statuslineCfg
	stdin := string(m.statuslineStdin(m.width))
	env := statuslineChildEnv(m.width, m.height)
	timeout := time.Duration(cfg.EffectiveTimeoutMs()) * time.Millisecond
	runCtx, cancel := context.WithCancel(context.Background())
	m.statuslineCmdCancel = cancel
	done := make(chan struct{})
	m.statuslineCmdDone = done
	return func() tea.Msg {
		defer close(done)
		msg := execStatuslineRun(runCtx, cfg.Command.Command, stdin, env, timeout)
		msg.seq = seq
		return msg
	}
}
