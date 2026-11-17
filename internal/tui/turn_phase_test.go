package tui

import (
	"context"
	"testing"
)

// Table test: every (phase x event) fixes which turn transitions are legal.
func TestTurnPhaseTableAllCombinations(t *testing.T) {
	allPhases := []TurnPhase{TurnIdle, TurnRunning, TurnRetryWait, TurnDraining, TurnExiting}
	allEvents := []TurnEvent{TurnEvBegin, TurnEvRetryWait, TurnEvRetryClear, TurnEvText, TurnEvCancel, TurnEvResponse, TurnEvLateResponse, TurnEvSendNow, TurnEvQuit}
	expected := map[TurnPhase]map[TurnEvent]bool{
		TurnIdle:      {TurnEvBegin: true, TurnEvQuit: true},
		TurnRunning:   {TurnEvRetryWait: true, TurnEvRetryClear: true, TurnEvText: true, TurnEvCancel: true, TurnEvResponse: true, TurnEvSendNow: true, TurnEvQuit: true},
		TurnRetryWait: {TurnEvRetryClear: true, TurnEvText: true, TurnEvCancel: true, TurnEvResponse: true, TurnEvSendNow: true, TurnEvQuit: true},
		TurnDraining:  {TurnEvBegin: true, TurnEvLateResponse: true, TurnEvSendNow: true, TurnEvCancel: true, TurnEvQuit: true},
		TurnExiting:   {TurnEvLateResponse: true, TurnEvQuit: true},
	}
	for _, phase := range allPhases {
		for _, ev := range allEvents {
			want := expected[phase][ev]
			if got := turnEventLegal(phase, ev); got != want {
				t.Errorf("turn phase %s event %s: legal=%v want %v", phase, ev, got, want)
			}
		}
	}
	// String() must name every phase/event (padrão item 1).
	for _, p := range allPhases {
		if p.String() == "unknown" {
			t.Errorf("phase %d has no String", int(p))
		}
	}
	for _, e := range allEvents {
		if e.String() == "unknown" {
			t.Errorf("event %d has no String", int(e))
		}
	}
}

// Invariant walk: begin -> retryWait -> text -> cancel -> lateResponse ->
// sendNow -> quit. After each step the fields must map to a valid phase and
// never to an impossible mix (retryWait without run, clear without run,
// pending/activeRunID out of sync).
func TestTurnInvariantsAcrossEventSequence(t *testing.T) {
	m := newModel(context.Background(), Options{})
	if got := m.turnPhase(); got != TurnIdle {
		t.Fatalf("initial phase=%s want idle", got)
	}
	if !m.validTurnState() {
		t.Fatal("initial state must be valid")
	}

	// begin
	_, cancel := context.WithCancel(context.Background())
	m = m.beginRun(cancel)
	if got := m.turnPhase(); got != TurnRunning {
		t.Fatalf("after begin phase=%s want running", got)
	}
	if !m.validTurnState() {
		t.Fatal("after begin state must be valid")
	}
	if err := m.checkTurnEvent(TurnEvRetryWait); err != nil {
		t.Fatalf("retryWait should be legal in running: %v", err)
	}

	// retryWait
	m.retryWait = &retryWaitState{attempt: 1, max: 5, summary: "x"}
	if got := m.turnPhase(); got != TurnRetryWait {
		t.Fatalf("after retryWait phase=%s want retryWait", got)
	}
	if !m.validTurnState() {
		t.Fatal("retryWait with active run must be valid")
	}

	// texto: streaming text does not clear the retry wait.
	if err := m.checkTurnEvent(TurnEvText); err != nil {
		t.Fatalf("text should be legal in retryWait: %v", err)
	}
	if got := m.turnPhase(); got != TurnRetryWait {
		t.Fatalf("text must keep retryWait phase, got %s", got)
	}
	if !m.validTurnState() {
		t.Fatal("after text state must stay valid")
	}

	// cancel: must zero both transient twins and enter draining.
	m.pendingStreamClear = true
	cancelledID := m.activeRunID
	m.cancelRun()
	if m.retryWait != nil {
		t.Fatal("cancel must clear retryWait")
	}
	if m.pendingStreamClear {
		t.Fatal("cancel must clear pendingStreamClear")
	}
	if got := m.turnPhase(); got != TurnDraining {
		t.Fatalf("after cancel phase=%s want draining (flush %v)", got, m.flushRunIDs)
	}
	if !m.validTurnState() {
		t.Fatal("after cancel state must be valid")
	}
	if _, ok := m.flushRunIDs[cancelledID]; !ok {
		t.Fatalf("cancel must record flushRunIDs[%d]", cancelledID)
	}

	// resposta tardia: draining the last flush entry returns to idle.
	delete(m.flushRunIDs, cancelledID)
	if got := m.turnPhase(); got != TurnIdle {
		t.Fatalf("after lateResponse drain phase=%s want idle", got)
	}
	if !m.validTurnState() {
		t.Fatal("after drain state must be valid")
	}
	if err := m.checkTurnEvent(TurnEvSendNow); err == nil {
		t.Fatal("sendNow must be illegal in idle")
	}

	// send-now from draining is legal (parked batch launches after drain).
	m.flushRunIDs[cancelledID] = "sess"
	if err := m.checkTurnEvent(TurnEvSendNow); err != nil {
		t.Fatalf("sendNow should be legal in draining: %v", err)
	}
	delete(m.flushRunIDs, cancelledID)

	// quit
	m.exiting = true
	if got := m.turnPhase(); got != TurnExiting {
		t.Fatalf("after quit phase=%s want exiting", got)
	}

	// Impossible mixes are rejected by validTurnState.
	bad := newModel(context.Background(), Options{})
	bad.retryWait = &retryWaitState{}
	if bad.validTurnState() {
		t.Fatal("retryWait without run must be invalid")
	}
	bad2 := newModel(context.Background(), Options{})
	bad2.pendingStreamClear = true
	if bad2.validTurnState() {
		t.Fatal("pendingStreamClear without run must be invalid")
	}
}

// The three settle paths zero exactly the same transient set.
func TestTurnSettlePathsZeroSameSet(t *testing.T) {
	// begin clears inherited leaks.
	m := newModel(context.Background(), Options{})
	m.retryWait = &retryWaitState{}
	m.pendingStreamClear = true
	_, cancel := context.WithCancel(context.Background())
	m = m.beginRun(cancel)
	if m.retryWait != nil || m.pendingStreamClear {
		t.Fatal("beginRun must clear retryWait and pendingStreamClear")
	}
	cancel()

	// cancel clears both (covered by repro, asserted again for the set).
	m.retryWait = &retryWaitState{}
	m.pendingStreamClear = true
	m.pending = true
	m.activeRunID = m.runID
	m.runCancel = func() {}
	m.cancelRun()
	if m.retryWait != nil || m.pendingStreamClear {
		t.Fatal("cancelRun must clear retryWait and pendingStreamClear")
	}
}
