package tui

import (
	"context"
	"testing"
	"time"
)

// Repro for M2 bugs: FAIL on code without the turn cleanup fix.
func TestReproCancelRunLeaksRetryWait(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.pending = true
	m.activeRunID = 5
	m.runID = 5
	m.runCancel = func() {}
	m.retryWait = &retryWaitState{attempt: 2, max: 10, summary: "upstream", deadline: time.Now().Add(time.Minute)}
	m.cancelRun()
	if m.retryWait != nil {
		t.Fatalf("cancelRun leaked retryWait: %#v", m.retryWait)
	}
}

func TestReproCancelRunLeaksPendingStreamClear(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.pending = true
	m.activeRunID = 5
	m.runID = 5
	m.runCancel = func() {}
	m.pendingStreamClear = true
	m.lastStreamClear = time.Now().Add(-time.Second)
	m.cancelRun()
	if m.pendingStreamClear {
		t.Fatalf("cancelRun leaked pendingStreamClear")
	}
	// Observable: a deferred clear must not fire ClearScreen while idle.
	m.pending = false
	m.activeRunID = 0
	updated, cmd := m.Update(streamClearFlushMsg{})
	_ = updated.(model)
	if cmd != nil {
		t.Fatalf("leaked pendingStreamClear fires ClearScreen while idle")
	}
}
