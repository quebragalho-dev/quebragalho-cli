//go:build !windows

package tui

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// stopping a foreground row must actually terminate its process. Starts
// a real sleeper in its own process group, points the pid lookup at it,
// drives the stop command built in Update, and requires the process to be
// gone. Cleanup kills and reaps the sleeper, so a failing test never leaves
// a stray process behind.
func TestSubagentsStopKillsForegroundProcess(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleeper: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	orig := liveChildPIDFunc
	reaped := false
	t.Cleanup(func() {
		liveChildPIDFunc = orig
		if !reaped {
			_ = cmd.Process.Kill()
			select {
			case <-waited:
			case <-time.After(10 * time.Second):
			}
		}
	})
	liveChildPIDFunc = func(string) (int, bool) { return cmd.Process.Pid, true }

	m := panelTestModel(time.Now())
	sessionID := "sess-r5-kill"
	m.subagents.rows = append(m.subagents.rows, subagentRow{
		key: sessionID, callKey: sessionID, sessionID: sessionID, name: "sleeper",
		state: subagentRunning, startedAt: time.Now(),
	})
	stop := m.buildStopCmd(sessionID)
	if stop == nil {
		t.Fatal("a running foreground row must produce a stop command")
	}
	if msg, ok := stop().(subagentStopDoneMsg); !ok || msg.err != nil {
		t.Fatalf("the stop command must succeed, got %#v", stop())
	}
	select {
	case err := <-waited:
		reaped = true
		if err == nil {
			t.Fatal("a killed sleeper must not exit cleanly")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the foreground process did not die after the panel stop")
	}
}
