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

// The phase x event x status table, fixed literally. The implementation
// consults legalTaskTransition; terminal phases have no outgoing edges to
// any other state.
func TestLegalTaskTransitionCoversAllStateEventPairs(t *testing.T) {
	phases := []taskPhase{phaseRunning, phaseKilling, phaseCompleted, phaseError, phaseKilled}
	events := []taskEvent{taskEventUpdate, taskEventExit, taskEventBeginKill, taskEventCommitKill, taskEventAbortKill, taskEventSetPID}
	statuses := []Status{StatusRunning, StatusCompleted, StatusError, StatusKilled}
	type triple struct {
		from  taskPhase
		event taskEvent
		to    Status
	}
	allowed := map[triple]bool{
		{phaseRunning, taskEventUpdate, StatusCompleted}:    true,
		{phaseRunning, taskEventUpdate, StatusError}:        true,
		{phaseRunning, taskEventUpdate, StatusRunning}:      true,
		{phaseRunning, taskEventExit, StatusCompleted}:      true,
		{phaseRunning, taskEventExit, StatusError}:          true,
		{phaseRunning, taskEventBeginKill, StatusRunning}:   true,
		{phaseRunning, taskEventSetPID, StatusRunning}:      true,
		{phaseKilling, taskEventUpdate, StatusCompleted}:    true,
		{phaseKilling, taskEventUpdate, StatusError}:        true,
		{phaseKilling, taskEventUpdate, StatusRunning}:      true,
		{phaseKilling, taskEventExit, StatusCompleted}:      true,
		{phaseKilling, taskEventExit, StatusError}:          true,
		{phaseKilling, taskEventCommitKill, StatusKilled}:   true,
		{phaseKilling, taskEventAbortKill, StatusRunning}:   true,
		{phaseKilling, taskEventAbortKill, StatusCompleted}: true,
		{phaseKilling, taskEventAbortKill, StatusError}:     true,

		{phaseCompleted, taskEventUpdate, StatusCompleted}: true,
		{phaseCompleted, taskEventExit, StatusCompleted}:   true,
		{phaseCompleted, taskEventExit, StatusError}:       true,
		{phaseError, taskEventUpdate, StatusError}:         true,
		{phaseError, taskEventExit, StatusCompleted}:       true,
		{phaseError, taskEventExit, StatusError}:           true,
		{phaseKilled, taskEventUpdate, StatusKilled}:       true,
		{phaseKilled, taskEventExit, StatusCompleted}:      true,
		{phaseKilled, taskEventExit, StatusError}:          true,
	}
	for _, from := range phases {
		for _, event := range events {
			for _, to := range statuses {
				want := allowed[triple{from, event, to}]
				if got := legalTaskTransition(from, event, to); got != want {
					t.Errorf("legalTaskTransition(%s, %s, %s) = %v, want %v",
						from, event, to, got, want)
				}
			}
		}
	}
	for _, phase := range phases {
		if strings.TrimSpace(phase.String()) == "" || phase.String() == "unknown" {
			t.Errorf("taskPhase.String() is invalid for %d", int(phase))
		}
	}
	for _, state := range statuses {
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

// The transition fixes the twins even when the event carries junk: commitKill
// forces exit -1, abortKill without a pending exit zeroes the exit, and
// terminal states cannot be reverted back to running.
func TestTaskTransitionFixesTwinsAgainstCallerJunk(t *testing.T) {
	manager, err := NewManagerWithOptions(ManagerOptions{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManagerWithOptions error = %v", err)
	}
	if _, err := manager.Register(RegisterInput{TaskID: "t", Type: "specialist", PID: 7}); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	manager.mu.Lock()
	applied, err := manager.transitionLocked("t", taskEventBeginKill, StatusRunning, 99, 7)
	manager.mu.Unlock()
	if err != nil || !applied {
		t.Fatalf("beginKill transition: applied=%v err=%v", applied, err)
	}
	manager.mu.Lock()
	applied, err = manager.transitionLocked("t", taskEventAbortKill, StatusRunning, 77, 0)
	manager.mu.Unlock()
	if err != nil || !applied {
		t.Fatalf("abortKill transition: applied=%v err=%v", applied, err)
	}
	if task, _ := manager.Get("t"); task.Status != StatusRunning || task.ExitCode != 0 ||
		!task.CompletedAt.IsZero() || task.PID != 7 {
		t.Fatalf("abortKill twins incoherent: %#v", task)
	}

	manager.mu.Lock()
	_, err = manager.transitionLocked("t", taskEventBeginKill, StatusRunning, 0, 7)
	if err == nil {
		applied, err = manager.transitionLocked("t", taskEventCommitKill, StatusKilled, 99, 0)
	}
	manager.mu.Unlock()
	if err != nil || !applied {
		t.Fatalf("commitKill transition: applied=%v err=%v", applied, err)
	}
	if task, _ := manager.Get("t"); task.Status != StatusKilled || task.ExitCode != -1 {
		t.Fatalf("commitKill kept caller exit %d, want -1: %#v", task.ExitCode, task)
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

// abortKill on any terminal task is a silent no-op that leaves the terminal
// state untouched.
func TestTaskAbortKillOffKillingIsSilentNoOp(t *testing.T) {
	manager, err := NewManagerWithOptions(ManagerOptions{
		RootDir:     t.TempDir(),
		KillProcess: func(int) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewManagerWithOptions error = %v", err)
	}
	for _, terminal := range []Status{StatusCompleted, StatusError, StatusKilled} {
		id := "t-" + string(terminal)
		if _, err := manager.Register(RegisterInput{TaskID: id, Type: "specialist", PID: 7}); err != nil {
			t.Fatalf("Register error = %v", err)
		}
		switch terminal {
		case StatusCompleted:
			if err := manager.UpdateStatus(id, StatusCompleted, 0); err != nil {
				t.Fatalf("UpdateStatus error = %v", err)
			}
		case StatusError:
			if err := manager.UpdateStatus(id, StatusError, -1); err != nil {
				t.Fatalf("UpdateStatus error = %v", err)
			}
		case StatusKilled:
			if err := manager.Kill(id); err != nil {
				t.Fatalf("Kill error = %v", err)
			}
		}
		before, _ := manager.Get(id)
		settled, err := manager.abortKill(id)
		if err != nil || settled != terminal {
			t.Fatalf("abortKill on %s: settled=%q err=%v, want %q/nil", terminal, settled, err, terminal)
		}
		after, _ := manager.Get(id)
		if after != before {
			t.Fatalf("abortKill on %s mutated state: before=%#v after=%#v", terminal, before, after)
		}
	}
}

// Kill with a failing terminate keeps running when the process is still alive,
// and settles on the recorded exit when the process exited naturally meanwhile.
func TestTaskKillFailedRestoreParity(t *testing.T) {
	newDeniedManager := func(t *testing.T, mutate func(*Manager)) *Manager {
		t.Helper()
		var manager *Manager
		var err error
		manager, err = NewManagerWithOptions(ManagerOptions{
			RootDir: t.TempDir(),
			KillProcess: func(pid int) error {
				if mutate != nil {
					mutate(manager)
				}
				return errTestKillDenied
			},
		})
		if err != nil {
			t.Fatalf("NewManagerWithOptions error = %v", err)
		}
		if _, err := manager.Register(RegisterInput{TaskID: "t", Type: "specialist", PID: 7}); err != nil {
			t.Fatalf("Register error = %v", err)
		}
		return manager
	}

	t.Run("still-alive-stays-running", func(t *testing.T) {
		manager := newDeniedManager(t, nil)
		err := manager.Kill("t")
		if err == nil || !strings.Contains(err.Error(), "test kill denied") {
			t.Fatalf("Kill error = %v, want the denial", err)
		}
		if task, _ := manager.Get("t"); task.Status != StatusRunning {
			t.Fatalf("failed kill did not keep running: %#v", task)
		}
	})

	t.Run("exited-meanwhile-settles-on-exit", func(t *testing.T) {
		manager := newDeniedManager(t, func(manager *Manager) {
			_ = manager.MarkExited("t", StatusCompleted, 0)
		})
		err := manager.Kill("t")
		if err == nil || !strings.Contains(err.Error(), "is completed") {
			t.Fatalf("Kill error = %v, want already-completed error", err)
		}
		if task, _ := manager.Get("t"); task.Status != StatusCompleted || task.ExitCode != 0 {
			t.Fatalf("exited task did not settle on completed: %#v", task)
		}
	})
}

// SetPID on a terminal task is a silent no-op (nil) that stores nothing:
// the launch-result parity the instant-exit race depends on, with the
// invariant (never store a pid on a terminal task) intact. The table still
// calls it illegal; only the public method maps it to nil.
func TestTaskSetPIDOnTerminalIsSilentNoOp(t *testing.T) {
	root := t.TempDir()
	manager, err := NewManagerWithOptions(ManagerOptions{
		RootDir:     root,
		KillProcess: func(pid int) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewManagerWithOptions error = %v", err)
	}
	for _, terminal := range []Status{StatusCompleted, StatusError, StatusKilled} {
		id := "t-" + string(terminal)
		pid := 0
		if terminal == StatusKilled {
			pid = 7
		}
		if _, err := manager.Register(RegisterInput{TaskID: id, Type: "specialist", PID: pid}); err != nil {
			t.Fatalf("Register error = %v", err)
		}
		switch terminal {
		case StatusCompleted:
			if err := manager.UpdateStatus(id, StatusCompleted, 0); err != nil {
				t.Fatalf("UpdateStatus error = %v", err)
			}
		case StatusError:
			if err := manager.UpdateStatus(id, StatusError, -1); err != nil {
				t.Fatalf("UpdateStatus error = %v", err)
			}
		case StatusKilled:
			if err := manager.Kill(id); err != nil {
				t.Fatalf("Kill error = %v", err)
			}
		}
		rawBefore, err := os.ReadFile(filepath.Join(root, id+".json"))
		if err != nil {
			t.Fatalf("read persisted task: %v", err)
		}
		if err := manager.SetPID(id, 4321); err != nil {
			t.Fatalf("SetPID on %s: got error %v, want silent nil", terminal, err)
		}
		if task, _ := manager.Get(id); task.PID != pid {
			t.Fatalf("SetPID on %s stored pid %d, want record untouched: %#v", terminal, task.PID, task)
		}
		rawAfter, err := os.ReadFile(filepath.Join(root, id+".json"))
		if err != nil {
			t.Fatalf("read persisted task: %v", err)
		}
		if string(rawAfter) != string(rawBefore) {
			t.Fatalf("SetPID on %s rewrote JSON:\nbefore %s\nafter %s", terminal, rawBefore, rawAfter)
		}
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
