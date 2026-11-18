//go:build windows

package tui

import (
	"os/exec"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
)

type statuslineProcessHandle struct {
	job *execution.WindowsProcessJob
}

// statuslineHardenCmd prepares a Windows statusline run to start suspended so
// it can be assigned to a Job Object (with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
// before executing any code. Cancel and close terminate the job (falling back
// to taskkill /T via the retained process handle if job setup failed) so no
// backgrounded descendant survives exit, timeout, or cancellation.
func statuslineHardenCmd(cmd *exec.Cmd) *statuslineProcessHandle {
	execution.PrepareWindowsSuspendedProcess(cmd)
	job := execution.NewWindowsProcessJob(cmd)
	cmd.WaitDelay = statuslineTreeKillGrace
	cmd.Cancel = func() error {
		return job.Terminate()
	}
	return &statuslineProcessHandle{job: job}
}

func (h *statuslineProcessHandle) afterStart() {
	h.job.Attach(execution.WindowsJobOptions{KillOnJobClose: true})
}

func (h *statuslineProcessHandle) killOnFailure() {
	_ = h.job.Terminate()
}

func (h *statuslineProcessHandle) close() {
	_ = h.job.Terminate()
	h.job.Close()
}
