package cli

import (
	"context"
	"errors"
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

// mcpBetweenTurns adapts an MCP registration runtime to the agent loop's
// turn-boundary hook: pending list_changed catalogs apply between turns. A
// runtime that cannot refresh (test doubles) is skipped silently.
func mcpBetweenTurns(runtime mcpToolRuntime) func(context.Context) {
	return func(ctx context.Context) {
		refresher, ok := runtime.(interface {
			RefreshPendingTools(context.Context) (bool, error)
		})
		if !ok || refresher == nil {
			return
		}
		_, _ = refresher.RefreshPendingTools(ctx)
	}
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

// mcpSkippedIncludingOptional merges the critical runtime's skipped reasons
// with the optional runtime's settled ones, so /mcp shows every failure
// with its reason — including optional defaults like keyless Exa.
func mcpSkippedIncludingOptional(critical mcpToolRuntime, optional *optionalMCPStartup) map[string]string {
	merged := mcpSkippedReasons(critical)
	for _, skipped := range optional.Skipped() {
		if skipped.Err == nil {
			continue
		}
		merged[skipped.Name] = redaction.ErrorMessage(skipped.Err, redaction.Options{})
	}
	return merged
}

// mcpStatusFunc reports the live states of the critical runtime plus the
// optional one once it settled. Nil runtimes contribute nothing.
func mcpStatusFunc(critical mcpToolRuntime, optional *optionalMCPStartup) func() []mcp.ServerStatus {
	return func() []mcp.ServerStatus {
		var status []mcp.ServerStatus
		if runtime, ok := critical.(*mcp.Runtime); ok && runtime != nil {
			status = append(status, runtime.Status()...)
		}
		status = append(status, optional.Status()...)
		return status
	}
}

// mcpReconnectFunc reconnects one server on whichever runtime owns the name.
func mcpReconnectFunc(critical mcpToolRuntime, optional *optionalMCPStartup) func(context.Context, string) error {
	return func(ctx context.Context, serverName string) error {
		if runtime, ok := critical.(*mcp.Runtime); ok && runtime != nil {
			if err := runtime.Reconnect(ctx, serverName); err != nil {
				var unknown *mcp.UnknownServerError
				if !errors.As(err, &unknown) {
					return err
				}
			} else {
				return nil
			}
		}
		if optional != nil {
			return optional.ReconnectServer(ctx, serverName)
		}
		return &mcp.UnknownServerError{Name: serverName}
	}
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
	// A literal ${VAR} must stay literal: url.Parse would percent-encode it
	// to %7B...%7D, hiding which variable the user must set. Redact textually
	// instead, without touching the ${} references.
	if strings.Contains(raw, "${") {
		return redactMCPURLTextual(raw, marker)
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

// redactMCPURLTextual redacts a URL that carries literal ${VAR} references
// without parsing it (parsing would escape the references). userinfo is
// dropped, sensitive query/fragment values become the marker — except values
// that are themselves ${VAR} references, which are display-safe by
// construction and must stay literal so the user knows what to set.
// Everything else stays byte-for-byte.
func redactMCPURLTextual(raw string, marker string) string {
	rest := raw
	out := ""
	if index := strings.Index(rest, "?"); index < 0 {
		if hash := strings.Index(rest, "#"); hash >= 0 {
			return rest[:hash] + "#" + redactMCPEnvAwareQuery(rest[hash+1:], marker)
		}
		return redactMCPUserinfoPrefix(rest)
	} else {
		out = redactMCPUserinfoPrefix(rest[:index])
		rest = rest[index+1:]
	}
	if hash := strings.Index(rest, "#"); hash >= 0 {
		return out + "?" + redactMCPEnvAwareQuery(rest[:hash], marker) + "#" + redactMCPEnvAwareQuery(rest[hash+1:], marker)
	}
	return out + "?" + redactMCPEnvAwareQuery(rest, marker)
}

// redactMCPUserinfoPrefix drops a userinfo@ prefix from the authority part of
// a URL string without parsing it.
func redactMCPUserinfoPrefix(raw string) string {
	schemeSplit := strings.Index(raw, "://")
	authority := raw
	prefix := ""
	if schemeSplit >= 0 {
		prefix = raw[:schemeSplit+3]
		authority = raw[schemeSplit+3:]
	}
	slash := strings.Index(authority, "/")
	host := authority
	suffix := ""
	if slash >= 0 {
		host = authority[:slash]
		suffix = authority[slash:]
	}
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	return prefix + host + suffix
}

// redactMCPEnvAwareQuery redacts sensitive query values like
// redactMCPRawQuery, but leaves any pair containing a ${VAR} reference
// untouched: the reference is display-safe and must stay literal.
func redactMCPEnvAwareQuery(rawQuery string, marker string) string {
	parts := strings.Split(rawQuery, "&")
	for index, part := range parts {
		if part == "" {
			continue
		}
		key, _, hasValue := strings.Cut(part, "=")
		if strings.Contains(part, "${") {
			continue
		}
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
