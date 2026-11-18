package swarm

import (
	"testing"
)

// wantTaskLegal fixa o conjunto legal de pares (fase x evento) -> fase alvo.
// É uma cópia independente da tabela de produção: qualquer par liberado ou
// proibido por engano em task_phase.go quebra este teste.
var wantTaskLegal = map[taskPhase]map[taskEvent]taskPhase{
	phasePending: {
		evSetPending:   phasePending,
		evSetRunning:   phaseRunning,
		evSetDone:      phaseDone,
		evSetFailed:    phaseFailed,
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

func TestTaskLegalCoversAllPhaseEventPairs(t *testing.T) {
	for _, phase := range allTaskPhases {
		for _, ev := range allTaskEvents {
			gotNext, gotOK := taskNext(phase, ev)
			wantNext, wantOK := wantTaskLegal[phase][ev]
			if gotOK != wantOK {
				t.Errorf("taskNext(%s, %s) legal=%v, want %v", phase, ev, gotOK, wantOK)
				continue
			}
			if gotOK && gotNext != wantNext {
				t.Errorf("taskNext(%s, %s) = %s, want %s", phase, ev, gotNext, wantNext)
			}
		}
	}
}

func TestTaskPhaseAbortReturnsToOrigin(t *testing.T) {
	if next, ok := taskNext(phaseHandingOffPending, evAbort); !ok || next != phasePending {
		t.Fatalf("abort de handing-off(pending) = %s,%v, want pending,true", next, ok)
	}
	if next, ok := taskNext(phaseHandingOffRunning, evAbort); !ok || next != phaseRunning {
		t.Fatalf("abort de handing-off(running) = %s,%v, want running,true", next, ok)
	}
}

func TestTaskPhaseTerminalHasNoExit(t *testing.T) {
	for _, terminal := range []taskPhase{phaseHandedOff, phaseDone, phaseFailed} {
		for _, ev := range []taskEvent{evBegin, evCommit, evFinishDone, evFinishFailed, evReassign, evReserve} {
			if _, ok := taskNext(terminal, ev); ok {
				t.Errorf("taskNext(%s, %s) deve ser ilegal (terminal sem saída)", terminal, ev)
			}
		}
	}
}

func TestTaskPhaseHandingOffProjectsToOrigin(t *testing.T) {
	if s := phaseHandingOffPending.status(); s != StatusPending {
		t.Fatalf("handing-off(pending) projeta %q, want pending", s)
	}
	if s := phaseHandingOffRunning.status(); s != StatusRunning {
		t.Fatalf("handing-off(running) projeta %q, want running", s)
	}
	if phaseHandingOffPending.claimed() != true || phaseHandingOffRunning.claimed() != true {
		t.Fatal("fases handing-off devem ter claimed=true")
	}
	if phasePending.claimed() || phaseRunning.claimed() {
		t.Fatal("pending/running devem ter claimed=false")
	}
	for _, terminal := range []taskPhase{phaseHandedOff, phaseDone, phaseFailed} {
		if !terminal.terminal() {
			t.Errorf("%s deve ser terminal", terminal)
		}
	}
	for _, live := range []taskPhase{phasePending, phaseRunning, phaseHandingOffPending, phaseHandingOffRunning} {
		if live.terminal() {
			t.Errorf("%s não deve ser terminal", live)
		}
	}
}

func TestTaskPhaseTwinsRoundTrip(t *testing.T) {
	for _, phase := range allTaskPhases {
		back, ok := phaseOf(phase.status(), phase.claimed())
		if !ok || back != phase {
			t.Errorf("phaseOf(%q, claimed=%v) = %s,%v, want %s,true", phase.status(), phase.claimed(), back, ok, phase)
		}
	}
	// Gêmeos que os escritores nunca produzem falham fechados.
	for _, status := range []TaskStatus{StatusDone, StatusFailed, StatusHandedOff} {
		if _, ok := phaseOf(status, true); ok {
			t.Errorf("phaseOf(%q, claimed=true) deve falhar (terminal + claim)", status)
		}
	}
	if _, ok := phaseOf(TaskStatus("bogus"), false); ok {
		t.Error("phaseOf(bogus) deve falhar")
	}
}

func TestTaskPhaseString(t *testing.T) {
	for _, phase := range allTaskPhases {
		if phase.String() == "" || phase.String() == "unknown" {
			t.Errorf("fase %d sem String()", int(phase))
		}
	}
	for _, ev := range allTaskEvents {
		if ev.String() == "" || ev.String() == "unknown" {
			t.Errorf("evento %d sem String()", int(ev))
		}
	}
	if taskPhase(99).String() != "unknown" || taskEvent(99).String() != "unknown" {
		t.Error("valores fora do enum devem dar unknown")
	}
}

func TestTransitionLockedWritesTwinsTogether(t *testing.T) {
	c := NewCoordinator()
	if _, err := c.Register("t", "a1", "team", "work"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := c.SetStatus("t", StatusRunning); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	task, _ := c.Get("t")
	c.mu.Lock()
	defer c.mu.Unlock()
	stored := c.tasks["t"]
	_ = task
	for _, phase := range allTaskPhases {
		for _, ev := range allTaskEvents {
			stored.Status, stored.handoff = phase.status(), phase.claimed()
			wantNext, wantOK := wantTaskLegal[phase][ev]
			gotNext, gotOK := c.transitionLocked(stored, ev)
			if gotOK != wantOK {
				t.Errorf("transitionLocked(%s, %s) ok=%v, want %v", phase, ev, gotOK, wantOK)
				continue
			}
			if !wantOK {
				// Recusa mantém o estado byte a byte.
				if stored.Status != phase.status() || stored.handoff != phase.claimed() {
					t.Errorf("recusa (%s, %s) mutou gêmeos para %q/%v", phase, ev, stored.Status, stored.handoff)
				}
				continue
			}
			if gotNext != wantNext {
				t.Errorf("transitionLocked(%s, %s) = %s, want %s", phase, ev, gotNext, wantNext)
			}
			// Gêmeos instalados juntos a partir da fase alvo.
			if stored.Status != wantNext.status() || stored.handoff != wantNext.claimed() {
				t.Errorf("gêmeos após (%s, %s) = %q/%v, want %q/%v",
					phase, ev, stored.Status, stored.handoff, wantNext.status(), wantNext.claimed())
			}
			// A projeção resultante sempre re-deriva a fase alvo.
			if back, ok := phaseOf(stored.Status, stored.handoff); !ok || back != wantNext {
				t.Errorf("phaseOf após (%s, %s) = %s,%v, want %s,true", phase, ev, back, ok, wantNext)
			}
		}
	}
}

func TestSetStatusEventMapping(t *testing.T) {
	for _, tc := range []struct {
		status TaskStatus
		ev     taskEvent
	}{
		{StatusPending, evSetPending},
		{StatusRunning, evSetRunning},
		{StatusDone, evSetDone},
		{StatusFailed, evSetFailed},
		{StatusHandedOff, evSetHandedOff},
	} {
		if ev, ok := setStatusEvent(tc.status); !ok || ev != tc.ev {
			t.Errorf("setStatusEvent(%q) = %s,%v, want %s,true", tc.status, ev, ok, tc.ev)
		}
	}
	if _, ok := setStatusEvent(TaskStatus("bogus")); ok {
		t.Error("setStatusEvent(bogus) deve falhar")
	}
}
