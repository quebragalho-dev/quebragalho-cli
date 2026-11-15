package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgcommands"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func (m model) toolsText() string {
	registered := m.registeredTools()
	count := len(registered)
	if len(registered) == 0 {
		return renderCommandCardTranscript(commandCard{
			Title:   "Ferramentas",
			Summary: []string{"0 registradas", "nenhuma ferramenta disponível"},
			Sections: []commandCardSection{{
				Title: "Registro",
				Fields: []commandField{
					{Key: "registradas", Value: "0"},
				},
			}},
			Actions: []string{"/mcp manage servers", "/permissions manage access"},
		})
	}

	names := make([]string, 0, count)
	for _, tool := range registered {
		names = append(names, tool.Name())
	}
	sort.Strings(names)
	available := make([]string, 0, len(names))
	for _, name := range names {
		available = append(available, commandBullet(name))
	}

	registeredSummary := fmt.Sprintf("%d registradas", count)
	if count == 1 {
		registeredSummary = "1 registrada"
	}

	return renderCommandCardTranscript(commandCard{
		Title:   "Ferramentas",
		Summary: []string{registeredSummary, "catálogo registrado"},
		Sections: []commandCardSection{
			{
				Title: "Registro",
				Fields: []commandField{
					{Key: "registradas", Value: fmt.Sprint(count)},
				},
			},
			{
				Title: "Disponíveis",
				Lines: available,
			},
		},
		Actions: []string{"/mcp manage servers", "/permissions manage access"},
	})
}

func (m *model) mcpText() string {
	width := 0
	if m.width > 0 {
		width = chatWidth(m.width)
	}
	return renderMCPView(m.mcpViewState(), width)
}

func (m *model) refreshMCPViewState() {
	var status []mcp.ServerStatus
	if m.mcpStatus != nil {
		status = m.mcpStatus()
	}
	m.mcpViewStateCache = BuildMCPViewState(MCPStateOptions{
		Config:          m.mcpConfig,
		Registry:        m.registry,
		PermissionStore: m.mcpPermissionStore,
		PermissionMode:  string(m.permissionMode),
		TokenStore:      m.mcpTokenStore,
		Skipped:         m.mcpSkipped,
		Status:          status,
	})
	m.mcpViewStateReady = true
}

func (m *model) mcpViewState() MCPViewState {
	if m.mcpViewStateReady {
		return m.mcpViewStateCache
	}
	// Older tests may construct a zero-value model; keep that path useful, while
	// production refreshes the cache before any MCP view can render.
	m.refreshMCPViewState()
	return m.mcpViewStateCache
}

func (m model) startMCPTranscriptCommand(args string) (model, tea.Cmd) {
	args = strings.TrimSpace(args)
	if args == "" {
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, tool: "mcp", text: m.mcpText()})
		return m, nil
	}
	parsedArgs, err := splitMCPCommandArgs(args)
	if err != nil {
		text := strings.Join([]string{
			"Ação de MCP falhou",
			err.Error(),
			"",
			m.mcpText(),
		}, "\n")
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, tool: "mcp", text: text})
		return m, nil
	}
	return m.startMCPCommand(mcpCommandRequest{origin: mcpCommandOriginTranscript, raw: args, args: parsedArgs})
}

func (m model) startMCPCommand(request mcpCommandRequest) (model, tea.Cmd) {
	if m.mcpCommand == nil {
		result := MCPCommandResult{
			ExitCode: 1,
			Error:    "Ação de MCP indisponível",
			Config:   m.mcpConfig,
		}
		return m.applyMCPCommandResultMessage(mcpCommandResultMsg{request: request, result: result}), nil
	}
	m.cancelMCPCommand()
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	m.mcpCommandSeq++
	request.id = m.mcpCommandSeq
	request.args = append([]string{}, request.args...)
	m.mcpCommandCancel = cancel
	runner := m.mcpCommand
	return m, func() tea.Msg {
		return mcpCommandResultMsg{
			request: request,
			result:  runner(ctx, request.args),
		}
	}
}

func (m *model) cancelMCPCommand() {
	if m.mcpCommandCancel != nil {
		m.mcpCommandCancel()
		m.mcpCommandCancel = nil
		m.mcpCommandSeq++
	}
}

