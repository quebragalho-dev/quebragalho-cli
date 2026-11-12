package cli

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

type mcpToolListItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SideEffect  string `json:"sideEffect"`
	Permission  string `json:"permission"`
}

// registerMCPToolsForWorkspace resolves and registers the workspace's MCP servers.
// This spawns stdio servers, so it gates the project config layer behind the
// workspace-trust check: trustRoot is the ORIGINAL launch directory (resolved before
// any --worktree reassignment) so a worktree of a trusted repo inherits that trust.
// resolveTrust fails closed, so an empty trustRoot or a store-read error excludes the
// project layer and a cloned repo cannot spawn its ./.qg/config.json MCP servers.
//
// It returns a trustSkip alongside the runtime so the caller can fold the MCP gate
// into the one-line trust notice (mirroring the hooks and plugins chokepoints);
// otherwise a workspace whose only project config is MCP would be gated silently.
func registerMCPToolsForWorkspace(ctx context.Context, workspaceRoot string, registry *tools.Registry, deps appDeps, autonomy mcp.PermissionAutonomy, trustRoot string, runners ...*execution.Runner) (mcpToolRuntime, trustSkip, error) {
	options := mcp.RegisterOptions{}
	if len(runners) > 0 {
		options.Execution = runners[0]
	}
	return registerMCPToolsForWorkspaceWithOptions(ctx, workspaceRoot, registry, deps, autonomy, trustRoot, options)
}

func registerMCPToolsForWorkspaceWithOptions(ctx context.Context, workspaceRoot string, registry *tools.Registry, deps appDeps, autonomy mcp.PermissionAutonomy, trustRoot string, options mcp.RegisterOptions) (mcpToolRuntime, trustSkip, error) {
	excludeProject, trustCheckErrored := resolveTrust(trustRoot)
	skip := trustSkip{
		excludedProjectConfig: excludeProject && projectMCPConfigExists(workspaceRoot),
		trustCheckErrored:     trustCheckErrored,
	}
	cfg, err := deps.resolveMCPConfig(workspaceRoot, excludeProject)
	if err != nil {
		return nil, skip, err
	}
	if len(cfg.Servers) == 0 {
		return noopMCPRuntime{}, skip, nil
	}
	store, err := deps.newMCPStore()
	if err != nil {
		return nil, skip, err
	}
	options.PermissionStore = store
	options.Autonomy = autonomy
	options.WorkspaceRoot = workspaceRoot
	// The project root keys the one-time project approval gate inside
	// RegisterTools: unapproved project servers are skipped with the approve
	// command as the reason.
	options.ProjectRoot = workspaceRoot
	runtime, err := deps.registerMCPTools(ctx, registry, cfg, options)
	return runtime, skip, err
}

func execMCPAutonomy(options execOptions) mcp.PermissionAutonomy {
	if options.skipPermissionsUnsafe || strings.EqualFold(strings.TrimSpace(options.autonomy), "high") {
		return mcp.AutonomyHigh
	}
	if strings.EqualFold(strings.TrimSpace(options.autonomy), "medium") {
		return mcp.AutonomyMedium
	}
	return mcp.AutonomyLow
}

// mcpSkippedReasons maps each skipped server to its redacted startup failure
// reason, so the TUI manager shows why a configured server has no tools.
func mcpSkippedReasons(runtime mcpToolRuntime) map[string]string {
	reasons := make(map[string]string)
	if runtime == nil {
		return reasons
	}
	for _, skipped := range runtime.Skipped() {
		if skipped.Err == nil {
			continue
		}
		reasons[skipped.Name] = redaction.ErrorMessage(skipped.Err, redaction.Options{})
	}
	return reasons
}

