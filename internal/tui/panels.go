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
	"github.com/charmbracelet/x/ansi"
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

func (m model) effortPreferenceKey() string { return m.providerName + "/" + m.modelName }

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
		items = append(items, pickerItem{Label: label, Value: info.ID, OwnerProvider: m.providerName, Meta: price, Detail: "bônus " + bonus + " · " + caps})
		if info.ID == m.modelName {
			selected = len(items) - 1
		}
	}

	if len(items) == 0 {
		return nil
	}
	return &commandPicker{kind: pickerModel, title: "Selecionar modelo", items: items, allItems: append([]pickerItem{}, items...), selected: selected}
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

func (m model) effortPanelLines(width int, value string) []string {
	labels := []string{}
	supported := m.panelReasoningEfforts()
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		label := " " + level + " "
		if !reasoningEffortAllowed(supported, modelregistry.ReasoningEffort(level)) {
			label = "(" + level + ")"
		}
		if level == value {
			label = "[" + level + "]"
		}
		labels = append(labels, label)
	}
	gap := "  "
	if width < 58 {
		gap = " "
	}
	labelLine := strings.Join(labels, gap)
	track := []rune(strings.Repeat("─", lipgloss.Width(labelLine)))
	if value != "auto" {
		if at := strings.Index(labelLine, "["+value+"]"); at >= 0 {
			track[at+1+len(value)/2] = '▲'
		}
	}
	lines := []string{"Faster" + strings.Repeat(" ", maxInt(1, len(track)-13)) + "Smarter", string(track), labelLine, "effort: " + value}
	return lines
}

func (m model) effortPanelOverlay(width int) string {
	value := "auto"
	if item, ok := m.picker.current(); ok {
		value = item.Value
	}
	lines := m.effortPanelLines(width, value)
	lines = append(lines, "", "←/→ ajustar · Enter padrão", "s só nesta sessão · Esc cancelar")
	return m.panelBlock(width, "Effort", lines)
}

func (m model) modelPanelOverlay(width int) string {
	if m.picker.title != "Selecionar modelo" {
		return m.modelPickerOverlay(width)
	}
	if m.modelPickerLoading {
		return m.modelPickerLoadingOverlay(width)
	}
	p := m.picker
	lines := []string{"Modelos · " + strconv.Itoa(len(p.items)) + " disponíveis", truncateDisplayWidth("Busca: "+p.query, maxInt(1, minInt(width, 100)-4)), ""}
	if len(p.items) == 0 {
		lines = append(lines, "Catálogo API ainda não disponível.")
	}
	count := minInt(len(p.items), maxInt(1, minInt(8, m.height-18)))
	start := panelListStart(len(p.items), count, p.selected)
	for i := start; i < start+count; i++ {
		item := p.items[i]
		mark := "  "
		if i == p.selected {
			mark = "› "
		}
		lines = append(lines, truncateDisplayWidth(mark+strconv.Itoa(i+1)+". "+item.Label, maxInt(1, minInt(width, 100)-4)))
	}
	if item, ok := p.current(); ok {
		lines = append(lines, "", item.Value, item.Meta, item.Detail)
		value := p.effort
		if value == "" {
			value = m.effortDisplay()
		}
		lines = append(lines, "", "effort: "+value+" · ←/→ ajustar")
	}
	lines = append(lines, "", "↑/↓ mover · / buscar · Tab lista · Enter padrão", "s só nesta sessão · Esc cancelar")
	return m.panelBlock(width, "Selecionar modelo", lines)
}

func (m model) handlePanelKey(key tea.KeyPressMsg) (model, tea.Cmd, bool) {
	p := m.picker
	if p == nil || (p.kind != pickerUsage && p.kind != pickerEffort && p.kind != pickerModel) {
		return m, nil, false
	}
	if keyIs(key, tea.KeyEsc) {
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
	if p.kind == pickerModel && p.searching && keyIs(key, tea.KeyTab) {
		p.searching = false
		return m, nil, true
	}
	if keyIs(key, tea.KeyLeft) || keyIs(key, tea.KeyRight) {
		delta := 1
		if keyIs(key, tea.KeyLeft) {
			delta = -1
		}
		if p.kind == pickerEffort {
			p.selected = clampInt(p.selected+delta, 0, len(p.items)-1)
		} else {
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
		}
		return m, nil, true
	}
	if p.kind == pickerModel && p.title == "Selecionar modelo" && keyText(key) == "/" && !p.searching {
		p.searching = true
		return m, nil, true
	}
	if keyIs(key, tea.KeyEnter) || (keyText(key) == "s" && !p.searching) {
		if p.kind == pickerModel && p.title != "Selecionar modelo" && keyIs(key, tea.KeyEnter) {
			next, cmd := m.choosePicker()
			return next.(model), cmd, true
		}
		next, cmd := m.applyPanelChoice(keyIs(key, tea.KeyEnter))
		return next, cmd, true
	}
	if p.kind == pickerEffort {
		return m, nil, true
	}
	if p.kind == pickerModel && p.title == "Selecionar modelo" && !p.searching && keyText(key) != "" {
		if n, err := strconv.Atoi(keyText(key)); err == nil && n > 0 && n <= len(p.items) {
			p.selected = n - 1
			p.effort = ""
		}
		return m, nil, true
	}
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
		preview := m
		preview.modelName = item.Value
		if owner, ok := m.savedProviderByName(item.OwnerProvider); ok {
			preview.providerProfile = owner
			preview.providerName = owner.Name
		}
		if p.effort != "" && p.effort != "auto" && !reasoningEffortAllowed(preview.panelReasoningEfforts(), modelregistry.ReasoningEffort(p.effort)) {
			return m.showTransientNoticeInline("Effort indisponível para o modelo selecionado", transientNoticeWarning), nil
		}
		if owner := item.OwnerProvider; owner != "" && !strings.EqualFold(owner, m.providerName) {
			var switched bool
			m, detail, switched, cmd, err = m.switchProviderModelScope(owner, item.Value, persist)
			if !switched {
				return m.showTransientNoticeInline(detail, transientNoticeWarning), cmd
			}
		} else {
			var switched bool
			m, detail, switched, err = m.handleModelCommandScope(item.Value, persist)
			if !switched {
				return m.showTransientNoticeInline(detail, transientNoticeWarning), cmd
			}
		}
		if m.modelName != item.Value {
			return m.showTransientNoticeInline(detail, transientNoticeWarning), cmd
		}
		if p.effort != "" {
			m, _ = m.handleEffortCommand(p.effort)
		} else {
			m = m.loadPanelEffort()
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
	overlay := m.effortPanelOverlay(width)
	hit, ok := m.overlayMouseHit(msg, overlay, width)
	if !ok || hit.y < 3 || hit.y > 4 {
		return mouseSelectionTarget{}, false
	}
	_, rows, _ := normalizeOverlayBlock(viewLines(overlay), width)
	line := ansi.Strip(rows[hit.y])
	if strings.Contains(line, "effort:") {
		return mouseSelectionTarget{}, false
	}
	for i, item := range m.picker.items {
		at := strings.Index(line, item.Value)
		if at < 0 {
			continue
		}
		x := lipgloss.Width(line[:at])
		if hit.x >= x && hit.x < x+len(item.Value) {
			m.picker.selected = i
			return mouseSelectionTarget{Scope: "picker", Kind: int(pickerEffort), Value: item.Value, Index: i}, true
		}
	}
	return mouseSelectionTarget{}, false
}
