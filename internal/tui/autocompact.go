package tui

import (
	"strings"

	"charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
)

// autoCompactUsageText is shown when /autocompact gets an argument the parser
// rejects. It states the accepted forms, not the current setting (see the
// status text for that).
const autoCompactUsageText = "Auto-compact\nUso: /autocompact <n|off|auto>. n é o limite em tokens, a partir de 20k (ex.: /autocompact 400k). off desliga; auto volta ao automático."

// handleAutoCompactCommand runs /autocompact through the production command
// path: an empty argument shows the current setting (the panel once part 3
// exists), "status" does the same explicitly, anything else is parsed and
// applied as the session limit.
func (m model) handleAutoCompactCommand(arg string) (model, tea.Cmd) {
	trimmed := strings.TrimSpace(arg)
	if trimmed == "" || strings.EqualFold(trimmed, "status") {
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

// autoCompactSummary describes the effective setting: the automatic trigger
// for the active model, the chosen limit, the clamped form when the window
// cannot hold the limit, or off. Model-derived numbers go through
// formatContextWindow (lowercased to match the k/m shorthand style) and the
// user's limit through config.FormatAutoCompactLimit.
func (m model) autoCompactSummary() string {
	window := modelregistry.AgentContextWindow(m.modelContextWindow(m.modelName))
	effective := agent.AutoCompactThreshold(window, m.autoCompactLimit)
	switch {
	case m.autoCompactLimit < 0:
		return "desligado (só compacta se o provedor recusar por excesso de contexto)"
	case m.autoCompactLimit == 0:
		return "automático (compacta em " + strings.ToLower(formatContextWindow(effective)) + " tokens neste modelo)"
	case effective == m.autoCompactLimit:
		return config.FormatAutoCompactLimit(m.autoCompactLimit) + " tokens"
	default:
		return config.FormatAutoCompactLimit(m.autoCompactLimit) + " tokens (neste modelo compacta em " +
			strings.ToLower(formatContextWindow(effective)) + ", o que cabe na janela de " + strings.ToLower(formatContextWindow(window)) + ")"
	}
}

// autoCompactStatusText is the /autocompact status card: the current setting
// plus how to change it.
func (m model) autoCompactStatusText() string {
	return "Auto-compact\nLimite: " + m.autoCompactSummary() + ".\nMude com /autocompact <n|off|auto> (ex.: /autocompact 400k)."
}
