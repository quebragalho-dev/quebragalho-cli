package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providerhealth"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

func TestSetupMissingCredentialEnv(t *testing.T) {
	tests := []struct {
		name    string
		profile config.ProviderProfile
		wantEnv string
		want    bool
	}{
		{
			name: "catalog provider",
			profile: config.ProviderProfile{
				Name:      "qg",
				CatalogID: "qg",
			},
			wantEnv: "QUEBRAGALHO_API_KEY",
			want:    true,
		},
		{
			name: "openai compatible without catalog",
			profile: config.ProviderProfile{
				Name:         "custom",
				ProviderKind: config.ProviderKindOpenAICompatible,
			},
			wantEnv: "OPENAI_API_KEY",
			want:    true,
		},
		{
			name: "credential resolved",
			profile: config.ProviderProfile{
				Name:         "openai",
				ProviderKind: config.ProviderKindOpenAI,
				APIKey:       "sk-test",
			},
			want: false,
		},
		{
			// Regression for issue #555: a custom OpenAI-compatible profile saved
			// with no credential (e.g. a local llama.cpp server with no auth) must
			// not be reported as missing one, or it gets filtered out of /model on
			// the next run even though it was configured exactly as intended.
			name: "custom openai compatible with no credential configured",
			profile: config.ProviderProfile{
				Name:      "local-llama",
				CatalogID: "custom-openai-compatible",
				BaseURL:   "http://192.168.1.50:8080/v1",
			},
			want: false,
		},
		{
			// Self-heal regression: profiles saved by the pre-fix wizard were
			// stamped with the catalog's own guessed default (OPENAI_API_KEY)
			// even when the endpoint needed no auth. That stale value must be
			// treated the same as unset so already-broken saved profiles start
			// working again without a config.json rewrite.
			name: "custom openai compatible with stale legacy default env",
			profile: config.ProviderProfile{
				Name:      "local-llama",
				CatalogID: "custom-openai-compatible",
				BaseURL:   "http://192.168.1.50:8080/v1",
				APIKeyEnv: "OPENAI_API_KEY",
			},
			want: false,
		},
		{
			name: "custom anthropic compatible with no credential configured",
			profile: config.ProviderProfile{
				Name:      "local-proxy",
				CatalogID: "custom-anthropic-compatible",
				BaseURL:   "http://localhost:9000/anthropic",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotEnv, got := setupMissingCredentialEnv(tt.profile)
			if got != tt.want || gotEnv != tt.wantEnv {
				t.Fatalf("setupMissingCredentialEnv() = (%q, %v), want (%q, %v)", gotEnv, got, tt.wantEnv, tt.want)
			}
		})
	}
}

func TestSetupProviderOptionsUseRuntimeSupportedCatalog(t *testing.T) {
	options := setupProviderOptions()
	got := make([]string, 0, len(options))
	for _, option := range options {
		got = append(got, option.ID)
	}

	want := make([]string, 0)
	for _, descriptor := range providercatalog.All() {
		if providercatalog.RuntimeSupported(descriptor) {
			want = append(want, descriptor.ID)
		}
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("setupProviderOptions IDs = %#v, want runtime-supported catalog IDs %#v", got, want)
	}
	for _, excluded := range []string{"bedrock", "vertex"} {
		for _, id := range got {
			if id == excluded {
				t.Fatalf("setupProviderOptions included unsupported provider %q in %#v", excluded, got)
			}
		}
	}
}

func TestSaveSetupProviderStoresPastedAPIKey(t *testing.T) {
	// Use the encrypted-file backend in the temp config dir so the test never
	// touches the real OS keychain.
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	configPath := filepath.Join(t.TempDir(), "qg", "config.json")

	result, err := saveSetupProvider(appDeps{
		userConfigPath: func() (string, error) {
			return configPath, nil
		},
	}, tui.SetupSelection{
		CatalogID: "qg",
		Model:     "muse-spark-1.3-contributor",
		APIKey:    "sk-pasted-secret",
	}, setupSaveOptions{})
	if err != nil {
		t.Fatalf("saveSetupProvider() error = %v", err)
	}

	if result.Provider.APIKey != "sk-pasted-secret" {
		t.Fatalf("Provider.APIKey = %q, want pasted key", result.Provider.APIKey)
	}
	if result.Provider.APIKeyEnv != "" {
		t.Fatalf("Provider.APIKeyEnv = %q, want empty when API key is pasted", result.Provider.APIKeyEnv)
	}

	cfg := readFileConfig(t, configPath)
	if cfg.ActiveProvider != "qg" {
		t.Fatalf("ActiveProvider = %q, want qg", cfg.ActiveProvider)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("Providers = %#v, want one provider", cfg.Providers)
	}
	// The capture flip: config.json must NOT hold the cleartext key — it carries the
	// APIKeyStored marker, and the secret lives in the co-located credential store.
	if cfg.Providers[0].APIKey != "" || cfg.Providers[0].APIKeyEnv != "" {
		t.Fatalf("config must not persist the key: APIKey %q APIKeyEnv %q", cfg.Providers[0].APIKey, cfg.Providers[0].APIKeyEnv)
	}
	if !cfg.Providers[0].APIKeyStored {
		t.Fatal("expected APIKeyStored marker in config")
	}
	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if key, ok, _ := store.Get("qg"); !ok || key != "sk-pasted-secret" {
		t.Fatalf("stored key = %q,%v; want sk-pasted-secret in the credential store", key, ok)
	}
}

