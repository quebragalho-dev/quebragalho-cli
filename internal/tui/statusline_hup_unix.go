//go:build !windows

package tui

import (
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
)

// sighupShouldWatch reports whether SIGHUP arrives as a signal. Under nohup
// (or anything else that starts qg with SIGHUP ignored) the handler stays
// out: qg used to ignore the hangup, and quitting there would newly kill a
// session the user detached on purpose.
func sighupShouldWatch() bool {
	return !signal.Ignored(syscall.SIGHUP)
}

// watchStatuslineHangup routes SIGHUP (a closed terminal tab) through the
// same orderly shutdown as SIGTERM: bubbletea only listens for SIGINT and
// SIGTERM, so without this the process would die at once and orphan a
// statusline run in flight. program.Quit is a no-op once Run has returned.
func watchStatuslineHangup(program *tea.Program, stop <-chan struct{}) {
	if !sighupShouldWatch() {
		return
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	select {
	case <-hup:
		program.Quit()
	case <-stop:
	}
}
