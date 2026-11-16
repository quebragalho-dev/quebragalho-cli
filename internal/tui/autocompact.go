package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
)

// autoCompactUsageText is shown when /autocompact gets an argument the parser
// rejects. It states the accepted forms, not the current setting (see the
// status text for that).
const autoCompactUsageText = "Auto-compact\nUso: /autocompact <n|off|auto>. n é o limite em tokens, a partir de 20k (ex.: /autocompact 400k). off desliga; auto volta ao automático."

// handleAutoCompactCommand runs /autocompact: an empty argument opens the
// panel, "status" shows the current setting, anything else is parsed and
// applied as the session limit.
func (m model) handleAutoCompactCommand(arg string) (model, tea.Cmd) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" {
		return m.openAutoCompactPrompt(), nil
	}
	if strings.EqualFold(trimmed, "status") {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.autoCompactStatusText()})
		return m, nil
	}
	limit, err := config.ParseAutoCompactLimit(trimmed)
	if err != nil {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: autoCompactUsageText})
		return m, nil
	}
	var notice string
	m, notice, err = m.applyAutoCompactLimit(limit)
	if err != nil {
		return m.appendSystemNotice(notice), nil
	}
	return m.showTransientNoticeInline(notice, transientNoticeSuccess), nil
}

// applyAutoCompactLimit sets the session limit and persists it to the user
// config, mirroring persistRecapsEnabled. The session value applies even when
// saving fails; the failure is reported as part of the returned text so the
// caller can surface it as a system notice instead of a transient one.
func (m model) applyAutoCompactLimit(limit int) (model, string, error) {
	m.autoCompactLimit = limit
	text := "Auto-compact: " + m.autoCompactSummary() + "."
	if m.pending {
		text += " Vale a partir da próxima mensagem."
	}
	if strings.TrimSpace(m.userConfigPath) == "" {
		return m, text, nil
	}
	if _, err := config.SetAutoCompactLimit(m.userConfigPath, limit); err != nil {
		return m, text + " Não foi possível salvar na configuração: " + err.Error(), err
	}
	return m, text, nil
}

// autoCompactSummaryParts describes the effective setting as a value and an
// optional note: the automatic trigger for the active model, the chosen
// limit, the clamped form when the window cannot hold the limit, or off.
// Model-derived numbers go through formatContextWindow (lowercased to match
// the k/m shorthand style) and the user's limit through
// config.FormatAutoCompactLimit. Each part stays short enough for one line
// of the panel at its minimum width.
func (m model) autoCompactSummaryParts() (value string, note string) {
	window := modelregistry.AgentContextWindow(m.modelContextWindow(m.modelName))
	effective := agent.AutoCompactThreshold(window, m.autoCompactLimit)
	switch {
	case m.autoCompactLimit < 0:
		return "desligado", "só compacta se o provedor recusar por excesso"
	case m.autoCompactLimit == 0:
		return "automático", "compacta em " + strings.ToLower(formatContextWindow(effective)) + " tokens neste modelo"
	case effective == m.autoCompactLimit:
		return config.FormatAutoCompactLimit(m.autoCompactLimit) + " tokens", ""
	default:
		return config.FormatAutoCompactLimit(m.autoCompactLimit) + " tokens", "neste modelo compacta em " +
			strings.ToLower(formatContextWindow(effective)) + "; a janela é de " + strings.ToLower(formatContextWindow(window))
	}
}

// autoCompactSummary is the one-line form of autoCompactSummaryParts, used
// by the notices and the status texts.
func (m model) autoCompactSummary() string {
	value, note := m.autoCompactSummaryParts()
	if note == "" {
		return value
	}
	return value + " (" + note + ")"
}

// autoCompactStatusText is the /autocompact status card: the current setting
// plus how to change it.
func (m model) autoCompactStatusText() string {
	return "Auto-compact\nLimite: " + m.autoCompactSummary() + ".\nMude com /autocompact <n|off|auto> (ex.: /autocompact 400k)."
}

// autoCompactStep is the arrow-key step of the /autocompact panel; steps stay
// on exact multiples so typing and stepping compose ("120k" + Up = "150k").
// autoCompactStepMax bounds the panel: no model window is near it.
const autoCompactStep = 50_000
const autoCompactStepMax = 2_000_000

// autoCompactPromptMaxRunes caps the panel input: 12 runes hold any readable
// limit ("2000000" is 7, "100000000" is 9) without room for pasted noise.
const autoCompactPromptMaxRunes = 12

// autoCompactPromptInvalidText is the panel's rejection line. It must fit
// the minimum overlay width (52 cells of content) so it is never cut.
const autoCompactPromptInvalidText = "Valor inválido: número a partir de 20k, off ou auto."

// autoCompactPromptState is the panel opened by a bare /autocompact: a short
// explanation, the current setting and one input line. The input holds what
// Enter will apply ("400k", "off", "auto"); the arrow keys rewrite it in
// 50k steps.
type autoCompactPromptState struct {
	input string
	err   string // shown under the input after a rejected Enter; cleared by any edit
}

// openAutoCompactPrompt opens the /autocompact panel. A positive session limit
// prefills the input; anything else starts empty. Opening is allowed while a
// run is in progress: the limit is read when the next run starts.
func (m model) openAutoCompactPrompt() model {
	input := ""
	if m.autoCompactLimit > 0 {
		input = config.FormatAutoCompactLimit(m.autoCompactLimit)
	}
	m.autoCompactPrompt = &autoCompactPromptState{input: input}
	m.clearSuggestions()
	return m
}

