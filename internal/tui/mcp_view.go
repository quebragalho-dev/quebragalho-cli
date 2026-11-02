package tui

import (
	"fmt"
	"strings"
	"time"
)

type MCPViewState struct {
	Servers     []MCPServerView
	Tools       []MCPToolView
	Permissions MCPPermissionSummary
	OAuth       MCPOAuthSummary
}

type MCPServerView struct {
	Name      string
	Transport string
	State     string
	Target    string
	Auth      string
	ToolCount int
}

type MCPToolView struct {
	ServerName   string
	Name         string
	RegistryName string
	SideEffect   string
	Permission   string
	Description  string
}

type MCPPermissionSummary struct {
	Mode         string
	GrantCount   int
	ServerGrants int
	ToolGrants   int
	PromptCount  int
	DeniedCount  int
	Grants       []MCPPermissionGrantView
}

type MCPPermissionGrantView struct {
	Target     string
	Autonomy   string
	ApprovedAt string
}

type MCPOAuthSummary struct {
	Servers []MCPOAuthServerView
}

type MCPOAuthServerView struct {
	ServerName      string
	Configured      bool
	HasToken        bool
	HasRefreshToken bool
	TokenType       string
	Scopes          []string
	ExpiresAt       time.Time
	Expired         bool
}

func renderMCPView(state MCPViewState, width int) string {
	lines := []string{
		"Gerenciar servidores MCP",
		pluralCount(len(state.Servers), "server"),
		"",
	}

	if !hasMCPViewContent(state) {
		lines = append(lines,
			"MCPs do usuário",
			"  Nenhum servidor MCP configurado.",
			"  › Adicionar servidor MCP      qg mcp add <name> --url <url>",
			"  › Adicionar MCP stdio local qg mcp add <name> -- <command> [args...]",
			"  › Listar configurados     qg mcp list",
			"",
			"Ações",
			"  adicionar remoto · adicionar stdio · listar · verificar",
		)
		return fitMCPManagerLines(lines, width)
	}

	if len(state.Servers) > 0 {
		lines = append(lines, "MCPs do usuário")
		lines = append(lines, mcpManagerServerLines(state.Servers)...)
	}
	if len(state.Tools) > 0 {
		lines = append(lines, "", "Ferramentas")
		lines = append(lines, mcpToolLines(state.Tools)...)
	}
	if hasMCPPermissionSummary(state.Permissions) {
		lines = append(lines, "", "Permissões")
		for _, line := range mcpPermissionLines(state.Permissions) {
			lines = append(lines, "  "+line)
		}
	}
	if len(state.OAuth.Servers) > 0 {
		lines = append(lines, "", "OAuth")
		lines = append(lines, mcpOAuthLines(state.OAuth.Servers)...)
	}

	lines = append(lines,
		"",
		"Ações",
		"  adicionar: qg mcp add <name> --url <url>",
		"  desconectar: qg mcp disable <name>",
		"  reconectar: qg mcp enable <name>",
		"  remover: qg mcp remove <name>",
	)
	return fitMCPManagerLines(lines, width)
}

func hasMCPViewContent(state MCPViewState) bool {
	return hasMCPOperationalContent(state) ||
		hasMCPPermissionActivity(state.Permissions)
}

func hasMCPOperationalContent(state MCPViewState) bool {
	return len(state.Servers) > 0 ||
		len(state.Tools) > 0 ||
		len(state.OAuth.Servers) > 0
}

func hasMCPPermissionActivity(summary MCPPermissionSummary) bool {
	return summary.GrantCount > 0 ||
		summary.ServerGrants > 0 ||
		summary.ToolGrants > 0 ||
		summary.PromptCount > 0 ||
		summary.DeniedCount > 0 ||
		len(summary.Grants) > 0
}

