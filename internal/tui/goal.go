package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/errhint"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

const goalContinuationPrompt = "Continue pursuing the active goal. Review the existing session context, make concrete progress, and use update_goal when the objective is complete or genuinely blocked."

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
		if task := sessions.CurrentGoalTask(m.activeSession.Goal); task != nil &&
			task.Status == sessions.GoalTaskBlocked {
			updated, event, err := m.sessionStore.ReactivateGoalTask(m.activeSession.SessionID)
			if err != nil {
				return m.appendGoalError(err.Error()), nil
			}
			m.activeSession = updated
			m.sessionEvents = append(m.sessionEvents, event)
			m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
				kind: rowSystem,
				text: "Retomando tarefa bloqueada: " + task.Title,
			})
		}
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
		m = m.appendGoalTaskListRow()
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
		m = m.appendGoalTaskListRow()
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
		input = strings.TrimSpace(strings.Join(fields[2:], " "))
	}
	if input == "" {
		return "", 0, fmt.Errorf("é preciso informar o objetivo da meta")
	}
	return input, budget, nil
}

func goalBudgetSpecified(input string) bool {
	return strings.HasPrefix(strings.TrimSpace(input), "--tokens")
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
	lines = append(lines, goalTaskListLines(goal)...)
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
	goal := m.activeSession.Goal
	if goal == nil {
		return ""
	}
	if len(goal.Tasks) == 0 {
		return "meta " + goalStatusDisplay(goal.Status)
	}
	done, total := sessions.GoalTaskProgress(goal)
	title := ""
	if current := sessions.CurrentGoalTask(goal); current != nil {
		title = " " + truncateDisplayWidth(current.Title, 24)
	}
	return fmt.Sprintf("meta %d/%d%s · %s", done, total, title, goalStatusDisplay(goal.Status))
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

// goalAutoSplitEnabled reports whether a single-sentence goal is split into
// tasks on its first run. Unset defaults to on.
func (m model) goalAutoSplitEnabled() bool {
	return m.goalAutoSplit == nil || *m.goalAutoSplit
}

// goalBetweenTasksMode normalizes the between-tasks mode, defaulting to
// compact. Resolve rejects unknown values; anything unexpected here still
// compacts rather than silently keeping a growing context.
func (m model) goalBetweenTasksMode() string {
	return config.GoalConfig{BetweenTasks: m.goalBetweenTasks}.BetweenTasksMode()
}

func (m model) goalSystemPrompt(base string) string {
	goal := m.activeSession.Goal
	if goal == nil {
		return base
	}
	var instruction strings.Builder
	fmt.Fprintf(&instruction,
		"Persistent goal for this session:\nObjective: %s\nStatus: %s",
		goal.Objective,
		goal.Status,
	)
	if len(goal.Tasks) > 0 {
		instruction.WriteString("\nTasks:")
		for i, task := range goal.Tasks {
			line := fmt.Sprintf("\n%d. [%s] %s", i+1, goalTaskStateEN(task.Status), task.Title)
			if strings.TrimSpace(task.Note) != "" {
				line += " — " + strings.TrimSpace(task.Note)
			}
			instruction.WriteString(line)
		}
		instruction.WriteString("\nWork ONLY on the current task; do not start other tasks. " +
			"Call update_goal with task_status \"complete\" (optional note in reason) when the current task is done, " +
			"or task_status \"blocked\" with a concrete reason when it cannot proceed. " +
			"Never call update_goal with status \"complete\" while tasks are pending; " +
			"the goal completes automatically once every task is done.")
	} else {
		instruction.WriteString("\nWhen the status is active, keep pursuing this objective across turns. " +
			"Call update_goal with complete only after the objective is genuinely achieved, " +
			"or blocked with a concrete reason when progress requires user input or an external change.")
		if m.goalAutoSplitEnabled() && goal.ContinuationCount == 0 {
			instruction.WriteString("\nBefore starting work, break this objective into 2-12 concrete tasks " +
				"by calling set_goal_tasks with short titles, then proceed with the first task without waiting. " +
				"If the objective is trivially a single step, skip the split and work directly.")
		}
	}
	if strings.TrimSpace(base) == "" {
		return instruction.String()
	}
	return base + "\n\n" + instruction.String()
}

// goalTaskStateEN renders a task state for model instructions (English, like
// the other model-facing goal text).
func goalTaskStateEN(status sessions.GoalTaskStatus) string {
	switch status {
	case sessions.GoalTaskActive:
		return "active"
	case sessions.GoalTaskDone:
		return "done"
	case sessions.GoalTaskBlocked:
		return "blocked"
	default:
		return "pending"
	}
}

// goalTaskStateDisplay renders a task state in Portuguese for display.
func goalTaskStateDisplay(status sessions.GoalTaskStatus) string {
	switch status {
	case sessions.GoalTaskActive:
		return "ativa"
	case sessions.GoalTaskDone:
		return "feita"
	case sessions.GoalTaskBlocked:
		return "bloqueada"
	default:
		return "pendente"
	}
}

// goalTaskListLines renders the checklist for /goal status and transcript
// rows, in Portuguese.
func goalTaskListLines(goal *sessions.Goal) []string {
	if goal == nil || len(goal.Tasks) == 0 {
		return nil
	}
	done, total := sessions.GoalTaskProgress(goal)
	lines := []string{fmt.Sprintf("tarefas (%d/%d feitas):", done, total)}
	for i, task := range goal.Tasks {
		line := fmt.Sprintf("%d. [%s] %s", i+1, goalTaskStateDisplay(task.Status), task.Title)
		if strings.TrimSpace(task.Note) != "" {
			line += " — " + strings.TrimSpace(task.Note)
		}
		lines = append(lines, line)
	}
	return lines
}

// goalTaskProgressSuffix appends " (tarefa i/N: title)" to continuation
// rows for goals with tasks.
func goalTaskProgressSuffix(goal *sessions.Goal) string {
	if goal == nil || len(goal.Tasks) == 0 {
		return ""
	}
	done, total := sessions.GoalTaskProgress(goal)
	if current := sessions.CurrentGoalTask(goal); current != nil {
		return fmt.Sprintf(" (tarefa %d/%d: %s)", done+1, total, truncateDisplayWidth(current.Title, 40))
	}
	return fmt.Sprintf(" (%d/%d tarefas feitas)", done, total)
}

// goalResetBoundary returns the event sequence where the current task era
// starts, for reset mode. Zero disables filtering: legacy goals, taskless
// goals, and other between-tasks modes see the full history.
func (m model) goalResetBoundary() int {
	goal := m.activeSession.Goal
	if goal == nil || len(goal.Tasks) == 0 {
		return 0
	}
	if m.goalBetweenTasksMode() != config.GoalBetweenTasksReset {
		return 0
	}
	if goal.TaskBoundary <= 0 {
		return 0
	}
	return goal.TaskBoundary
}

// goalEraEvents restricts events to the current task era in reset mode: the
// boundary sequence starts the next run, so everything before it belongs to
// an older task. Outside reset mode (or without a boundary) it returns events
// unchanged. An empty result is honest — the new task simply has no history
// yet — and every caller already handles it (bare prompt, rebuilt wire miss).
func (m model) goalEraEvents(events []sessions.Event) []sessions.Event {
	if boundary := m.goalResetBoundary(); boundary > 0 {
		return sessions.EventsFromSeq(events, boundary)
	}
	return events
}

// goalEraBlock renders the objective, task list, and notes appended to reset
// prompts: the only pre-switch context the model receives.
func goalEraBlock(goal *sessions.Goal) string {
	var block strings.Builder
	block.WriteString("Current task context (earlier history was cleared between tasks):")
	if goal != nil {
		fmt.Fprintf(&block, "\nObjective: %s", goal.Objective)
		for i, task := range goal.Tasks {
			line := fmt.Sprintf("\n%d. [%s] %s", i+1, goalTaskStateEN(task.Status), task.Title)
			if strings.TrimSpace(task.Note) != "" {
				line += " — " + strings.TrimSpace(task.Note)
			}
			block.WriteString(line)
		}
	}
	return block.String()
}

// appendGoalTaskListRow surfaces a freshly set checklist (from /goal create,
// /goal edit, or a set_goal_tasks tool call) in the transcript. Taskless
// goals add nothing.
func (m model) appendGoalTaskListRow() model {
	lines := goalTaskListLines(m.activeSession.Goal)
	if len(lines) == 0 {
		return m
	}
	m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
		kind: rowSystem,
		text: strings.Join(lines, "\n"),
	})
	return m
}

