package tui

import (
	"math"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

// formatContextTokens renders token counts for the context meter: 850,
// 1k, 300k, 483.9k, 1m, 1.5m. One decimal, ".0" dropped, k promotes to m
// at 1000 (never "1000k").
func formatContextTokens(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	if n < 1_000_000 {
		if tenths := (n*10 + 500) / 1000; tenths >= 10000 {
			return "1m"
		} else {
			return formatContextTenths(tenths, "k")
		}
	}
	return formatContextTenths((n*10+500000)/1_000_000, "m")
}

func formatContextTenths(tenths int, suffix string) string {
	if tenths%10 == 0 {
		return strconv.Itoa(tenths/10) + suffix
	}
	return strconv.Itoa(tenths/10) + "." + strconv.Itoa(tenths%10) + suffix
}

// contextLevel is the meter's color band: accent below 60%, amber at
// 60-84%, red at 85% and up.
type contextLevel int

const (
	contextLevelLow contextLevel = iota
	contextLevelMid
	contextLevelHigh
)

func contextFillLevel(pct int) contextLevel {
	switch {
	case pct >= 85:
		return contextLevelHigh
	case pct >= 60:
		return contextLevelMid
	default:
		return contextLevelLow
	}
}

func (level contextLevel) style() lipgloss.Style {
	switch level {
	case contextLevelHigh:
		return qgTheme.red
	case contextLevelMid:
		return qgTheme.amber
	default:
		return qgTheme.accent
	}
}

// contextMeterUse returns the last completed call's input tokens for the
// live context meter. The usage belongs to the session, not to the model:
// after a model switch the same figure renders against the new window, and
// the next completed call corrects it.
func (m model) contextMeterUse() (int, bool) {
	if m.meterLive && m.lastUsageSeen && m.lastUsage.InputTokens > 0 {
		return m.lastUsage.InputTokens, true
	}
	return 0, false
}

// contextMeterFill caps the fill percent: rounded, never 100 unless full,
// never 0 with use above zero.
func contextMeterFill(used, window int) int {
	if used >= window {
		return 100
	}
	pct := int(math.Round(float64(used) / float64(window) * 100))
	if pct > 99 {
		return 99
	}
	if pct < 1 {
		return 1
	}
	return pct
}

// composerMeterLine renders the composer's bottom border as a live context
// meter: k filled cells in the level color, the rest in the border color.
// ╰ takes the level color when there is fill; ╯ only when full. Without
// known used tokens and a real window the border stays plain.
func (m model) composerMeterLine(width int) string {
	inner := maxInt(1, width-2)
	used, ok := m.contextMeterUse()
	window := m.apiContextWindow()
	if !ok || window <= 0 {
		return qgTheme.lineStrong.Render("╰" + strings.Repeat("─", inner) + "╯")
	}
	pct := contextMeterFill(used, window)
	level := contextFillLevel(pct).style()
	full := pct == 100
	k := int(math.Round(float64(pct) * float64(inner) / 100))
	if k < 1 {
		k = 1
	}
	if k > inner {
		k = inner
	}
	// k >= 1 here: use is known, so ╰ always takes the level color.
	cornerL := level.Render("╰")
	cornerR := qgTheme.lineStrong.Render("╯")
	if full {
		cornerR = level.Render("╯")
	}
	return cornerL + level.Render(strings.Repeat("━", k)) + qgTheme.lineStrong.Render(strings.Repeat("─", inner-k)) + cornerR
}

// contextStatusText renders the status line's right side for the meter: the
// fill percent plus used/window, the window alone before the first response,
// or just the used figure when the window is unknown.
func (m model) contextStatusText(width int) string {
	used, ok := m.contextMeterUse()
	window := m.apiContextWindow()
	if ok && window > 0 {
		pct := contextMeterFill(used, window)
		styled := contextFillLevel(pct).style().Bold(true)
		if width < 58 {
			return styled.Render(strconv.Itoa(pct) + "%")
		}
		label := strconv.Itoa(pct) + "% · " + formatContextTokens(used) + "/" + formatContextTokens(window)
		at := strings.Index(label, " · ")
		return styled.Render(label[:at]) + qgTheme.faint.Render(label[at:])
	}
	if ok {
		return qgTheme.faint.Render(formatContextTokens(used))
	}
	if window > 0 {
		return qgTheme.faint.Render("janela " + formatContextTokens(window))
	}
	return ""
}
