package providercatalog

import "strings"

// qgCredentialEnvVars are the environment variables that carry the user's
// Quebra Galho key: QG_API_KEY (documented first choice) and the catalog's
// QUEBRAGALHO_API_KEY. QG reads them itself (config.ProviderProfile.EnvAPIKey);
// nothing QG spawns needs them.
var qgCredentialEnvVars = []string{"QG_API_KEY", "QUEBRAGALHO_API_KEY"}

// QGCredentialEnvVars returns the names of the environment variables that carry
// the user's Quebra Galho key.
func QGCredentialEnvVars() []string {
	return append([]string(nil), qgCredentialEnvVars...)
}

// WithoutQGCredentials returns env (KEY=VALUE entries, as from os.Environ) minus
// the Quebra Galho key variables. Every path that hands the inherited
// environment to a child process (agent shell commands, hooks, MCP servers)
// runs it through here, so a key exported in the user's shell never reaches a
// process the model, a repo, or a plugin controls. Names compare
// case-insensitively, matching Windows environment semantics. The input slice
// is not modified.
func WithoutQGCredentials(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if isQGCredentialEnvVar(key) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func isQGCredentialEnvVar(key string) bool {
	for _, name := range qgCredentialEnvVars {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return true
		}
	}
	return false
}