// closeAutoCompactPromptDismiss closes the panel without applying anything.
func (m model) closeAutoCompactPromptDismiss() (model, tea.Cmd) {
	m.autoCompactPrompt = nil
	return m.showTransientNoticeInline("Auto-compact: nada alterado.", transientNoticeInfo), nil
}

// stepAutoCompactLimit moves a panel limit one 50k step: up returns the next
// multiple of 50k strictly above current (capped at autoCompactStepMax),
// down the previous multiple strictly below it (never below 50k). A
// non-positive current (empty, off, auto or invalid input) steps from zero,
// so both directions first land on 50k.
func stepAutoCompactLimit(current int, up bool) int {
	if up {
		stepped := (current/autoCompactStep + 1) * autoCompactStep
		if stepped < autoCompactStep {
			stepped = autoCompactStep
		}
		if stepped > autoCompactStepMax {
			stepped = autoCompactStepMax
		}
		return stepped
	}
	stepped := ((current - 1) / autoCompactStep) * autoCompactStep
	if stepped < autoCompactStep {
		stepped = autoCompactStep
	}
	return stepped
}

// autoCompactPromptCurrent reads the panel input as a step base: the positive
// number it parses to, or 0 when it is empty, off, auto or invalid.
func (p *autoCompactPromptState) current() int {
	limit, err := config.ParseAutoCompactLimit(p.input)
	if err != nil || limit <= 0 {
		return 0
	}
	return limit
}

// appendAutoCompactInput appends text to the panel input, truncating to the
// rune cap. Any edit clears a previous rejection error.
func (p *autoCompactPromptState) appendInput(text string) {
	runes := []rune(p.input + text)
	if len(runes) > autoCompactPromptMaxRunes {
		runes = runes[:autoCompactPromptMaxRunes]
	}
	p.input = string(runes)
	p.err = ""
}

// handleAutoCompactPromptKey captures typing for the /autocompact panel. It
// fully owns the keystroke while the panel is open: typing edits the input,
// arrows step it, Enter applies, Esc/Ctrl+C dismiss without changing anything.
func (m model) handleAutoCompactPromptKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.autoCompactPrompt
	switch {
	case keyIs(msg, tea.KeyEsc), keyCtrl(msg, 'c'):
		return m.closeAutoCompactPromptDismiss()
	case keyIs(msg, tea.KeyEnter):
		trimmed := strings.TrimSpace(p.input)
		if trimmed == "" {
			return m.closeAutoCompactPromptDismiss()
		}
		limit, err := config.ParseAutoCompactLimit(trimmed)
		if err != nil {
			p.err = autoCompactPromptInvalidText
			return m, nil
		}
		var notice string
		m, notice, err = m.applyAutoCompactLimit(limit)
		m.autoCompactPrompt = nil
		if err != nil {
			return m.appendSystemNotice(notice), nil
		}
		return m.showTransientNoticeInline(notice, transientNoticeSuccess), nil
	case keyIs(msg, tea.KeyUp), keyIs(msg, tea.KeyRight):
		p.input = config.FormatAutoCompactLimit(stepAutoCompactLimit(p.current(), true))
		p.err = ""
		return m, nil
	case keyIs(msg, tea.KeyDown), keyIs(msg, tea.KeyLeft):
		p.input = config.FormatAutoCompactLimit(stepAutoCompactLimit(p.current(), false))
		p.err = ""
		return m, nil
	case keyBackspace(msg):
		if r := []rune(p.input); len(r) > 0 {
			p.input = string(r[:len(r)-1])
		}
		p.err = ""
		return m, nil
	case keyCtrl(msg, 'u'):
		p.input = ""
		p.err = ""
		return m, nil
	default:
		if keyPrintable(msg) {
			p.appendInput(keyText(msg))
		}
		return m, nil
	}
}

// autoCompactPromptOverlay renders the /autocompact panel as a centered modal,
// matching the cloud-STT key prompt's box. Every line stays at most 52 cells
// so it survives the minimum overlay width; the current setting takes two
// lines (value, then its note) for that reason.
func (m model) autoCompactPromptOverlay(width int) string {
	if m.autoCompactPrompt == nil {
		return ""
	}
	p := m.autoCompactPrompt
	overlayWidth := minInt(width, pickerOverlayMaxWidth)
	if overlayWidth < pickerOverlayMinWidth {
		overlayWidth = width
	}
	value := qgTheme.accent.Render("▌") + qgTheme.faint.Render("400k")
	if p.input != "" {
		value = qgTheme.ink.Render(p.input) + qgTheme.accent.Render("▌")
	}
	input := qgTheme.userPrompt.Render("limite > ") + value
	lines := []string{
		qgTheme.ink.Render("Compacta a conversa sozinho ao chegar perto do"),
		qgTheme.ink.Render("limite escolhido, mesmo que o modelo aceite mais."),
		qgTheme.ink.Render("As mensagens antigas viram um resumo."),
		qgTheme.ink.Render("Exemplo: /autocompact 400k"),
		"",
	}
	current, note := m.autoCompactSummaryParts()
	lines = append(lines, qgTheme.ink.Render("Atual: "+current))
	if note != "" {
		lines = append(lines, qgTheme.faint.Render("("+note+")"))
	}
	lines = append(lines, "", input)
	if p.err != "" {
		lines = append(lines, qgTheme.red.Render(p.err), "")
	} else {
		lines = append(lines, "")
	}
	lines = append(lines,
		qgTheme.faint.Render("↑/↓ 50k em 50k · número, off ou auto"),
		qgTheme.faint.Render("Enter salva · Esc cancela"),
	)
	return centerRenderedBlock(styledBlockFillTitle(overlayWidth, "Auto-compact", lines, qgTheme.lineStrong, lipgloss.NewStyle()), width)
}
