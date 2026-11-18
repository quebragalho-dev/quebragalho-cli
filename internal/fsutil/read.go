package fsutil

import (
	"os"
	"runtime"
	"time"
)

// windowsReadRetryBudget bounds how long ReadFileWithRetry keeps retrying a
// transiently locked file on Windows: long enough to outlast a concurrent
// marker replacement (temp-file write plus RenameWithRetry, which itself
// retries for ~100ms), short enough to keep a wedged helper from hanging
// command startup.
const windowsReadRetryBudget = 2 * time.Second

const (
	readRetryInitialDelay = 5 * time.Millisecond
	readRetryMaxDelay     = 100 * time.Millisecond
)

// ReadFileWithRetry reads the file at path, retrying briefly on Windows when
// the open races a concurrent replacement (another process swapping the file
// via rename, so the open fails with ERROR_SHARING_VIOLATION) or a transient
// scanner/indexer lock. A missing file and every other error return
// immediately, as on every other OS — only the transient lock errors retry.
// read overrides the default shared-access open so tests can exercise the
// retry path; pass nil to read through readFileShared (which on Windows
// opens with delete share so a concurrent rename can proceed).
func ReadFileWithRetry(path string, read func(string) ([]byte, error)) ([]byte, error) {
	return readFileWithRetry(runtime.GOOS, path, read, time.Sleep)
}

// readFileWithRetry is ReadFileWithRetry with the platform and the sleeper
// injected, so the retry decision, the backoff shape and the total budget are
// unit-testable on any OS. The budget is accounted from the scheduled sleeps
// rather than the wall clock, so a fake sleeper keeps the test loop bounded.
func readFileWithRetry(goos string, path string, read func(string) ([]byte, error), sleep func(time.Duration)) ([]byte, error) {
	if read == nil {
		read = readFileShared
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	var slept time.Duration
	delay := readRetryInitialDelay
	for {
		data, err := read(path)
		if err == nil {
			return data, nil
		}
		if !isRetryableReadError(goos, err) {
			return nil, err
		}
		if slept+delay > windowsReadRetryBudget {
			return nil, err
		}
		sleep(delay)
		slept += delay
		delay *= 2
		if delay > readRetryMaxDelay {
			delay = readRetryMaxDelay
		}
	}
}

// isRetryableReadError reports whether err is a transient Windows file-lock
// error worth another read attempt: the sharing/lock violations RenameWithRetry
// already retries on, plus permission errors (a lock held without read share
// surfaces as access denied). goos is a parameter — callers pass
// runtime.GOOS — so the decision is unit-testable on any platform.
func isRetryableReadError(goos string, err error) bool {
	if goos != "windows" {
		return false
	}
	return os.IsPermission(err) || isWindowsSharingOrLockViolation(err)
}
