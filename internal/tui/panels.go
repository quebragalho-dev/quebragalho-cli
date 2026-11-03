package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
)

type usageAccountResult struct {
	picker     *commandPicker
	usage      qgaccount.UsageReport
	balance    qgaccount.Balance
	err        error
	balanceErr error
}

func (m model) panelReasoningEfforts() []modelregistry.ReasoningEffort {
	efforts := m.availableReasoningEfforts()
	if len(efforts) == 0 && m.providerProfile.CatalogID == "qg" {
		if info := m.apiModel(m.modelName); info != nil && info.Capabilities.Reasoning {
			return []modelregistry.ReasoningEffort{"low", "medium", "high", "xhigh", "max"}
		}
	}
	return efforts
}

// panelReasoningEffortsFor resolves the accepted levels for a marked (not
// necessarily active) model: the active model's ring when it matches,
// otherwise live discovery, the registry (catalog + name inference), and the
// QG reasoning fallback. Nil when the model exposes no effort controls.
func (m model) panelReasoningEffortsFor(modelID string) []modelregistry.ReasoningEffort {
	if id := strings.TrimSpace(modelID); id == "" || strings.EqualFold(id, strings.TrimSpace(m.modelName)) {
		return m.panelReasoningEfforts()
	}
	if efforts, found := reasoningEffortsFromModels(m.discoveredModelsForActiveProvider(), modelID); found && len(efforts) > 0 {
		return efforts
	}
	if registry, err := modelregistry.DefaultRegistry(); err == nil {
		if efforts := registry.ReasoningEfforts(modelID); len(efforts) > 0 {
			return efforts
		}
	}
	if m.providerProfile.CatalogID == "qg" {
		for _, candidate := range m.discoveredModelsForActiveProvider() {
			if strings.EqualFold(strings.TrimSpace(candidate.ID), strings.TrimSpace(modelID)) && candidate.Reasoning {
				return []modelregistry.ReasoningEffort{"low", "medium", "high", "xhigh", "max"}
			}
		}
	}
	return nil
}

func (m model) effortPreferenceKey() string { return m.providerName + "/" + m.modelName }

// effortDisplayLabel maps a reasoning-effort value to its Portuguese display
// label. Values (low, medium, ...) stay English everywhere else: config,
// protocol and tests keep comparing the raw value.
func effortDisplayLabel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return "automático"
	case "none":
		return "nenhum"
	case "minimal":
		return "mínimo"
	case "low":
		return "baixo"
	case "medium":
		return "médio"
	case "high":
		return "alto"
	case "xhigh":
		return "muito alto"
	case "max":
		return "máximo"
	default:
		return value
	}
}

// canonicalEffortInput maps a Portuguese display label (with or without
// accent) or an English value to its canonical English value. Auto accepts
// both languages. Unknown inputs return the trimmed input unchanged.
func canonicalEffortInput(input string) string {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "automático", "automatico":
		return "auto"
	case "nenhum":
		return "none"
	case "mínimo", "minimo":
		return "minimal"
	case "baixo":
		return "low"
	case "médio", "medio":
		return "medium"
	case "alto":
		return "high"
	case "muito alto":
		return "xhigh"
	case "máximo", "maximo":
		return "max"
	default:
		return strings.TrimSpace(input)
	}
}

func (m model) loadPanelEffort() model {
	if m.userConfigPath == "" {
		return m
	}
	data, err := os.ReadFile(m.userConfigPath)
	if err != nil {
		return m
	}
	var cfg config.FileConfig
	if json.Unmarshal(data, &cfg) != nil {
		return m
	}
	effort := modelregistry.ReasoningEffort(cfg.Preferences.ReasoningEfforts[m.effortPreferenceKey()])
	if effort == "auto" {
		m.reasoningEffort = ""
	} else if modelregistry.ValidReasoningEffort(effort) {
		m.reasoningEffort = effort
	}
	return m
}

