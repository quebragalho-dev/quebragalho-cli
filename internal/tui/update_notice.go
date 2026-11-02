package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/updatenotice"
)

// updateCheckInterval is how often the footer re-checks for a new release
// after the startup check.
const updateCheckInterval = time.Hour

// updateCheckMsg carries the stored state after a footer check. The handler
// derives the shown version from it and schedules the next check.
type updateCheckMsg struct {
	state updatenotice.State
}

// updateCheckTickMsg fires when the hourly re-check is due.
type updateCheckTickMsg struct{}

// updateCheckEnabled reports whether the footer check runs: it needs a config
// and must survive the same kill-switch as the startup screen.
func (m model) updateCheckEnabled() bool {
	return m.updateNotice != nil && updatenotice.Enabled(*m.updateNotice)
}

// updateCheckInitCmd issues the startup footer check, or nil when disabled so
// Init schedules nothing.
func (m model) updateCheckInitCmd() tea.Cmd {
	if !m.updateCheckEnabled() {
		return nil
	}
	return m.updateCheckCmd()
}

// updateCheckCmd loads the state, refreshes when due, and reports the fresh
// state. The network runs outside Update; the file merge goes through
// updatenotice.Update, never a stale-copy save.
func (m model) updateCheckCmd() tea.Cmd {
	cfg := *m.updateNotice
	stateFile := m.updateNoticeState
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		return updateCheckMsg{state: refreshUpdateNotice(ctx, cfg, stateFile)}
	}
}

// refreshUpdateNotice folds one check into the state file and returns the
// fresh state. A fresh state short-circuits before any network.
func refreshUpdateNotice(ctx context.Context, cfg updatenotice.Config, stateFile string) updatenotice.State {
	state := updatenotice.Load(stateFile)
	if !updatenotice.Due(cfg, state) {
		return state
	}
	refreshed := updatenotice.Refresh(ctx, cfg, updatenotice.State{})
	_ = updatenotice.RecordCheck(stateFile, refreshed)
	return updatenotice.Load(stateFile)
}

// scheduleUpdateCheck queues the next hourly re-check.
func scheduleUpdateCheck() tea.Cmd {
	return tea.Tick(updateCheckInterval, func(time.Time) tea.Msg {
		return updateCheckTickMsg{}
	})
}

// updateNoticeSegment renders the quiet footer notice for the space left
// after the existing states: the first whole form wins, else nothing. The
// version is never cut, and existing states never shrink to make room.
func (m model) updateNoticeSegment(remaining int) string {
	version := strings.TrimSpace(m.updateAvailable)
	if version == "" {
		return ""
	}
	if full := qgTheme.amber.Render("nova versão "+version) +
		qgTheme.muted.Render(" · reinicie para instalar"); lipgloss.Width(full) <= remaining {
		return full
	}
	if mid := qgTheme.amber.Render("nova "+version) +
		qgTheme.muted.Render(" · reinicie"); lipgloss.Width(mid) <= remaining {
		return mid
	}
	if tiny := qgTheme.amber.Render("nova " + version); lipgloss.Width(tiny) <= remaining {
		return tiny
	}
	return ""
}
