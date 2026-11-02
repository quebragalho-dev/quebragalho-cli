// keybinding_help.go renders the `?` keyboard-shortcut overlay. QG has a
// rich set of chord bindings (Ctrl+T effort, drill-in subchat,
// Shift+Tab permission mode, …) that are otherwise invisible — only learnable
// by reading the source. A single-key `?` overlay (opened on an empty composer)
// lists them grouped, so the keymap is discoverable the way the reference TUIs
// do it. The list is declarative and hand-curated to match the real handlers in
// model.go's Update switch; keep them in sync when a binding changes.
//
// Configurable bindings (toggleDetailed, toggleMouse, cycleReasoning,
// toggleSidebar) pull their key label from m.keyBindings so a user
// who remaps them in config.json sees the actual chords, not the defaults.
package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// keybinding is one row in the help overlay: the key chord and what it does.
type keybinding struct {
	keys string
	desc string
}

// keybindingGroup is a titled cluster of related bindings.
type keybindingGroup struct {
	title    string
	bindings []keybinding
}

// buildKeybindingGroups returns the full, grouped shortcut list shown by the
// `?` overlay. Configurable bindings use the user's remapped labels (via
// m.keyBindings) when present; unconfigured bindings get their built-in labels.
// Sourced from the real key cases in model.go (Update) — not invented. When a
// binding is added/changed there, update this method too.
func (m model) buildKeybindingGroups() []keybindingGroup {
	ctrlCDescription := "cancela a execução e sai"
	if m.btw.active {
		ctrlCDescription = "volta à sessão principal se o BTW estiver parado"
	}
	return []keybindingGroup{
		{
			title: "Chat",
			bindings: []keybinding{
				{"Enter", "envia a mensagem"},
				{"Shift+Enter / Alt+Enter", "insere uma quebra de linha (mensagem multilinha)"},
				{"Esc (\u00d72)", "cancela a execução / fecha o pop-up / limpa a entrada"},
				{"Ctrl+C", ctrlCDescription},
				{"Ctrl+X then letter", "comandos com / comuns (m=/model, r=/resume, \u2026)"},
				{"Ctrl+X ?", "lista todos os atalhos Ctrl+X com /"},
				{"?", "mostra esta ajuda (com a entrada vazia)"},
			},
		},
		{
			title: "Modelo e execução",
			bindings: []keybinding{
				{labelOr(m.keyBindings.cycleReasoning, "Ctrl+T"), "alterna o nível de raciocínio (auto \u2192 low \u2192 medium \u2192 high)"},
				{"Shift+Tab", "alterna o modo de permissão (auto \u2194 ask)"},
			},
		},
		{
			title: "Navegação e rolagem",
			bindings: []keybinding{
				{"PgUp / PgDn", "rola o histórico por página"},
				{"\u2191 / \u2193", "rola ou move dentro do pop-up / entrada multilinha"},
				{"Ctrl+P / Ctrl+N", "item anterior / próximo nos menus (estilo emacs)"},
				{labelOr(m.keyBindings.toggleDetailed, "Ctrl+O"), "alterna o histórico detalhado (tela cheia)"},
				{labelOr(m.keyBindings.toggleSidebar, "Ctrl+B"), "mostra / oculta os detalhes da execução"},
				{labelOr(m.keyBindings.toggleMouse, "Ctrl+E"), "libera o mouse para selecionar texto com arrasto e copiar"},
				{"Tab", "confirma a seleção do autocompletar / seletor"},
			},
		},
		{
			title: "Especialistas e seletores",
			bindings: []keybinding{
				{"Click a specialist card", "entra na subsessão dele"},
				{"\u2191 / Esc (in a sub-session)", "volta para o chat principal"},
				{"Ctrl+F (in /model)", "alterna o modelo destacado como favorito"},
				{"Click a tool card", "expande / recolhe a saída dele"},
				{"Right-click", "cola a área de transferência"},
			},
		},
	}
}

// keybindingHelpFooter is the dismiss hint shown at the bottom of the overlay.
const keybindingHelpFooter = "? ou Esc para fechar \u00b7 /help para comandos com /"

// renderKeybindingHelpLines builds the overlay body lines (group titles +
// aligned key/description rows + footer), wrapped to the inner width. Exposed
// separately from the framed renderer so tests can assert on content without
// the border chrome.
func (m model) renderKeybindingHelpLines(innerWidth int) []string {
	groups := m.buildKeybindingGroups()
	keyColumn := keybindingKeyColumnWidth(groups)
	lines := make([]string, 0, 64)
	for index, group := range groups {
		if index > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, qgTheme.accent.Render(group.title))
		for _, binding := range group.bindings {
			lines = append(lines, formatKeybindingLine(binding, keyColumn, innerWidth))
		}
	}
	lines = append(lines, "")
	lines = append(lines, qgTheme.faint.Render(keybindingHelpFooter))
	return lines
}

// keybindingKeyColumnWidth returns the width of the key column: the widest key
// chord across all groups, so the descriptions align in a clean second column.
func keybindingKeyColumnWidth(groups []keybindingGroup) int {
	widest := 0
	for _, group := range groups {
		for _, binding := range group.bindings {
			if n := len([]rune(binding.keys)); n > widest {
				widest = n
			}
		}
	}
	return widest
}

// formatKeybindingLine renders one "  <keys>   <desc>" row: the key chord in
// the accent color, padded to keyColumn, then the muted description truncated
// to fit innerWidth.
func formatKeybindingLine(binding keybinding, keyColumn int, innerWidth int) string {
	keys := binding.keys
	pad := keyColumn - len([]rune(keys))
	if pad < 0 {
		pad = 0
	}
	keyCell := qgTheme.ink.Render(keys) + strings.Repeat(" ", pad)
	// Indent(2) + keyCell + gap(2) consumed before the description.
	descBudget := innerWidth - 2 - keyColumn - 2
	if descBudget < 4 {
		descBudget = 4
	}
	desc := qgTheme.muted.Render(truncateRunes(binding.desc, descBudget))
	return "  " + keyCell + "  " + desc
}

// the given terminal width. Vertical centering is handled by the caller's
// overlay compositing pipeline (overlayViewportLines in transcriptView).
func (m model) renderKeybindingHelpOverlay(width int) string {
	overlayWidth := keybindingHelpOverlayWidth(width)
	lines := m.renderKeybindingHelpLines(overlayWidth - 4)
	block := styledBlockFillTitle(overlayWidth, "Keyboard Shortcuts", lines, qgTheme.line, lipgloss.NewStyle())
	return centerRenderedBlock(block, width)
}

// keybindingHelpOverlayWidth picks the overlay width: wide enough that the
// descriptions don't truncate next to the key column, capped, and never wider
// than the terminal.
func keybindingHelpOverlayWidth(terminalWidth int) int {
	width := 76
	if terminalWidth-4 < width {
		width = terminalWidth - 4
	}
	if width < 24 {
		width = 24
	}
	return width
}