func (m model) liveQGModelPicker() *commandPicker {
	if m.providerProfile.CatalogID != "qg" {
		return nil
	}
	items := []pickerItem{}
	selected := 0
	for _, candidate := range m.discoveredModelsForActiveProvider() {
		info := candidate.APIModel
		if candidate.Source != "live" || info == nil {
			continue
		}
		label := info.ID
		if info.DisplayName != nil && strings.TrimSpace(*info.DisplayName) != "" {
			label = *info.DisplayName
		}
		bonus := "não"
		if info.AcceptsBonus {
			bonus = "sim"
		}
		caps := fmt.Sprintf("tools %t · vision %t · reasoning %t", info.Capabilities.Tools, info.Capabilities.Vision, info.Capabilities.Reasoning)
		price := "BRL não informado"
		if info.PricingBRL.Currency == "BRL" {
			money := func(value string) string {
				if strings.TrimSpace(value) == "" {
					return "n/d"
				}
				return "R$ " + strings.ReplaceAll(value, ".", ",")
			}
			price = "BRL/1M entrada " + money(info.PricingBRL.Input) + " · saída " + money(info.PricingBRL.Output) + " · cache " + money(info.PricingBRL.CachedInput)
		}
		items = append(items, pickerItem{Label: label, Value: info.ID, OwnerProvider: m.providerName, Meta: price, Detail: "bônus " + bonus + " · " + caps,
			Window:       apiModelContextWindow(info),
			PriceIn:      formatPalettePriceBRL(info.PricingBRL.Input),
			PriceOut:     formatPalettePriceBRL(info.PricingBRL.Output),
			HasTools:     info.Capabilities.Tools,
			HasVision:    info.Capabilities.Vision,
			HasReasoning: info.Capabilities.Reasoning,
			HasBonus:     info.AcceptsBonus,
			Favorite:     m.favoriteModels[info.ID],
		})
	}

	if len(items) == 0 {
		return nil
	}
	items, selected = orderLivePaletteItems(m, items)
	return &commandPicker{kind: pickerModel, title: "Escolher modelo", items: items, allItems: append([]pickerItem{}, items...), selected: selected, panel: true}
}

// orderLivePaletteItems groups the live palette the way the generic picker
// does — favorites first ("Favoritos"), then the active model and recent
// history ("Recentes"), then the rest ("Catálogo") — keeping discovery order
// inside each group and pointing selected at the active model.
func orderLivePaletteItems(m model, items []pickerItem) ([]pickerItem, int) {
	recent := map[string]bool{}
	for _, pair := range m.recentModelPairsForPicker() {
		recent[strings.TrimSpace(pair.Model)] = true
	}
	var favorites, recents, catalog []pickerItem
	for _, item := range items {
		switch {
		case m.favoriteModels[item.Value]:
			item.Group = "Favoritos"
			favorites = append(favorites, item)
		case strings.TrimSpace(item.Value) == strings.TrimSpace(m.modelName) || recent[item.Value]:
			item.Group = "Recentes"
			recents = append(recents, item)
		default:
			item.Group = "Catálogo"
			catalog = append(catalog, item)
		}
	}
	ordered := append(append(append([]pickerItem{}, favorites...), recents...), catalog...)
	selected := 0
	for index, item := range ordered {
		if strings.TrimSpace(item.Value) == strings.TrimSpace(m.modelName) {
			selected = index
		}
	}
	return ordered, selected
}

func (m model) panelBlock(width int, title string, lines []string) string {
	w := minInt(width, 100)
	inner := maxInt(1, w-4)
	fitted := []string{}
	for _, line := range lines {
		fitted = append(fitted, wrapPlainText(line, inner)...)
	}
	for i, line := range fitted {
		fitted[i] = fitStyledLine(line, inner)
	}
	if m.height > 0 {
		limit := maxInt(5, m.height-10)
		if len(fitted) > limit {
			fitted = append(append(fitted[:limit-3], "…"), fitted[len(fitted)-2:]...)
		}
	}
	border := qgTheme.accent.Render
	heading := truncateDisplayWidth(" "+title+" ", maxInt(1, w-4))
	rows := []string{border("╭─" + heading + strings.Repeat("─", maxInt(0, w-3-lipgloss.Width(heading))) + "╮")}
	for _, line := range fitted {
		rows = append(rows, border("│ ")+line+strings.Repeat(" ", maxInt(0, inner-lipgloss.Width(line)))+border(" │"))
	}
	rows = append(rows, border("╰"+strings.Repeat("─", maxInt(0, w-2))+"╯"))
	block := centerRenderedBlock(strings.Join(rows, "\n"), width)
	rows = strings.Split(block, "\n")
	for i, row := range rows {
		rows[i] = fitStyledLine(row, width) + strings.Repeat(" ", maxInt(0, width-lipgloss.Width(row)))
	}
	return strings.Join(rows, "\n")
}

func (m model) effortPanelOverlay(width int) string {
	return m.effortLadderOverlay(width)
}

