package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

const (
	defaultStartupWidth  = 96
	defaultStartupHeight = 30
	minStartupWidth      = 58
)

// emptyStateTagline is the landing footer's line ("democratizando a ia.",
// followed by a heart).
const emptyStateTagline = "democratizando a ia."

// emptyStateHeart follows the tagline, painted with the theme's red.
const emptyStateHeart = "♥"

// brandTier is how much of the brand the empty state draws at a terminal size.
type brandTier int

const (
	// brandTierText is a one-line "quebra galho" over the tagline.
	brandTierText brandTier = iota
	// brandTierCompact puts the mascot beside the one-line wordmark, for
	// panes too small for the card.
	brandTierCompact
	// brandTierCard is the welcome card: a rounded border in the accent
	// around the mascot, the name, the tagline and the session's details.
	brandTierCard
)

const (
	brandCompactGap = 3 // cells between the mascot and the text block
	brandMargin     = 2 // cells kept free on each side of the art
	brandCardPad    = 2 // cells between the card's border and its content
	brandCardGap    = 4 // cells between the mascot and the card's text
	// brandCardMinText is the narrowest the card's text gets: the tagline
	// still fits, and longer details are cut.
	brandCardMinText = 24
	// brandDomain follows the name in the card, in faint text.
	brandDomain = ".dev"
	// emptyStateChromeRows is what the frame around the stream area (title
	// bar, rules, composer, status line) takes from the terminal height; the
	// idle shortcut hint above the composer takes one more row when shown.
	emptyStateChromeRows = 6
)

// brandCompactWidth reserves room for the widest text line, the tagline.
var brandCompactWidth = brandMascotWidth + brandCompactGap + lipgloss.Width(emptyStateTagline+" "+emptyStateHeart)

// emptyState renders the centered stream-area block shown while the
// transcript has no real content: the brand art and tagline.
func (m model) emptyState(width int) string {
	lines := m.emptyStateLines(width)

	// Vertically center within the stream area.
	gap := clamp((m.emptyStateRows()-len(lines))/2, 0, 12)
	return strings.Repeat("\n", gap) + strings.Join(lines, "\n") + strings.Repeat("\n", gap)
}

// emptyStateRows estimates the stream area's height from the real terminal
// height (the default before the first resize). Unlike
// normalizedStartupHeight it has no 18-row floor, so a short pane picks
// smaller art and no centering gap pushes it out of view.
func (m model) emptyStateRows() int {
	height := m.height
	if height <= 0 {
		height = defaultStartupHeight
	}
	rows := height - emptyStateChromeRows
	if m.composerIdleHint() != "" {
		rows--
	}
	return rows
}

func (m model) emptyStateWithOverlay(width int, overlay string) string {
	lines := viewLines(overlay)
	for index := range lines {
		lines[index] = fitStyledLine(lines[index], width)
	}

	// Center the palette in the visible chat area. While the command palette is
	// open it replaces the empty-state wordmark instead of sitting below it.
	available := normalizedStartupHeight(m.height) - 5
	if m.titleBarInTranscriptBody() {
		available -= 2
	}
	gap := maxInt(0, (available-len(lines))/2)
	return strings.Repeat("\n", gap) + strings.Join(lines, "\n") + strings.Repeat("\n", gap)
}

// emptyStateTier picks the largest brand art that fits: the welcome card,
// then the mascot beside one line of text, then the line alone. Without
// color the card comes without the mascot.
func (m model) emptyStateTier(width int) brandTier {
	rows := m.emptyStateRows()
	fits := func(art int) bool { return width >= art+2*brandMargin }
	if fits(2+2*brandCardPad+m.brandCardSide()+brandCardMinText) && rows >= m.brandCardRows() {
		return brandTierCard
	}
	if m.canDrawMascot() && fits(brandCompactWidth) && rows >= brandMascotCellRows+2 {
		return brandTierCompact
	}
	return brandTierText
}

// canDrawMascot reports whether the mascot's fixed colors will show: they need
// color output. Its dark outline keeps it readable on light terminals too.
func (m model) canDrawMascot() bool {
	return !m.noColor
}

