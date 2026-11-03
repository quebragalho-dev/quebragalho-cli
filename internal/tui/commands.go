package tui

import (
	"strings"
)

type commandKind int

const (
	commandEmpty commandKind = iota
	commandPrompt
	commandHelp
	commandClear
	commandExit
	commandTools
	commandMCP
	commandPermissions
	commandPS
	commandStop
	commandAttach
	commandSandboxSetup
	commandProvider
	commandModel
	commandContext
	commandUsage
	commandConfig
	commandDebug
	commandDoctor
	commandLogout
	commandPlan
	commandSearch
	commandResume
	commandRename
	commandSpec
	commandInit
	commandCompact
	commandRewind
	commandEffort
	commandFast
	commandStyle
	commandTheme
	commandTranscript
	commandBash
	commandImage
	commandAddDir
	commandSelfCorrect
	commandTurns
	commandProfile
	commandRetry
	commandEdit
	commandCopy
	commandExport
	commandNew
	commandBTW
	commandSkills
	commandLoop
	commandGoal
	commandVoice
	commandSTTModel
	commandUnknown
)

type commandGroup string

const (
	commandGroupSession commandGroup = "session"
	commandGroupModel   commandGroup = "model"
	commandGroupRuntime commandGroup = "runtime"
	commandGroupTools   commandGroup = "tools"
	commandGroupMeta    commandGroup = "meta"
)

type commandDefinition struct {
	name        string
	aliases     []string
	usage       string
	group       commandGroup
	description string
	kind        commandKind
}

type parsedCommand struct {
	kind commandKind
	text string
	name string
}

