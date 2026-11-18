//go:build windows

package config

import (
	"os/exec"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
)

// configureCommandProcess starts the process suspended so it can be
// assigned to the job object before its main thread (and therefore any
// code it runs) executes.
func configureCommandProcess(cmd *exec.Cmd) {
	execution.PrepareWindowsSuspendedProcess(cmd)
}

func startCommandProcess(cmd *exec.Cmd) (*commandProcess, error) {
	proc, err := execution.StartWindowsProcessJob(cmd, execution.WindowsJobOptions{})
	if err != nil {
		return nil, err
	}
	return &commandProcess{job: proc}, nil
}

// commandProcess tracks a started provider command so its entire process
// tree can be terminated on timeout.
type commandProcess struct {
	job *execution.WindowsProcessJob
}

func attachCommandProcess(cmd *exec.Cmd) *commandProcess {
	proc := execution.NewWindowsProcessJob(cmd)
	proc.Attach(execution.WindowsJobOptions{})
	return &commandProcess{job: proc}
}

func resumeMainThread(pid int) bool {
	return execution.ResumeProcessThreads(pid)
}

func (p *commandProcess) Terminate() {
	if p == nil || p.job == nil {
		return
	}
	_ = p.job.Terminate()
}

// Close releases the retained handles without touching any still-running
// descendants: the job carries no KILL_ON_JOB_CLOSE limit, so on the success
// path a provider command's detached helpers keep running exactly as they did
// before job objects were introduced. Descendant termination happens
// explicitly via Terminate, called only on timeout/error.
func (p *commandProcess) Close() {
	if p == nil || p.job == nil {
		return
	}
	p.job.Close()
}
