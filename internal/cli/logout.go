package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	// The config path locates the credential store, so resolve it before
	// deleting anything: a failure here must leave every credential in place.
	configPath, err := deps.userConfigPath()
	if err != nil {
		return result, err
	}
	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		return result, err
	}
	// Raw read, not config.Resolve: semantic validation errors must never block
	// removing credentials. Only unreadable or unparseable JSON aborts, since the
	// inline key could not be cleared then anyway.
	var saved config.FileConfig
	data, err := os.ReadFile(configPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return result, fmt.Errorf("read config %s: %w", configPath, err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &saved); err != nil {
			return result, fmt.Errorf("invalid config JSON %s: %w", configPath, err)
		}
	}
	manager, err := newAuthManager(deps, stdout)
	if err != nil {
		return result, err
	}
	// Every profile the login gate treats as QG, plus the legacy "qg" OAuth name.
	names := []string{providercatalog.QGID}
	for _, profile := range saved.Providers {
		if isQGProfile(profile) && !slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, profile.Name) }) {
			names = append(names, profile.Name)
		}
	}
	for _, name := range names {
		oauthRemoved, err := manager.Logout(name)
		if err != nil {
			return result, err
		}
		keyRemoved, err := store.Delete(name)
		if err != nil {
			return result, err
		}
		inlineCleared, err := config.ClearProviderInlineKey(configPath, name)
		if err != nil {
			return result, err
		}
		result.OAuthRemoved = result.OAuthRemoved || oauthRemoved
		result.KeyRemoved = result.KeyRemoved || keyRemoved
		result.InlineCleared = result.InlineCleared || inlineCleared
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
		_ = writeLogoutHelp(stdout)
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
	if _, err := fmt.Fprintln(stdout, logoutMessage(result)); err != nil {
		return exitCrash
	}
	return exitSuccess
}

func writeLogoutHelp(w io.Writer) error {
	_, err := fmt.Fprint(w, `Uso: qg logout [--json]

Remove a chave de API salva do Quebra Galho do cofre de credenciais, o
marcador apiKeyStored, qualquer apiKey inline em config.json e tokens OAuth legados.
O perfil continua em config.json; a próxima abertura interativa mostra o login.

QG_API_KEY / QUEBRAGALHO_API_KEY no seu ambiente não são tocadas: remova-as
você para sair por completo.

Opções:
      --json   Imprime o resultado em JSON
  -h, --help   Mostra esta ajuda
`)
	return err
}

// logoutMessage is the human summary shared by `qg logout` and the TUI /logout.
func logoutMessage(result LogoutResult) string {
	msg := "Já desconectado: nenhuma credencial Quebra Galho guardada."
	if result.Removed {
		msg = "Desconectado do Quebra Galho."
	}
	if result.EnvStillActive != "" {
		msg += fmt.Sprintf("\nAtenção: %s continua definido no seu ambiente e segue ativo; remova-o para desconectar de vez.", result.EnvStillActive)
	}
	return msg
}
