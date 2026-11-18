package fsutil

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

func TestRenameWithLongRetrySucceedsAfterTransientLockFailures(t *testing.T) {
	var attempts int
	var sleeps []time.Duration
	err := renameWithLongRetry("windows", "src", "dst", func(src, dst string) error {
		attempts++
		if src != "src" || dst != "dst" {
			t.Fatalf("rename args = %q %q, want src dst", src, dst)
		}
		if attempts < 3 {
			return syscall.Errno(32) // ERROR_SHARING_VIOLATION
		}
		return nil
	}, func(d time.Duration) { sleeps = append(sleeps, d) })
	if err != nil {
		t.Fatalf("expected success after transient failures, got: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(sleeps) != 2 || sleeps[0] != 5*time.Millisecond || sleeps[1] != 10*time.Millisecond {
		t.Fatalf("sleeps = %v, want [5ms 10ms] backoff", sleeps)
	}
}

func TestRenameWithLongRetryReturnsImmediatelyOffWindows(t *testing.T) {
	violation := syscall.Errno(32) // a sharing violation anywhere else is fatal
	var attempts int
	var sleeps int
	err := renameWithLongRetry("linux", "src", "dst", func(string, string) error {
		attempts++
		return violation
	}, func(time.Duration) { sleeps++ })
	if !errors.Is(err, violation) {
		t.Fatalf("expected violation back, got: %v", err)
	}
	if attempts != 1 || sleeps != 0 {
		t.Fatalf("attempts = %d, sleeps = %d: off-windows errors must not retry", attempts, sleeps)
	}
}

func TestRenameWithLongRetryNonRetryableErrorFailsFastOnWindows(t *testing.T) {
	sentinel := errors.New("disk on fire")
	var attempts int
	var sleeps int
	err := renameWithLongRetry("windows", "src", "dst", func(string, string) error {
		attempts++
		return sentinel
	}, func(time.Duration) { sleeps++ })
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
	if attempts != 1 || sleeps != 0 {
		t.Fatalf("attempts = %d, sleeps = %d: unrelated errors must not retry", attempts, sleeps)
	}
}

func TestRenameWithLongRetryCommittedReplacementNeverRetries(t *testing.T) {
	committed := &CommittedReplacementCleanupError{BackupPath: "dst.bak", Cause: syscall.Errno(32)}
	var attempts int
	var sleeps int
	err := renameWithLongRetry("windows", "src", "dst", func(string, string) error {
		attempts++
		return committed
	}, func(time.Duration) { sleeps++ })
	if !errors.Is(err, committed) {
		t.Fatalf("expected committed error back, got: %v", err)
	}
	if attempts != 1 || sleeps != 0 {
		t.Fatalf("attempts = %d, sleeps = %d: a consumed source must never retry", attempts, sleeps)
	}
}

func TestRenameWithLongRetryExhaustsBudgetWithGrowingBackoff(t *testing.T) {
	// EACCES is a permission error on every platform; on Windows the
	// racing-reader rename failure (ERROR_ACCESS_DENIED) classifies the same
	// way through os.IsPermission.
	denied := syscall.EACCES
	var attempts int
	var sleeps []time.Duration
	err := renameWithLongRetry("windows", "src", "dst", func(string, string) error {
		attempts++
		return denied
	}, func(d time.Duration) { sleeps = append(sleeps, d) })
	if !errors.Is(err, denied) {
		t.Fatalf("expected the last error back, got: %v", err)
	}
	if attempts != len(sleeps)+1 {
		t.Fatalf("attempts = %d with %d sleeps, want exactly one trailing attempt", attempts, len(sleeps))
	}
	if len(sleeps) < 3 {
		t.Fatalf("sleeps = %v: want several growing delays before giving up", sleeps)
	}
	var total time.Duration
	for i, delay := range sleeps {
		total += delay
		if i > 0 && delay < sleeps[i-1] {
			t.Fatalf("sleeps = %v: backoff must not shrink", sleeps)
		}
		if delay > 100*time.Millisecond {
			t.Fatalf("sleeps = %v: delay %v exceeds the 100ms cap", sleeps, delay)
		}
	}
	if total > windowsTransientLockRetryBudget {
		t.Fatalf("total slept = %v, exceeds the %v budget", total, windowsTransientLockRetryBudget)
	}
	if total < windowsTransientLockRetryBudget-100*time.Millisecond {
		t.Fatalf("total slept = %v: gave up well before the %v budget", total, windowsTransientLockRetryBudget)
	}
}
