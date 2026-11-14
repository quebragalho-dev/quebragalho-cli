//go:build windows

package tui

import (
	tea "charm.land/bubbletea/v2"
)

// sighupShouldWatch is always false on Windows: there is no SIGHUP.
func sighupShouldWatch() bool {
	return false
}

// watchStatuslineHangup is a no-op on Windows: there is no SIGHUP.
func watchStatuslineHangup(_ *tea.Program, _ <-chan struct{}) {}
