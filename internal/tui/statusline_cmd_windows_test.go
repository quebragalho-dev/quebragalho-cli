//go:build windows

package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"golang.org/x/sys/windows"
)

const statuslineStillActive = uint32(259) // STILL_ACTIVE

// statuslineTestProcessAlive reports whether pid is still active on Windows.
func statuslineTestProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(handle) }()

	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == statuslineStillActive
}

// TestStatuslineCommandKillsDetachedBackgroundChildOnWindowsExit verifies that
// a statusline script leaving a detached background child behind on normal exit
// has that child terminated by the Job Object before execStatuslineRun returns.
func TestStatuslineCommandKillsDetachedBackgroundChildOnWindowsExit(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "detached.pid")
	reapStatuslineTestHelper(t, pidFile)

	scriptPath := filepath.Join(dir, "statusline.cmd")
	script := "@echo off\r\n" +
		"set \"GO_WANT_HELPER_PROCESS=1\"\r\n" +
		"\"" + escapeCmdPercent(exePath) + "\" -test.run=TestStatuslineSleepHelper -- leakdetached \"" + escapeCmdPercent(pidFile) + "\" SL-DETACHED-OK\r\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Chdir(dir)

	msg := execStatuslineRun(context.Background(), "statusline.cmd", "{}", statuslineChildEnv(80, 24), 10*time.Second)
	if msg.errNote != "" {
		t.Fatalf("errNote = %q, want success", msg.errNote)
	}
	if len(msg.lines) != 1 || !strings.Contains(msg.lines[0], "SL-DETACHED-OK") {
		t.Fatalf("lines = %#v, want SL-DETACHED-OK", msg.lines)
	}

	childPID := statuslineTestWaitPID(t, pidFile)
	if childPID == 0 {
		t.Fatal("background child never recorded its pid")
	}
	deadline := time.Now().Add(5 * time.Second)
	for statuslineTestProcessAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if statuslineTestProcessAlive(childPID) {
		t.Fatalf("background child pid %d still alive after statusline exit", childPID)
	}
}

// TestStatuslineCommandKillsBackgroundChildOnWindowsTimeout verifies that a
// statusline timeout kills both the script and any backgrounded descendant.
func TestStatuslineCommandKillsBackgroundChildOnWindowsTimeout(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "timeout.pid")
	reapStatuslineTestHelper(t, pidFile)

	scriptPath := filepath.Join(dir, "statusline-slow.cmd")
	script := "@echo off\r\n" +
		"set \"GO_WANT_HELPER_PROCESS=1\"\r\n" +
		"\"" + escapeCmdPercent(exePath) + "\" -test.run=TestStatuslineSleepHelper -- leakdetached \"" + escapeCmdPercent(pidFile) + "\" SL-BEFORE-TIMEOUT\r\n" +
		"\"" + escapeCmdPercent(exePath) + "\" -test.run=TestStatuslineSleepHelper -- hold 30s\r\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Chdir(dir)

	msg := execStatuslineRun(context.Background(), "statusline-slow.cmd", "{}", statuslineChildEnv(80, 24), 2*time.Second)
	if msg.errNote == "" || !strings.Contains(msg.errNote, "tempo") {
		t.Fatalf("errNote = %q, want a timeout note", msg.errNote)
	}

	childPID := statuslineTestWaitPID(t, pidFile)
	if childPID == 0 {
		t.Fatal("background child never recorded its pid")
	}
	deadline := time.Now().Add(5 * time.Second)
	for statuslineTestProcessAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if statuslineTestProcessAlive(childPID) {
		t.Fatalf("background child pid %d still alive after statusline timeout", childPID)
	}
}

// TestStatuslineQuitKillsInFlightRunWindows verifies that cancelling an
// in-flight statusline run via quit terminates the entire Job Object tree.
func TestStatuslineQuitKillsInFlightRunWindows(t *testing.T) {
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "quit.pid")
	reapStatuslineTestHelper(t, pidFile)

	scriptPath := filepath.Join(dir, "statusline-quit.cmd")
	script := "@echo off\r\n" +
		"set \"GO_WANT_HELPER_PROCESS=1\"\r\n" +
		"\"" + escapeCmdPercent(exePath) + "\" -test.run=TestStatuslineSleepHelper -- leakdetached \"" + escapeCmdPercent(pidFile) + "\" SL-RUNNING\r\n" +
		"\"" + escapeCmdPercent(exePath) + "\" -test.run=TestStatuslineSleepHelper -- hold 30s\r\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	t.Chdir(dir)

	m := limeTestModel()
	m.statuslineCfg = config.StatusLineConfig{
		Command:   &config.StatusLineCommand{Command: "statusline-quit.cmd"},
		TimeoutMs: 60000,
	}
	m, cmd := m.startStatuslineRun(false)
	if cmd == nil {
		t.Fatal("no run started")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- execCmd(cmd) }()

	childPID := statuslineTestWaitPID(t, pidFile)
	if childPID == 0 {
		t.Fatal("background child never recorded its pid")
	}
	if !statuslineTestProcessAlive(childPID) {
		t.Fatal("precondition: background child is not running")
	}

	_, quitCmd := m.quit()
	if _, ok := execCmd(quitCmd).(tea.QuitMsg); !ok {
		t.Fatal("quit must return tea.Quit")
	}
	select {
	case msg := <-done:
		if _, ok := msg.(statuslineCmdMsg); !ok {
			t.Fatalf("run returned %T, want statuslineCmdMsg", msg)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled run did not return, want a fast job kill")
	}
	deadline := time.Now().Add(5 * time.Second)
	for statuslineTestProcessAlive(childPID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if statuslineTestProcessAlive(childPID) {
		t.Fatalf("background child pid %d still alive after quit", childPID)
	}
}

func escapeCmdPercent(s string) string {
	return strings.ReplaceAll(s, "%", "%%")
}