// applyMCPReconnectResult refreshes the /mcp view after a session-runtime
// reconnect and reports the outcome in the transcript.
func (m model) applyMCPReconnectResult(msg mcpReconnectResultMsg) model {
	m.refreshMCPViewState()
	if msg.err != nil {
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, tool: "mcp", text: "Reconectar " + msg.server + " falhou: " + strings.TrimSpace(msg.err.Error())})
	} else {
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, tool: "mcp", text: "Servidor MCP " + msg.server + " reconectado."})
	}
	m.mcpManager = &mcpManagerState{}
	m.clearSuggestions()
	return m
}

func (m model) applyMCPCommandResultMessage(msg mcpCommandResultMsg) model {
	if msg.request.id != 0 && msg.request.id != m.mcpCommandSeq {
		return m
	}
	m.mcpCommandCancel = nil
	switch msg.request.origin {
	case mcpCommandOriginManager:
		text := ""
		m, text = m.applyMCPCommandResult(strings.Join(msg.request.args, " "), msg.result)
		m.mcpManager = &mcpManagerState{selected: msg.request.managerSelected, query: msg.request.managerQuery}
		if items := m.mcpManagerItems(); len(items) > 0 {
			m.mcpManager.selected = clampInt(m.mcpManager.selected, 0, len(items)-1)
		}
		if text != "" {
			m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, tool: "mcp", text: text})
		}
	case mcpCommandOriginWizard:
		m = m.applyMCPAddWizardSaveResult(msg.result, msg.request.wizardDisabled)
	default:
		text := ""
		m, text = m.applyMCPCommandResult(msg.request.raw, msg.result)
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, tool: "mcp", text: text})
	}
	return m
}

func (m model) applyMCPCommandResult(args string, result MCPCommandResult) (model, string) {
	if result.ExitCode != 0 || strings.TrimSpace(result.Error) != "" {
		message := strings.TrimSpace(result.Error)
		if message == "" {
			message = strings.TrimSpace(result.Output)
		}
		if message == "" {
			message = "Comando MCP falhou"
		}
		return m, strings.Join([]string{
			"Ação de MCP falhou",
			message,
			"",
			m.mcpText(),
		}, "\n")
	}
	if len(result.Config.Servers) > 0 || len(m.mcpConfig.Servers) > 0 {
		m.mcpConfig = result.Config
		m.refreshMCPViewState()
	}
	output := strings.TrimSpace(result.Output)
	if output == "" {
		output = "qg mcp " + args
	}
	return m, strings.Join([]string{
		"Ação de MCP concluída",
		output,
		"",
		m.mcpText(),
	}, "\n")
}

func splitMCPCommandArgs(args string) ([]string, error) {
	args = strings.TrimSpace(args)
	if args == "" {
		return nil, nil
	}
	out := []string{}
	var current strings.Builder
	var quote rune
	hasToken := false
	runes := []rune(args)
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		if quote != 0 {
			if r == quote {
				quote = 0
				hasToken = true
				continue
			}
			if r == '\\' && index+1 < len(runes) && runes[index+1] == quote {
				index++
				current.WriteRune(runes[index])
				hasToken = true
				continue
			}
			current.WriteRune(r)
			hasToken = true
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			hasToken = true
		case r == '\\' && index+1 < len(runes) && (runes[index+1] == '\'' || runes[index+1] == '"' || strings.TrimSpace(string(runes[index+1])) == ""):
			index++
			current.WriteRune(runes[index])
			hasToken = true
		case strings.TrimSpace(string(r)) == "":
			if hasToken {
				out = append(out, current.String())
				current.Reset()
				hasToken = false
			}
		default:
			current.WriteRune(r)
			hasToken = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in MCP command")
	}
	if hasToken {
		out = append(out, current.String())
	}
	return out, nil
}

func (m model) permissionsText() string {
	if m.sandboxStore == nil {
		return m.permissionsTextWithStore(nil)
	}
	return m.permissionsTextWithStore(m.sandboxStore)
}

