package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/errhint"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

const goalContinuationPrompt = "Continue the active goal from where you stopped. Your earlier work and tool results are in the context above: do not re-read files or re-run checks you already did unless something changed since. If the objective is already achieved and verified, call update_goal with complete now. Otherwise make concrete progress and call update_goal when the objective is complete or genuinely blocked."

func (m model) handleGoalCommand(args string) (model, tea.Cmd) {
	action, rest := splitGoalCommand(args)
	if rest != "" && (action == "status" || action == "pause" || action == "resume" || action == "clear") {
		return m.appendGoalError("Uso: /goal " + action), nil
	}
	switch action {
	case "status":
		return m.appendGoalStatus(), nil
	case "pause":
		if m.activeSession.Goal == nil {
			return m.appendGoalError("Nenhuma meta definida para esta sessão."), nil
		}
		if m.pending {
			m.cancelRun()
			return m, nil
		}
		return m.setGoalStatus(sessions.GoalStatusPaused, "paused by user"), nil
	case "resume":
		if m.pending {
			return m.appendGoalError("Já há uma execução em andamento."), nil
		}
		if m.activeSession.Goal == nil {
			return m.appendGoalError("Nenhuma meta definida para esta sessão."), nil
		}
		if m.activeSession.Goal.Status == sessions.GoalStatusBudgetLimited &&
			m.activeSession.Goal.TokenBudget > 0 &&
			m.activeSession.Goal.TokensUsed >= m.activeSession.Goal.TokenBudget {
			return m.appendGoalError("O orçamento de tokens esgotou. Aumente com /goal edit --tokens N <objective>."), nil
		}
		m = m.setGoalStatus(sessions.GoalStatusActive, "")
		return m.launchGoalContinuationIfReady()
	case "clear":
		if m.activeSession.Goal == nil {
			return m.appendGoalError("Nenhuma meta definida para esta sessão."), nil
		}
		if m.pending {
			m.cancelRun()
		}
		updated, event, err := m.sessionStore.ClearGoal(m.activeSession.SessionID)
		if err != nil {
			return m.appendGoalError(err.Error()), nil
		}
		m.activeSession = updated
		m.sessionEvents = append(m.sessionEvents, event)
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, text: "Meta apagada."})
		return m, nil
	case "edit":
		objective, budget, err := parseGoalObjective(rest)
		if err != nil {
			return m.appendGoalError(err.Error()), nil
		}
		if m.activeSession.Goal == nil {
			return m.appendGoalError("Nenhuma meta definida para esta sessão."), nil
		}
		if m.pending {
			return m.appendGoalError("Pause a execução atual antes de editar a meta."), nil
		}
		if !goalBudgetSpecified(rest) {
			budget = m.activeSession.Goal.TokenBudget
		}
		updated, event, err := m.sessionStore.EditGoal(m.activeSession.SessionID, objective, budget)
		if err != nil {
			return m.appendGoalError(err.Error()), nil
		}
		m.activeSession = updated
		m.sessionEvents = append(m.sessionEvents, event)
		message := "Meta atualizada e retomada: " + objective
		if updated.Goal.Status == sessions.GoalStatusBudgetLimited {
			message = "Meta atualizada, mas o orçamento de tokens segue esgotado. Aumente o orçamento ou use --tokens 0 para remover o limite."
		}
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, text: message})
		return m.launchGoalContinuationIfReady()
	case "create":
		if m.pending {
			return m.appendGoalError("Já há uma execução em andamento."), nil
		}
		objective, budget, err := parseGoalObjective(rest)
		if err != nil {
			return m.appendGoalError(err.Error()), nil
		}
		if m.activeSession.Goal != nil {
			return m.appendGoalError("Esta sessão já tem uma meta. Use /goal edit <objective> ou /goal clear antes."), nil
		}
		m, err = m.ensureActiveSession(objective)
		if err != nil {
			return m.appendGoalError("erro ao criar sessão: " + err.Error()), nil
		}
		updated, event, err := m.sessionStore.CreateGoal(m.activeSession.SessionID, objective, budget)
		if err != nil {
			return m.appendGoalError(err.Error()), nil
		}
		m.activeSession = updated
		m.sessionEvents = append(m.sessionEvents, event)
		return m.launchPrompt(objective)
	default:
		return m.appendGoalError("Ação desconhecida. Use /goal, /goal pause, /goal resume, /goal edit <objective> ou /goal clear."), nil
	}
}

func splitGoalCommand(args string) (string, string) {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || strings.EqualFold(trimmed, "status") {
		return "status", ""
	}
	first, rest, _ := strings.Cut(trimmed, " ")
	switch strings.ToLower(first) {
	case "status", "pause", "resume", "clear":
		return strings.ToLower(first), strings.TrimSpace(rest)
	case "edit":
		return "edit", strings.TrimSpace(rest)
	default:
		return "create", trimmed
	}
}