// cutGoalWireToBoundary drops pre-task history from the structured wire after
// a reset-mode task switch, so later user submits replay only the current
// task. The snapshot is rewritten with the cut wire (or dropped when nothing
// post-boundary exists yet) so restarting the session cannot restore the
// cleared history; resume falls back to the boundary-filtered log either way.
func (m model) cutGoalWireToBoundary() model {
	if m.goalResetBoundary() <= 0 {
		return m
	}
	msgs := sessions.RepairOrphanToolCalls(sessions.ReconstructContinuationMessages(
		m.goalEraEvents(m.sessionEvents)))
	loaded := append([]string(nil), m.continuation.loadedTools...)
	m.continuation = turnContinuation{}
	if len(msgs) == 0 {
		if m.sessionStore != nil && m.activeSession.SessionID != "" {
			_ = m.sessionStore.InvalidateContinuation(m.activeSession.SessionID)
		}
		return m
	}
	m.continuation = turnContinuation{
		sessionID:   m.activeSession.SessionID,
		modelID:     m.modelName,
		goalKey:     goalKeyForSession(m.activeSession),
		messages:    msgs,
		loadedTools: loaded,
	}
	m.persistSnapshot(sessions.ContinuationSnapshot{
		SessionID:       m.activeSession.SessionID,
		ModelID:         m.modelName,
		Provider:        m.providerName,
		ReasoningEffort: string(m.reasoningEffort),
		GoalKey:         goalKeyForSession(m.activeSession),
		EventSeq:        m.activeSession.EventCount,
		Messages:        msgs,
		LoadedTools:     loaded,
	})
	return m
}

