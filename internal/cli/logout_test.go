package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/oauth"
)

// logoutEnv isolates every credential location in a temp dir and returns deps
// whose config path is the one the credential store lives beside.
func logoutEnv(t *testing.T) (appDeps, string) {
	t.Helper()
	withAuthStore(t)
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	t.Setenv("QG_API_KEY", "")
	t.Setenv("QUEBRAGALHO_API_KEY", "")
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	configPath := filepath.Join(xdg, "qg", "config.json")
	return fillAppDeps(appDeps{userConfigPath: func() (string, error) { return configPath, nil }}), configPath
}

func TestLogoutMessageNamesEnvVariableNeverItsValue(t *testing.T) {
	t.Setenv("QG_API_KEY", "qg_super_secret_value")
	msg := logoutMessage(LogoutResult{Removed: true, EnvStillActive: "QG_API_KEY"})
	if !strings.Contains(msg, "QG_API_KEY") {
		t.Fatalf("message should name the variable: %q", msg)
	}
	if strings.Contains(msg, "qg_super_secret_value") {
		t.Fatalf("message leaked the variable value: %q", msg)
	}
}

// seedQGCredentials stores every kind of QG credential: vault key, marker,
// inline key and an OAuth token.
func seedQGCredentials(t *testing.T, configPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"providers":[{"name":"qg","apiKey":"sk-inline","apiKeyStored":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.ProviderKeyStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("qg", "sk-vault"); err != nil {
		t.Fatal(err)
	}
	tokens, err := oauth.NewStore(oauth.StoreOptions{FilePath: os.Getenv("QG_OAUTH_TOKENS_PATH")})
	if err != nil {
		t.Fatal(err)
	}
	if err := tokens.Save(oauth.ProviderKey("qg"), oauth.Token{AccessToken: "at", RefreshToken: "rt"}); err != nil {
		t.Fatal(err)
	}
}

func assertQGCredentialsGone(t *testing.T, configPath string) {
	t.Helper()
	store, _ := config.ProviderKeyStore()
	if _, ok, _ := store.Get("qg"); ok {
		t.Error("vault key still present")
	}
	tokens, _ := oauth.NewStore(oauth.StoreOptions{FilePath: os.Getenv("QG_OAUTH_TOKENS_PATH")})
	if statuses, _ := tokens.Status(oauth.KeyPrefixProvider); len(statuses) != 0 {
		t.Errorf("oauth token still present: %v", statuses)
	}
	raw, _ := os.ReadFile(configPath)
	if strings.Contains(string(raw), "sk-inline") || strings.Contains(string(raw), "apiKeyStored") {
		t.Errorf("config still holds credentials: %s", raw)
	}
	if !strings.Contains(string(raw), `"qg"`) {
		t.Errorf("profile was removed from config: %s", raw)
	}
}

func TestLogoutQGClearsStoreMarkerInlineAndOAuth(t *testing.T) {
	deps, configPath := logoutEnv(t)
	seedQGCredentials(t, configPath)

	result, err := logoutQG(deps, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.KeyRemoved || !result.InlineCleared || !result.OAuthRemoved || result.EnvStillActive != "" {
		t.Fatalf("result = %+v, want all three removed and no env", result)
	}
	assertQGCredentialsGone(t, configPath)
}

func TestLogoutQGWarnsWhenEnvKeySet(t *testing.T) {
	deps, configPath := logoutEnv(t)
	seedQGCredentials(t, configPath)
	t.Setenv("QG_API_KEY", "sk-env")

	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"logout"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "QG_API_KEY") || strings.Contains(stdout.String(), "sk-env") {
		t.Fatalf("output = %q, want a warning naming QG_API_KEY without its value", stdout.String())
	}
	assertQGCredentialsGone(t, configPath)
}

func TestLogoutEnvStillActiveNamesFallbackVariable(t *testing.T) {
	deps, _ := logoutEnv(t)
	t.Setenv("QUEBRAGALHO_API_KEY", "sk-env")
	result, err := logoutQG(deps, &bytes.Buffer{})
	if err != nil || result.EnvStillActive != "QUEBRAGALHO_API_KEY" {
		t.Fatalf("result = %+v err=%v, want QUEBRAGALHO_API_KEY", result, err)
	}
}

func TestLogoutNoopWhenLoggedOut(t *testing.T) {
	deps, _ := logoutEnv(t)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"logout"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "já deslogado") {
		t.Fatalf("output = %q, want the already-logged-out message", stdout.String())
	}
}

func TestLogoutJSON(t *testing.T) {
	deps, configPath := logoutEnv(t)
	seedQGCredentials(t, configPath)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"logout", "--json"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"keyRemoved": true`, `"inlineCleared": true`, `"oauthRemoved": true`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("json = %s, missing %s", stdout.String(), want)
		}
	}
}

func TestAuthLogoutQGUsesSharedCore(t *testing.T) {
	deps, configPath := logoutEnv(t)
	seedQGCredentials(t, configPath)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "logout", "qg"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	assertQGCredentialsGone(t, configPath)
}

// Legacy consumers of `auth logout qg --json` read "provider" and "removed".
func TestAuthLogoutQGJSONKeepsLegacyFields(t *testing.T) {
	deps, configPath := logoutEnv(t)
	seedQGCredentials(t, configPath)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "logout", "qg", "--json"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"provider": "qg"`, `"removed": true`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("json = %s, missing %s", stdout.String(), want)
		}
	}
}

func TestLogoutIgnoresWhitespaceOnlyEnvKey(t *testing.T) {
	deps, _ := logoutEnv(t)
	t.Setenv("QG_API_KEY", "  ")
	result, err := logoutQG(deps, &bytes.Buffer{})
	if err != nil || result.EnvStillActive != "" {
		t.Fatalf("result = %+v err=%v, want no env warning", result, err)
	}
}

func TestLogoutSurfacesConfigPathError(t *testing.T) {
	deps, _ := logoutEnv(t)
	deps.userConfigPath = func() (string, error) { return "", errors.New("no config dir") }
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"logout"}, &stdout, &stderr, deps); code == exitSuccess {
		t.Fatalf("exit = success, want failure; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no config dir") {
		t.Fatalf("stderr = %q, want the path error", stderr.String())
	}
}
