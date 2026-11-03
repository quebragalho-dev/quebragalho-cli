package tui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
)

// Compact /model palette and /effort ladder rendering. The palette shows one
// line per model (favorite star, name, context window, entry price); the
// selected row expands into capabilities + exit price. The ladder shows one
// line per accepted level with a five-dot gauge. Both overlays fit their lines
// to the frame width so no line ever exceeds the terminal at 50/80/120.

const (
	modelPaletteMaxWidth = 78
	effortLadderMaxWidth = 72
)

// pickerContextWindow renders a model's context window the way the title bar
// does, lowercased for the palette ("1M" -> "1m", "200K" -> "200k").
// Swap for formatContextTokens once the context-meter work lands in dev.
func pickerContextWindow(window int) string {
	return strings.ToLower(formatContextWindow(window))
}

// formatPalettePriceBRL renders a BRL amount string ("1.648") as "R$ 1,65":
// two decimals with a comma, the way prices read elsewhere in BRL. A positive
// value that would round to zero shows "< R$ 0,01" (never "R$ 0,00"), the way
// the per-call cost already does.
func formatPalettePriceBRL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if parsed, err := strconv.ParseFloat(strings.ReplaceAll(value, ",", "."), 64); err == nil {
		if parsed <= 0 {
			return ""
		}
		rounded := strconv.FormatFloat(parsed, 'f', 2, 64)
		if rounded == "0.00" {
			return "< R$ 0,01"
		}
		return "R$ " + strings.ReplaceAll(rounded, ".", ",")
	}
	return "R$ " + strings.ReplaceAll(value, ".", ",")
}

// formatPalettePriceUSD renders a catalog float cost ("$%g"), "" when unknown.
func formatPalettePriceUSD(cost float64) string {
	if cost <= 0 {
		return ""
	}
	return "$" + strconv.FormatFloat(cost, 'f', -1, 64)
}

func apiModelContextWindow(info *qgaccount.ModelInfo) int {
	if info == nil || info.ContextLength == nil {
		return 0
	}
	if *info.ContextLength <= 0 {
		return 0
	}
	return int(*info.ContextLength)
}

// orderAcceptedEfforts sorts accepted levels into ladder order (auto is the
// ladder's first row, not part of this ring).
func orderAcceptedEfforts(efforts []modelregistry.ReasoningEffort) []modelregistry.ReasoningEffort {
	rank := map[modelregistry.ReasoningEffort]int{
		modelregistry.ReasoningEffortNone:      0,
		modelregistry.ReasoningEffortMinimal:   1,
		modelregistry.ReasoningEffortLow:       2,
		modelregistry.ReasoningEffortMedium:    3,
		modelregistry.ReasoningEffortHigh:      4,
		modelregistry.ReasoningEffort("xhigh"): 5,
		modelregistry.ReasoningEffortMax:       6,
	}
	ordered := append([]modelregistry.ReasoningEffort{}, efforts...)
	// Stable insertion by rank; unknown values sink to the end in input order.
	out := make([]modelregistry.ReasoningEffort, 0, len(ordered))
	seen := map[modelregistry.ReasoningEffort]bool{}
	for rankWant := 0; rankWant <= 6; rankWant++ {
		for _, effort := range ordered {
			rankGot, ok := rank[effort]
			if !ok {
				rankGot = 99
			}
			if rankGot == rankWant && !seen[effort] {
				out = append(out, effort)
				seen[effort] = true
			}
		}
	}
	for _, effort := range ordered {
		if !seen[effort] {
			out = append(out, effort)
			seen[effort] = true
		}
	}
	return out
}

// effortLadderHint is the short phrase behind each ladder row.
func effortLadderHint(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "o modelo decide quanto pensar"
	case "none":
		return "responde sem pensar"
	case "minimal":
		return "raciocínio mínimo"
	case "low":
		return "respostas rápidas"
	case "medium":
		return "equilíbrio"
	case "high":
		return "pensa com calma"
	case "xhigh":
		return "tarefas longas e difíceis"
	case "max":
		return "mais lento e mais caro"
	default:
		return ""
	}
}

// effortLadderFilled maps a 1-based rank among total levels to filled dots.
func effortLadderFilled(rank, total int) int {
	if total <= 0 || rank <= 0 {
		return 0
	}
	return minInt(5, (rank*5+total-1)/total)
}

// paletteFrameWidth caps the overlay (~78 palette, ~72 ladder) and shrinks to
// width-4 on smaller screens.
func paletteFrameWidth(termWidth, max int) int {
	return maxInt(1, minInt(max, termWidth-4))
}

