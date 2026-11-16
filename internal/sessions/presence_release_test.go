package sessions

import (
	"testing"
)

// TestPresenceLeaseHeldAcrossTempDirLifetime reproduces the Windows TUI
// failure on Linux: a presence lease acquired the way ensureActiveSession
// does and never released keeps the lock file open. On Windows the open,
// locked presence.lock cannot be unlinked, so t.TempDir() cleanup fails the
// test; on Linux the same state shows as a fresh non-blocking acquire on the
// same path failing while the lease is held.
func TestPresenceLeaseHeldAcrossTempDirLifetime(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(StoreOptions{RootDir: dir})
	session, err := store.Create(CreateInput{SessionID: "presence_repro"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.TryAcquireSessionLock(session.SessionID)
	if err != nil {
		t.Fatalf("acquire presence lease: %v", err)
	}
	_ = lease // Held past the end of the test, like TUI models that return
	// without switching session: nothing releases it.
	t.Cleanup(func() {
		// Registered after t.TempDir, so this runs before the removal: a
		// second open file description must be able to take the lock once
		// every holder released it.
		release, file, err := tryAcquireFileLock(store.presenceLockPath(session.SessionID))
		if err != nil {
			t.Errorf("presence lease still held at cleanup, TempDir removal would fail on Windows: %v", err)
			return
		}
		if release != nil {
			release()
		}
		if file != nil {
			_ = file.Close()
		}
	})
}
