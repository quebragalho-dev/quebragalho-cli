//go:build !windows

package tui

import (
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// The hangup watcher must not touch an ignored SIGHUP (nohup): installing
// signal.Notify would undo the ignore, so the ignore surviving the watch
// proves the guard. Repeatable under any starting disposition: ignore,
// watch, require the ignore intact, then reset so later tests' children
// inherit the default.
func TestSighupWatcherStaysOutWhenIgnored(t *testing.T) {
	signal.Ignore(syscall.SIGHUP)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		watchStatuslineHangup(nil, stop)
	}()
	defer func() {
		close(stop)
		<-done
		signal.Reset(syscall.SIGHUP)
	}()
	time.Sleep(100 * time.Millisecond)
	if !signal.Ignored(syscall.SIGHUP) {
		t.Fatal("watcher installed a SIGHUP handler over an ignored SIGHUP")
	}
}
