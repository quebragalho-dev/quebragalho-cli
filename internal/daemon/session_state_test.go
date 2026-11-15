package daemon

import (
	"context"
	"errors"
	"testing"
)

func TestLegalSessTransitionTable(t *testing.T) {
	states := []SessionState{SessionQueued, SessionRunning, SessionDone, SessionFailed}
	events := []sessEvent{sessEventStart, sessEventFinishDone, sessEventFinishFailed}
	want := map[SessionState]map[sessEvent]SessionState{
		SessionQueued: {
			sessEventStart: SessionRunning, sessEventFinishDone: SessionDone, sessEventFinishFailed: SessionFailed,
		},
		SessionRunning: {
			sessEventStart: SessionRunning, sessEventFinishDone: SessionDone, sessEventFinishFailed: SessionFailed,
		},
		SessionDone: {
			sessEventFinishDone: SessionDone, sessEventFinishFailed: SessionDone,
		},
		SessionFailed: {
			sessEventFinishDone: SessionFailed, sessEventFinishFailed: SessionFailed,
		},
	}
	for _, from := range states {
		for _, event := range events {
			got, ok := legalSessTransition(from, event)
			wantTo, wantOK := want[from][event]
			if ok != wantOK {
				t.Fatalf("legalSessTransition(%s, %s) ok = %v, want %v", from, event, ok, wantOK)
			}
			if ok && got != wantTo {
				t.Fatalf("legalSessTransition(%s, %s) = %s, want %s", from, event, got, wantTo)
			}
		}
	}
	// Terminal never restarts; queued/running cover every event.
	if _, ok := legalSessTransition(SessionDone, sessEventStart); ok {
		t.Fatal("done --start--> must refuse")
	}
	if _, ok := legalSessTransition(SessionFailed, sessEventStart); ok {
		t.Fatal("failed --start--> must refuse")
	}
	q := SessionQueued
	if q.String() != "queued" {
		t.Fatalf("SessionState String = %q, want queued", q.String())
	}
}

// Queued that never gets a lease finishes failed without ever running:
// a drained pool refuses the lease and the session records the refusal.
func TestSessionQueuedFailsBeforeLease(t *testing.T) {
	pool, _ := NewPool(PoolOptions{Size: 1, Launcher: func(context.Context, WorkerSpec) (WorkerHandle, error) {
		t.Fatal("launcher must not run after the drain")
		return nil, errors.New("unreachable")
	}})
	pool.Drain()
	mgr, _ := NewSessionManager(SessionManagerOptions{Pool: pool})
	sess, err := mgr.Start(context.Background(), WorkerSpec{Session: "q"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-sess.Done()
	if sess.State() != SessionFailed {
		t.Fatalf("state = %s, want failed for a lease refused after the drain", sess.State())
	}
	if !errors.Is(sess.Err(), ErrPoolDraining) {
		t.Fatalf("err = %v, want ErrPoolDraining", sess.Err())
	}
	buffered, live, cancel := sess.Subscribe()
	defer cancel()
	select {
	case _, ok := <-live:
		if ok {
			t.Fatal("live channel after terminal finish must be closed")
		}
	default:
		// Subscribe on a finished session returns an already-closed channel;
		// a non-blocking check may race the close, so drain to be sure.
		for range live {
		}
	}
	if len(buffered) != 0 {
		t.Fatalf("buffered = %v, want empty for a session that never ran", buffered)
	}
}