func (m model) emptyStateLines(width int) []string {
	var lines []string
	switch m.emptyStateTier(width) {
	case brandTierCard:
		lines = m.brandCard(width)
	case brandTierCompact:
		column := []string{brandWordmarkText(), brandTagline()}
		if orient := m.emptyStateOrientation(); orient != "" {
			column = append(column, orient)
		}
		lines = brandBesideMascot(width, brandCompactWidth, brandCompactGap, column)
	default:
		lines = append(lines, centerLine(brandWordmarkText(), width), centerLine(brandTagline(), width))
		if orient := m.emptyStateOrientation(); orient != "" {
			lines = append(lines, "", centerLine(orient, width))
		}
		// A very short pane keeps the first lines that fit.
		if rows := m.emptyStateRows(); rows > 0 && len(lines) > rows {
			lines = lines[:rows]
		}
	}
	for index := range lines {
		lines[index] = fitStyledLine(lines[index], width)
	}
	return lines
}

// brandCardMascot is the art on the card's left: the mascot, or nothing
// without color.
func (m model) brandCardMascot() []string {
	if !m.canDrawMascot() {
		return nil
	}
	return brandMascotLines()
}

// brandCardSide is the cells the mascot and its gap take in the card's rows.
func (m model) brandCardSide() int {
	if !m.canDrawMascot() {
		return 0
	}
	return brandMascotWidth + brandCardGap
}

// brandCardRows is the card's height: its content between two borders, each
// with a blank row inside.
func (m model) brandCardRows() int {
	return maxInt(len(m.brandCardMascot()), len(m.brandCardColumn())) + 4
}

// brandCardColumn is the card's text: the name with its domain, the tagline
// and the session's details, as in kimi-code's welcome card.
func (m model) brandCardColumn() []string {
	label := qgTheme.faint.Bold(true)
	value := qgTheme.ink.Bold(false)
	detail := func(name, text string) string {
		return label.Render(fmt.Sprintf("%-10s", name)) + " " + value.Render(text)
	}
	column := []string{
		qgTheme.ink.Bold(true).Render("quebra") + qgTheme.accent.Bold(true).Render("galho") + qgTheme.faint.Render(brandDomain),
		brandTagline(),
		"",
		detail("diretório", shortenPath(m.cwd)),
	}
	if branch := strings.TrimSpace(m.gitBranch); branch != "" {
		column = append(column, detail("branch", branch))
	}
	column = append(column, detail("modelo", displayValue(strings.TrimSpace(m.modelName), "sem modelo")))
	if version := displayVersion(m.appVersion); version != "" {
		column = append(column, detail("versão", version))
	}
	return column
}

// brandCard draws the welcome card centered in width: a rounded border in the
// accent around the mascot and brandCardColumn, centered on each other. The
// card shrinks to the width, down to brandCardMinText cells of text; longer
// lines are cut.
func (m model) brandCard(width int) []string {
	mascot := m.brandCardMascot()
	column := m.brandCardColumn()
	side := m.brandCardSide()
	text := 0
	for _, line := range column {
		text = maxInt(text, lipgloss.Width(line))
	}
	inner := minInt(2*brandCardPad+side+text, width-2*brandMargin-2)
	textWidth := inner - 2*brandCardPad - side
	border := qgTheme.accent.Bold(false)
	edge := border.Render("│")
	indent := strings.Repeat(" ", maxInt(0, (width-inner-2)/2))
	blank := indent + edge + strings.Repeat(" ", inner) + edge
	rows := maxInt(len(mascot), len(column))
	mascotTop := (rows - len(mascot)) / 2
	columnTop := (rows - len(column)) / 2
	lines := []string{indent + border.Render("╭"+strings.Repeat("─", inner)+"╮"), blank}
	for row := 0; row < rows; row++ {
		content := strings.Repeat(" ", brandCardPad)
		if side > 0 {
			art := strings.Repeat(" ", brandMascotWidth)
			if index := row - mascotTop; index >= 0 && index < len(mascot) {
				art = mascot[index]
			}
			content += art + strings.Repeat(" ", brandCardGap)
		}
		if index := row - columnTop; index >= 0 && index < len(column) {
			content += fitStyledLine(column[index], textWidth)
		}
		content += strings.Repeat(" ", maxInt(0, inner-lipgloss.Width(content)))
		lines = append(lines, indent+edge+content+edge)
	}
	return append(lines, blank, indent+border.Render("╰"+strings.Repeat("─", inner)+"╯"))
}

