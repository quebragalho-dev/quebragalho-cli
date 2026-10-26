package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

// LogoutResult reports which QG credentials a logout removed. The profile stays
// in config.json; only credentials go away.
type LogoutResult struct {
	KeyRemoved    bool `json:"keyRemoved"`
	InlineCleared bool `json:"inlineCleared"`
	OAuthRemoved  bool `json:"oauthRemoved"`
	// EnvStillActive names the API-key variable still set in the environment, which
	// logout cannot unset.
	EnvStillActive string `json:"envStillActive,omitempty"`
}

func (r LogoutResult) removedAny() bool { return r.KeyRemoved || r.InlineCleared || r.OAuthRemoved }

// logoutQG clears every saved QG credential: OAuth token, vault key and marker,
// and the inline apiKey. Removal errors surface instead of reporting success
// while a credential remains.
func logoutQG(deps appDeps, stdout io.Writer) (LogoutResult, error) {
	var result LogoutResult
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
	if configPath, perr := deps.userConfigPath(); perr == nil {
		if _, err := config.ClearProviderKeyStored(configPath, providercatalog.QGID); err != nil {
			return result, err
		}
		if result.InlineCleared, err = config.ClearProviderInlineKey(configPath, providercatalog.QGID); err != nil {
			return result, err
		}
	}
	for _, name := range []string{"QG_API_KEY", "QUEBRAGALHO_API_KEY"} {
		if os.Getenv(name) != "" {
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
	if result.removedAny() {
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
