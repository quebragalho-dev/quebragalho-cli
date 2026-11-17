package sessions

import (
	"testing"
)

// TestPresenceLeaseHeldAcrossTempDirLifetime reproduces the Windows TUI
// failure on Linux: a presence lease acquired the way ensureActiveSession
// does and never released keeps the lock file open. On Windows the open,
// locked presence.lock cannot be unlinked, so t.TempDir() cleanup fails the
// test; on Linux the same state shows as a fresh non-blocking acquire on the
// same path failing while the lease is held. Registering
// ReleaseSessionLocksUnderForTest for the store root (what the TUI choke
// points do) frees the lock before the removal runs.
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
		// Runs last: after the release below, a second open file description
		// must be able to take the lock.
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
	// Runs first: the TUI choke-point hook for the store root.
	t.Cleanup(func() {
		ReleaseSessionLocksUnderForTest(dir)
	})
}

// TestReleaseSessionLocksUnderForTest scopes the release to the given root:
// leases under it drop whatever the reference count, leases elsewhere stay
// held, and an empty root is a no-op.
func TestReleaseSessionLocksUnderForTest(t *testing.T) {
	under := NewStore(StoreOptions{RootDir: t.TempDir()})
	underSession, err := under.Create(CreateInput{SessionID: "repro_under"})
	if err != nil {
		t.Fatal(err)
	}
	outside := NewStore(StoreOptions{RootDir: t.TempDir()})
	outsideSession, err := outside.Create(CreateInput{SessionID: "repro_outside"})
	if err != nil {
		t.Fatal(err)
	}
	// A same-process holder, then a second one bumping the reference count:
	// the release must drop the lease anyway.
	if _, err := under.TryAcquireSessionLock(underSession.SessionID); err != nil {
		t.Fatalf("acquire under root: %v", err)
	}
	if _, err := under.TryAcquireSessionLock(underSession.SessionID); err != nil {
		t.Fatalf("re-acquire under root: %v", err)
	}
	held, err := outside.TryAcquireSessionLock(outsideSession.SessionID)
	if err != nil {
		t.Fatalf("acquire outside root: %v", err)
	}
	_ = held

	ReleaseSessionLocksUnderForTest("")
	assertLockHeld(t, under.presenceLockPath(underSession.SessionID), "empty root must not release anything")

	ReleaseSessionLocksUnderForTest(under.RootDir)
	assertLockFree(t, under.presenceLockPath(underSession.SessionID), "lease under root")
	assertLockHeld(t, outside.presenceLockPath(outsideSession.SessionID), "lease outside root")

	// Idempotent: a second call finds nothing and changes nothing.
	ReleaseSessionLocksUnderForTest(under.RootDir)
	assertLockFree(t, under.presenceLockPath(underSession.SessionID), "lease under root after repeated release")
	assertLockHeld(t, outside.presenceLockPath(outsideSession.SessionID), "lease outside root after repeated release")

	held.Release()
	assertLockFree(t, outside.presenceLockPath(outsideSession.SessionID), "lease outside root after its own release")
}

// assertLockFree fails when a fresh non-blocking acquire on path refuses.
func assertLockFree(t *testing.T, path, what string) {
	t.Helper()
	release, file, err := tryAcquireFileLock(path)
	if err != nil {
		t.Fatalf("%s still held: %v", what, err)
	}
	if release != nil {
		release()
	}
	if file != nil {
		_ = file.Close()
	}
}

// assertLockHeld fails when a fresh non-blocking acquire on path succeeds.
func assertLockHeld(t *testing.T, path, what string) {
	t.Helper()
	release, file, err := tryAcquireFileLock(path)
	if err == nil {
		if release != nil {
			release()
		}
		if file != nil {
			_ = file.Close()
		}
		t.Fatalf("%s must stay held", what)
	}
}