func (m model) modelPanelOverlay(width int) string {
	if m.picker == nil || !(m.picker.kind == pickerModel && m.picker.panel) {
		return m.modelPickerOverlay(width)
	}
	return m.modelPaletteOverlay(width)
}

func (m model) handlePanelKey(key tea.KeyPressMsg) (model, tea.Cmd, bool) {
	p := m.picker
	if p == nil || (p.kind != pickerUsage && p.kind != pickerEffort && p.kind != pickerModel) {
		return m, nil, false
	}
	if keyIs(key, tea.KeyEsc) {
		// A Tab-opened ladder restores the palette; every other overlay closes.
		if p.kind == pickerEffort && p.resume != nil {
			m.picker = p.resume
			return m, nil, true
		}
		m.picker = nil
		m.clearModelPickerLoadState()
		return m, nil, true
	}
	if keyCtrl(key, 'c') {
		return m, nil, false
	}
	if p.kind == pickerUsage {
		if keyIs(key, tea.KeyTab) || keyIs(key, tea.KeyLeft) || keyIs(key, tea.KeyRight) {
			p.usageTab = 1 - p.usageTab
			if p.usageTab == 1 && !p.accountLoading {
				p.accountLoading = true
				p.account = nil
				return m, m.fetchPanelAccount(), true
			}
		}
		return m, nil, true
	}
	if p.kind == pickerEffort {
		// The ladder has no search, so "s" keeps its shortcut meaning with or
		// without Alt. Arrows and Backspace fall through to the shared picker
		// keys; everything else is swallowed so it never reaches the composer.
		if keyIs(key, tea.KeyEnter) || strings.EqualFold(keyText(key), "s") {
			next, cmd := m.applyPanelChoice(keyIs(key, tea.KeyEnter))
			return next, cmd, true
		}
		if keyIs(key, tea.KeyLeft) || keyIs(key, tea.KeyRight) {
			delta := 1
			if keyIs(key, tea.KeyLeft) {
				delta = -1
			}
			p.selected = clampInt(p.selected+delta, 0, len(p.items)-1)
			return m, nil, true
		}
		if keyIs(key, tea.KeyUp) || keyIs(key, tea.KeyDown) || keyBackspace(key) {
			return m, nil, false
		}
		return m, nil, true
	}
	// Model palette from here on.
	if m.modelPickerIsLoading() {
		return m, nil, true
	}
	if !p.panel {
		// The registry fallback keeps the generic popup behavior (global keys).
		return m, nil, false
	}
	// The palette searches as you type, so letter shortcuts moved to Alt:
	// Alt+s applies to the session, Alt+f toggles the favorite (Ctrl+f kept
	// as a synonym). Plain letters fall through to the shared query filter.
	if keyAlt(key) {
		switch {
		case strings.EqualFold(keyText(key), "s"):
			next, cmd := m.applyPanelChoice(false)
			return next, cmd, true
		case strings.EqualFold(keyText(key), "f"):
			return m.toggleModelFavorite(), nil, true
		}
		return m, nil, true
	}
	if keyIs(key, tea.KeyTab) {
		// Zoom into the marked model's levels; Esc returns to the palette.
		if item, ok := p.current(); ok && strings.TrimSpace(item.Value) != "" {
			preselect := p.effort
			if preselect == "" {
				preselect = m.effortDisplay()
			}
			m.picker = m.newEffortPickerFor(item.Value, preselect, p)
		}
		return m, nil, true
	}
	if keyIs(key, tea.KeyEnter) {
		next, cmd := m.applyPanelChoice(true)
		return next, cmd, true
	}
	if keyIs(key, tea.KeyLeft) || keyIs(key, tea.KeyRight) {
		delta := 1
		if keyIs(key, tea.KeyLeft) {
			delta = -1
		}
		if item, ok := p.current(); ok {
			preview := m
			preview.modelName = item.Value
			if owner, ok := m.savedProviderByName(item.OwnerProvider); ok {
				preview.providerProfile = owner
				preview.providerName = owner.Name
			}
			efforts := preview.newEffortPicker()
			value := p.effort
			if value == "" {
				value = m.effortDisplay()
			}
			index := 0
			for i, item := range efforts.items {
				if item.Value == value {
					index = i
				}
			}
			p.effort = efforts.items[clampInt(index+delta, 0, len(efforts.items)-1)].Value
		}
		return m, nil, true
	}
	// ↑/↓ move, Backspace deletes, printable keys filter: shared handling.
	return m, nil, false
}

