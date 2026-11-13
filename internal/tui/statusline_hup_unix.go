//go:build !windows

package tui

import (
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

// watchStatuslineHangup routes SIGHUP (a closed terminal tab) through the
// same orderly shutdown as SIGTERM: bubbletea only listens for SIGINT and
// SIGTERM, so without this the process would die at once and orphan a
// statusline run in flight. program.Quit is a no-op once Run returned.
func watchStatuslineHangup(program *tea.Program, stop <-chan struct{}) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	select {
	case <-hup:
		program.Quit()
	case <-stop:
	}
}