func parseGoalObjective(input string) (string, int, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", 0, fmt.Errorf("é preciso informar o objetivo da meta")
	}
	budget := 0
	if strings.HasPrefix(input, "--tokens") {
		fields := strings.Fields(input)
		if len(fields) < 3 || fields[0] != "--tokens" {
			return "", 0, fmt.Errorf("uso: /goal [--tokens N] <objective>")
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil || value < 0 {
			return "", 0, fmt.Errorf("o orçamento de tokens da meta deve ser um inteiro não negativo")
		}
		budget = value
		// Strip only the "--tokens N" prefix, preserving the rest of the
		// text verbatim, so a multi-line goal keeps its line breaks.
		input = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input[len("--tokens"):]), fields[1]))
	}
	if input == "" {
		return "", 0, fmt.Errorf("é preciso informar o objetivo da meta")
	}
	if count := utf8.RuneCountInString(input); count > sessions.GoalObjectiveMaxLength {
		return "", 0, fmt.Errorf(
			"o objetivo tem %s caracteres e o limite é %s; coloque os detalhes num arquivo (ex.: SPEC.md) e cite o arquivo na meta",
			qgaccount.GroupThousands(strconv.Itoa(count)),
			qgaccount.GroupThousands(strconv.Itoa(sessions.GoalObjectiveMaxLength)),
		)
	}
	return input, budget, nil
}

func goalBudgetSpecified(input string) bool {
	return strings.HasPrefix(strings.TrimSpace(input), "--tokens")
}

// goalContinuationSummary renders the "Continuando meta:" row: only the
// first non-empty line, cut at 80 runes with "…" when truncated, so a
// long multi-line objective does not flood the transcript on every
// continuation.
func goalContinuationSummary(objective string) string {
	for _, line := range strings.Split(objective, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			runes := []rune(trimmed)
			if len(runes) <= 80 {
				return trimmed
			}
			return string(runes[:80]) + "…"
		}
	}
	return strings.TrimSpace(objective)
}

func (m model) appendGoalStatus() model {
	goal := m.activeSession.Goal
	if goal == nil {
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
			kind: rowSystem,
			text: "Meta\nestado: nenhum\nComece uma com /goal <objective>.",
		})
		return m
	}
	budget := "ilimitado"
	if goal.TokenBudget > 0 {
		budget = fmt.Sprintf("%d / %d tokens", goal.TokensUsed, goal.TokenBudget)
	} else if goal.TokensUsed > 0 {
		budget = fmt.Sprintf("%d tokens usados", goal.TokensUsed)
	}
	lines := []string{
		"Meta",
		"estado: " + goalStatusDisplay(goal.Status),
		"objetivo: " + goal.Objective,
		"orçamento: " + budget,
		fmt.Sprintf(
			"continuações automáticas: %d / %d",
			goal.ContinuationCount,
			goalContinuationLimit(goal),
		),
	}
	if goal.StatusReason != "" {
		lines = append(lines, "motivo: "+goal.StatusReason)
	}
	m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, text: strings.Join(lines, "\n")})
	return m
}

func goalContinuationLimit(goal *sessions.Goal) int {
	if goal != nil && goal.ContinuationLimit > 0 {
		return goal.ContinuationLimit
	}
	return sessions.GoalMaxConsecutiveContinuations
}

func (m model) goalFooterSummary() string {
	if m.activeSession.Goal == nil {
		return ""
	}
	return "meta " + goalStatusDisplay(m.activeSession.Goal.Status)
}

// goalStatusDisplay renders a goal status in Portuguese for display. The
// sessions.GoalStatus value itself stays in English as the stored contract.
func goalStatusDisplay(status sessions.GoalStatus) string {
	switch status {
	case sessions.GoalStatusActive:
		return "ativa"
	case sessions.GoalStatusPaused:
		return "pausada"
	case sessions.GoalStatusBlocked:
		return "bloqueada"
	case sessions.GoalStatusBudgetLimited:
		return "orçamento esgotado"
	case sessions.GoalStatusUsageLimited:
		return "limite de uso"
	case sessions.GoalStatusComplete:
		return "concluída"
	default:
		return string(status)
	}
}

// goalStatusSentence renders a one-line goal status change in Portuguese.
func goalStatusSentence(status sessions.GoalStatus) string {
	switch status {
	case sessions.GoalStatusActive:
		return "Meta ativa."
	case sessions.GoalStatusPaused:
		return "Meta pausada."
	case sessions.GoalStatusBlocked:
		return "Meta bloqueada."
	case sessions.GoalStatusBudgetLimited:
		return "Meta com orçamento esgotado."
	case sessions.GoalStatusUsageLimited:
		return "Meta com limite de uso."
	case sessions.GoalStatusComplete:
		return "Meta concluída."
	default:
		return "Meta " + string(status) + "."
	}
}

func (m model) appendGoalError(message string) model {
	m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowError, text: "Meta: " + message})
	return m
}

func (m model) setGoalStatus(status sessions.GoalStatus, reason string) model {
	if m.sessionStore == nil || m.activeSession.SessionID == "" || m.activeSession.Goal == nil {
		return m.appendGoalError("Estado da meta indisponível.")
	}
	updated, event, err := m.sessionStore.UpdateGoal(m.activeSession.SessionID, status, reason)
	if err != nil {
		return m.appendGoalError(err.Error())
	}
	m.activeSession = updated
	m.sessionEvents = append(m.sessionEvents, event)
	m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
		kind: rowSystem,
		text: goalStatusSentence(status),
	})
	return m
}