func TestSaveSetupProviderStoresCustomEndpointSelection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "qg", "config.json")

	result, err := saveSetupProvider(appDeps{
		userConfigPath: func() (string, error) {
			return configPath, nil
		},
	}, tui.SetupSelection{
		CatalogID: "qg",
		Name:      "minimax",
		BaseURL:   "https://api.minimax.io/v1",
		Model:     "MiniMax-M3",
	}, setupSaveOptions{})
	if err != nil {
		t.Fatalf("saveSetupProvider() error = %v", err)
	}

	if result.Provider.Name != "minimax" {
		t.Fatalf("Provider.Name = %q, want minimax", result.Provider.Name)
	}
	if result.Provider.BaseURL != "https://api.minimax.io/v1" {
		t.Fatalf("Provider.BaseURL = %q, want custom endpoint", result.Provider.BaseURL)
	}
	if result.Provider.Model != "MiniMax-M3" {
		t.Fatalf("Provider.Model = %q, want typed model", result.Provider.Model)
	}

	cfg := readFileConfig(t, configPath)
	if cfg.ActiveProvider != "minimax" {
		t.Fatalf("ActiveProvider = %q, want minimax", cfg.ActiveProvider)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("Providers = %#v, want one provider", cfg.Providers)
	}
	profile := cfg.Providers[0]
	if profile.Name != "minimax" || profile.CatalogID != "qg" {
		t.Fatalf("stored provider identity = %#v, want minimax on the qg catalog", profile)
	}
	if profile.BaseURL != "https://api.minimax.io/v1" || profile.Model != "MiniMax-M3" {
		t.Fatalf("stored provider endpoint/model = %#v", profile)
	}
}

func TestSaveSetupProviderCLIOptionsOverrideCustomEndpointSelection(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "qg", "config.json")

	result, err := saveSetupProvider(appDeps{
		userConfigPath: func() (string, error) {
			return configPath, nil
		},
	}, tui.SetupSelection{
		CatalogID: "qg",
		Name:      "selection-name",
		BaseURL:   "https://selection.example/v1",
		Model:     "selection-model",
	}, setupSaveOptions{
		name:    "cli-name",
		baseURL: "https://cli.example/v1",
	})
	if err != nil {
		t.Fatalf("saveSetupProvider() error = %v", err)
	}

	if result.Provider.Name != "cli-name" {
		t.Fatalf("Provider.Name = %q, want CLI name", result.Provider.Name)
	}
	if result.Provider.BaseURL != "https://cli.example/v1" {
		t.Fatalf("Provider.BaseURL = %q, want CLI endpoint", result.Provider.BaseURL)
	}
	if result.Provider.Model != "selection-model" {
		t.Fatalf("Provider.Model = %q, want selection model", result.Provider.Model)
	}

	cfg := readFileConfig(t, configPath)
	if cfg.ActiveProvider != "cli-name" {
		t.Fatalf("ActiveProvider = %q, want cli-name", cfg.ActiveProvider)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("Providers = %#v, want one provider", cfg.Providers)
	}
	profile := cfg.Providers[0]
	if profile.Name != "cli-name" || profile.BaseURL != "https://cli.example/v1" || profile.Model != "selection-model" {
		t.Fatalf("stored provider = %#v, want CLI name/baseURL and selection model", profile)
	}
}

