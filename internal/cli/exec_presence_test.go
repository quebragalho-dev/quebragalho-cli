package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// presenceHeldExecChild holds the session presence lease from a separate
// process until it is killed. Returns a cancel function for defer and the
// child's pid (read from the readiness file the child writes).
func presenceHeldExecChild(t *testing.T, root, sessionID string) (cancel func(), pid int) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "child-ready")
	cmd := exec.Command(os.Args[0], "-test.run=TestExecResumePresenceChildHelper")
	cmd.Env = append(os.Environ(),
		"QG_TEST_EXEC_LOCK_CHILD=1",
		"QG_TEST_EXEC_LOCK_ROOT="+root,
		"QG_TEST_EXEC_LOCK_SESSION="+sessionID,
		"QG_TEST_EXEC_LOCK_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
			t.Fatal("child never signalled readiness")
		}
		time.Sleep(20 * time.Millisecond)
	}
	pid = cmd.Process.Pid
	if data, err := os.ReadFile(ready); err == nil {
		if parsed, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && parsed > 0 {
			pid = parsed
		}
	}
	return func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}, pid
}

// TestExecResumePresenceChildHelper is the child half of the presence tests:
// it holds the lease until killed. It is never run directly by the test suite
// (guarded by the env gate).
func TestExecResumePresenceChildHelper(t *testing.T) {
	if os.Getenv("QG_TEST_EXEC_LOCK_CHILD") != "1" {
		t.Skip("child helper")
	}
	root := os.Getenv("QG_TEST_EXEC_LOCK_ROOT")
	sessionID := os.Getenv("QG_TEST_EXEC_LOCK_SESSION")
	ready := os.Getenv("QG_TEST_EXEC_LOCK_READY")
	store := sessions.NewStore(sessions.StoreOptions{RootDir: root})
	lock, err := store.TryAcquireSessionLock(sessionID)
	if err != nil {
		t.Fatalf("child acquire: %v", err)
	}
	defer lock.Release()
	if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("child ready: %v", err)
	}
	time.Sleep(60 * time.Second) // hold until killed
}

// TestExecResumePresenceHeldRefuses spawns a child holding the session
// presence lease and proves `qg exec --resume` refuses that session in pt-BR,
// whether the id was passed explicitly or resolved as the latest resumable.
// The holder is a child process: an in-process acquire would share the lease
// registry by design (same process, same owner).
func TestExecResumePresenceHeldRefuses(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	store := sessions.NewStore(sessions.StoreOptions{})
	session, err := store.Create(sessions.CreateInput{SessionID: "exec_presence_held"})
	if err != nil {
		t.Fatal(err)
	}
	cancel, _ := presenceHeldExecChild(t, store.RootDir, session.SessionID)
	defer cancel()

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"explicit resume", []string{"exec", "--resume", session.SessionID, "hello"}},
		{"bare resume (latest)", []string{"exec", "--prompt", "hello", "--resume"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exitCode, _, stderr := runExecWithEcho(t, tc.args)
			if exitCode == 0 {
				t.Fatal("exec --resume must refuse a session another live qg process holds")
			}
			if !strings.Contains(stderr, "outro terminal") {
				t.Fatalf("refusal must be pt-BR and name the contention, got: %q", stderr)
			}
		})
	}
}