func (m model) applyPanelChoice(persist bool) (model, tea.Cmd) {
	p := m.picker
	if m.modelPickerIsLoading() {
		return m, nil
	}
	item, ok := p.current()
	if !ok {
		return m, nil
	}
	if m.pending {
		return m, nil
	}
	var cmd tea.Cmd
	var err error
	detail := ""
	if p.kind == pickerModel {
		if owner := strings.TrimSpace(item.OwnerProvider); owner != "" && !strings.EqualFold(owner, strings.TrimSpace(m.providerName)) {
			return m.showTransientNoticeInline(qgLoginGuidance(), transientNoticeWarning), nil
		}
		preview := m
		preview.modelName = item.Value
		if p.effort != "" && p.effort != "auto" && !reasoningEffortAllowed(preview.panelReasoningEfforts(), modelregistry.ReasoningEffort(p.effort)) {
			return m.showTransientNoticeInline("Nível indisponível para o modelo selecionado", transientNoticeWarning), nil
		}
		var switched bool
		m, detail, switched, err = m.handleModelCommandScope(item.Value, persist)
		if !switched {
			return m.showTransientNoticeInline(detail, transientNoticeWarning), nil
		}
		if m.modelName != item.Value {
			return m.showTransientNoticeInline(detail, transientNoticeWarning), cmd
		}
		if p.effort != "" {
			m, _ = m.handleEffortCommand(p.effort)
		} else {
			m = m.loadPanelEffort()
		}
	} else if target := strings.TrimSpace(p.effortModel); target != "" && !strings.EqualFold(target, strings.TrimSpace(m.modelName)) {
		// Tab ladder over a marked model: switch to it, then set the level.
		var switched bool
		m, detail, switched, err = m.applyModelEffortSwitch(target, item.Value, persist)
		if !switched {
			return m.showTransientNoticeInline(detail, transientNoticeWarning), nil
		}
	} else {
		m, _ = m.handleEffortCommand(item.Value)
	}
	if persist && p.kind == pickerModel && m.userConfigPath != "" && err == nil {
		saved, checkErr := config.ProviderPersisted(m.userConfigPath, m.providerProfile.Name)
		if checkErr != nil {
			err = checkErr
		} else if !saved {
			err = fmt.Errorf("provedor não cadastrado; modelo não salvo como padrão")
		}
	}
	if persist && m.userConfigPath != "" && err == nil {
		_, err = config.SetReasoningEffort(m.userConfigPath, m.effortPreferenceKey(), m.effortDisplay())
	}
	m.picker = nil
	m.clearModelPickerLoadState()
	notice := "Aplicado só nesta sessão"
	tone := transientNoticeSuccess
	if persist {
		notice = "Padrão salvo"
		if m.userConfigPath == "" {
			notice = "Aplicado à sessão; configuração indisponível"
		}
	}
	if err != nil {
		notice = "Aplicado à sessão; falha ao salvar padrão: " + err.Error()
		tone = transientNoticeWarning
	}
	return m.showTransientNoticeInline(notice, tone), cmd
}

// applyModelEffortSwitch switches to the ladder's marked model and sets the
// chosen level on top, mirroring the palette branch (provider persistence
// failure surfaces through err for the shared notice tail).
func (m model) applyModelEffortSwitch(target, effort string, persist bool) (model, string, bool, error) {
	m, detail, switched, err := m.handleModelCommandScope(target, persist)
	if !switched {
		return m, detail, false, err
	}
	if strings.TrimSpace(effort) != "" {
		m, _ = m.handleEffortCommand(effort)
	} else {
		m = m.loadPanelEffort()
	}
	if persist && m.userConfigPath != "" && err == nil {
		saved, checkErr := config.ProviderPersisted(m.userConfigPath, m.providerProfile.Name)
		if checkErr != nil {
			err = checkErr
		} else if !saved {
			err = fmt.Errorf("provedor não cadastrado; modelo não salvo como padrão")
		}
	}
	return m, detail, true, err
}

func (m model) fetchPanelAccount() tea.Cmd {
	p := m.picker
	if m.providerProfile.CatalogID != "qg" {
		return func() tea.Msg {
			return usageAccountResult{picker: p, err: fmt.Errorf("conta disponível apenas no provedor QG")}
		}
	}
	profile := m.profileWithCredential(m.providerProfile)
	client := qgaccount.NewClient(profile.APIKey)
	if profile.BaseURL != "" {
		client.BaseURL = profile.BaseURL
	}
	ctx := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		report, err := client.Usage(ctx, qgaccount.UsageParams{Scope: "user"})
		balance, balanceErr := client.Balance(ctx)
		return usageAccountResult{picker: p, usage: report, balance: balance, err: err, balanceErr: balanceErr}
	}
}