// brandBesideMascot draws the mascot with column to its right, the two
// centered on each other. The block is blockWidth cells wide and centered in
// width.
func brandBesideMascot(width, blockWidth, gapWidth int, column []string) []string {
	mascot := brandMascotLines()
	rows := maxInt(len(mascot), len(column))
	mascotTop := (rows - len(mascot)) / 2
	columnTop := (rows - len(column) + 1) / 2
	indent := strings.Repeat(" ", maxInt(0, (width-blockWidth)/2))
	gap := strings.Repeat(" ", gapWidth)
	lines := make([]string, 0, rows)
	for row := 0; row < rows; row++ {
		art := strings.Repeat(" ", brandMascotWidth)
		if index := row - mascotTop; index >= 0 && index < len(mascot) {
			art = mascot[index]
		}
		text := ""
		if index := row - columnTop; index >= 0 && index < len(column) {
			text = column[index]
		}
		lines = append(lines, indent+art+gap+text)
	}
	return lines
}

// brandWordmarkText is the one-line wordmark for panes too small for the
// card.
func brandWordmarkText() string {
	return qgTheme.ink.Bold(true).Render("quebra") + " " + qgTheme.accent.Render("galho")
}

func brandTagline() string {
	return qgTheme.muted.Render(emptyStateTagline) + " " + qgTheme.red.Render(emptyStateHeart)
}

func (m model) emptyStateOrientation() string {
	parts := make([]string, 0, 2)
	if version := displayVersion(m.appVersion); version != "" {
		parts = append(parts, version)
	}
	if branch := strings.TrimSpace(m.gitBranch); branch != "" {
		parts = append(parts, branch)
	}
	if len(parts) == 0 {
		return ""
	}
	return qgTheme.faint.Render(strings.Join(parts, "  ·  "))
}

// displayVersion formats the CLI build version for display: numeric releases
// gain a "v" prefix (0.2.0 → v0.2.0), anything else (dev, git SHAs) passes
// through unchanged. Empty stays empty so unset builds show nothing.
func displayVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	if version[0] >= '0' && version[0] <= '9' {
		return "v" + version
	}
	return version
}

// Wordmark renders the brand art shown on the TUI's empty state, for reuse
// outside the TUI (e.g. `qg --version`): the small pixel wordmark, which fits
// narrow terminals, and the tagline.
// It is deliberately uncolored: only newModel resolves --theme/QG_THEME, so
// painting the global palette here would ignore a selected light theme and be
// unreadable on light terminals. The terminal's default foreground works
// everywhere.
func Wordmark() string {
	rows := brandWordmarkSmall.rows()
	lines := make([]string, 0, len(rows)+2)
	for _, row := range rows {
		lines = append(lines, strings.TrimRight(row, " "))
	}
	lines = append(lines, "", emptyStateTagline+" "+emptyStateHeart)
	return strings.Join(lines, "\n")
}

// styledBlock draws a rounded box around lines with the given border style,
// padding every row to the full width.
func styledBlock(width int, lines []string, borderStyle lipgloss.Style) string {
	return styledBlockFill(width, lines, borderStyle, lipgloss.NewStyle())
}

// styledBlockFill is styledBlock with a fill style painting the row padding,
// so tinted cards (permission, panel surfaces) read as solid bands. On tiny
// terminals every card loses its side borders (top/bottom rules stay) so the
// 4 border cells go back to content.
func styledBlockFill(width int, lines []string, borderStyle lipgloss.Style, fill lipgloss.Style) string {
	if width < 4 {
		width = 4
	}

	if widthTier(width) == tierTiny {
		rule := borderStyle.Render(strings.Repeat("─", width))
		body := make([]string, 0, len(lines)+2)
		body = append(body, rule)
		for _, line := range lines {
			body = append(body, fitStyledLine(line, width))
		}
		body = append(body, rule)
		return strings.Join(body, "\n")
	}

	rule := strings.Repeat("─", width-2)
	top := borderStyle.Render("╭" + rule + "╮")
	bottom := borderStyle.Render("╰" + rule + "╯")
	body := make([]string, 0, len(lines)+2)
	body = append(body, top)
	for _, line := range lines {
		available := width - 4
		fitted := fitStyledLine(line, available)
		pad := fill.Render(strings.Repeat(" ", maxInt(0, available-lipgloss.Width(fitted))))
		body = append(body, borderStyle.Render("│ ")+fitted+pad+borderStyle.Render(" │"))
	}
	body = append(body, bottom)
	return strings.Join(body, "\n")
}

// middleTruncate shortens a path-like value from the middle so both the
// leading segment and the file name survive: internal/…/root.go.
func middleTruncate(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return truncateRunes(value, limit)
	}
	keep := limit - 1
	front := keep / 2
	back := keep - front
	return string(runes[:front]) + "…" + string(runes[len(runes)-back:])
}

