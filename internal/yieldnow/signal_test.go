package yieldnow

import (
	"sync"
	"testing"
	"time"
)

// A parked waiter wakes the moment Request fires instead of waiting out the
// full duration.
func TestRequestWakesParkedWaiter(t *testing.T) {
	s := New()
	wake, release := s.Park()
	defer release()
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-wake:
		case <-time.After(10 * time.Second):
			t.Error("parked waiter was not woken by Request")
		}
	}()
	time.Sleep(50 * time.Millisecond)
	if !s.Request() {
		t.Fatal("Request must report a woken wait")
	}
	<-done
}

// Request with no waiter parked reports nothing to wake.
func TestRequestWithoutWaiterIsFalse(t *testing.T) {
	s := New()
	if s.Request() {
		t.Fatal("Request with no parked waiter must return false")
	}
	wake, release := s.Park()
	release()
	// Let the release land: a Request racing it must not hang, either way.
	_ = wake
	if s.Request() {
		t.Fatal("Request after every waiter left must return false")
	}
}

// A nil Signal never wakes anything and never panics.
func TestNilSignalIsSafe(t *testing.T) {
	var s *Signal
	if s.Request() {
		t.Fatal("Request on nil Signal must return false")
	}
	wake, release := s.Park()
	release()
	select {
	case <-wake:
		t.Fatal("nil Signal must never wake")
	case <-time.After(20 * time.Millisecond):
	}
}

// One Signal per run: firing an old run's Signal never wakes the next run.
func TestFiredSignalDoesNotLeakIntoNextRun(t *testing.T) {
	old := New()
	wake, release := old.Park()
	release()
	_ = wake

	next := New()
	nextWake, nextRelease := next.Park()
	defer nextRelease()
	if old.Request() {
		t.Fatal("old run with no waiter must not report a wake")
	}
	select {
	case <-nextWake:
		t.Fatal("firing the old run's Signal woke the next run")
	case <-time.After(50 * time.Millisecond):
	}
}

// Concurrent parks converge on one broadcast: a single Request wakes every
// waiter parked on the run's Signal, the count returns to zero, and a later
// Request is false again.
func TestConcurrentParkAndRequest(t *testing.T) {
	s := New()
	const waiters = 16
	parked := make(chan struct{}, waiters)
	woken := make(chan struct{}, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			wake, release := s.Park()
			defer release()
			parked <- struct{}{}
			select {
			case <-wake:
				woken <- struct{}{}
			case <-time.After(10 * time.Second):
			}
		}()
	}
	for i := 0; i < waiters; i++ {
		select {
		case <-parked:
		case <-time.After(10 * time.Second):
			t.Fatal("waiter did not park")
		}
	}
	// Every waiter holds the same wake channel: one Request wakes them all.
	if !s.Request() {
		t.Fatal("Request must report the woken waits")
	}
	wg.Wait()
	close(woken)
	n := 0
	for range woken {
		n++
	}
	if n != waiters {
		t.Fatalf("%d waiters woken, want %d", n, waiters)
	}
	if s.Request() {
		t.Fatal("Request after every waiter left must return false")
	}
}

// Context plumbing carries the run's Signal to the tools.
func TestContextCarriesSignal(t *testing.T) {
	if FromContext(t.Context()) != nil {
		t.Fatal("empty context must carry no Signal")
	}
	s := New()
	ctx := WithSignal(t.Context(), s)
	if FromContext(ctx) != s {
		t.Fatal("context must carry the run's Signal")
	}
	if FromContext(WithSignal(t.Context(), nil)) != nil {
		t.Fatal("a nil Signal must not attach to the context")
	}
}
