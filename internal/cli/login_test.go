package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

func loginTestDeps(t *testing.T, stdin io.Reader) (appDeps, string) {
	t.Helper()
	t.Setenv("QG_CRED_STORAGE", "encrypted-file") // never touch the real OS keychain in tests
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	deps := appDeps{
		stdin:          stdin,
		userConfigPath: func() (string, error) { return configPath, nil },
	}
	return deps, configPath
}

func readLoginConfig(t *testing.T, configPath string) config.FileConfig {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg config.FileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

// The happy path: prompt reads the key, the gateway validates it, the key lands
// in the credential store (config.json keeps only the marker, never the secret).
func TestLoginSavesValidatedKey(t *testing.T) {
	deps, configPath := loginTestDeps(t, strings.NewReader("sk-test-login-key\n"))
	validated := ""
	deps.validateLoginKey = func(_ context.Context, key string) error {
		validated = key
		return nil
	}

	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"login"}, &stdout, &stderr, deps)
	if exitCode != exitSuccess {
		t.Fatalf("exit = %d, want %d: %s", exitCode, exitSuccess, stderr.String())
	}
	if validated != "sk-test-login-key" {
		t.Fatalf("validator received %q, want the prompted key", validated)
	}

	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatalf("ProviderKeyStoreAt: %v", err)
	}
	stored, ok, err := store.Get("qg")
	if err != nil || !ok || stored != "sk-test-login-key" {
		t.Fatalf("stored key = (%q, %v, %v), want sk-test-login-key", stored, ok, err)
	}

	cfg := readLoginConfig(t, configPath)
	if cfg.ActiveProvider != "qg" {
		t.Fatalf("ActiveProvider = %q, want qg", cfg.ActiveProvider)
	}
	if len(cfg.Providers) != 1 || !cfg.Providers[0].APIKeyStored {
		t.Fatalf("providers = %#v, want one apiKeyStored profile", cfg.Providers)
	}
	if cfg.Providers[0].APIKey != "" {
		t.Fatalf("config.json leaked the key: %q", cfg.Providers[0].APIKey)
	}
	if strings.Contains(stdout.String(), "sk-test-login-key") {
		t.Fatalf("stdout echoed the key: %q", stdout.String())
	}
}

// A rejected key must not be persisted, and the failure message must never
// contain the raw key even when the underlying error embeds it.
func TestLoginRejectsInvalidKeyWithoutSaving(t *testing.T) {
	deps, configPath := loginTestDeps(t, strings.NewReader("sk-bad-key\n"))
	deps.validateLoginKey = func(_ context.Context, key string) error {
		return errors.New("gateway rejected Bearer " + key)
	}

	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"login"}, &stdout, &stderr, deps)
	if exitCode != exitProvider {
		t.Fatalf("exit = %d, want %d: %s", exitCode, exitProvider, stderr.String())
	}
	if strings.Contains(stderr.String(), "sk-bad-key") {
		t.Fatalf("stderr leaked the key: %q", stderr.String())
	}
	if _, err := os.Stat(configPath); err == nil {
		cfg := readLoginConfig(t, configPath)
		if len(cfg.Providers) > 0 && cfg.Providers[0].APIKeyStored {
			t.Fatal("invalid key was persisted (apiKeyStored set)")
		}
	}
}

// --api-key makes login usable without a TTY (scripts, CI).
func TestLoginAcceptsFlagKey(t *testing.T) {
	deps, configPath := loginTestDeps(t, strings.NewReader(""))
	validated := ""
	deps.validateLoginKey = func(_ context.Context, key string) error {
		validated = key
		return nil
	}

	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"login", "--api-key", "sk-flag-key"}, &stdout, &stderr, deps)
	if exitCode != exitSuccess {
		t.Fatalf("exit = %d, want %d: %s", exitCode, exitSuccess, stderr.String())
	}
	if validated != "sk-flag-key" {
		t.Fatalf("validator received %q, want sk-flag-key", validated)
	}
	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatalf("ProviderKeyStoreAt: %v", err)
	}
	if stored, ok, _ := store.Get("qg"); !ok || stored != "sk-flag-key" {
		t.Fatalf("stored key = (%q, %v), want sk-flag-key", stored, ok)
	}
}

func TestLoginRejectsEmptyKeyWithoutValidating(t *testing.T) {
	deps, _ := loginTestDeps(t, strings.NewReader("\n"))
	called := false
	deps.validateLoginKey = func(context.Context, string) error {
		called = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"login"}, &stdout, &stderr, deps)
	if exitCode != exitUsage {
		t.Fatalf("exit = %d, want %d: %s", exitCode, exitUsage, stderr.String())
	}
	if called {
		t.Fatal("validator called for an empty key")
	}
	if !strings.Contains(stderr.String(), "--api-key") {
		t.Fatalf("stderr = %q, want --api-key guidance", stderr.String())
	}
}

func TestLoginHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"login", "--help"}, &stdout, &stderr, appDeps{stdin: strings.NewReader("")})
	if exitCode != exitSuccess {
		t.Fatalf("exit = %d, want %d: %s", exitCode, exitSuccess, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"qg login", "--api-key", "plaintext", "hidden", "--device", "QG_API_KEY", "QUEBRAGALHO_API_KEY", "qg exec --api-key"} {
		if !strings.Contains(out, want) {
			t.Fatalf("login help missing %q:\n%s", want, out)
		}
	}
	// The help must describe the real fallback instead of promising the key is
	// never written to config.json — with the store down it is, in plaintext.
	if strings.Contains(out, "never written to config.json") {
		t.Fatalf("login help still denies the plaintext fallback:\n%s", out)
	}
}

// A store failure must not go unnoticed: the key falls back into config.json in
// plaintext, so the CLI warns on stderr (never echoing the key itself).
func TestLoginWarnsWhenKeyStoreFallsBack(t *testing.T) {
	deps, configPath := loginTestDeps(t, strings.NewReader(""))
	t.Setenv("QG_CRED_STORAGE", "bogus") // credstore.New rejects it: store unavailable
	deps.validateLoginKey = func(context.Context, string) error { return nil }

	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"login", "--api-key", "sk-fallback-key"}, &stdout, &stderr, deps)
	if exitCode != exitSuccess {
		t.Fatalf("exit = %d, want %d: %s", exitCode, exitSuccess, stderr.String())
	}
	warn := stderr.String()
	if !strings.Contains(warn, "warning:") || !strings.Contains(warn, "plaintext") {
		t.Fatalf("stderr must warn about the plaintext fallback, got %q", warn)
	}
	if strings.Contains(warn, "sk-fallback-key") {
		t.Fatalf("warning leaked the API key: %q", warn)
	}

	cfg := readLoginConfig(t, configPath)
	if len(cfg.Providers) != 1 || cfg.Providers[0].APIKey != "sk-fallback-key" {
		t.Fatalf("expected the key inline after fallback, got %#v", cfg.Providers)
	}
	if cfg.Providers[0].APIKeyStored {
		t.Fatalf("fallback must not claim apiKeyStored: %#v", cfg.Providers[0])
	}
}

// The real validator hits GET <base>/models with the Bearer key: 200 accepts,
// a 401-class status rejects, and transport failures surface as errors.
func TestValidateQuebraGalhoKey(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sk-live" {
			t.Errorf("Authorization = %q, want Bearer sk-live", r.Header.Get("Authorization"))
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()

	if err := validateQuebraGalhoKey(context.Background(), server.URL, "sk-live", server.Client()); err != nil {
		t.Fatalf("valid key rejected: %v", err)
	}

	status = http.StatusUnauthorized
	if err := validateQuebraGalhoKey(context.Background(), server.URL, "sk-live", server.Client()); err == nil {
		t.Fatal("401 accepted, want rejection")
	}

	server.Close()
	if err := validateQuebraGalhoKey(context.Background(), server.URL, "sk-live", http.DefaultClient); err == nil {
		t.Fatal("transport failure accepted, want error")
	}
}

func TestLoginNoBrowserWithoutDeviceIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"login", "--no-browser"}, &stdout, &stderr, appDeps{stdin: strings.NewReader("")}); code != exitUsage {
		t.Fatalf("exit = %d, want usage", code)
	}
	if !strings.Contains(stderr.String(), "--device") {
		t.Fatalf("stderr = %q, want guidance naming --device", stderr.String())
	}
}

func TestLoginHelpPrecedenceMatchesCode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"login", "--help"}, &stdout, &stderr, appDeps{stdin: strings.NewReader("")}); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "--no-browser") {
		t.Fatalf("login help does not document --no-browser:\n%s", out)
	}
	// Real order: inline key > environment > saved key.
	inline := strings.Index(out, "inline")
	env := strings.Index(out, "QG_API_KEY")
	saved := strings.LastIndex(out, "saved key")
	if inline < 0 || env < 0 || saved < 0 || !(inline < env && env < saved) {
		t.Fatalf("help must order inline > env > saved key (idx %d, %d, %d):\n%s", inline, env, saved, out)
	}
}
