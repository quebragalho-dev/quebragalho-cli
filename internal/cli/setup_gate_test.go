package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

func TestResolveInteractiveSetupOpensOnboardingWithoutKey(t *testing.T) {
	resolved := config.ResolvedConfig{Provider: config.ProviderProfile{
		Name: "qg", CatalogID: "qg", ProviderKind: config.ProviderKindOpenAICompatible,
	}}
	got, onboarding := resolveInteractiveSetup(resolved)
	if !onboarding {
		t.Fatal("a keyless qg profile must open the first-run onboarding")
	}
	if got.ActiveProvider == "" {
		t.Fatal("resolved config should stay coherent")
	}
}

func TestResolveInteractiveSetupSkipsWhenStoredKeyExists(t *testing.T) {
	resolved := config.ResolvedConfig{Provider: config.ProviderProfile{
		Name: "qg", CatalogID: "qg", ProviderKind: config.ProviderKindOpenAICompatible, APIKeyStored: true,
	}}
	if _, onboarding := resolveInteractiveSetup(resolved); onboarding {
		t.Fatal("a saved credential must not reopen onboarding")
	}
}

// resolveSetupGate resolves a real user config file and returns the gate's verdict.
func resolveSetupGate(t *testing.T, configJSON string, env map[string]string) bool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(configJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if env == nil {
		env = map[string]string{}
	}
	resolved, err := config.Resolve(config.ResolveOptions{UserConfigPath: path, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	_, onboarding := resolveInteractiveSetup(resolved)
	return onboarding
}

func TestInteractiveSetupOpensWhenOnlyOtherProviderUsable(t *testing.T) {
	withAuthStore(t)
	cfg := `{"activeProvider":"backup","providers":[{"name":"qg","catalogId":"qg"},{"name":"backup","providerKind":"openai-compatible","baseUrl":"https://example.test/v1","apiKey":"sk-b","model":"m"}]}`
	if !resolveSetupGate(t, cfg, nil) {
		t.Fatal("a usable non-QG provider must not satisfy the login gate")
	}
}

func TestInteractiveSetupSkipsWithStoredQGKey(t *testing.T) {
	withAuthStore(t)
	cfg := `{"activeProvider":"qg","providers":[{"name":"qg","catalogId":"qg","apiKeyStored":true}]}`
	if resolveSetupGate(t, cfg, nil) {
		t.Fatal("a stored QG key must skip the login")
	}
}

func TestInteractiveSetupSkipsWithQGEnvKey(t *testing.T) {
	withAuthStore(t)
	cfg := `{"activeProvider":"qg","providers":[{"name":"qg","catalogId":"qg"}]}`
	for _, name := range []string{"QG_API_KEY", "QUEBRAGALHO_API_KEY"} {
		if resolveSetupGate(t, cfg, map[string]string{name: "sk-env"}) {
			t.Fatalf("%s must count as logged in", name)
		}
	}
}

func TestInteractiveSetupOpensAfterLogout(t *testing.T) {
	deps, configPath := logoutEnv(t)
	seedQGCredentials(t, configPath)
	if _, err := logoutQG(deps, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Resolve(config.ResolveOptions{UserConfigPath: configPath, Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, onboarding := resolveInteractiveSetup(resolved); !onboarding {
		t.Fatal("after logout the login must open again")
	}
}
