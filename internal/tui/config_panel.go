package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// configRowValue identifies a /config panel row. It is passed through the
// generic pickerItem.Value slot so Enter can route back to the setting the
// row was built from.
const (
	configRowProvider    = "provedor"
	configRowModel       = "modelo"
	configRowEffort      = "nivel"
	configRowAutoCompact = "autocompact"
	configRowPermission  = "permissao"
	configRowVoice       = "voz"
	configRowSTT         = "ditado"
	configRowTheme       = "tema"
	configRowStatusline  = "statusline"
	configRowStyle       = "estilo"
	configRowRecaps      = "recaps"
	configRowMCP         = "mcp"
	configRowAPIKey      = "chave-api"
)

// ligadoDesligado renders a toggle state as "ligado"/"desligado" for the
// /config panel rows that flip in place (voice mode, recaps).
func ligadoDesligado(v bool) string {
	if v {
		return "ligado"
	}
	return "desligado"
}

// newConfigPicker builds the unified /config panel: one searchable list over
// the generic commandPicker, grouped by section, with the live value of every
// setting on the right (Meta). It reuses the pickerOverlay frame (92/56,
// lineStrong, "buscar > " search line, "❯ " marker) instead of a new frame.
func (m model) newConfigPicker() *commandPicker {
	permissionLabel, _ := m.modeLabel()
	items := []pickerItem{
		{Group: "MODELO", Label: "Provedor", Value: configRowProvider, Meta: displayValue(m.providerName, "nenhum")},
		{Group: "MODELO", Label: "Modelo", Value: configRowModel, Meta: displayValue(m.modelName, "nenhum")},
		{Group: "MODELO", Label: "Nível de raciocínio", Value: configRowEffort, Meta: effortDisplayLabel(m.effortDisplay())},
		{Group: "MODELO", Label: "Resumir conversa a partir de", Value: configRowAutoCompact, Meta: m.configAutoCompactValue()},
		{Group: "EXECUÇÃO", Label: "Modo de permissão", Value: configRowPermission, Meta: permissionLabel, Detail: "Shift+Tab alterna"},
		{Group: "VOZ", Label: "Modo de voz", Value: configRowVoice, Meta: ligadoDesligado(m.dictation.voiceModeEnabled)},
		{Group: "VOZ", Label: "Modelo de ditado", Value: configRowSTT, Meta: m.dictation.currentModelLabel()},
		{Group: "APARÊNCIA", Label: "Tema", Value: configRowTheme, Meta: m.themeDisplayLabel()},
		{Group: "APARÊNCIA", Label: "Barra de status", Value: configRowStatusline, Meta: m.configStatuslineValue()},
		{Group: "APARÊNCIA", Label: "Estilo de resposta", Value: configRowStyle, Meta: responseStyleLabel(displayValue(m.responseStyle, defaultResponseStyle))},
		{Group: "APARÊNCIA", Label: "Resumo ao voltar", Value: configRowRecaps, Meta: ligadoDesligado(m.recapsEnabled)},
		{Group: "CONEXÕES", Label: "Servidores MCP", Value: configRowMCP, Meta: m.configMCPValue()},
		{Group: "CONEXÕES", Label: "Chave de API", Value: configRowAPIKey, Meta: m.configAPIKeyValue()},
	}
	return &commandPicker{
		kind:     pickerConfig,
		title:    "Configurações",
		items:    items,
		allItems: append([]pickerItem{}, items...),
	}
}

// configAutoCompactValue is the one-line panel readout for the auto-compact
// setting: the short value only, so the row fits narrow overlays.
func (m model) configAutoCompactValue() string {
	value, _ := m.autoCompactSummaryParts()
	return value
}

// configAPIKeyValue reports whether an API key is configured without ever
// revealing it: the same apiKeyState readout the old /config card used.
func (m model) configAPIKeyValue() string {
	set := strings.TrimSpace(m.providerProfile.APIKey) != "" || m.providerProfile.APIKeyStored
	return apiKeyState(set)
}

// configStatuslineValue summarizes the statusline customization without
// embedding the full layout or command.
func (m model) configStatuslineValue() string {
	if m.statuslineCfg.IsCustom() {
		return "personalizada"
	}
	return "padrão"
}

