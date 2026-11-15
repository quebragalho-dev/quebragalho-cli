package background

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errTestKillDenied = errors.New("test kill denied")

// Commit B (M1): the state x event table, fixed literally. The implementation
// consults legalTaskTransition; this test pins every combination so a
// stray row (e.g. allowing completed -> running) fails loudly.
func TestLegalTaskTransitionCoversAllStateEventPairs(t *testing.T) {
	states := []Status{StatusRunning, StatusCompleted, StatusError, StatusKilled}
	events := []taskEvent{taskEventUpdate, taskEventExit, taskEventKill, taskEventRestore, taskEventSetPID}
	type triple struct {
		from  Status
		event taskEvent
		to    Status
	}
	allowed := map[triple]bool{
		{StatusRunning, taskEventUpdate, StatusCompleted}: true,
		{StatusRunning, taskEventUpdate, StatusError}:     true,
		{StatusRunning, taskEventUpdate, StatusRunning}:   true,
		{StatusRunning, taskEventExit, StatusCompleted}:   true,
		{StatusRunning, taskEventExit, StatusError}:       true,
		{StatusRunning, taskEventKill, StatusKilled}:      true,
		{StatusRunning, taskEventSetPID, StatusRunning}:   true,

		{StatusCompleted, taskEventUpdate, StatusCompleted}: true,
		{StatusCompleted, taskEventExit, StatusCompleted}:   true,
		{StatusCompleted, taskEventExit, StatusError}:       true,
		{StatusError, taskEventUpdate, StatusError}:         true,
		{StatusError, taskEventExit, StatusCompleted}:       true,
		{StatusError, taskEventExit, StatusError}:           true,
		{StatusKilled, taskEventUpdate, StatusKilled}:       true,
		{StatusKilled, taskEventExit, StatusCompleted}:      true,
		{StatusKilled, taskEventExit, StatusError}:          true,
		{StatusKilled, taskEventRestore, StatusRunning}:     true,
	}
	for _, from := range states {
		for _, event := range events {
			for _, to := range states {
				want := allowed[triple{from, event, to}]
				if got := legalTaskTransition(from, event, to); got != want {
					t.Errorf("legalTaskTransition(%s, %s, %s) = %v, want %v",
						from, event, to, got, want)
				}
			}
		}
	}
	for _, state := range states {
		if strings.TrimSpace(state.String()) == "" {
			t.Errorf("Status.String() is empty for %q", string(state))
		}
	}
	for _, event := range events {
		if strings.TrimSpace(event.String()) == "" || event.String() == "unknown" {
			t.Errorf("taskEvent.String() is empty for %d", int(event))
		}
	}
}

// The transition writes the twins together: running always has zero
// CompletedAt and exit 0, killed always carries exit -1, completed/error
// always stamp CompletedAt, and SetPID touches only the PID.
func TestTaskTransitionKeepsTwinsCoherent(t *testing.T) {
	now := sequenceClock(
		time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 7, 9, 0, 1, 0, time.UTC),
		time.Date(2026, 6, 7, 9, 0, 2, 0, time.UTC),
		time.Date(2026, 6, 7, 9, 0, 3, 0, time.UTC),
	)
	manager, err := NewManagerWithOptions(ManagerOptions{
		RootDir:     t.TempDir(),
		Now:         now,
		KillProcess: func(pid int) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewManagerWithOptions error = %v", err)
	}
	if _, err := manager.Register(RegisterInput{TaskID: "t", Type: "specialist", PID: 7}); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if err := manager.SetPID("t", 9); err != nil {
		t.Fatalf("SetPID error = %v", err)
	}
	if task, _ := manager.Get("t"); task.PID != 9 || task.Status != StatusRunning ||
		task.ExitCode != 0 || !task.CompletedAt.IsZero() {
		t.Fatalf("SetPID touched more than the pid: %#v", task)
	}
	if err := manager.Kill("t"); err != nil {
		t.Fatalf("Kill error = %v", err)
	}
	if task, _ := manager.Get("t"); task.Status != StatusKilled || task.ExitCode != -1 ||
		task.CompletedAt.IsZero() || task.PID != 9 {
		t.Fatalf("killed twins incoherent: %#v", task)
	}
}