func (m model) goalRegistry() *tools.Registry {
	registry := cloneToolRegistry(m.registry)
	if m.activeSession.SessionID == "" {
		return registry
	}
	for _, tool := range tools.NewGoalTools(m.sessionStore, m.activeSession.SessionID) {
		registry.Register(tool)
	}
	return registry
}

func (m model) goalSystemPrompt(base string) string {
	goal := m.activeSession.Goal
	if goal == nil {
		return base
	}
	instruction := fmt.Sprintf(
		"Persistent goal for this session:\nObjective: %s\nStatus: %s\n"+
			"When the status is active, keep pursuing this objective across turns. "+
			"As soon as the objective is achieved and verified, call update_goal with status complete in that same turn, before your final answer; "+
			"ending a turn without update_goal makes the session send you an automatic continuation. "+
			"Call update_goal with blocked and a concrete reason only when progress requires user input or an external change.",
		goal.Objective,
		goal.Status,
	)
	if strings.TrimSpace(base) == "" {
		return instruction
	}
	return base + "\n\n" + instruction
}

func (m model) launchGoalContinuationIfReady() (model, tea.Cmd) {
	goal := m.activeSession.Goal
	if goal == nil || goal.Status != sessions.GoalStatusActive || m.pending ||
		m.compactInFlight || m.exiting || m.provider == nil ||
		m.goalContinuationsSuspended {
		return m, nil
	}
	updated, event, reserved, err := m.sessionStore.ReserveGoalContinuation(m.activeSession.SessionID)
	if err != nil {
		return m.appendGoalError("reserve automatic continuation: " + err.Error()), nil
	}
	m.activeSession = updated
	if event != nil {
		m.sessionEvents = append(m.sessionEvents, *event)
	}
	if !reserved {
		if event != nil && updated.Goal != nil && updated.Goal.Status == sessions.GoalStatusPaused {
			m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
				kind: rowSystem,
				text: fmt.Sprintf(
					"Meta pausada após %d continuações automáticas. Revise o progresso e use /goal resume para continuar.",
					goalContinuationLimit(updated.Goal),
				),
			})
		}
		return m, nil
	}
	goal = updated.Goal
	m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
		kind: rowSystem,
		text: "Continuando meta: " + goalContinuationSummary(goal.Objective),
	})
	prompt := m.sessionPrompt(goalContinuationPrompt)
	m, err = m.appendSessionEvent(sessions.EventMessage, map[string]any{
		"role":    "goal",
		"content": goalContinuationPrompt,
	})
	if err != nil {
		return m.appendGoalError("erro ao gravar a sessão: " + err.Error()), nil
	}
	runCtx, cancel := context.WithCancel(m.ctx)
	m = m.beginRun(cancel)
	return m, tea.Batch(m.runAgentWithOptions(m.activeRunID, runCtx, prompt, nil, tuiAgentRunOptions{goalContinuation: true}), m.spinner.Tick)
}

func (m model) reconcileGoalAfterRun(usageEvents []qgruntime.Usage, runErr error) model {
	if m.sessionStore == nil || m.activeSession.SessionID == "" {
		return m
	}
	loaded, err := m.sessionStore.Get(m.activeSession.SessionID)
	if err != nil || loaded == nil {
		if err != nil {
			return m.appendGoalError("reload after run: " + err.Error())
		}
		return m
	}
	if loaded.Goal == nil {
		return m
	}
	tokens := 0
	for _, event := range usageEvents {
		tokens += event.TotalTokens()
	}
	if tokens > 0 {
		updated, _, addErr := m.sessionStore.AddGoalUsage(loaded.SessionID, tokens)
		if addErr != nil {
			return m.appendGoalError("account usage: " + addErr.Error())
		}
		loaded = &updated
	}
	if runErr != nil && loaded.Goal.Status == sessions.GoalStatusActive {
		status := sessions.GoalStatusPaused
		reason := "run stopped after an error"
		if errhint.Classify(runErr) == errhint.RateLimit {
			status = sessions.GoalStatusUsageLimited
			reason = "provider usage limit reached"
		}
		updated, _, updateErr := m.sessionStore.UpdateGoal(
			loaded.SessionID,
			status,
			reason,
		)
		if updateErr != nil {
			return m.appendGoalError("pausa após erro: " + updateErr.Error())
		}
		loaded = &updated
		message := "Meta pausada porque a execução parou com erro. Use /goal resume para continuar."
		if status == sessions.GoalStatusUsageLimited {
			message = "Meta parada no limite de uso do provedor. Use /goal resume quando houver capacidade."
		}
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, text: message})
	}
	m.activeSession = *loaded
	if events, readErr := m.sessionStore.ReadEvents(loaded.SessionID); readErr == nil {
		m.sessionEvents = events
	}
	return m
}