func TestVerifySetupProviderDistinguishesMissingFromRejectedKey(t *testing.T) {
	// AUDIT-M1: verifying a remote provider with no key must say "no API key found",
	// not probe and report "the provider rejected the API key". The probe must not run.
	probed := false
	deps := appDeps{probeProviderHealth: func(context.Context, providerhealth.Options) providerhealth.Result {
		probed = true
		return providerhealth.Result{}
	}}
	_, err := verifySetupProvider(deps, config.ProviderProfile{
		Name:         "openai",
		ProviderKind: config.ProviderKindOpenAI,
		BaseURL:      "https://api.openai.com/v1",
		Model:        "gpt-4.1",
	})
	if err == nil || !strings.Contains(err.Error(), "no API key found") {
		t.Fatalf("missing key should report 'no API key found', got %v", err)
	}
	if !strings.Contains(err.Error(), "QG_API_KEY") || !strings.Contains(err.Error(), "QUEBRAGALHO_API_KEY") || !strings.Contains(err.Error(), "qg setup qg") {
		t.Fatalf("missing key must cite both gateway env vars and qg setup qg, got %v", err)
	}
	if strings.Contains(err.Error(), "qg auth") {
		t.Fatalf("missing key must not cite the removed auth commands, got %v", err)
	}
	if probed {
		t.Fatal("must not probe the endpoint when no key is configured")
	}

	// A keyless LOCAL provider still probes (no key needed).
	probed = false
	_, _ = verifySetupProvider(deps, config.ProviderProfile{
		Name:         "ollama",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      "http://localhost:11434/v1",
		Model:        "llama3",
	})
	if !probed {
		t.Fatal("a keyless local provider should still be probed")
	}
}

// `qg setup qg --api-key <key>` persists the key in one step: setup is fully
// self-sufficient without a pre-existing env var.
func TestSetupQGWithAPIKeyFlagStoresKey(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	deps := appDeps{
		userConfigPath: func() (string, error) { return configPath, nil },
	}

	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"setup", "qg", "--api-key", "sk-setup-key"}, &stdout, &stderr, deps)
	if exitCode != exitSuccess {
		t.Fatalf("exit = %d, want %d: %s", exitCode, exitSuccess, stderr.String())
	}

	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatalf("ProviderKeyStoreAt: %v", err)
	}
	stored, ok, err := store.Get("qg")
	if err != nil || !ok || stored != "sk-setup-key" {
		t.Fatalf("stored key = (%q, %v, %v), want sk-setup-key", stored, ok, err)
	}
	cfg := readLoginConfig(t, configPath)
	if len(cfg.Providers) != 1 || !cfg.Providers[0].APIKeyStored {
		t.Fatalf("providers = %#v, want one apiKeyStored profile", cfg.Providers)
	}
	if cfg.ActiveProvider != "qg" {
		t.Fatalf("ActiveProvider = %q, want qg", cfg.ActiveProvider)
	}
}

// A store failure during setup must warn: the key lands inline in config.json
// in plaintext, and a silent downgrade of the storage guarantee is a defect.
func TestSetupWarnsWhenKeyStoreFallsBack(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "bogus") // credstore.New rejects it: store unavailable
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	deps := appDeps{
		userConfigPath: func() (string, error) { return configPath, nil },
	}

	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"setup", "qg", "--api-key", "sk-setup-fallback"}, &stdout, &stderr, deps)
	if exitCode != exitSuccess {
		t.Fatalf("exit = %d, want %d: %s", exitCode, exitSuccess, stderr.String())
	}
	warn := stderr.String()
	if !strings.Contains(warn, "warning:") || !strings.Contains(warn, "plaintext") {
		t.Fatalf("stderr must warn about the plaintext fallback, got %q", warn)
	}
	if strings.Contains(warn, "sk-setup-fallback") {
		t.Fatalf("warning leaked the API key: %q", warn)
	}
	cfg := readLoginConfig(t, configPath)
	if len(cfg.Providers) != 1 || cfg.Providers[0].APIKey != "sk-setup-fallback" {
		t.Fatalf("expected the key inline after fallback, got %#v", cfg.Providers)
	}
	if cfg.Providers[0].APIKeyStored {
		t.Fatalf("fallback must not claim apiKeyStored: %#v", cfg.Providers[0])
	}
}

// login → setup must not orphan the stored key: re-running setup without a key
// keeps the apiKeyStored marker so the credential store stays reachable.
func TestSetupAfterLoginKeepsStoredKey(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	deps := appDeps{
		stdin:          strings.NewReader("sk-login-key\n"),
		userConfigPath: func() (string, error) { return configPath, nil },
	}
	deps.validateLoginKey = func(context.Context, string) error { return nil }

	if code := runWithDeps([]string{"login"}, io.Discard, &bytes.Buffer{}, deps); code != exitSuccess {
		t.Fatalf("login exit = %d, want success", code)
	}
	deps.stdin = strings.NewReader("")
	if code := runWithDeps([]string{"setup", "qg"}, io.Discard, &bytes.Buffer{}, deps); code != exitSuccess {
		t.Fatalf("setup exit = %d, want success", code)
	}

	cfg := readLoginConfig(t, configPath)
	if len(cfg.Providers) != 1 || !cfg.Providers[0].APIKeyStored {
		t.Fatalf("providers = %#v, want the apiKeyStored marker preserved", cfg.Providers)
	}
	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatalf("ProviderKeyStoreAt: %v", err)
	}
	if stored, ok, _ := store.Get("qg"); !ok || stored != "sk-login-key" {
		t.Fatalf("stored key = (%q, %v), want sk-login-key", stored, ok)
	}
}