var commandDefinitions = []commandDefinition{
	{
		name:        "/model",
		usage:       "/model [list|id]",
		group:       commandGroupModel,
		description: "Mostra ou troca o modelo ativo.",
		kind:        commandModel,
	},
	{
		name:        "/stt-model",
		usage:       "/stt-model",
		group:       commandGroupModel,
		description: "Escolhe o modelo de ditado (fala para texto).",
		kind:        commandSTTModel,
	},
	{
		name:        "/voice",
		usage:       "/voice",
		group:       commandGroupRuntime,
		description: "Liga ou desliga o modo de voz (" + voiceCaptureUsage + ").",
		kind:        commandVoice,
	},
	{
		name:        "/plan",
		usage:       "/plan [status|on|off]",
		group:       commandGroupSession,
		description: "Entra no modo de planejamento; status consulta e off sai.",
		kind:        commandPlan,
	},
	{
		name:        "/permissions",
		usage:       "/permissions",
		group:       commandGroupRuntime,
		description: "Mostra o modo de permissão e o ambiente isolado.",
		kind:        commandPermissions,
	},
	{
		name:        "/ps",
		usage:       "/ps",
		group:       commandGroupRuntime,
		description: "Lista terminais em segundo plano.",
		kind:        commandPS,
	},
	{
		name:        "/stop",
		usage:       "/stop [session_id]",
		group:       commandGroupRuntime,
		description: "Encerra os terminais em segundo plano.",
		kind:        commandStop,
	},
	{
		name:        "/attach",
		usage:       "/attach [session_id]",
		group:       commandGroupRuntime,
		description: "Digita direto em um terminal interativo em execução.",
		kind:        commandAttach,
	},
	{
		name:        "/sandbox-setup",
		usage:       "/sandbox-setup",
		group:       commandGroupRuntime,
		description: "Prepara o ambiente isolado nativo desta plataforma.",
		kind:        commandSandboxSetup,
	},
	{
		name:        "/tools",
		usage:       "/tools",
		group:       commandGroupTools,
		description: "Lista as ferramentas registradas.",
		kind:        commandTools,
	},
	{
		name:        "/skills",
		usage:       "/skills",
		group:       commandGroupTools,
		description: "Lista as skills instaladas; rode uma direto com /<skill-name> [args].",
		kind:        commandSkills,
	},
	{name: "/usage", usage: "/usage", group: commandGroupSession, description: "Resumo da última chamada, custo e janela de contexto.", kind: commandUsage},
	{
		name:        "/context",
		usage:       "/context",
		group:       commandGroupSession,
		description: "Mostra a pasta de trabalho e o contexto do runtime.",
		kind:        commandContext,
	},
	{
		name:        "/image",
		usage:       "/image <path> | clear",
		group:       commandGroupSession,
		description: "Anexa imagem local ou PDF à próxima mensagem. /image clear remove os anexos pendentes.",
		kind:        commandImage,
	},
	{
		name:        "/add-dir",
		usage:       "/add-dir [path]",
		group:       commandGroupRuntime,
		description: "Libera escrita fora da pasta de trabalho nesta sessão; sem argumento lista as pastas liberadas.",
		kind:        commandAddDir,
	},
	{
		name:        "/clear",
		usage:       "/clear",
		group:       commandGroupMeta,
		description: "Limpa o histórico visível.",
		kind:        commandClear,
	},
	{
		name:        "/new",
		usage:       "/new",
		group:       commandGroupSession,
		description: "Abre uma sessão nova; a atual pode ser retomada em /resume.",
		kind:        commandNew,
	},
	{
		name:        "/btw",
		usage:       "/btw [question]",
		group:       commandGroupSession,
		description: "Abre uma conversa paralela isolada; rode /btw de novo para voltar.",
		kind:        commandBTW,
	},
	{
		name:        "/search",
		aliases:     []string{"/find"},
		usage:       "/search <query>",
		group:       commandGroupTools,
		description: "Busca nos eventos da sessão. Exige o termo de busca.",
		kind:        commandSearch,
	},
	{
		name:        "/mcp",
		aliases:     []string{"/mcp-status"},
		usage:       "/mcp",
		group:       commandGroupTools,
		description: "Mostra servidores MCP.",
		kind:        commandMCP,
	},
	{
		name:        "/resume",
		aliases:     []string{"/sessions"},
		usage:       "/resume [id]",
		group:       commandGroupSession,
		description: "Lista sessões recentes ou mostra como retomar.",
		kind:        commandResume,
	},
	{
		name:        "/rename",
		usage:       "/rename [title]",
		group:       commandGroupSession,
		description: "Renomeia a sessão atual (sem título abre um editor).",
		kind:        commandRename,
	},
	{
		name:        "/spec",
		usage:       "/spec <task>",
		group:       commandGroupSession,
		description: "Escreve uma spec para revisar antes de editar.",
		kind:        commandSpec,
	},
	{
		name:        "/init",
		usage:       "/init",
		group:       commandGroupSession,
		description: "Investiga o repo e gera um AGENTS.md para o agente.",
		kind:        commandInit,
	},
	{
		name:        "/compact",
		usage:       "/compact [status|now]",
		group:       commandGroupSession,
		description: "Compacta o histórico agora ou mostra o estado (/compact status).",
		kind:        commandCompact,
	},
	{
		name:        "/transcript",
		usage:       "/transcript",
		group:       commandGroupSession,
		description: "Liga ou desliga a visão detalhada do histórico.",
		kind:        commandTranscript,
	},
	{
		name:        "/rewind",
		aliases:     []string{"/undo"},
		usage:       "/rewind [latest|<sequence>]",
		group:       commandGroupSession,
		description: "Volta a pasta de trabalho a um ponto de restauração e corta a sessão.",
		kind:        commandRewind,
	},
	{
		name:        "/effort",
		usage:       "/effort [list|level|auto]",
		group:       commandGroupModel,
		description: "Mostra ou ajusta o nível de raciocínio nos modelos suportados.",
		kind:        commandEffort,
	},
	{
		name:        "/fast",
		usage:       "/fast",
		group:       commandGroupModel,
		description: "Liga ou desliga o modo rápido nos modelos ChatGPT suportados.",
		kind:        commandFast,
	},
	{
		name:        "/style",
		usage:       "/style [list|balanced|concise|explanatory|review]",
		group:       commandGroupSession,
		description: "Mostra ou ajusta o estilo de resposta.",
		kind:        commandStyle,
	},
	{
		name:        "/selfcorrect",
		aliases:     []string{"/sc"},
		usage:       "/selfcorrect [status|on|off|tests|full|lsp]",
		group:       commandGroupSession,
		description: "Mostra ou ajusta a autocorreção após editar (padrão só LSP; on/tests/full incluem os testes do projeto; off/lsp desligam os testes).",
		kind:        commandSelfCorrect,
	},
	{
		name:        "/turns",
		usage:       "/turns [n]",
		group:       commandGroupSession,
		description: "Mostra ou ajusta os turnos com ferramenta por execução (aumente em tarefas longas).",
		kind:        commandTurns,
	},
	{
		name:        "/profile",
		usage:       "/profile [status|balanced|fast|thorough]",
		group:       commandGroupSession,
		description: "Mostra ou troca o perfil de execução (turnos, raciocínio, autocorreção, escalação; o modelo não muda).",
		kind:        commandProfile,
	},
	{
		name:        "/retry",
		usage:       "/retry",
		group:       commandGroupSession,
		description: "Reenvia sua última mensagem.",
		kind:        commandRetry,
	},
	{
		name:        "/edit",
		usage:       "/edit",
		group:       commandGroupSession,
		description: "Traz sua última mensagem para a entrada para editar e reenviar.",
		kind:        commandEdit,
	},
	{
		name:        "/copy",
		usage:       "/copy",
		group:       commandGroupSession,
		description: "Copia a última resposta.",
		kind:        commandCopy,
	},
	{
		name:        "/export",
		usage:       "/export [path]",
		group:       commandGroupSession,
		description: "Grava o histórico da conversa em um arquivo.",
		kind:        commandExport,
	},
	{
		name:        "/loop",
		usage:       "/loop [interval] <prompt|/command> | /loop list | /loop stop [id|all]",
		group:       commandGroupSession,
		description: "Repete uma mensagem ou comando em intervalo (ex.: /loop 5m /babysit-prs); sem intervalo roda no próprio ritmo.",
		kind:        commandLoop,
	},
	{
		name:        "/goal",
		usage:       "/goal [--tokens N] <objective> | status | pause | resume | edit | clear",
		group:       commandGroupSession,
		description: "Define um objetivo e trabalha nele até concluir.",
		kind:        commandGoal,
	},
	{
		name:        "/help",
		usage:       "/help",
		group:       commandGroupMeta,
		description: "Mostra os comandos disponíveis.",
		kind:        commandHelp,
	},
	{
		name:        "/doctor",
		aliases:     []string{"/health"},
		usage:       "/doctor [fix|--connectivity]",
		group:       commandGroupRuntime,
		description: "Mostra o diagnóstico, abre correções seguras ou testa o provedor.",
		kind:        commandDoctor,
	},
	{
		name:        "/logout",
		usage:       "/logout",
		group:       commandGroupRuntime,
		description: "Remove as credenciais salvas do Quebra Galho e fecha o app.",
		kind:        commandLogout,
	},
	{
		name:        "/config",
		usage:       "/config [recaps on|off]",
		group:       commandGroupRuntime,
		description: "Mostra a configuração ativa; alterna ajustes (recaps on|off).",
		kind:        commandConfig,
	},
	{
		name:        "/debug",
		aliases:     []string{"/debug-mode"},
		usage:       "/debug",
		group:       commandGroupRuntime,
		description: "Mostra o modo debug.",
		kind:        commandDebug,
	},
	{
		name:        "/theme",
		usage:       "/theme [list|auto|name]",
		group:       commandGroupSession,
		description: "Escolhe o tema de cor (sem argumento abre o seletor; auto detecta o fundo do terminal).",
		kind:        commandTheme,
	},
	{
		name:        "/exit",
		aliases:     []string{"/quit"},
		usage:       "/exit",
		group:       commandGroupMeta,
		description: "Sai do QG.",
		kind:        commandExit,
	},
}

