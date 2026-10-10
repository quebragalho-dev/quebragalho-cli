package oauth

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/lockutil"
)

const fileLockTimeout = 5 * time.Second

// acquireFileLock takes a cross-process advisory lock through a stable lock
// file. It retries with a short backoff until a timeout. Kernel lock lifetime
// provides crash recovery. The sidecar is removed by its holder on release,
// while the flock is still held: no other process can own the lock at that
// instant, and a waiter that raced the unlink recreates the path on its next
// O_CREAT poll (lockutil.openLockFileAt creates on open). Removing after
// Release — or while someone else holds the lock — would be the racy form that
// splits the rendezvous across inodes. A platform that refuses to unlink an
// open file (Windows) keeps the sidecar; harmless, just untidy.
func acquireFileLock(lockPath string, now func() time.Time) (func(), error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	deadline := now().Add(fileLockTimeout)
	for {
		lock, err := lockutil.TryAcquireFileLock(lockPath)
		if err == nil {
			return func() {
				_ = os.Remove(lockPath)
				_ = lock.Release()
			}, nil
		}
		if !errors.Is(err, lockutil.ErrLockHeld) {
			return nil, fmt.Errorf("oauth: acquire token lock: %w", err)
		}
		if now().After(deadline) {
			return nil, fmt.Errorf("oauth: timed out acquiring token lock %s", filepath.Base(lockPath))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