// goalTaskSwitched reports whether a finished run moved the goal to another
// task (or finished every task). Blocked and legacy goals never switch.
func goalTaskSwitched(before, after *sessions.Goal) (switched bool, finished bool) {
	if before == nil || after == nil || len(before.Tasks) == 0 {
		return false, false
	}
	if after.Status == sessions.GoalStatusComplete && before.Status != sessions.GoalStatusComplete {
		return true, true
	}
	previous := sessions.CurrentGoalTask(before)
	current := sessions.CurrentGoalTask(after)
	if previous == nil || current == nil {
		return false, false
	}
	return current.ID != previous.ID, false
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
		text: "Continuando meta: " + goal.Objective + goalTaskProgressSuffix(goal),
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

// launchGoalNextStepIfReady continues a goal after a run, applying the
// between-tasks mode when the run just moved to another task. Legacy goals
// and mid-task continuations follow the historical path unchanged.
func (m model) launchGoalNextStepIfReady() (model, tea.Cmd) {
	if !m.goalTaskAdvanced {
		return m.launchGoalContinuationIfReady()
	}
	m.goalTaskAdvanced = false
	switch m.goalBetweenTasksMode() {
	case config.GoalBetweenTasksKeep:
		return m.launchGoalContinuationIfReady()
	case config.GoalBetweenTasksReset:
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
			kind: rowSystem,
			text: "Contexto limpo entre tarefas: a próxima tarefa começa só com objetivo, lista e notas.",
		})
		return m.launchGoalContinuationIfReady()
	default:
		if m.compactInFlight || m.sessionStore == nil || m.activeSession.SessionID == "" {
			return m.launchGoalContinuationIfReady()
		}
		if m.sessionCompactor == nil && !m.hasSessionBackedCompactor() {
			return m.launchGoalContinuationIfReady()
		}
		m.goalContinueAfterCompact = true
		m.compactRequests++
		m.lastCompactError = ""
		m.lastCompactResult = nil
		m.compactInFlight = true
		m.compactFrame = 0
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
			kind: rowSystem,
			text: "Compactando contexto antes da próxima tarefa…",
		})
		return m, tea.Batch(m.runCompact(), m.spinner.Tick)
	}
}

func (m model) reconcileGoalAfterRun(usageEvents []qgruntime.Usage, runErr error) model {
	before := m.activeSession.Goal
	// Consumed here so a skipped continuation (queued message, permission
	// prompt) cannot retrigger the between-tasks mode after a later run.
	m.goalTaskAdvanced = false
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
	if switched, finished := goalTaskSwitched(before, loaded.Goal); switched {
		m.goalTaskAdvanced = true
		if !finished && m.goalBetweenTasksMode() == config.GoalBetweenTasksReset {
			// The new era starts at the next run: everything logged so far
			// belongs to the old task.
			if bounded, err := m.sessionStore.SetGoalTaskBoundary(loaded.SessionID, loaded.EventCount+1); err != nil {
				m = m.appendGoalError("registrar fronteira da tarefa: " + err.Error())
			} else {
				loaded = &bounded
				m.activeSession = bounded
			}
			m = m.cutGoalWireToBoundary()
		}
		if finished {
			done, total := sessions.GoalTaskProgress(loaded.Goal)
			m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
				kind: rowSystem,
				text: fmt.Sprintf("Meta concluída: %d de %d tarefas feitas.", done, total),
			})
		} else if previous := sessions.CurrentGoalTask(before); previous != nil {
			if current := sessions.CurrentGoalTask(loaded.Goal); current != nil {
				done, total := sessions.GoalTaskProgress(loaded.Goal)
				m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
					kind: rowSystem,
					text: fmt.Sprintf("Tarefa concluída: %s. Próxima (%d/%d): %s.",
						previous.Title, done+1, total, current.Title),
				})
			}
		}
	}
	return m
}