func (m model) permissionsTextWithStore(store grantLister) string {
	mode := string(m.permissionMode)
	if store == nil {
		return renderCommandCardTranscript(commandCard{
			Title:   "Permissões",
			Summary: []string{mode + " permissões", "autorizações indisponíveis"},
			Sections: []commandCardSection{
				{
					Title: "Estado",
					Fields: []commandField{
						{Key: "modo", Value: mode},
					},
				},
				{
					Title: "Autorizações",
					Lines: []string{"autorizações persistentes: indisponíveis"},
				},
			},
		})
	}

	grants, err := store.List()
	if err != nil {
		return renderCommandCardTranscript(commandCard{
			Title:   "Permissões",
			Summary: []string{mode + " permissões", "erro de autorizações"},
			Sections: []commandCardSection{
				{
					Title: "Estado",
					Fields: []commandField{
						{Key: "modo", Value: mode},
					},
				},
				{
					Title: "Autorizações",
					Lines: []string{"erro: " + err.Error()},
				},
			},
		})
	}
	prefixes := []sandbox.CommandPrefixGrant{}
	if prefixStore, ok := store.(commandPrefixGrantLister); ok {
		var prefixErr error
		prefixes, prefixErr = prefixStore.ListCommandPrefixes()
		if prefixErr != nil {
			return renderCommandCardTranscript(commandCard{
				Title:   "Permissões",
				Summary: []string{mode + " permissões", "erro de autorizações"},
				Sections: []commandCardSection{
					{
						Title: "Estado",
						Fields: []commandField{
							{Key: "modo", Value: mode},
						},
					},
					{
						Title: "Autorizações",
						Lines: []string{"erro: " + prefixErr.Error()},
					},
				},
			})
		}
	}

	snapshots := qgcommands.SandboxGrantSnapshots(grants)
	grantRows := []commandRow{}
	if len(snapshots) == 0 && len(prefixes) == 0 {
		grantRows = append(grantRows, commandRow{Text: "nenhuma"})
	} else {
		for _, grant := range snapshots {
			line := fmt.Sprintf("%s [%s]", grant.ToolName, grant.Decision)
			if grant.ApprovedAt != "" {
				line += " aprovada em " + grant.ApprovedAt
			}
			if grant.Reason != "" {
				line += " - " + grant.Reason
			}
			grantRows = append(grantRows, commandRow{Text: line})
		}
		for _, grant := range prefixes {
			line := fmt.Sprintf("%s `%s` [command-prefix]", grant.ToolName, strings.Join(grant.Prefix, " "))
			if grant.ApprovedAt != "" {
				line += " aprovada em " + grant.ApprovedAt
			}
			if grant.Reason != "" {
				line += " - " + grant.Reason
			}
			grantRows = append(grantRows, commandRow{Text: line})
		}
	}

	return renderCommandCardTranscript(commandCard{
		Title:   "Permissões",
		Summary: []string{mode + " permissões", formatGrantCount(len(snapshots) + len(prefixes))},
		Sections: []commandCardSection{
			{
				Title: "Estado",
				Fields: []commandField{
					{Key: "modo", Value: mode},
				},
			},
			{
				Title: "Autorizações",
				Rows:  grantRows,
			},
		},
	})
}

// grantLister is the subset of sandbox.GrantStore used by permissionsText().
// It exists to let tests inject error-stub stores without reaching for a real
// filesystem path.
type grantLister interface {
	List() ([]sandbox.Grant, error)
}

type commandPrefixGrantLister interface {
	ListCommandPrefixes() ([]sandbox.CommandPrefixGrant, error)
}

func formatGrantCount(count int) string {
	if count == 0 {
		return "nenhuma autorização persistente"
	}
	if count == 1 {
		return "1 autorização persistente"
	}
	return fmt.Sprintf("%d autorizações persistentes", count)
}

func firstProviderDisplayValue(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (m model) modelText(args string) string {
	return renderCommandOutput(commandOutput{
		Title:  "Modelo",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "Ativo",
			Lines: []string{
				"modelo: " + displayValue(m.modelName, "nenhum"),
				"provedor: " + displayValue(m.providerName, "nenhum"),
				"nível: " + effortDisplayLabel(m.effortDisplay()),
			},
		}},
		Hints: []string{"use /model list para ver os modelos ou /model <id> para trocar o modelo desta sessão"},
	})
}

// avgTurnLatencyText reports the session's rolling average turn wall-time for
// /context — the "is it slow?" signal a user otherwise can only feel. "n/a" until
// a turn has completed.
func (m model) avgTurnLatencyText() string {
	if m.turnLatencyCount == 0 {
		return "n/a"
	}
	turns := turnCountText(m.turnLatencyCount)
	avgSeconds := m.turnLatencySum.Seconds() / float64(m.turnLatencyCount)
	if m.turnTTFTCount > 0 {
		ttftSeconds := m.turnTTFTSum.Seconds() / float64(m.turnTTFTCount)
		return fmt.Sprintf("%.1fs de média (%.1fs até o primeiro token, %s)", avgSeconds, ttftSeconds, turns)
	}
	return fmt.Sprintf("%.1fs de média (%s)", avgSeconds, turns)
}

func turnCountText(count int) string {
	if count == 1 {
		return "1 turno"
	}
	return fmt.Sprintf("%d turnos", count)
}

