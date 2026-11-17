// Package filelock provides a non-blocking exclusive advisory lock on an
// already-open file handle. It is the shared primitive behind the MCP
// permissions and sandbox grants stores, which previously duplicated the same
// Flock/LockFileEx sequence under different names.
//
// The lock is held on the open file description (not the path): the caller
// opens the stable ".lockfile" sibling once, retries TryLockExclusive until it
// reports locked, and closes the file after Unlock. The kernel releases the
// lock if the holder exits, so no stale-file reclaim protocol is needed and
// the lock path is never renamed or removed while contended. Timeouts, retry
// delays, and lock filenames stay with the callers and are unchanged here.
package filelock

import (
	"os"
)

// TryLockExclusive attempts a non-blocking exclusive lock on file. It reports
// locked=true when the lock was acquired; locked=false means another process
// (or handle) holds it. Any other error is an I/O failure.
func TryLockExclusive(file *os.File) (bool, error) {
	return tryLockExclusive(file)
}

// Unlock releases a lock acquired by TryLockExclusive. The caller still owns
// the file handle and must close it.
func Unlock(file *os.File) error {
	return unlock(file)
}
