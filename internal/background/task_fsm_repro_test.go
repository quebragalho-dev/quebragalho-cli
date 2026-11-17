package background

import (
	"testing"
)

// Commit A (M1): reproduces terminal resurrection via UpdateStatus and the
// incoherent twins (killed with exit 0). FAILS on untouched dev; passes once
// the explicit state machine lands.
func TestTaskUpdateStatusRefusesTerminalResurrection(t *testing.T) {
	reach := []struct {
		name     string
		terminal Status
		setup    func(t *testing.T, manager *Manager)
	}{
		{"completed", StatusCompleted, func(t *testing.T, manager *Manager) {
			if err := manager.UpdateStatus("t", StatusCompleted, 0); err != nil {
				t.Fatalf("setup UpdateStatus(completed) error = %v", err)
			}
		}},
		{"error", StatusError, func(t *testing.T, manager *Manager) {
			if err := manager.UpdateStatus("t", StatusError, -1); err != nil {
				t.Fatalf("setup UpdateStatus(error) error = %v", err)
			}
		}},
		{"killed", StatusKilled, func(t *testing.T, manager *Manager) {
			if err := manager.Kill("t"); err != nil {
				t.Fatalf("setup Kill error = %v", err)
			}
		}},
	}
	for _, tc := range reach {
		t.Run(tc.name, func(t *testing.T) {
			manager, err := NewManagerWithOptions(ManagerOptions{
				RootDir:     t.TempDir(),
				KillProcess: func(pid int) error { return nil },
			})
			if err != nil {
				t.Fatalf("NewManagerWithOptions error = %v", err)
			}
			if _, err := manager.Register(RegisterInput{TaskID: "t", Type: "specialist", PID: 7}); err != nil {
				t.Fatalf("Register error = %v", err)
			}
			tc.setup(t, manager)
			if err := manager.UpdateStatus("t", StatusRunning, 0); err == nil {
				t.Fatalf("%s -> running via UpdateStatus: got nil error, want refusal", tc.terminal)
			}
			task, ok := manager.Get("t")
			if !ok || task.Status != tc.terminal {
				t.Fatalf("terminal state not preserved: %#v", task)
			}
			if task.CompletedAt.IsZero() {
				t.Fatalf("terminal task lost its CompletedAt: %#v", task)
			}
		})
	}
}

// Running tasks must never become killed through UpdateStatus: only Kill
// signals the process, and a killed mark with exit 0 is incoherent.
func TestTaskUpdateStatusRunningToKilledRefused(t *testing.T) {
	manager, err := NewManagerWithOptions(ManagerOptions{RootDir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewManagerWithOptions error = %v", err)
	}
	if _, err := manager.Register(RegisterInput{TaskID: "t", Type: "specialist", PID: 7}); err != nil {
		t.Fatalf("Register error = %v", err)
	}
	if err := manager.UpdateStatus("t", StatusKilled, 0); err == nil {
		t.Fatal("running -> killed via UpdateStatus: got nil error, want refusal (killed comes only from Kill with exit -1)")
	}
	task, ok := manager.Get("t")
	if !ok || task.Status != StatusRunning {
		t.Fatalf("running state not preserved: %#v", task)
	}
}
