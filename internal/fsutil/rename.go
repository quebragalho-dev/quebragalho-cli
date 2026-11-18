// Package fsutil provides small filesystem helpers shared across packages.
package fsutil

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
)

// CommittedReplacementCleanupError reports that a replacement was committed,
// but the old destination retained at BackupPath could not be removed. Callers
// must treat the replacement itself as successful and surface the cleanup
// problem separately.
type CommittedReplacementCleanupError struct {
	BackupPath string
	Cause      error
}

func (err *CommittedReplacementCleanupError) Error() string {
	return fmt.Sprintf("replacement committed, but backup %s could not be removed: %v", err.BackupPath, err.Cause)
}

func (err *CommittedReplacementCleanupError) Unwrap() error {
	return err.Cause
}

// ReplaceWithRetry publishes src over dst using the platform's replacement
// primitive, retrying on the same transient Windows lock errors RenameWithRetry
// handles. On Windows it uses ReplaceFileW so an existing destination keeps its
// DACL and selected metadata instead of receiving the temporary file's inherited
// DACL. ReplaceFileW is not observer-atomic and may briefly leave dst absent;
// callers that cannot tolerate that must synchronize their own readers. On Unix
// replacement uses os.Rename.
//
// replace overrides the platform primitive so tests can exercise the retry path;
// pass nil for the default.
func ReplaceWithRetry(src, dst string, replace func(src, dst string) error) error {
	if replace == nil {
		replace = replaceExisting
	}
	return RenameWithRetry(src, dst, replace)
}

// RenameWithLongRetry renames src to dst like RenameWithRetry, but with the
// ~2s growing-backoff budget instead of ~100ms of fixed 10ms retries: sandbox
// marker writers lose the race against readers holding the file open mid-read
// (on Windows that rename fails with access denied), and under load a single
// short burst is not enough to outlast them. rename overrides os.Rename so
// tests can exercise the retry path; pass nil to use os.Rename.
func RenameWithLongRetry(src, dst string, rename func(src, dst string) error) error {
	return renameWithLongRetry(runtime.GOOS, src, dst, rename, time.Sleep)
}

// renameWithLongRetry is RenameWithLongRetry with the platform and the sleeper
// injected, so the retry decision, the backoff shape and the total budget are
// unit-testable on any OS. The budget is accounted from the scheduled sleeps
// rather than the wall clock, so a fake sleeper keeps the test loop bounded.
func renameWithLongRetry(goos string, src, dst string, rename func(src, dst string) error, sleep func(time.Duration)) error {
	if rename == nil {
		rename = os.Rename
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	var slept time.Duration
	delay := readRetryInitialDelay
	for {
		err := rename(src, dst)
		if err == nil {
			return nil
		}
		var committed *CommittedReplacementCleanupError
		if errors.As(err, &committed) {
			// The source has already been consumed; retrying would rename
			// the wrong file.
			return err
		}
		if goos != "windows" || (!os.IsPermission(err) && !isWindowsSharingOrLockViolation(err)) {
			return err
		}
		if slept+delay > windowsTransientLockRetryBudget {
			return err
		}
		sleep(delay)
		slept += delay
		delay *= 2
		if delay > readRetryMaxDelay {
			delay = readRetryMaxDelay
		}
	}
}

// RenameWithRetry renames src to dst, retrying briefly on Windows when the
// destination is transiently locked (antivirus scanners, search indexers, or
// a concurrent reader holding the file open). rename overrides os.Rename so
// tests can exercise the retry path; pass nil to use os.Rename.
func RenameWithRetry(src, dst string, rename func(src, dst string) error) error {
	if rename == nil {
		rename = os.Rename
	}
	var err error
	for i := 0; i < 10; i++ {
		err = rename(src, dst)
		if err == nil {
			return nil
		}
		var committed *CommittedReplacementCleanupError
		if errors.As(err, &committed) {
			// The source has already been consumed. In particular, never retry if
			// the cleanup cause itself is a transient Windows sharing violation.
			break
		}
		if runtime.GOOS == "windows" {
			if os.IsPermission(err) || isWindowsSharingOrLockViolation(err) {
				time.Sleep(10 * time.Millisecond)
				continue
			}
		}
		break
	}
	return err
}

func isWindowsSharingOrLockViolation(err error) bool {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		const ERROR_SHARING_VIOLATION syscall.Errno = 32
		const ERROR_LOCK_VIOLATION syscall.Errno = 33
		return errno == ERROR_SHARING_VIOLATION || errno == ERROR_LOCK_VIOLATION
	}
	return false
}