func parseCommand(input string) parsedCommand {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return parsedCommand{kind: commandEmpty}
	}

	// "!cmd" is a shell escape (the footer advertises "! bash"): run it directly.
	if strings.HasPrefix(trimmed, "!") {
		return parsedCommand{kind: commandBash, text: strings.TrimSpace(trimmed[1:])}
	}

	if strings.HasPrefix(trimmed, "/") {
		name, args := splitCommand(trimmed)
		if name == "/provider" || name == "/providers" {
			return parsedCommand{kind: commandProvider, name: name, text: args}
		}
		command, ok := resolveCommand(name)
		if ok {
			return parsedCommand{kind: command.kind, name: command.name, text: args}
		}
		return parsedCommand{kind: commandUnknown, text: trimmed}
	}

	return parsedCommand{kind: commandPrompt, text: trimmed}
}

func splitCommand(input string) (string, string) {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return "", ""
	}

	name := parts[0]
	args := strings.TrimSpace(input[len(name):])
	return strings.ToLower(name), args
}

func resolveCommand(name string) (commandDefinition, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	for _, command := range commandDefinitions {
		if normalized == command.name {
			return command, true
		}
		for _, alias := range command.aliases {
			if normalized == alias {
				return command, true
			}
		}
	}
	return commandDefinition{}, false
}

