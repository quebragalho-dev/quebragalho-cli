//go:build windows

package sessions

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// errSessionLockHeld marks a non-blocking lock refusal (as opposed to an
// I/O error opening the lock file), so callers can report who holds it.
var errSessionLockHeld = errors.New("qg session lock held")

// acquireFileLock takes an exclusive OS lock (LockFileEx) on the session's .lock
// file so concurrent processes — and separate Store instances within one process
// — sharing the same RootDir serialize their session mutations, matching the
// flock behavior on unix. It blocks until the lock is available and returns a
// release function; closing the handle and unlocking the region releases it.
func (store *Store) acquireFileLock(sessionID string) (func(), error) {
	path := store.lockPath(sessionID)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open qg session lock: %w", err)
	}
	handle := windows.Handle(file.Fd())
	overlapped := new(windows.Overlapped)
	// Lock a fixed 1-byte region. A blocking exclusive lock (no
	// LOCKFILE_FAIL_IMMEDIATELY) waits until any current holder releases.
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock qg session: %w", err)
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
		_ = file.Close()
	}, nil
}

// tryAcquireFileLock is the non-blocking variant used for the session
// presence lease: LOCKFILE_FAIL_IMMEDIATELY reports a held region at once
// instead of waiting, so a second terminal gets a refusal, not a hang.
// Separate handles in one process conflict the same way cross-process
// handles do, matching the unix behavior.
func tryAcquireFileLock(path string) (release func(), file *os.File, err error) {
	file, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open qg session lock: %w", err)
	}
	handle := windows.Handle(file.Fd())
	overlapped := new(windows.Overlapped)
	if err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped); err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%w: %v", errSessionLockHeld, err)
	}
	return func() {
		_ = windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
	}, file, nil
}
