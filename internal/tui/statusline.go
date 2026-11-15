package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// Statusline field ids a custom layout may reference. Stable and English;
// the display stays Portuguese. "cache" reuses the default statusline cache
// segment ("cache —" before the first call, "cache 75%" after). The external
// command is not a field: its output appends as its own footer lines (see
// statusLineCustom).
var statuslineFieldIDs = []string{
	"mode", "effort", "fast", "voice", "background", "goal",
	"context", "branch", "update", "cwd", "cache",
	"model", "speed", "cost", "time",
}

// statuslineFieldKnown reports whether id names a registered field.
func statuslineFieldKnown(id string) bool {
	for _, known := range statuslineFieldIDs {
		if id == known {
			return true
		}
	}
	return false
}

// unknownStatusLineFields lists every unregistered id in a custom layout, in
// order, so /statusline surfaces one actionable warning.
func unknownStatusLineFields(cfg config.StatusLineConfig) []string {
	var unknown []string
	for _, line := range cfg.Lines {
		for _, id := range append(append([]string{}, line.Left...), line.Right...) {
			if !statuslineFieldKnown(id) {
				unknown = append(unknown, id)
			}
		}
	}
	return unknown
}

// statuslineSeparator is the default join between segments, matching the
// built-in line's separator ink.
func (m model) statuslineSeparator() string {
	if sep := m.statuslineCfg.Separator; sep != "" {
		return qgTheme.line.Render(sep)
	}
	return qgTheme.line.Render(" │ ")
}

// statusLineCustom renders a user/project-configured layout: up to three
// lines, each with left ids and right ids joined by the separator, followed
// by the external command's cached output filling the remaining footer rows.
// With a command holding output or an error, the script keeps one guaranteed
// line and the fields keep the rest, dropping the last field lines first.
// It never runs on the default path, so the built-in line stays byte-identical.
func (m model) statusLineCustom(width int) string {
	cfg := m.statuslineCfg
	lines := cfg.Lines
	hasScript := m.statuslineCommandActive() &&
		(strings.TrimSpace(m.statuslineCmdOutput) != "" || strings.TrimSpace(m.statuslineCmdErr) != "")
	maxFields := maxStatusLineRenderLines
	if hasScript {
		maxFields = maxStatusLineRenderLines - 1
	}
	if len(lines) > maxFields {
		lines = lines[:maxFields]
	}
	sep := m.statuslineSeparator()
	var out []string
	for _, line := range lines {
		if len(out) >= maxStatusLineRenderLines {
			break
		}
		left := "  " + joinStatuslineSegments(m, line.Left, sep, width)
		right := joinStatuslineSegments(m, line.Right, sep, width)
		out = append(out, fitStyledLine(joinHeaderLine(left, right, width), width))
	}
	for _, cmdLine := range strings.Split(m.statuslineCmdOutput, "\n") {
		if len(out) >= maxStatusLineRenderLines {
			break
		}
		if strings.TrimSpace(cmdLine) == "" {
			continue
		}
		out = append(out, fitStyledLine("  "+cmdLine, width))
	}
	if m.statuslineCmdErr != "" && strings.TrimSpace(m.statuslineCmdOutput) == "" && len(out) < maxStatusLineRenderLines {
		out = append(out, fitStyledLine("  "+qgTheme.faint.Render(m.statuslineCmdErr), width))
	}
	// While an exit/cancel confirmation is armed, the first custom line shows
	// the same hint the built-in line shows, keeping the footer height stable.
	// When the confirmation expires the user layout returns untouched.
	if hint, ok := m.customConfirmHintLine(width); ok {
		if len(out) == 0 {
			return hint
		}
		out[0] = hint
	}
	return strings.Join(out, "\n")
}

// customConfirmHintLine renders the armed exit/cancel hint with the same
// text and styling the built-in status line uses, so a custom layout never
// hides it. False when no confirmation is armed.
func (m model) customConfirmHintLine(width int) (string, bool) {
	prefix := "  "
	btwChip := ""
	if m.btw.active {
		btwChip = qgTheme.amber.Render("BTW") + qgTheme.muted.Render(" · ")
	}
	switch {
	case m.exitConfirmActive:
		return fitStyledLine(prefix+btwChip+qgTheme.amber.Render("●")+" "+qgTheme.amber.Render(ctrlCExitConfirmText), width), true
	case m.cancelConfirmActive:
		return fitStyledLine(prefix+btwChip+qgTheme.amber.Render("●")+" "+qgTheme.amber.Render(escCancelConfirmText), width), true
	default:
		return "", false
	}
}

// maxStatusLineRenderLines caps the custom render defensively; config
// validation already rejects more.
const maxStatusLineRenderLines = 3

