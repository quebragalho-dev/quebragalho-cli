//go:build !windows

package tui

import (
	"os/signal"
	"syscall"
	"testing"
)

// The hangup watcher stays out when qg starts with SIGHUP ignored (nohup):
// installing a Quit there would newly kill a session the user detached on
// purpose.
func TestSighupWatcherStaysOutWhenIgnored(t *testing.T) {
	if !sighupShouldWatch() {
		t.Fatal("SIGHUP is already ignored in this test process")
	}
	signal.Ignore(syscall.SIGHUP)
	defer signal.Reset(syscall.SIGHUP)
	if sighupShouldWatch() {
		t.Fatal("watcher must stay out when SIGHUP starts ignored")
	}
}