func mcpManagerServerLines(servers []MCPServerView) []string {
	lines := make([]string, 0, len(servers)*3)
	for index, server := range servers {
		name := displayValue(strings.TrimSpace(server.Name), "sem nome")
		transport := displayValue(strings.TrimSpace(server.Transport), "desconhecido")
		state := displayValue(strings.TrimSpace(server.State), "configurado")
		prefix := "  "
		if index == 0 {
			prefix = "› "
		}

		parts := []string{name, state}
		if auth := strings.TrimSpace(server.Auth); auth != "" {
			parts = append(parts, auth)
		}
		if server.ToolCount > 0 {
			parts = append(parts, pluralCount(server.ToolCount, "tool"))
		}
		parts = append(parts, transport)
		lines = append(lines, prefix+strings.Join(parts, " · "))
		if target := strings.TrimSpace(server.Target); target != "" {
			lines = append(lines, "  "+target)
		}
		if actions := strings.TrimSpace(mcpServerActionLine(server)); actions != "" {
			lines = append(lines, "  "+actions)
		}
	}
	return lines
}

func mcpToolLines(tools []MCPToolView) []string {
	grouped := map[string][]MCPToolView{}
	order := []string{}
	for _, tool := range tools {
		serverName := displayValue(strings.TrimSpace(tool.ServerName), "desconhecido")
		if _, ok := grouped[serverName]; !ok {
			order = append(order, serverName)
		}
		grouped[serverName] = append(grouped[serverName], tool)
	}

	lines := make([]string, 0, len(tools)+len(order))
	for _, serverName := range order {
		serverTools := grouped[serverName]
		lines = append(lines, commandBullet(fmt.Sprintf("%s ferramentas (%d)", serverName, len(serverTools))))
		for _, tool := range serverTools {
			lines = append(lines, "  "+commandBullet(mcpToolLine(tool)))
		}
	}
	return lines
}

func mcpToolLine(tool MCPToolView) string {
	name := strings.TrimSpace(tool.RegistryName)
	if name == "" {
		name = strings.Trim(strings.Join([]string{"mcp", tool.ServerName, tool.Name}, "_"), "_")
	}
	if name == "" {
		name = "mcp_tool"
	}

	sideEffect := displayValue(strings.TrimSpace(tool.SideEffect), "desconhecido")
	permission := displayValue(strings.TrimSpace(tool.Permission), "perguntar")
	displayName := displayValue(strings.TrimSpace(tool.Name), name)
	line := fmt.Sprintf("%s [%s/%s]", displayName, sideEffect, permission)
	if name != "" && name != displayName {
		line += " - " + name
	}
	if target := mcpToolTarget(tool); target != "" && target != displayName {
		line += " - " + target
	}
	if description := strings.TrimSpace(tool.Description); description != "" {
		line += " - " + description
	}
	return line
}

func mcpToolTarget(tool MCPToolView) string {
	server := strings.TrimSpace(tool.ServerName)
	name := strings.TrimSpace(tool.Name)
	switch {
	case server != "" && name != "":
		return server + "/" + name
	case server != "":
		return server + "/*"
	default:
		return name
	}
}

func hasMCPPermissionSummary(summary MCPPermissionSummary) bool {
	return strings.TrimSpace(summary.Mode) != "" ||
		summary.GrantCount > 0 ||
		summary.ServerGrants > 0 ||
		summary.ToolGrants > 0 ||
		summary.PromptCount > 0 ||
		summary.DeniedCount > 0 ||
		len(summary.Grants) > 0
}