func mcpToolList(registry *tools.Registry) []mcpToolListItem {
	registered := registry.All()
	items := make([]mcpToolListItem, 0, len(registered))
	for _, tool := range registered {
		if !strings.HasPrefix(tool.Name(), "mcp_") {
			continue
		}
		safety := tool.Safety()
		items = append(items, mcpToolListItem{
			Name:        tool.Name(),
			Description: tool.Description(),
			SideEffect:  string(safety.SideEffect),
			Permission:  string(safety.Permission),
		})
	}
	sort.Slice(items, func(left int, right int) bool {
		return items[left].Name < items[right].Name
	})
	return items
}

func formatMCPToolList(items []mcpToolListItem) string {
	if len(items) == 0 {
		return "Nenhuma ferramenta MCP configurada."
	}
	lines := []string{"Ferramentas MCP:"}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("  %s [%s/%s] - %s", item.Name, item.SideEffect, item.Permission, item.Description))
	}
	return strings.Join(lines, "\n")
}

func formatMCPServerList(servers map[string]config.MCPServerConfig, probe map[string]mcp.ProbeResult, missing map[string][]string, pending map[string]bool, trusted bool) string {
	if len(servers) == 0 {
		return "Nenhum servidor MCP configurado."
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := []string{"Servidores MCP:"}
	for _, name := range names {
		server := servers[name]
		state := "ativado"
		if server.Disabled {
			state = "desativado"
		}
		identity := strings.TrimSpace(server.Command)
		if identity == "" {
			identity = redactMCPURL(server.URL, "[REDACTED]")
		}
		suffix := ""
		if !server.Disabled {
			if !trusted && server.ProjectConfigured {
				suffix = " · ? não verificado (projeto não confiável)"
			} else if pending[name] {
				suffix = " · ? aguarda aprovação (qg mcp approve " + name + ")"
			} else if names, ok := missing[name]; ok {
				suffix = " · ✘ " + mcp.MissingEnvError(name, names)
			} else if result, ok := probe[name]; ok {
				if result.Err != nil {
					suffix = " · ✘ " + shortMCPProbeReason(result.Err)
				} else if result.ToolCount == 1 {
					suffix = " · ✔ 1 tool"
				} else {
					suffix = fmt.Sprintf(" · ✔ %d tools", result.ToolCount)
				}
			}
		}
		lines = append(lines, fmt.Sprintf("  %s [%s] %s %s%s", name, server.Type, state, identity, suffix))
	}
	return strings.Join(lines, "\n")
}

// shortMCPProbeReason condenses a probe failure to its first line so one dead
// server cannot flood the server list.
func shortMCPProbeReason(err error) string {
	reason := strings.TrimSpace(err.Error())
	if index := strings.Index(reason, "\n"); index >= 0 {
		reason = strings.TrimSpace(reason[:index])
	}
	const maxProbeReason = 160
	if len(reason) > maxProbeReason {
		reason = strings.TrimSpace(reason[:maxProbeReason]) + "…"
	}
	return reason
}

func redactMCPURL(raw string, marker string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.User != nil {
		parsed.User = nil
	}
	if parsed.RawQuery != "" {
		parsed.RawQuery = redactMCPRawQuery(parsed.RawQuery, marker)
	}
	if parsed.Fragment != "" {
		parsed.Fragment = redactMCPRawQuery(parsed.Fragment, marker)
	}
	out := parsed.String()
	if strings.TrimSpace(out) == "" {
		return raw
	}
	return out
}

func redactMCPRawQuery(rawQuery string, marker string) string {
	parts := strings.Split(rawQuery, "&")
	for index, part := range parts {
		if part == "" {
			continue
		}
		key, _, hasValue := strings.Cut(part, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			decodedKey = key
		}
		if !isSensitiveMCPDisplayKey(decodedKey) {
			continue
		}
		if hasValue {
			parts[index] = key + "=" + marker
		} else {
			parts[index] = key
		}
	}
	return strings.Join(parts, "&")
}

func isSensitiveMCPDisplayKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	if key == "key" {
		return true
	}
	for _, token := range []string{"token", "secret", "password", "passwd", "api_key", "apikey", "access_key", "auth", "credential"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}