func joinHeaderLine(left string, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return left + "  " + right
	}
	return left + strings.Repeat(" ", gap) + right
}

type headerCandidate struct {
	left  string
	right string
}

func startupHeaderLine(width int, candidates []headerCandidate) string {
	for _, candidate := range candidates {
		line := joinHeaderLine(candidate.left, candidate.right, width)
		if lipgloss.Width(line) <= width {
			return line
		}
	}
	// Nothing fits whole: truncate the most minimal candidate rather than
	// inventing different content.
	if len(candidates) == 0 {
		return ""
	}
	last := candidates[len(candidates)-1]
	return fitStyledLine(joinHeaderLine(last.left, last.right, width), width)
}

func centerLine(line string, width int) string {
	padding := (width - lipgloss.Width(line)) / 2
	if padding < 0 {
		padding = 0
	}
	return strings.Repeat(" ", padding) + line
}

func rightAlignedLine(line string, width int) string {
	padding := width - lipgloss.Width(line)
	if padding < 0 {
		padding = 0
	}
	return strings.Repeat(" ", padding) + line
}

func indentBlock(block string, spaces int) string {
	if spaces <= 0 {
		return block
	}

	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(block, "\n")
	for index, line := range lines {
		lines[index] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func fitStyledLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(line) <= width {
		return line
	}
	return truncateStyledLine(line, width)
}

func truncateStyledLine(line string, width int) string {
	const resetANSI = "\x1b[0m"

	ellipsis := "…"
	ellipsisWidth := lipgloss.Width(ellipsis)
	if width <= ellipsisWidth {
		return ellipsis
	}

	targetWidth := width - ellipsisWidth
	usedWidth := 0
	sawANSI := false
	openLink := false

	var builder strings.Builder
	for index := 0; index < len(line); {
		if line[index] == '\x1b' {
			end := ansiSequenceEnd(line, index)
			if end > index {
				sequence := line[index:end]
				builder.WriteString(sequence)
				sawANSI = true
				// Track OSC 8 hyperlink state: truncating between an open and
				// its terminator would leak the link onto everything after.
				if strings.HasPrefix(sequence, "\x1b]8;") {
					openLink = sequence != "\x1b]8;;\x1b\\" && sequence != "\x1b]8;;\a"
				}
				index = end
				continue
			}
		}

		glyph, size := utf8.DecodeRuneInString(line[index:])
		if glyph == utf8.RuneError && size == 0 {
			break
		}

		glyphWidth := lipgloss.Width(string(glyph))
		if usedWidth+glyphWidth > targetWidth {
			break
		}
		builder.WriteString(line[index : index+size])
		usedWidth += glyphWidth
		index += size
	}

	if openLink {
		builder.WriteString("\x1b]8;;\x1b\\")
	}
	builder.WriteString(ellipsis)
	if sawANSI {
		builder.WriteString(resetANSI)
	}
	return builder.String()
}

func ansiSequenceEnd(value string, start int) int {
	if start >= len(value) || value[start] != '\x1b' {
		return start
	}
	index := start + 1
	if index >= len(value) {
		return index
	}

	switch value[index] {
	case '[':
		// CSI: terminated by a final byte in 0x40–0x7e.
		for index++; index < len(value); index++ {
			if value[index] >= 0x40 && value[index] <= 0x7e {
				return index + 1
			}
		}
		return len(value)
	case ']':
		// OSC (e.g. the OSC 8 hyperlinks on tool-card paths): terminated by
		// BEL or ST (ESC \). Without this branch the truncator treated the
		// payload as printable text, wrecking the width math.
		for index++; index < len(value); index++ {
			if value[index] == '\a' {
				return index + 1
			}
			if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '\\' {
				return index + 2
			}
		}
		return len(value)
	default:
		return minInt(start+2, len(value))
	}
}

// chatWidth resolves the render width for the chat surface. Unlike the old
// splash floor it respects genuinely tiny terminals (so the <58 tier can
// engage). The 24-cell floor is deliberate: below it the cards' own minimum
// width makes the layout meaningless, so we accept terminal-side wrapping
// there rather than degrade every wider tier.
func chatWidth(width int) int {
	if width <= 0 {
		return defaultStartupWidth
	}
	if width < 24 {
		return 24
	}
	return width
}

func normalizedStartupHeight(height int) int {
	if height <= 0 {
		return defaultStartupHeight
	}
	if height < 18 {
		return 18
	}
	return height
}

func clamp(value int, minimum int, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}