func formatGroupedCommandHelp() string {
	lines := []string{"Comandos", "status: info"}
	for _, group := range commandGroupOrder() {
		groupLines := commandHelpLinesForGroup(group)
		if len(groupLines) == 0 {
			continue
		}
		lines = append(lines, commandGroupTitle(group))
		lines = append(lines, groupLines...)
	}
	lines = append(lines, "hint: escreva um texto para perguntar ao QG")
	return strings.Join(lines, "\n")
}

func commandHelpLinesForGroup(group commandGroup) []string {
	lines := []string{}
	for _, command := range commandDefinitions {
		if command.group != group {
			continue
		}
		lines = append(lines, "  "+formatCommandHelpLine(command))
	}
	return lines
}

func formatCommandHelpLine(command commandDefinition) string {
	label := command.usage
	if len(command.aliases) > 0 {
		label += " (" + strings.Join(command.aliases, ", ") + ")"
	}
	return label + " - " + command.description
}

func commandSelectionRequiresInput(name string) bool {
	command, ok := resolveCommand(name)
	return ok && commandUsageRequiresInput(command.usage)
}

func commandRequiredInputHint(name string) string {
	command, ok := resolveCommand(name)
	if !ok {
		return ""
	}
	placeholder := commandUsageRequiredPlaceholder(command.usage)
	if placeholder == "" {
		return ""
	}
	return "[" + placeholder + "]"
}

func commandUsageRequiresInput(usage string) bool {
	return commandUsageRequiredPlaceholder(usage) != ""
}

func commandUsageRequiredPlaceholder(usage string) string {
	optionalDepth := 0
	var placeholder strings.Builder
	inPlaceholder := false
	for _, char := range usage {
		if inPlaceholder {
			if char == '>' {
				return strings.TrimSpace(placeholder.String())
			}
			placeholder.WriteRune(char)
			continue
		}
		switch char {
		case '[':
			optionalDepth++
		case ']':
			if optionalDepth > 0 {
				optionalDepth--
			}
		case '<':
			if optionalDepth == 0 {
				inPlaceholder = true
			}
		}
	}
	return ""
}

func commandGroupOrder() []commandGroup {
	return []commandGroup{
		commandGroupModel,
		commandGroupSession,
		commandGroupRuntime,
		commandGroupTools,
		commandGroupMeta,
	}
}

func commandGroupTitle(group commandGroup) string {
	switch group {
	case commandGroupModel:
		return "Modelo"
	case commandGroupSession:
		return "Sessão"
	case commandGroupRuntime:
		return "Execução"
	case commandGroupTools:
		return "Ferramentas"
	case commandGroupMeta:
		return "Meta"
	default:
		return string(group)
	}
}