func (m model) contextText() string {
	toolCount := len(m.registeredTools())
	return renderCommandCardTranscript(commandCard{
		Title: "Contexto",
		Summary: []string{
			"runtime go",
			string(m.permissionMode) + " permissões",
			pluralizeCount(toolCount, "ferramenta", "ferramentas"),
		},
		Sections: []commandCardSection{
			{
				Title: "Execução",
				Fields: []commandField{
					{Key: "cwd", Value: displayValue(m.cwd, "desconhecido")},
					{Key: "provedor", Value: displayValue(m.providerName, "nenhum")},
					{Key: "modelo", Value: displayValue(m.modelName, "nenhum")},
					{Key: "nível", Value: m.effortDisplay()},
					{Key: "estilo", Value: displayValue(m.responseStyle, defaultResponseStyle)},
					{Key: "uso", Value: m.usageSummaryText()},
					{Key: "cache", Value: m.cacheEfficiencyText()},
					{Key: "latência", Value: m.avgTurnLatencyText()},
					{Key: "turnos máximos", Value: fmt.Sprint(m.agentOptions.MaxTurns)},
				},
			},
			{
				Title: "Sessão",
				Fields: []commandField{
					{Key: "ativa", Value: displayValue(m.activeSession.SessionID, "nenhuma")},
					{Key: "raiz", Value: displayValue(m.sessionRootDir(), "desconhecida")},
					{Key: "compactação", Value: contextCompactionStatus(m.compactionStatus())},
				},
			},
			{
				Title: "Ferramentas",
				Fields: []commandField{
					{Key: "registradas", Value: fmt.Sprint(toolCount)},
				},
			},
		},
		Actions: []string{"/permissions manage access", "/tools inspect catalog"},
	})
}

func (m model) registeredTools() []tools.Tool {
	if m.registry == nil {
		return nil
	}
	return m.registry.All()
}

func (m model) sessionRootDir() string {
	if m.sessionStore == nil {
		return ""
	}
	return m.sessionStore.RootDir
}

func pluralizeCount(count int, singular string, plural string) string {
	label := plural
	if count == 1 {
		label = singular
	}
	return fmt.Sprintf("%d %s", count, label)
}

func contextCompactionStatus(status string) string {
	switch status {
	case "not compacted":
		return "ociosa"
	case "compacting context":
		return "compactando contexto"
	case "compacted manually":
		return "compactada manualmente"
	case "manual compactor completed without changes":
		return "compactação manual concluída sem alterações"
	case "manual compaction failed":
		return "falha na compactação manual"
	case "requested, awaiting manual compactor":
		return "solicitada, aguardando compactador manual"
	default:
		return status
	}
}

// onOff renders a boolean preference as "on"/"off" for config display.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (m model) configText() string {
	return renderCommandOutput(commandOutput{
		Title:  "Config",
		Status: commandStatusOK,
		Sections: []commandSection{
			{
				Title: "Runtime",
				Lines: []string{
					"runtime: go",
					fmt.Sprintf("max turns: %d", m.agentOptions.MaxTurns),
					"permission mode: " + string(m.permissionMode),
					"recaps: " + onOff(m.recapsEnabled),
				},
			},
			{
				Title: "Provider",
				Lines: []string{
					"provider: " + displayValue(m.providerName, "none"),
					"model: " + displayValue(m.modelName, "none"),
					"api key: " + apiKeyState(strings.TrimSpace(m.providerProfile.APIKey) != "" || m.providerProfile.APIKeyStored),
				},
			},
		},
	})
}

func (m model) debugText() string {
	state := "idle"
	if m.pending {
		state = "running"
	}
	return renderCommandOutput(commandOutput{
		Title:  "Debug",
		Status: commandStatusInfo,
		Sections: []commandSection{{
			Title: "Runtime",
			Lines: []string{
				"run state: " + state,
				"active run: " + fmt.Sprint(m.activeRunID),
				"pending permission: " + boolText(m.pendingPermission != nil),
			},
		}},
	})
}

// skillsText is the /skills fallback when NO skills are installed — an install
// hint. With skills present /skills opens the searchable skill picker instead
// (see newSkillPicker), matching how /model works.
func (m model) skillsText() string {
	return renderCommandOutput(commandOutput{
		Title:  "Skills",
		Status: commandStatusInfo,
		Sections: []commandSection{{
			Lines: []string{"No skills installed."},
		}},
		Hints: []string{
			"install one: create <skills-dir>/<name>/SKILL.md (see `qg skills`)",
		},
	})
}