// paletteFrame wraps fitted content lines in an accent border and centers the
// block. Every returned line is exactly overlayWidth cells wide.
func paletteFrame(content []string, overlayWidth int) string {
	border := qgTheme.accent.Render
	rows := make([]string, 0, len(content)+2)
	rows = append(rows, border("╭"+strings.Repeat("─", maxInt(0, overlayWidth-2))+"╮"))
	inner := maxInt(0, overlayWidth-4)
	for _, line := range content {
		rows = append(rows, border("│ ")+fillPaletteLine(line, inner, transparentSurface)+border(" │"))
	}
	rows = append(rows, border("╰"+strings.Repeat("─", maxInt(0, overlayWidth-2))+"╯"))
	return strings.Join(rows, "\n")
}

// paletteHints renders footer/header hints: keys in ink, the rest faint,
// wrapped into two lines when one would overflow.
func paletteHints(inner int, keys []string, labels []string) []string {
	full := ""
	for i := range keys {
		if i > 0 {
			full += qgTheme.faint.Render(" · ")
		}
		full += qgTheme.ink.Render(keys[i]) + qgTheme.faint.Render(" "+labels[i])
	}
	if lipgloss.Width(full) <= inner {
		return []string{fitStyledLine(full, inner)}
	}
	half := (len(keys) + 1) / 2
	first, second := "", ""
	for i := range keys {
		part := qgTheme.ink.Render(keys[i]) + qgTheme.faint.Render(" "+labels[i])
		if i < half {
			if first != "" {
				first += qgTheme.faint.Render(" · ")
			}
			first += part
		} else {
			if second != "" {
				second += qgTheme.faint.Render(" · ")
			}
			second += part
		}
	}
	return []string{fitStyledLine(first, inner), fitStyledLine(second, inner)}
}

// padCenteredBlock pads every line of a centered block to the terminal width,
// like panelBlock does, so width assertions see uniform lines.
func padCenteredBlock(block string, width int) string {
	rows := strings.Split(block, "\n")
	for i, row := range rows {
		rows[i] = fitStyledLine(row, width) + strings.Repeat(" ", maxInt(0, width-lipgloss.Width(row)))
	}
	return strings.Join(rows, "\n")
}

// paletteModelName is the model id shown in headers: the ladder target, else
// the active model.
func (m model) paletteModelName() string {
	if m.picker != nil && strings.TrimSpace(m.picker.effortModel) != "" {
		return strings.TrimSpace(m.picker.effortModel)
	}
	return strings.TrimSpace(m.modelName)
}

// modelPalettePage slices the visible palette rows: the same row budget the
// old panel used, plus "mais N" for the undisplayed tail.
func (m model) modelPalettePage() (visible []pickerItem, start int, more int) {
	p := m.picker
	total := len(p.items)
	if total == 0 {
		return nil, 0, 0
	}
	count := minInt(total, maxInt(1, minInt(8, m.height-18)))
	start = panelListStart(total, count, clampInt(p.selected, 0, total-1))
	visible = p.items[start : start+count]
	more = total - (start + count)
	return visible, start, more
}

func renderPaletteSearchLine(query string, width int) string {
	prompt := qgTheme.userPrompt.Render("❯ ")
	cursor := qgTheme.accent.Render("▏")
	if strings.TrimSpace(query) == "" {
		return fitStyledLine(prompt+cursor+qgTheme.faint.Render("busque por nome"), width)
	}
	return fitStyledLine(prompt+qgTheme.ink.Render(query)+cursor, width)
}

// renderPaletteRow renders one palette row (plus the expanded detail line for
// the selected row): "* "/two-space favorite mark, name left, window + entry
// price right ("1m · R$ 0,52"); the price drops first on narrow frames while
// the window stays.
func renderPaletteRow(inner int, selected bool, item pickerItem) []string {
	surface := transparentSurface
	marker := surface(qgTheme.faintest).Render("  ")
	if selected {
		surface = qgTheme.onSel
		marker = surface(qgTheme.accent).Render("▌ ")
	}
	star := "  "
	if item.Favorite {
		star = surface(qgTheme.accent).Render("* ")
	}
	name := strings.TrimSpace(item.Label)
	if name == "" {
		name = strings.TrimSpace(item.Value)
	}
	window := pickerContextWindow(item.Window)
	rightFull, rightSlim := "", window
	if window != "" && strings.TrimSpace(item.PriceIn) != "" {
		rightFull = window + " · " + strings.TrimSpace(item.PriceIn)
	} else if strings.TrimSpace(item.PriceIn) != "" {
		rightFull, rightSlim = strings.TrimSpace(item.PriceIn), strings.TrimSpace(item.PriceIn)
	}
	// The entry price drops before the name is ever cut: shrink the right
	// side to the window (or nothing) first, truncate the name last.
	leftBase := marker + star
	right := rightFull
	if lipgloss.Width(leftBase)+1+lipgloss.Width(name)+1+lipgloss.Width(right) > inner &&
		lipgloss.Width(leftBase)+1+lipgloss.Width(name)+1+lipgloss.Width(rightSlim) <= inner {
		right = rightSlim
	}
	nameBudget := maxInt(1, inner-lipgloss.Width(leftBase)-1-lipgloss.Width(right)-1)
	if lipgloss.Width(leftBase)+1+lipgloss.Width(name)+1+lipgloss.Width(right) > inner {
		name = truncateDisplayWidth(name, nameBudget)
	}
	line := leftBase + surface(qgTheme.ink).Render(name)
	if strings.TrimSpace(right) != "" {
		gap := inner - lipgloss.Width(leftBase) - lipgloss.Width(name) - lipgloss.Width(right)
		line += strings.Repeat(" ", maxInt(1, gap)) + surface(qgTheme.faint).Render(right)
	}
	lines := []string{fillPaletteLine(line, inner, surface)}
	if selected {
		if detail := renderPaletteDetail(inner, item); detail != "" {
			lines = append(lines, detail)
		}
	}
	return lines
}

