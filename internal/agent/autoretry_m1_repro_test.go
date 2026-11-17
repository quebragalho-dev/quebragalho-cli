package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Repro for M1: a wait followed by a non-retryable error must settle the
// previous wait (emit Active:false). On dev it returns (false, nil) without
// settling, leaving waiting=true and no clear emitted.
func TestRetrierNonRetryableAfterWaitSettles(t *testing.T) {
	var waits []time.Duration
	var infos []RetryWaitInfo
	retrier := newModelCallRetrier(Options{
		RetrySleep:  instantRetrySleep(&waits),
		OnRetryWait: func(info RetryWaitInfo) { infos = append(infos, info) },
	})
	retrier.jitter = func(d time.Duration) time.Duration { return d }

	proceed, err := retrier.waitAfterError(context.Background(), errors.New("503 Service Unavailable"), false)
	if err != nil || !proceed {
		t.Fatalf("retryable must schedule a wait, got proceed=%v err=%v", proceed, err)
	}
	if len(infos) != 1 || !infos[0].Active {
		t.Fatalf("want one Active:true after schedule, got %#v", infos)
	}

	proceed, err = retrier.waitAfterError(context.Background(), errors.New("401 unauthorized"), false)
	if err != nil || proceed {
		t.Fatalf("non-retryable must end the call quietly, got proceed=%v err=%v", proceed, err)
	}
	if len(infos) != 2 || infos[1].Active {
		t.Fatalf("non-retryable after a wait must emit Active:false clear, got %#v", infos)
	}
}
