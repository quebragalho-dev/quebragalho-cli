//go:build windows

package cli

import (
	"fmt"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// TestExecResumePresenceHeldRefusesOnWindows is the Windows regression for
// the locked-file read failure: a byte-range LockFileEx makes every read of
// presence.lock fail with ERROR_LOCK_VIOLATION (the PID used to live at
// offset 0, inside the locked byte), so the refusal dropped the holder's
// PID. The pid hint now lives in an unlocked presence.pid sidecar, written by
// the holder after it acquires. Only compiled on Windows; validated on the
// Windows E2E VM (no Windows runner for this package on this machine).
func TestExecResumePresenceHeldRefusesOnWindows(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	store := sessions.NewStore(sessions.StoreOptions{})
	session, err := store.Create(sessions.CreateInput{SessionID: "exec_presence_held_windows"})
	if err != nil {
		t.Fatal(err)
	}
	cancel, childPID := presenceHeldExecChild(t, store.RootDir, session.SessionID)
	defer cancel()

	exitCode, _, stderr := runExecWithEcho(t, []string{"exec", "--resume", session.SessionID, "hello"})
	if exitCode == 0 {
		t.Fatal("exec --resume must refuse a session another live qg process holds")
	}
	if !strings.Contains(stderr, "outro terminal") {
		t.Fatalf("refusal must be pt-BR and name the contention, got: %q", stderr)
	}
	// The sidecar is never locked, so the refusal can always name the
	// holder even on Windows: the pid must be visible.
	if !strings.Contains(stderr, fmt.Sprintf("PID %d", childPID)) {
		t.Fatalf("refusal must name the holder pid (%d), got: %q", childPID, stderr)
	}
}
