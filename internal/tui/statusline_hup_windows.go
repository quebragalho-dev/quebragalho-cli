//go:build windows

package tui

import (
	tea "charm.land/bubbletea/v2"
)

// watchStatuslineHangup is a no-op on Windows: there is no SIGHUP.
func watchStatuslineHangup(_ *tea.Program, _ <-chan struct{}) {}
