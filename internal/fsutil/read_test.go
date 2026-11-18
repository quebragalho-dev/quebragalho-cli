package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestIsRetryableReadErrorDecision(t *testing.T) {
	// Wrap like os.ReadFile does: a *PathError carrying the raw errno, so
	// os.IsPermission sees exactly what production hands it.
	sharing := &os.PathError{Op: "open", Path: "marker", Err: syscall.Errno(32)} // ERROR_SHARING_VIOLATION
	locking := &os.PathError{Op: "open", Path: "marker", Err: syscall.Errno(33)} // ERROR_LOCK_VIOLATION
	denied := &os.PathError{Op: "open", Path: "marker", Err: syscall.EACCES}
	missing := &os.PathError{Op: "open", Path: "marker", Err: syscall.ENOENT}
	broken := errors.New("disk on fire")

	cases := []struct {
		name string
		goos string
		err  error
		want bool
	}{
		{"sharing violation on windows retries", "windows", sharing, true},
		{"lock violation on windows retries", "windows", locking, true},
		{"permission error on windows retries", "windows", denied, true},
		{"missing file on windows never retries", "windows", missing, false},
		{"unrelated error on windows never retries", "windows", broken, false},
		{"sharing violation off windows never retries", "linux", sharing, false},
		{"sharing violation on darwin never retries", "darwin", sharing, false},
		{"permission error off windows never retries", "linux", denied, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableReadError(tc.goos, tc.err); got != tc.want {
				t.Fatalf("isRetryableReadError(%q, %v) = %v, want %v", tc.goos, tc.err, got, tc.want)
			}
		})
	}
}

func TestReadFileWithRetrySucceedsAfterTransientLockFailures(t *testing.T) {
	const payload = `{"schemaVersion":2}`
	var attempts int
	var sleeps []time.Duration
	data, err := readFileWithRetry("windows", "marker", func(path string) ([]byte, error) {
		attempts++
		if path != "marker" {
			t.Fatalf("read path = %q, want %q", path, "marker")
		}
		if attempts < 3 {
			return nil, syscall.Errno(32) // ERROR_SHARING_VIOLATION
		}
		return []byte(payload), nil
	}, func(d time.Duration) { sleeps = append(sleeps, d) })
	if err != nil {
		t.Fatalf("expected success after transient failures, got: %v", err)
	}
	if string(data) != payload {
		t.Fatalf("data = %q, want %q", data, payload)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(sleeps) != 2 || sleeps[0] != 5*time.Millisecond || sleeps[1] != 10*time.Millisecond {
		t.Fatalf("sleeps = %v, want [5ms 10ms] backoff", sleeps)
	}
}

func TestReadFileWithRetryReturnsImmediatelyOffWindows(t *testing.T) {
	sentinel := syscall.Errno(32) // a sharing violation anywhere else is fatal
	var attempts int
	var sleeps int
	_, err := readFileWithRetry("linux", "marker", func(string) ([]byte, error) {
		attempts++
		return nil, sentinel
	}, func(time.Duration) { sleeps++ })
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got: %v", err)
	}
	if attempts != 1 || sleeps != 0 {
		t.Fatalf("attempts = %d, sleeps = %d: off-windows errors must not retry", attempts, sleeps)
	}
}

func TestReadFileWithRetryMissingFileNeverRetriesOnWindows(t *testing.T) {
	missing := &os.PathError{Op: "open", Path: "marker", Err: syscall.ENOENT}
	var attempts int
	var sleeps int
	_, err := readFileWithRetry("windows", "marker", func(string) ([]byte, error) {
		attempts++
		return nil, missing
	}, func(time.Duration) { sleeps++ })
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected not-exist error, got: %v", err)
	}
	if attempts != 1 || sleeps != 0 {
		t.Fatalf("attempts = %d, sleeps = %d: a missing file must not retry", attempts, sleeps)
	}
}

func TestReadFileWithRetryExhaustsBudgetWithGrowingBackoff(t *testing.T) {
	violation := syscall.Errno(32) // ERROR_SHARING_VIOLATION
	var attempts int
	var sleeps []time.Duration
	_, err := readFileWithRetry("windows", "marker", func(string) ([]byte, error) {
		attempts++
		return nil, violation
	}, func(d time.Duration) { sleeps = append(sleeps, d) })
	if !errors.Is(err, violation) {
		t.Fatalf("expected the last violation back, got: %v", err)
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

func TestReadFileWithRetryNilReadFallsBackToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	data, err := ReadFileWithRetry(path, nil)
	if err != nil {
		t.Fatalf("ReadFileWithRetry with nil read: %v", err)
	}
	if string(data) != `{}` {
		t.Fatalf("data = %q, want %q", data, `{}`)
	}
}