func mcpPermissionLines(summary MCPPermissionSummary) []string {
	lines := []string{
		"modo: " + displayValue(strings.TrimSpace(summary.Mode), "unknown"),
		fmt.Sprintf("autorizações persistentes: %d", summary.GrantCount),
		fmt.Sprintf("autorizações de servidor: %d", summary.ServerGrants),
		fmt.Sprintf("autorizações de ferramenta: %d", summary.ToolGrants),
		fmt.Sprintf("solicitadas nesta sessão: %d", summary.PromptCount),
		fmt.Sprintf("negadas nesta sessão: %d", summary.DeniedCount),
	}
	if len(summary.Grants) == 0 {
		return lines
	}
	for _, grant := range summary.Grants {
		target := displayValue(strings.TrimSpace(grant.Target), "desconhecido")
		autonomy := displayValue(strings.TrimSpace(grant.Autonomy), "baixo")
		line := fmt.Sprintf("%s [%s]", target, autonomy)
		if approvedAt := strings.TrimSpace(grant.ApprovedAt); approvedAt != "" {
			line += " aprovado em " + approvedAt
		}
		lines = append(lines, commandBullet(line))
	}
	return lines
}

func mcpOAuthLines(servers []MCPOAuthServerView) []string {
	lines := make([]string, 0, len(servers))
	for _, server := range servers {
		name := displayValue(strings.TrimSpace(server.ServerName), "sem nome")
		parts := []string{name}
		if server.Configured {
			parts = append(parts, "configurado")
			if server.HasToken {
				parts = append(parts, "token")
			} else {
				parts = append(parts, "sem token")
			}
			if server.HasRefreshToken {
				parts = append(parts, "refresh")
			}
			if server.Expired {
				parts = append(parts, "expirado")
			} else if !server.ExpiresAt.IsZero() {
				parts = append(parts, "expira em", server.ExpiresAt.UTC().Format(time.RFC3339))
			}
			if tokenType := strings.TrimSpace(server.TokenType); tokenType != "" {
				parts = append(parts, tokenType)
			}
			if len(server.Scopes) > 0 {
				parts = append(parts, "escopos", strings.Join(server.Scopes, ","))
			}
		} else {
			parts = append(parts, "não configurado")
		}
		lines = append(lines, commandBullet(strings.Join(parts, " ")))
		if actions := mcpOAuthActionLine(server); actions != "" {
			lines = append(lines, actions)
		}
	}
	return lines
}

func mcpServerActionLine(server MCPServerView) string {
	name := strings.TrimSpace(server.Name)
	if name == "" {
		return ""
	}
	actions := []string{"qg mcp check " + name}
	if strings.EqualFold(strings.TrimSpace(server.State), "disabled") {
		actions = append(actions, "qg mcp enable "+name)
	} else {
		actions = append(actions, "qg mcp disable "+name)
	}
	actions = append(actions, "qg mcp remove "+name)
	if strings.EqualFold(strings.TrimSpace(server.Auth), "oauth") {
		actions = append(actions, "qg mcp oauth login "+name)
	}
	return "    ações: " + strings.Join(actions, " | ")
}

func mcpOAuthActionLine(server MCPOAuthServerView) string {
	name := strings.TrimSpace(server.ServerName)
	if name == "" || !server.Configured {
		return ""
	}
	actions := []string{}
	if server.HasToken {
		actions = append(actions, "qg mcp oauth refresh "+name, "qg mcp oauth logout "+name)
	} else {
		actions = append(actions, "qg mcp oauth login "+name)
	}
	if server.Expired && !server.HasRefreshToken {
		actions = append(actions, "qg mcp oauth refresh "+name)
	}
	return "    ações: " + strings.Join(dedupeStrings(actions), " | ")
}

func dedupeStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func fitMCPManagerLines(lines []string, width int) string {
	if width <= 0 {
		return strings.Join(lines, "\n")
	}
	for index, line := range lines {
		lines[index] = fitStyledLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func pluralCount(count int, noun string) string {
	switch noun {
	case "server":
		if count == 1 {
			return "1 servidor"
		}
		return fmt.Sprintf("%d servidores", count)
	case "tool":
		if count == 1 {
			return "1 ferramenta"
		}
		return fmt.Sprintf("%d ferramentas", count)
	default:
		if count == 1 {
			return "1 " + noun
		}
		return fmt.Sprintf("%d %ss", count, noun)
	}
}
