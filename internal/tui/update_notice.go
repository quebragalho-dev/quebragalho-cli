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

// updateNoticeMinPath is the narrowest the workspace path shrinks to make
// room for the notice. Below that the path drops instead.
const updateNoticeMinPath = 12

// updateNoticeForms lists the footer notice from longest to shortest. The
// version always renders whole; the tail is muted and the head is amber.
func (m model) updateNoticeForms() []string {
	version := strings.TrimSpace(m.updateAvailable)
	if version == "" {
		return nil
	}
	return []string{
		qgTheme.amber.Render("nova versão "+version) + qgTheme.muted.Render(" · reinicie para instalar"),
		qgTheme.amber.Render("nova "+version) + qgTheme.muted.Render(" · reinicie"),
		qgTheme.amber.Render("nova " + version),
	}
}

// updateNoticeSegment renders the quiet footer notice for the given space:
// the first whole form wins, else nothing. The version is never cut. Mode,
// effort, context, rate, and branch never shrink; the workspace path yields
// separately in joinStatusWithNotice.
func (m model) updateNoticeSegment(remaining int) string {
	for _, form := range m.updateNoticeForms() {
		if lipgloss.Width(form) <= remaining {
			return form
		}
	}
	return ""
}

// statusPathSegment renders the workspace path truncated to take cells,
// exactly like the notice-free footer does.
func statusPathSegment(shown string, take int) string {
	return qgTheme.line.Render(" │ ") + qgTheme.faint.Render(truncateDisplayWidth(shown, take))
}

// joinStatusWithNotice shares the width left over by mode, effort, context,
// rate, and branch between the workspace path and the update notice, which
// yield in order: the path shrinks to updateNoticeMinPath cells, the notice
// steps down through its forms, the path drops with its separator, and only
// then the notice drops. Blocks stay two cells apart, and the join below
// always has room for its own padding, so the version is never cut. It
// reports false when no notice fits even without the path: the caller then
// renders the notice-free footer, which keeps the path.
func (m model) joinStatusWithNotice(left, right string, forms []string, width int) (string, bool) {
	avail := width - 2 - lipgloss.Width(left) - lipgloss.Width(right)
	styledRight := ""
	sep := ""
	if right != "" {
		styledRight = qgTheme.faint.Render(right)
		sep = "  "
	}
	sepWidth := lipgloss.Width(sep)
	if path := strings.TrimSpace(m.cwd); path != "" {
		shown := shortenPath(path)
		natural := lipgloss.Width(shown)
		floor := minInt(natural, updateNoticeMinPath)
		// The path yields first while the notice stays whole.
		if take := minInt(natural, avail-3-sepWidth-lipgloss.Width(forms[0])); take >= floor {
			return joinHeaderLine(left+statusPathSegment(shown, take), styledRight+sep+forms[0], width), true
		}
		// Then the notice steps down with the path at its floor.
		for _, form := range forms[1:] {
			if floor+sepWidth+lipgloss.Width(form) <= avail-3 {
				return joinHeaderLine(left+statusPathSegment(shown, floor), styledRight+sep+form, width), true
			}
		}
	}
	// Then the path drops and the notice takes the space alone, longest
	// form first — but only when that actually fits a notice.
	if form := m.updateNoticeSegment(avail - sepWidth); form != "" {
		if styledRight != "" {
			styledRight += sep
		}
		return joinHeaderLine(left, styledRight+form, width), true
	}
	return "", false
}