// renderPaletteDetail is the expanded second line of the selected row:
// present capabilities left ("ferramentas · imagens · raciocínio", "bônus" in
// amber), exit price right ("saída R$ 0,93"). "" when there is nothing to show.
func renderPaletteDetail(inner int, item pickerItem) string {
	caps := []string{}
	if item.HasTools {
		caps = append(caps, qgTheme.faint.Render("ferramentas"))
	}
	if item.HasVision {
		caps = append(caps, qgTheme.faint.Render("imagens"))
	}
	if item.HasReasoning {
		caps = append(caps, qgTheme.faint.Render("raciocínio"))
	}
	if item.HasBonus {
		caps = append(caps, qgTheme.amber.Render("bônus"))
	}
	right := ""
	if strings.TrimSpace(item.PriceOut) != "" {
		right = qgTheme.faint.Render("saída " + strings.TrimSpace(item.PriceOut))
	}
	if len(caps) == 0 && right == "" {
		return ""
	}
	left := strings.Repeat(" ", 4) + strings.Join(caps, qgTheme.faint.Render(" · "))
	line := left
	if right != "" {
		// The exit price stays; the capability list is trimmed first.
		if lipgloss.Width(left)+1+lipgloss.Width(right) > inner {
			if budget := inner - lipgloss.Width(right) - 1; budget <= 4 {
				return fillPaletteLine(right, inner, transparentSurface)
			} else {
				left = fitStyledLine(left, budget)
			}
		}
		gap := inner - lipgloss.Width(left) - lipgloss.Width(right)
		line = left + strings.Repeat(" ", maxInt(1, gap)) + right
	}
	return fillPaletteLine(line, inner, transparentSurface)
}

// modelPaletteOverlay renders the compact /model palette: 78-column frame,
// "modelo" title left with the level shortcut right, immediate search,
// faint group labels without numbering, expanded selected row, footer hints.
func (m model) modelPaletteOverlay(width int) string {
	p := m.picker
	if m.modelPickerLoading {
		return m.modelPickerLoadingOverlay(width)
	}
	overlayWidth := paletteFrameWidth(width, modelPaletteMaxWidth)
	inner := maxInt(1, overlayWidth-4)
	draft := strings.TrimSpace(p.effort)
	levelLabel := effortDisplayLabel(m.effortDisplay())
	if draft != "" {
		levelLabel = effortDisplayLabel(draft)
	}
	headerLeft := qgTheme.accent.Bold(true).Render("modelo")
	headerRight := qgTheme.faint.Render("nível "+levelLabel+" · ") + qgTheme.ink.Render("Tab")
	headerGap := inner - lipgloss.Width(headerLeft) - lipgloss.Width(headerRight)
	header := headerLeft
	if headerGap < 1 {
		header = fitStyledLine(headerLeft, inner)
	} else {
		header += strings.Repeat(" ", headerGap) + headerRight
	}
	content := []string{fillPaletteLine(header, inner, transparentSurface), renderPaletteSearchLine(p.query, inner)}
	if status := strings.TrimSpace(m.modelPickerLoadError); status != "" {
		content = append(content, fillPaletteLine(qgTheme.faint.Render(status), inner, transparentSurface))
	}
	visible, start, more := m.modelPalettePage()
	if len(p.items) == 0 {
		content = append(content, fillPaletteLine(qgTheme.faint.Render("Nenhum modelo correspondente"), inner, transparentSurface))
	} else {
		lastGroup := ""
		for index, item := range visible {
			if item.Group != "" && item.Group != lastGroup {
				content = append(content, fillPaletteLine(qgTheme.faint.Render(item.Group), inner, transparentSurface))
				lastGroup = item.Group
			}
			content = append(content, renderPaletteRow(inner, start+index == p.selected, item)...)
		}
		if more > 0 {
			content = append(content, fillPaletteLine(qgTheme.faint.Render("↓ mais "+strconv.Itoa(more)), inner, transparentSurface))
		}
	}
	content = append(content, paletteHints(inner,
		[]string{"Enter", "Alt+s", "Tab", "Alt+f", "Esc"},
		[]string{"usar", "sessão", "nível", "favoritar", "fechar"})...)
	return padCenteredBlock(centerRenderedBlock(paletteFrame(content, overlayWidth), width), width)
}