// The transition fixes the twins even when the event carries junk: kill
// forces exit -1, restore zeroes the exit, update keeps the given exit.
func TestTaskTransitionFixesTwinsAgainstCallerJunk(t *testing.T) {
	manager, err := NewManagerWithOptions(ManagerOptions{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManagerWithOptions error = %v", err)
	}
	if _, err := manager.Register(RegisterInput{TaskID: "t", Type: "specialist", PID: 7}); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	manager.mu.Lock()
	applied, err := manager.transitionLocked("t", taskEventKill, StatusKilled, 99, 7)
	manager.mu.Unlock()
	if err != nil || !applied {
		t.Fatalf("kill transition: applied=%v err=%v", applied, err)
	}
	if task, _ := manager.Get("t"); task.ExitCode != -1 {
		t.Fatalf("kill kept caller exit %d, want -1: %#v", task.ExitCode, task)
	}
	manager.mu.Lock()
	applied, err = manager.transitionLocked("t", taskEventRestore, StatusRunning, 77, 7)
	manager.mu.Unlock()
	if err != nil || !applied {
		t.Fatalf("restore transition: applied=%v err=%v", applied, err)
	}
	if task, _ := manager.Get("t"); task.Status != StatusRunning || task.ExitCode != 0 ||
		!task.CompletedAt.IsZero() || task.PID != 7 {
		t.Fatalf("restore twins incoherent: %#v", task)
	}
}

// A failed Kill restores the running twins exactly: status running, exit 0,
// zero CompletedAt, same pid.
func TestTaskFailedKillRestoresRunningTwins(t *testing.T) {
	manager, err := NewManagerWithOptions(ManagerOptions{
		RootDir:     t.TempDir(),
		KillProcess: func(pid int) error { return errTestKillDenied },
	})
	if err != nil {
		t.Fatalf("NewManagerWithOptions error = %v", err)
	}
	if _, err := manager.Register(RegisterInput{TaskID: "t", Type: "specialist", PID: 7}); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if err := manager.Kill("t"); err == nil {
		t.Fatal("Kill with denied terminate returned nil, want the denial")
	}
	if task, _ := manager.Get("t"); task.Status != StatusRunning || task.ExitCode != 0 ||
		!task.CompletedAt.IsZero() || task.PID != 7 {
		t.Fatalf("failed kill twins incoherent: %#v", task)
	}
}

// Same-terminal UpdateStatus is an idempotent no-op: it keeps the first
func TestTaskUpdateStatusSameTerminalIsIdempotent(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerWithOptions(ManagerOptions{RootDir: root})
	if err != nil {
		t.Fatalf("NewManagerWithOptions error = %v", err)
	}
	if _, err := manager.Register(RegisterInput{TaskID: "t", Type: "specialist", PID: 7}); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if err := manager.UpdateStatus("t", StatusError, -1); err != nil {
		t.Fatalf("UpdateStatus(error) error = %v", err)
	}
	before, _ := manager.Get("t")
	rawBefore, err := os.ReadFile(filepath.Join(root, "t.json"))
	if err != nil {
		t.Fatalf("read persisted task: %v", err)
	}
	if err := manager.UpdateStatus("t", StatusError, 5); err != nil {
		t.Fatalf("idempotent UpdateStatus(error) error = %v", err)
	}
	after, _ := manager.Get("t")
	if after.Status != StatusError || after.ExitCode != before.ExitCode ||
		!after.CompletedAt.Equal(before.CompletedAt) {
		t.Fatalf("same-terminal update rewrote twins: before=%#v after=%#v", before, after)
	}
	rawAfter, err := os.ReadFile(filepath.Join(root, "t.json"))
	if err != nil {
		t.Fatalf("read persisted task: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("idempotent update rewrote JSON:\nbefore %s\nafter %s", rawBefore, rawAfter)
	}
}

// Refused transitions are pure rejections: in-memory state and persisted
// JSON stay byte-for-byte identical.
func TestTaskRefusedTransitionLeavesPersistedJSONUntouched(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerWithOptions(ManagerOptions{
		RootDir:     root,
		KillProcess: func(pid int) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewManagerWithOptions error = %v", err)
	}
	if _, err := manager.Register(RegisterInput{TaskID: "t", Type: "specialist", PID: 7}); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if err := manager.UpdateStatus("t", StatusCompleted, 0); err != nil {
		t.Fatalf("UpdateStatus(completed) error = %v", err)
	}
	rawBefore, err := os.ReadFile(filepath.Join(root, "t.json"))
	if err != nil {
		t.Fatalf("read persisted task: %v", err)
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"update-to-running", func() error { return manager.UpdateStatus("t", StatusRunning, 0) }},
		{"update-to-error", func() error { return manager.UpdateStatus("t", StatusError, -1) }},
		{"setpid", func() error { return manager.SetPID("t", 11) }},
		{"kill", func() error { return manager.Kill("t") }},
	} {
		if err := tc.call(); err == nil {
			t.Fatalf("%s: got nil error, want refusal", tc.name)
		}
	}
	if task, _ := manager.Get("t"); task.Status != StatusCompleted || task.ExitCode != 0 {
		t.Fatalf("refused transition mutated state: %#v", task)
	}
	rawAfter, err := os.ReadFile(filepath.Join(root, "t.json"))
	if err != nil {
		t.Fatalf("read persisted task: %v", err)
	}
	if string(rawAfter) != string(rawBefore) {
		t.Fatalf("refused transition rewrote JSON:\nbefore %s\nafter %s", rawBefore, rawAfter)
	}
}
