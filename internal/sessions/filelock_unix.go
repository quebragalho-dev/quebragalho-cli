//go:build !windows

package sessions

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// errSessionLockHeld marks a non-blocking lock refusal (as opposed to an
// I/O error opening the lock file), so callers can report who holds it.
var errSessionLockHeld = errors.New("qg session lock held")

// acquireFileLock takes an exclusive OS advisory lock (flock) on the session's
// .lock file so concurrent processes sharing the same RootDir (e.g. CLI rewind
// vs TUI) serialize their session mutations. It blocks until the lock is
// available and returns a release function. The lock is held via an open file
// descriptor; closing it releases the lock.
func (store *Store) acquireFileLock(sessionID string) (func(), error) {
	path := store.lockPath(sessionID)
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open qg session lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock qg session: %w", err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}

// tryAcquireFileLock is the non-blocking variant used for the session
// presence lease: it returns errSessionLockHeld (wrapping the errno) when
// another open description — another process or another handle in this
// process — already holds an exclusive lock.
func tryAcquireFileLock(path string) (release func(), file *os.File, err error) {
	file, err = os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open qg session lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%w: %v", errSessionLockHeld, err)
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	}, file, nil
}