// effortLadderOverlay renders the /effort dot ladder: 72-column frame, the
// accepted range up top, one row per level (name + five dots + hint), the
// marked row with "›" on the selection background, the level in use tagged
// "atual".
func (m model) effortLadderOverlay(width int) string {
	p := m.picker
	overlayWidth := paletteFrameWidth(width, effortLadderMaxWidth)
	inner := maxInt(1, overlayWidth-4)
	modelName := m.paletteModelName()
	levels := []pickerItem{}
	for _, item := range p.items {
		if strings.ToLower(strings.TrimSpace(item.Value)) == "auto" {
			continue
		}
		levels = append(levels, item)
	}
	current := strings.ToLower(strings.TrimSpace(m.effortDisplay()))
	if current == "" {
		current = "auto"
	}
	rangeLine := modelName + " não tem níveis ajustáveis"
	if len(levels) > 0 {
		rangeLine = modelName + " aceita de " + effortDisplayLabel(levels[0].Value) + " a " + effortDisplayLabel(levels[len(levels)-1].Value)
	}
	content := []string{
		fillPaletteLine(qgTheme.accent.Bold(true).Render("Nível"), inner, transparentSurface),
		fillPaletteLine(qgTheme.faint.Render(truncateDisplayWidth(rangeLine, inner)), inner, transparentSurface),
	}
	autoSelected := p.selected == 0
	content = append(content, renderLadderRow(inner, autoSelected, "automático", 0, effortLadderHint("auto"), current == "auto")...)
	for rank, item := range levels {
		selected := p.selected == rank+1
		content = append(content, renderLadderRow(inner, selected, effortDisplayLabel(item.Value), effortLadderFilled(rank+1, len(levels)), effortLadderHint(item.Value), strings.ToLower(strings.TrimSpace(item.Value)) == current)...)
	}
	content = append(content, paletteHints(inner,
		[]string{"↑/↓", "Enter", "s", "Esc"},
		[]string{"mover", "usar sempre", "só nesta sessão", "fechar"})...)
	return padCenteredBlock(centerRenderedBlock(paletteFrame(content, overlayWidth), width), width)
}

// renderLadderRow renders one ladder row: "›"/blank mark, name in 13 columns,
// five dots (accent filled, faint rest; auto uses "·····"), faint hint,
// right-aligned "atual" in accent for the level in use.
func renderLadderRow(inner int, selected bool, name string, filled int, hint string, isCurrent bool) []string {
	surface := transparentSurface
	mark := "  "
	if selected {
		surface = qgTheme.onSel
		mark = surface(qgTheme.accent).Render("› ")
	}
	nameCell := truncateDisplayWidth(name, 13)
	nameCell += strings.Repeat(" ", maxInt(0, 13-lipgloss.Width(nameCell)))
	dotCells := ""
	if filled == 0 {
		dotCells = surface(qgTheme.faint).Render("·····")
	} else {
		for i := 0; i < 5; i++ {
			if i < filled {
				dotCells += surface(qgTheme.accent).Render("●")
			} else {
				dotCells += surface(qgTheme.faint).Render("●")
			}
		}
	}
	base := mark + surface(qgTheme.ink).Render(nameCell) + " " + dotCells
	current := ""
	if isCurrent {
		current = surface(qgTheme.accent).Render("atual")
	}
	// The "atual" tag never clips: the hint shrinks first.
	hintText := truncateDisplayWidth(hint, maxInt(0, inner-lipgloss.Width(base)-2-lipgloss.Width(current)-1))
	line := base
	if hintText != "" {
		line += "  " + surface(qgTheme.faint).Render(hintText)
	}
	if current != "" {
		line += strings.Repeat(" ", maxInt(1, inner-lipgloss.Width(line)-lipgloss.Width(current))) + current
	}
	return []string{fillPaletteLine(line, inner, surface)}
}
