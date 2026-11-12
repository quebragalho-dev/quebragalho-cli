//go:build !windows

package specialist

import (
	"context"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/background"
)

// A foreground child registers its pid while running (so the TUI can stop
// it) and unregisters on exit; terminating the process group ends it.
func TestRunChildProcessRegistersLivePIDUntilExit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan ChildRunResult, 1)
	go func() {
		result, err := runChildProcess(ctx, "sess-live", "sleep", []string{"60"}, nil)
		if err == nil {
			done <- result
			return
		}
		done <- ChildRunResult{ExitCode: -1}
	}()
	deadline := time.Now().Add(10 * time.Second)
	var pid int
	for time.Now().Before(deadline) {
		var ok bool
		pid, ok = LiveChildPID("sess-live")
		if ok && pid > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid <= 0 {
		cancel()
		t.Fatal("running child must publish its pid")
	}
	if err := background.TerminateProcess(pid); err != nil {
		t.Fatalf("TerminateProcess: %v", err)
	}
	select {
	case result := <-done:
		if result.ExitCode == 0 {
			t.Fatal("killed child must not exit 0")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit after group kill")
	}
	if _, ok := LiveChildPID("sess-live"); ok {
		t.Fatal("exited child must be unregistered")
	}
}
