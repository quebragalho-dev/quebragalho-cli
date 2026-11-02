package tui

import (
	"fmt"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

type currentPlanReader interface {
	CurrentPlan() []tools.PlanItem
}

func (m model) handlePlanCommand(args string) (model, string) {
	switch strings.ToLower(strings.TrimSpace(args)) {
	case "status":
		return m, m.planText()
	case "", "on":
		if m.pending {
			return m, "Não dá para trocar o modo de plano com um turno em andamento."
		}
		if m.permissionMode == agent.PermissionModePlan {
			return m, "Modo de planejamento\nJá ativo. Ferramentas de escrita e shell ficam ocultas até /plan off."
		}
		m.permissionModeBeforePlan = m.permissionMode
		m.permissionMode = agent.PermissionModePlan
		return m, "Modo de planejamento\nAtivo: planejamento só de leitura. Ferramentas de escrita e shell ficam ocultas até /plan off."
	case "off":
		if m.pending {
			return m, "Não dá para trocar o modo de plano com um turno em andamento."
		}
		if m.permissionMode != agent.PermissionModePlan {
			return m, "Modo de planejamento\nNão está ativo no momento."
		}
		restored := m.permissionModeBeforePlan
		if restored == "" {
			restored = agent.PermissionModeAuto
		}
		m.permissionMode = restored
		m.permissionModeBeforePlan = ""
		return m, "Modo de planejamento\nEncerrado. Modo de permissão restaurado para " + string(restored) + "."
	default:
		return m, "Modo de planejamento\nUso: /plan [status|on|off]"
	}
}

// planModeCommandUnavailable reports whether a local (non-tool) TUI command
// must be blocked while plan mode is active. Plan mode's tool-advertisement
// gate only covers agent tool calls; these commands run entirely inside the
// TUI process and would mutate the workspace or spawn a host process outside
// that gate: /rewind restores files from a checkpoint, /export writes a
// transcript file to disk, /sandbox-setup runs native platform setup,
// /spec forks a drafting session, /mcp <sub> mutates server configuration, and
// /init's whole job is writing AGENTS.md (which plan mode then denies).
// Bare /mcp (empty text) only opens the read-only manager view, so it stays
// available. Modeled on btwCommandUnavailable's shape for the analogous BTW guard.
func planModeCommandUnavailable(command parsedCommand) bool {
	switch command.kind {
	case commandRewind, commandExport, commandSandboxSetup, commandSpec, commandInit:
		return true
	case commandMCP:
		// Bare /mcp only opens the read-only manager view; subcommands mutate config.
		return strings.TrimSpace(command.text) != ""
	default:
		return false
	}
}

func (m model) planText() string {
	tool, ok := m.registry.Get("update_plan")
	if !ok {
		return "Nenhum plano ativo."
	}

	reader, ok := tool.(currentPlanReader)
	if !ok {
		return "Nenhum plano ativo."
	}

	plan := reader.CurrentPlan()
	if len(plan) == 0 {
		return "Nenhum plano ativo."
	}

	lines := make([]string, 0, len(plan)+1)
	lines = append(lines, "Plano atual")
	for index, item := range plan {
		line := fmt.Sprintf("%d. [%s] %s", index+1, item.Status, item.Content)
		if item.Notes != "" {
			line += "\n   Notas: " + item.Notes
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
