//go:build !windows

package tui

import (
	"os/exec"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
)

type statuslineProcessHandle struct {
	cmd *exec.Cmd
}

// statuslineHardenCmd makes one statusline run killable as a single unit, so
// a timeout cannot leak backgrounded children: the child leads its own
// process group and Cancel signals the whole group, mirroring
// tools.hardenProcessLifetime. WaitDelay is the backstop because the pipe and
// its copier belong to os/exec: after Cancel it closes the parent ends after the
// grace period even when an escaped grandchild in its own group still holds
// them open.
func statuslineHardenCmd(cmd *exec.Cmd) *statuslineProcessHandle {
	execution.ConfigureProcessGroup(cmd)
	cmd.WaitDelay = statuslineTreeKillGrace
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Launch-time invariant (ConfigureProcessGroup above), not
		// rediscovery: the leader may already have exited while a
		// grandchild holds the pipes, and rediscovery then misses the
		// group it configured.
		return execution.KillProcessGroup(cmd.Process.Pid)
	}
	return &statuslineProcessHandle{cmd: cmd}
}

func (h *statuslineProcessHandle) afterStart() {}

func (h *statuslineProcessHandle) killOnFailure() {
	if h.cmd.Process != nil {
		_ = execution.KillProcessGroup(h.cmd.Process.Pid)
	}
}

func (h *statuslineProcessHandle) close() {}
