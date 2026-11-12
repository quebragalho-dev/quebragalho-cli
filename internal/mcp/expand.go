package mcp

import (
	"os"
	"regexp"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// mcpEnvRefPattern matches ${VAR} and ${VAR:-default}. The default cannot
// contain a closing brace; nested references are not expanded. Bare $VAR,
// empty ${}, and malformed names stay literal so a typo degrades to a visible
// literal instead of a silent empty string.
var mcpEnvRefPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?::-([^}]*))?\}`)

// ExpandServerEnv expands ${VAR} and ${VAR:-default} in the command, args,
// env values, URL, and header values from the process environment. OAuth
// material is intentionally left alone. Names with no value and no default
// expand to "" and are reported once in missing.
func ExpandServerEnv(raw config.MCPServerConfig) (config.MCPServerConfig, []string) {
	var missing []string
	seen := make(map[string]bool)
	expand := func(value string) string {
		if !strings.Contains(value, "${") {
			return value
		}
		return mcpEnvRefPattern.ReplaceAllStringFunc(value, func(match string) string {
			inner := match[2 : len(match)-1]
			name, def, hasDefault := strings.Cut(inner, ":-")
			if value, ok := os.LookupEnv(name); ok {
				return value
			}
			if hasDefault {
				return def
			}
			if !seen[name] {
				seen[name] = true
				missing = append(missing, name)
			}
			return ""
		})
	}
	expanded := raw
	expanded.Command = expand(raw.Command)
	if raw.Args != nil {
		expanded.Args = make([]string, len(raw.Args))
		for index, arg := range raw.Args {
			expanded.Args[index] = expand(arg)
		}
	}
	if raw.Env != nil {
		expanded.Env = make(map[string]string, len(raw.Env))
		for key, value := range raw.Env {
			expanded.Env[key] = expand(value)
		}
	}
	expanded.URL = expand(raw.URL)
	if raw.Headers != nil {
		expanded.Headers = make(map[string]string, len(raw.Headers))
		for key, value := range raw.Headers {
			expanded.Headers[key] = expand(value)
		}
	}
	return expanded, missing
}

// ExpandMCPConfigEnv expands every server in cfg. It returns the expanded
// config and, per server, the environment names that had no value and no
// default. Callers skip or report those servers instead of connecting with
// silently emptied targets.
func ExpandMCPConfigEnv(cfg config.MCPConfig) (config.MCPConfig, map[string][]string) {
	expanded := config.MCPConfig{Servers: make(map[string]config.MCPServerConfig, len(cfg.Servers))}
	missing := make(map[string][]string)
	for name, raw := range cfg.Servers {
		server, names := ExpandServerEnv(raw)
		expanded.Servers[name] = server
		if len(names) > 0 {
			missing[name] = names
		}
	}
	return expanded, missing
}

// MissingEnvError formats the skipped-server reason for a server whose
// targets referenced undefined environment variables.
func MissingEnvError(serverName string, names []string) string {
	return "Servidor MCP " + serverName + " usa variável(is) de ambiente não definida(s): " +
		strings.Join(names, ", ") + " (defina no ambiente ou use ${VAR:-padrão})"
}
