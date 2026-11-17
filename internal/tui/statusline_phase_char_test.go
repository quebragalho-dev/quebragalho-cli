package tui

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// Characterization of the external statusline command lifecycle (sm3 M2).
// Pure refactoring: these pin the current field behavior and must pass
// unchanged before and after the explicit phase machine lands.
func charStatuslineModel() model {
	m := limeTestModel()
	m.width, m.height = 100, 30
	m.statuslineCfg = config.StatusLineConfig{
		Lines:   []config.StatusLineLine{{Left: []string{"mode"}}},
		Command: &config.StatusLineCommand{Command: "echo hi"},
	}
	return m
}

func TestCharStatuslineStartArmsSingleFlight(t *testing.T) {
	m := charStatuslineModel()
	seq := m.statuslineCmdSeq
	m, cmd := m.startStatuslineRun(false)
	if cmd == nil {
		t.Fatal("first start must produce a run")
	}
	if !m.statuslineCmdInFlight || m.statuslineCmdCancel == nil || m.statuslineCmdDone == nil {
		t.Fatalf("start must arm InFlight/Cancel/Done: %+v", m.statuslineCmdSeq)
	}
	if m.statuslineCmdSeq != seq+1 {
		t.Fatalf("start must bump seq: was %d now %d", seq, m.statuslineCmdSeq)
	}
	if m.statuslineCmdStartedAt.IsZero() {
		t.Fatal("start must record StartedAt")
	}
	before := m.statuslineCmdSeq
	m2, cmd2 := m.startStatuslineRun(false)
	if cmd2 != nil {
		t.Fatal("second non-forced start must not overlap")
	}
	if m2.statuslineCmdSeq != before || m2.statuslineRefreshDue {
		t.Fatalf("second start must change nothing: seq %d refreshDue %v", m2.statuslineCmdSeq, m2.statuslineRefreshDue)
	}
	m3, cmd3 := m.startStatuslineRun(true)
	if cmd3 != nil {
		t.Fatal("forced start during flight must not overlap either")
	}
	if !m3.statuslineRefreshDue {
		t.Fatal("forced start during flight must set RefreshDue")
	}
	if m3.statuslineCmdSeq != before {
		t.Fatalf("forced start during flight must not bump seq: %d", m3.statuslineCmdSeq)
	}
}

func TestCharStatuslineApplyConsumesRefreshDue(t *testing.T) {
	m := charStatuslineModel()
	m, _ = m.startStatuslineRun(false)
	m, _ = m.startStatuslineRun(true) // force during flight -> RefreshDue
	if !m.statuslineRefreshDue {
		t.Fatal("precondition: RefreshDue set")
	}
	seq := m.statuslineCmdSeq
	m2, cmd := m.applyStatuslineCmdMsg(statuslineCmdMsg{seq: seq, lines: []string{"one"}})
	if cmd == nil {
		t.Fatal("apply must consume a queued refresh with a new run")
	}
	if m2.statuslineRefreshDue {
		t.Fatal("apply must clear RefreshDue when consuming it")
	}
	if m2.statuslineCmdSeq != seq+1 || !m2.statuslineCmdInFlight {
		t.Fatalf("consumed refresh must start the next run: seq=%d inFlight=%v", m2.statuslineCmdSeq, m2.statuslineCmdInFlight)
	}
}

func TestCharStatuslineStaleSeqDiscarded(t *testing.T) {
	m := charStatuslineModel()
	m, _ = m.startStatuslineRun(false)
	m, _ = m.applyStatuslineCmdMsg(statuslineCmdMsg{seq: m.statuslineCmdSeq, lines: []string{"fresh"}})
	kept := m.statuslineCmdOutput
	stale := statuslineCmdMsg{seq: m.statuslineCmdSeq - 1, lines: []string{"stale"}}
	m2, cmd := m.applyStatuslineCmdMsg(stale)
	if cmd != nil {
		t.Fatal("stale apply must produce no command")
	}
	if m2.statuslineCmdOutput != kept {
		t.Fatalf("stale seq overwrote output: %q", m2.statuslineCmdOutput)
	}
	if m2.statuslineCmdInFlight {
		t.Fatal("stale apply must not arm a flight")
	}
}

func TestCharStatuslineCancelClearsFlight(t *testing.T) {
	m := charStatuslineModel()
	m, _ = m.startStatuslineRun(false)
	m, _ = m.startStatuslineRun(true)
	m2 := m.cancelStatuslineRun()
	if m2.statuslineCmdInFlight || m2.statuslineRefreshDue || m2.statuslineCmdCancel != nil {
		t.Fatalf("cancel must clear InFlight/RefreshDue/Cancel: %+v", m2.statuslineCmdSeq)
	}
	// Cancel is idempotent: a second cancel keeps idle.
	m3 := m2.cancelStatuslineRun()
	if m3.statuslineCmdInFlight || m3.statuslineRefreshDue {
		t.Fatal("second cancel must stay idle")
	}
}

func TestCharStatuslineTickGenGuard(t *testing.T) {
	m := charStatuslineModel()
	gen := m.statuslineTickGen
	// A tick from a superseded generation is discarded: no run, no re-arm.
	after, cmd := m.Update(statuslineTickMsg{gen: gen - 1})
	got := after.(model)
	if got.statuslineCmdInFlight || cmd != nil {
		t.Fatalf("old-gen tick must be discarded: inFlight=%v cmd=%v", got.statuslineCmdInFlight, cmd != nil)
	}
	// Current generation starts a run when due and re-arms the chain.
	after2, cmd2 := m.Update(statuslineTickMsg{gen: gen})
	got2 := after2.(model)
	if !got2.statuslineCmdInFlight || cmd2 == nil {
		t.Fatalf("current-gen tick must start a run: inFlight=%v cmd=%v", got2.statuslineCmdInFlight, cmd2 != nil)
	}
}
