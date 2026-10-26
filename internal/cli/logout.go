package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

// LogoutResult reports which QG credentials a logout removed. The profile stays
// in config.json; only credentials go away.
type LogoutResult struct {
	// Provider and Removed keep the legacy `auth logout --json` shape.
	Provider      string `json:"provider"`
	Removed       bool   `json:"removed"`
	KeyRemoved    bool   `json:"keyRemoved"`
	InlineCleared bool   `json:"inlineCleared"`
	OAuthRemoved  bool   `json:"oauthRemoved"`
	// EnvStillActive names the API-key variable still set in the environment, which
	// logout cannot unset.
	EnvStillActive string `json:"envStillActive,omitempty"`
}

// logoutQG clears every saved QG credential: OAuth token, vault key and marker,
// and the inline apiKey. Removal errors surface instead of reporting success
// while a credential remains.
func logoutQG(deps appDeps, stdout io.Writer) (LogoutResult, error) {
	result := LogoutResult{Provider: providercatalog.QGID}
	manager, err := newAuthManager(deps, stdout)
	if err != nil {
		return result, err
	}
	if result.OAuthRemoved, err = manager.Logout(providercatalog.QGID); err != nil {
		return result, err
	}
	if result.KeyRemoved, err = config.ForgetProviderKey(providercatalog.QGID); err != nil {
		return result, err
	}
	configPath, err := deps.userConfigPath()
	if err != nil {
		return result, err
	}
	if result.InlineCleared, err = config.ClearProviderInlineKey(configPath, providercatalog.QGID); err != nil {
		return result, err
	}
	result.Removed = result.KeyRemoved || result.InlineCleared || result.OAuthRemoved
	for _, name := range []string{"QG_API_KEY", "QUEBRAGALHO_API_KEY"} {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			result.EnvStillActive = name
			break
		}
	}
	return result, nil
}

// runLogout implements `qg logout [--json]`, shared with `qg auth logout qg`.
func runLogout(args []string, stdout, stderr io.Writer, deps appDeps) int {
	parsed, err := parseAuthArgs("logout", args)
	if err != nil {
		return writeExecUsageError(stderr, err.Error())
	}
	if parsed.help {
		_ = writeAuthHelp(stdout)
		return exitSuccess
	}
	if len(parsed.positional) != 0 {
		return writeExecUsageError(stderr, "usage: qg logout [--json]")
	}
	result, err := logoutQG(deps, stdout)
	if err != nil {
		return writeAppError(stderr, redaction.ErrorMessage(err, redaction.Options{}), exitCrash)
	}
	if parsed.json {
		if err := writePrettyJSON(stdout, result); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	msg := "Nenhuma credencial salva do Quebra Galho: já deslogado.\n"
	if result.Removed {
		msg = "Logged out of Quebra Galho.\n"
	}
	if result.EnvStillActive != "" {
		msg += fmt.Sprintf("Aviso: %s continua definida no ambiente e segue ativa; remova-a do shell para sair de vez.\n", result.EnvStillActive)
	}
	if _, err := fmt.Fprint(stdout, msg); err != nil {
		return exitCrash
	}
	return exitSuccess
}
