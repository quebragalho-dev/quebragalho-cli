package oauth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/lockutil"
)

// The sidecar must not outlive its work: once an uncontended holder releases,
// the lock file is removed so `qg auth logout` leaves no residue behind.
func TestAcquireFileLockRemovesLockFileOnRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth-tokens.json.lockfile")
	unlock, err := acquireFileLock(path, time.Now)
	if err != nil {
		t.Fatalf("acquireFileLock: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file should exist while held: %v", err)
	}
	unlock()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed on release, stat err = %v", err)
	}
}

// Cleanup only happens to a file THIS caller held: while an external holder owns
// the lock, a timed-out acquire must leave the file alone (removing it would
// split the flock rendezvous across inodes).
func TestAcquireFileLockKeepsLockFileWhileContended(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oauth-tokens.json.lockfile")
	held, err := lockutil.TryAcquireFileLock(path)
	if err != nil {
		t.Fatalf("external holder: %v", err)
	}
	defer func() { _ = held.Release() }()

	base := time.Now()
	calls := 0
	now := func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(fileLockTimeout + time.Second) // expire the deadline on the first retry
	}
	unlock, err := acquireFileLock(path, now)
	if err == nil {
		if unlock != nil {
			unlock()
		}
		t.Fatal("expected a timeout while another holder owns the lock")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("lock file must survive while contended: %v", statErr)
	}
}
