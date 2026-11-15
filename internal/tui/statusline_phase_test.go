package tui

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

func statusActiveModel(inFlight, refreshDue bool) model {
	m := limeTestModel()
	m.width, m.height = 100, 30
	m.statuslineCfg = config.StatusLineConfig{
		Lines:   []config.StatusLineLine{{Left: []string{"mode"}}},
		Command: &config.StatusLineCommand{Command: "echo hi"},
	}
	m.statuslineCmdInFlight = inFlight
	m.statuslineRefreshDue = refreshDue
	m.statuslineCmdSeq = 3
	if inFlight {
		m.statuslineCmdCancel = func() {}
		m.statuslineCmdDone = make(chan struct{})
	}
	return m
}

func statusOffModel() model {
	m := limeTestModel()
	m.statuslineCfg = config.StatusLineConfig{}
	return m
}

// Exhaustive phase x event table for the statusline command lifecycle.
func TestStatusPhaseTable(t *testing.T) {
	want := map[StatusPhase]map[StatusEvent]bool{
		StatusOff: {
			StatusEvApply: true, StatusEvCancel: true, StatusEvTick: true, StatusEvReconfig: true,
		},
		StatusIdle: {
			StatusEvStart: true, StatusEvApply: true, StatusEvCancel: true, StatusEvTick: true, StatusEvReconfig: true,
		},
		StatusFlying: {
			StatusEvStart: true, StatusEvApply: true, StatusEvCancel: true, StatusEvTick: true, StatusEvReconfig: true,
		},
		StatusFlyingRefreshDue: {
			StatusEvStart: true, StatusEvApply: true, StatusEvCancel: true, StatusEvTick: true, StatusEvReconfig: true,
		},
	}
	// Only Start-in-Off is illegal.
	mkModel := map[StatusPhase]func() model{
		StatusOff: func() model { return statusOffModel() },
		StatusIdle: func() model {
			m := statusActiveModel(false, false)
			m.statuslineTickGen = 7
			return m
		},
		StatusFlying: func() model {
			m := statusActiveModel(true, false)
			m.statuslineTickGen = 7
			return m
		},
		StatusFlyingRefreshDue: func() model {
			m := statusActiveModel(true, true)
			m.statuslineTickGen = 7
			return m
		},
	}
	for phase, mk := range mkModel {
		for ev := StatusEvStart; ev <= StatusEvReconfig; ev++ {
			legal := want[phase][ev]
			m := mk()
			if got := m.statusPhase(); got != phase {
				t.Fatalf("fixture phase = %v, want %v", got, phase)
			}
			if statusEventLegal(phase, ev) != legal {
				t.Errorf("statusLegal[%v][%v] = %v, want %v", phase, ev, statusEventLegal(phase, ev), legal)
			}
			args := statusArgs{force: true, seq: m.statuslineCmdSeq, gen: m.statuslineTickGen}
			if ev == StatusEvStart {
				args = statusArgs{force: false}
			}
			if ev == StatusEvTick {
				args = statusArgs{gen: m.statuslineTickGen}
			}
			_, err := m.statusTransition(ev, args)
			if legal != (err == nil) {
				// Start-in-Off is the single illegal cell; stale seq/gen are
				// covered by the guard tests below, not here.
				t.Errorf("phase %v event %v: legal=%v err=%v", phase, ev, legal, err)
			}
		}
	}
	// The single illegal cell keeps state.
	off := statusOffModel()
	if _, err := off.statusTransition(StatusEvStart, statusArgs{}); err == nil {
		t.Fatal("Start in Off must be refused")
	}
	if off.statuslineCmdInFlight {
		t.Fatal("illegal Start in Off must not arm a flight")
	}
}

// Stale seq/gen are refused keeping state even in legal cells.
func TestStatusPhaseStaleGuards(t *testing.T) {
	m := statusActiveModel(true, false)
	m.statuslineTickGen = 7
	if _, err := m.statusTransition(StatusEvApply, statusArgs{seq: 2}); err == nil {
		t.Fatal("stale seq must be refused")
	}
	if !m.statuslineCmdInFlight {
		t.Fatal("stale apply must keep the flight")
	}
	if _, err := m.statusTransition(StatusEvTick, statusArgs{gen: 6}); err == nil {
		t.Fatal("old tick gen must be refused")
	}
	if m.statuslineCmdSeq != 3 {
		t.Fatalf("stale tick changed seq: %d", m.statuslineCmdSeq)
	}
}

// RefreshDue is set by construction on forced start during flight and
// consumed by the matching apply starting the next run.
func TestStatusPhaseRefreshDueCycle(t *testing.T) {
	m := statusActiveModel(false, false)
	cmd, err := m.statusTransition(StatusEvStart, statusArgs{})
	if err != nil || cmd == nil {
		t.Fatalf("Idle Start must fly: err=%v cmd=%v", err, cmd != nil)
	}
	if m.statusPhase() != StatusFlying {
		t.Fatalf("after start: %v", m.statusPhase())
	}
	if _, err := m.statusTransition(StatusEvStart, statusArgs{force: true}); err != nil {
		t.Fatal(err)
	}
	if m.statusPhase() != StatusFlyingRefreshDue {
		t.Fatalf("forced start during flight: %v", m.statusPhase())
	}
	seq := m.statuslineCmdSeq
	cmd2, err := m.statusTransition(StatusEvApply, statusArgs{seq: seq, lines: []string{"one"}})
	if err != nil {
		t.Fatal(err)
	}
	if cmd2 == nil {
		t.Fatal("apply must consume RefreshDue with a new run")
	}
	if m.statusPhase() != StatusFlying || m.statuslineRefreshDue {
		t.Fatalf("after consume: phase=%v refreshDue=%v", m.statusPhase(), m.statuslineRefreshDue)
	}
}