func (m model) usagePanelOverlay(width int) string {
	tabs := "[Sessão]   Conta"
	if m.picker.usageTab == 1 {
		tabs = "Sessão   [Conta]"
	}
	lines := []string{tabs, ""}
	if m.picker.usageTab == 0 {
		lines = append(lines, "Última chamada")
		if m.lastUsageSeen {
			u := m.lastUsage
			lines = append(lines, "Modelo: "+displayValue(m.lastUsageReceipt.meta["model"], "não informado"),
				"Entrada: "+formatUsageTokens(u.InputTokens)+" · saída: "+formatUsageTokens(u.OutputTokens),
				"Cache lido (incluído): "+formatUsageTokens(u.CachedInputTokens),
				"Total: "+formatUsageTokens(u.TotalTokens), "Custo: "+callCostText(m.lastUsageReceipt))
		} else {
			lines = append(lines, "Uso não informado")
		}
		lines = append(lines, "", "Modelo ativo: "+m.modelName)
		if window, _ := m.contextWindowDisplay(); window > 0 {
			lines = append(lines, "Janela: "+formatUsageTokens(window)+" tokens")
			if m.lastUsageSeen && m.lastUsageReceipt.meta["model"] == m.modelName && m.lastUsage.InputTokens > 0 {
				percent := float64(m.lastUsage.InputTokens) / float64(window) * 100
				value := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", percent), "0"), ".")
				lines = append(lines, "Uso da janela: "+strings.ReplaceAll(value, ".", ",")+"% ("+formatUsageTokens(m.lastUsage.InputTokens)+" / "+formatUsageTokens(window)+")")
			}
		} else {
			lines = append(lines, "Janela: não informado pela API")
		}
	} else if result := m.picker.account; result == nil {
		lines = append(lines, "Consultando conta…")
	} else {
		if result.err != nil {
			lines = append(lines, "Uso indisponível: "+result.err.Error())
		} else {
			u := result.usage
			count := func(n int64) string { return qgaccount.GroupThousands(strconv.FormatInt(n, 10)) }
			lines = append(lines, "Período: "+u.From+" a "+u.To, "Escopo: "+u.Scope, "")
			for _, row := range [][2]string{{"Chamadas:", count(u.Requests)}, {"Entrada:", count(u.InputTokens)}, {"Saída:", count(u.OutputTokens)}, {"Cache lido (incluído):", count(u.CachedInputTokens)}, {"Total:", count(u.Total())}} {
				lines = append(lines, fmt.Sprintf("%-24s%s", row[0], row[1]))
			}
			lines = append(lines, "", "Custo no período: indisponível", "A API não detalha custo nem uso por modelo.")
		}
		if result.balanceErr != nil {
			lines = append(lines, "Saldo indisponível")
		} else if result.balance.Currency == "BRL" {
			if balance, err := qgaccount.FormatBRL(result.balance.Available); err == nil {
				lines = append(lines, fmt.Sprintf("%-24s%s", "Saldo disponível:", balance))
			}
		}
	}
	lines = append(lines, "", "←/→ ou Tab alternar · Esc fechar")
	return m.panelBlock(width, "Uso", lines)
}

func panelListStart(total, count, selected int) int {
	start := selectableListStart(total, count, selected)
	if selected >= start+count {
		start = selected - count + 1
	}
	return maxInt(0, start)
}

func (m *model) selectEffortPanelAtMouse(msg tea.MouseMsg) (mouseSelectionTarget, bool) {
	width := chatWidth(m.width)
	overlay := m.effortLadderOverlay(width)
	hit, ok := m.overlayMouseHit(msg, overlay, width)
	// Ladder rows start after the top border, the "Nível" title and the
	// accepted-range line; auto is row 0, one row per level after it.
	index := hit.y - 3
	if !ok || index < 0 || index >= len(m.picker.items) {
		return mouseSelectionTarget{}, false
	}
	m.picker.selected = index
	item := m.picker.items[index]
	return mouseSelectionTarget{Scope: "picker", Kind: int(pickerEffort), Value: item.Value, Index: index}, true
}
