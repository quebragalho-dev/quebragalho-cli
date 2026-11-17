package daemon

import (
	"context"
	"errors"
	"testing"
)

// Forcing Drain between the draining check and the lease select must still
// refuse: the select can pick the free slot even with drained closed.
func TestPoolAcquireNeverAdmitsAfterDrain(t *testing.T) {
	const trials = 50
	admits := 0
	for i := 0; i < trials; i++ {
		pool, err := NewPool(PoolOptions{Size: 1, Launcher: func(context.Context, WorkerSpec) (WorkerHandle, error) {
			return &fakeWorker{pid: 1, exitCode: 0}, nil
		}})
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}
		checkReached := make(chan struct{})
		drainDone := make(chan struct{})
		var once bool
		pool.preSelectHook = func() {
			if !once {
				once = true
				close(checkReached)
				<-drainDone
			}
		}
		acquired := make(chan error, 1)
		go func() {
			acquired <- pool.acquire(context.Background())
		}()
		<-checkReached
		pool.Drain()
		close(drainDone)
		err = <-acquired
		if err == nil {
			admits++
			pool.release()
		} else if !errors.Is(err, ErrPoolDraining) {
			t.Fatalf("acquire after drain err = %v, want ErrPoolDraining", err)
		}
	}
	if admits > 0 {
		t.Fatalf("acquire admitted %d/%d runs racing Drain (want 0: never admit after the drain)", admits, trials)
	}
}
