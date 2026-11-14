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
// expand to "" and are reported once in missing. Non-empty substituted values
// are returned in secrets so callers can redact them back out of user-,
// transcript-, and model-visible text.
func ExpandServerEnv(raw config.MCPServerConfig) (config.MCPServerConfig, []string, map[string]string) {
	var missing []string
	secrets := make(map[string]string)
	seen := make(map[string]bool)
	expand := func(value string) string {
		if !strings.Contains(value, "${") {
			return value
		}
		return mcpEnvRefPattern.ReplaceAllStringFunc(value, func(match string) string {
			inner := match[2 : len(match)-1]
			name, def, hasDefault := strings.Cut(inner, ":-")
			if value, ok := os.LookupEnv(name); ok {
				if value != "" {
					secrets[name] = value
				}
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
	return expanded, missing, secrets
}

// WithExpandedEnv returns a copy of the server with ${VAR}/${VAR:-default}
// expanded from the process environment (command, args, env values, URL,
// header values), plus the names that had no value and no default. The
// identity is untouched: approval and permission grants anchor the configured
// target, not the ambient environment.
func (server Server) WithExpandedEnv() (Server, []string) {
	raw := config.MCPServerConfig{
		Command: server.Command,
		Args:    server.Args,
		Env:     server.Env,
		URL:     server.URL,
		Headers: server.Headers,
	}
	expanded, missing, secrets := ExpandServerEnv(raw)
	server.Command = expanded.Command
	server.Args = expanded.Args
	server.Env = expanded.Env
	server.URL = expanded.URL
	server.Headers = expanded.Headers
	server.Secrets = secrets
	return server, missing
}

// MissingEnvError formats the skipped-server reason for a server whose
// targets referenced undefined environment variables.
func MissingEnvError(serverName string, names []string) string {
	return "Servidor MCP " + serverName + " usa variável(is) de ambiente não definida(s): " +
		strings.Join(names, ", ") + " (defina no ambiente ou use ${VAR:-padrão})"
}