// joinStatuslineSegments renders the known ids and joins the non-empty ones.
func joinStatuslineSegments(m model, ids []string, sep string, width int) string {
	var parts []string
	for _, id := range ids {
		if segment := m.statuslineSegment(id, width); strings.TrimSpace(segment) != "" {
			parts = append(parts, segment)
		}
	}
	return strings.Join(parts, sep)
}

// statuslineSegment renders one registered field, "" when it has nothing to
// show at this width. Each case reuses the built-in builders so custom and
// default agree on content.
func (m model) statuslineSegment(id string, width int) string {
	switch id {
	case "mode":
		modeText, modeStyle := m.modeLabel()
		chip := qgTheme.accent.Render("●") + " " + modeStyle.Render(modeText)
		if m.btw.active {
			chip = qgTheme.amber.Render("BTW") + qgTheme.muted.Render(" · ") + chip
		}
		return chip
	case "effort":
		return qgTheme.accent.Render(m.statusEffort())
	case "fast":
		if m.activeServiceTier() == "priority" {
			return qgTheme.accent.Render("rápido")
		}
		return ""
	case "voice":
		// An active recording takes over, like the built-in line: the mic is
		// the most time-sensitive thing on screen.
		if chip := m.dictationStatusChip(); chip != "" && m.dictation.active() {
			return chip
		}
		return m.voiceModeIndicator()
	case "background":
		return m.backgroundTerminalSummary()
	case "goal":
		if summary := m.goalFooterSummary(); summary != "" {
			return qgTheme.accent.Render("◎ ") + qgTheme.muted.Render(summary)
		}
		return ""
	case "context":
		return m.contextStatusText(width)
	case "branch":
		if branch := strings.TrimSpace(m.gitBranch); branch != "" {
			return qgTheme.muted.Render(truncateDisplayWidth(branch, 24))
		}
		return ""
	case "update":
		return m.updateNoticeSegment(width)
	case "cwd":
		if cwd := strings.TrimSpace(m.cwd); cwd != "" {
			return qgTheme.faint.Render(truncateDisplayWidth(shortenPath(cwd), maxInt(8, width/3)))
		}
		return ""
	case "cache":
		// The session prompt-cache hit rate, reusing the default statusline
		// cache segment: "cache —" before the first call, "cache 75%" after.
		seg, _ := cacheStatusSegment(m.cacheStatusData())
		return seg
	case "model":
		if name := strings.TrimSpace(m.modelName); name != "" {
			return qgTheme.accent.Render(truncateDisplayWidth(name, 48))
		}
		return ""
	case "speed":
		if rate, live := m.rateText(); rate != "" {
			if live {
				number, _, _ := strings.Cut(rate, " ")
				return qgTheme.accent.Render("●") + " " + qgTheme.accent.Render(number) + qgTheme.faint.Render(" tok/s")
			}
			return qgTheme.faint.Render(rate)
		}
		return ""
	case "cost":
		if total, ok := m.sessionCostBRLRat(); ok {
			return qgTheme.muted.Render(formatBRLRat(total))
		}
		return ""
	case "time":
		if elapsed := m.sessionElapsed(); elapsed != "" {
			return qgTheme.muted.Render(elapsed)
		}
		return ""
	default:
		return ""
	}
}

// rateText is the shared core behind the composer speed readout and the
// custom "speed" field: the exact provider rate while emitting, the live
// estimate, else the last completed rate. "" before the first response.
func (m model) rateText() (string, bool) {
	if m.pending && m.streamLive && m.pendingPermission == nil {
		if exact := usageRate(m.streamUsage); exact != "" {
			return exact, true
		}
		if est := m.liveEstimateRate(); est != "" {
			return est, true
		}
		return "", true
	}
	return strings.TrimSpace(m.lastCompletedRate), false
}

// sessionElapsed renders the session wall time ("45s", "3m 4s", "2h 5m") from
// the session creation stamp. "" when the stamp is missing or unparsable.
func (m model) sessionElapsed() string {
	created := strings.TrimSpace(m.activeSession.CreatedAt)
	if created == "" {
		return ""
	}
	start, err := time.Parse(time.RFC3339, created)
	if err != nil {
		return ""
	}
	elapsed := m.now().Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}
	seconds := int(elapsed.Seconds())
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	switch {
	case hours > 0:
		return strconv.Itoa(hours) + "h " + strconv.Itoa(minutes) + "m"
	case minutes > 0:
		return strconv.Itoa(minutes) + "m " + strconv.Itoa(secs) + "s"
	default:
		return strconv.Itoa(secs) + "s"
	}
}