// configMCPValue counts the configured MCP servers for the panel readout.
func (m model) configMCPValue() string {
	if n := len(m.mcpViewState().Servers); n > 0 {
		return pluralCount(n, "server")
	}
	return "nenhum"
}

// refreshConfigPicker rebuilds the open /config panel from the current session
// state (after an in-place toggle), keeping the search query and selection.
func (m model) refreshConfigPicker() model {
	query := ""
	selected := ""
	if m.picker != nil && m.picker.kind == pickerConfig {
		query = m.picker.query
		if item, ok := m.picker.current(); ok {
			selected = item.Value
		}
	}
	m.picker = m.newConfigPicker()
	m.picker.query = query
	m.picker.applyQuery()
	m.selectPickerValue(selected)
	return m
}

// chooseConfigRow applies Enter on a /config panel row: the two toggle rows
// (recaps, voice mode) flip in place and persist; every other row closes the
// panel and re-dispatches the bare command that already owns that setting, so
// the existing selector/panel runs unchanged. Closing the follow-up surface
// with Esc (or applying it) lands back in the chat; reopening the panel is a
// new /config away.
func (m model) chooseConfigRow(item pickerItem) (tea.Model, tea.Cmd) {
	switch strings.TrimSpace(item.Value) {
	case configRowRecaps:
		m.recapsEnabled = !m.recapsEnabled
		if !m.recapsEnabled {
			m = m.cancelIdleRecap()
		}
		if err := m.persistRecapsEnabled(); err != nil {
			m = m.appendSystemNotice("Resumo ao voltar: falha ao salvar: " + err.Error())
		} else {
			m = m.showTransientNoticeInline("Resumo ao voltar "+ligadoDesligado(m.recapsEnabled)+".", transientNoticeSuccess)
		}
		return m.refreshConfigPicker(), nil
	case configRowVoice:
		next, cmd := m.toggleVoiceMode()
		m = next
		if m.picker != nil && m.picker.kind != pickerConfig {
			// Enabling voice without a configured backend opens its own
			// first-use/download panel: leave it up instead of the list.
			return m, cmd
		}
		return m.refreshConfigPicker(), cmd
	default:
		return m.openConfigRowTarget(item)
	}
}

// openConfigRowTarget closes the panel and runs the bare command that owns the
// row's setting, reusing its current handler (picker, panel, or status card).
func (m model) openConfigRowTarget(item pickerItem) (tea.Model, tea.Cmd) {
	m.picker = nil
	var target parsedCommand
	switch strings.TrimSpace(item.Value) {
	case configRowProvider, configRowModel:
		target = parsedCommand{kind: commandModel, name: "/model"}
	case configRowEffort:
		target = parsedCommand{kind: commandEffort, name: "/effort"}
	case configRowAutoCompact:
		target = parsedCommand{kind: commandAutoCompact, name: "/autocompact"}
	case configRowPermission:
		target = parsedCommand{kind: commandPermissions, name: "/permissions"}
	case configRowSTT:
		target = parsedCommand{kind: commandSTTModel, name: "/stt-model"}
	case configRowTheme:
		target = parsedCommand{kind: commandTheme, name: "/theme"}
	case configRowStatusline:
		target = parsedCommand{kind: commandStatusline, name: "/statusline"}
	case configRowStyle:
		target = parsedCommand{kind: commandStyle, name: "/style"}
	case configRowMCP:
		target = parsedCommand{kind: commandMCP, name: "/mcp"}
	case configRowAPIKey:
		target = parsedCommand{kind: commandProvider, name: "/provider"}
	default:
		return m, nil
	}
	return m.dispatchCommand(target)
}

// renderConfigPickerFooter renders the panel's single footer hint: keys in ink,
// the rest faint, following the paletteHints pattern.
func renderConfigPickerFooter(innerWidth int) string {
	sep := qgTheme.faint.Render(" · ")
	full := qgTheme.ink.Render("↑/↓") + qgTheme.faint.Render(" mover") + sep +
		qgTheme.ink.Render("Enter") + qgTheme.faint.Render(" alterar") + sep +
		qgTheme.faint.Render("digite para buscar") + sep +
		qgTheme.ink.Render("Esc") + qgTheme.faint.Render(" fechar")
	return fitStyledLine(full, innerWidth)
}
