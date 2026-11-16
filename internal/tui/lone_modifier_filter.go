package tui

import (
	tea "charm.land/bubbletea/v2"
)

// programEventFilter is the single event filter of the main TUI program.
func programEventFilter(goos string) func(tea.Model, tea.Msg) tea.Msg {
	mouse := mouseEventFilter()
	return func(current tea.Model, msg tea.Msg) tea.Msg {
		return mouse(current, msg)
	}
}
