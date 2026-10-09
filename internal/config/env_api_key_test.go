package config

import (
	"testing"
)

func TestEnvAPIKeyPrefersQGKeyForGatewayProfile(t *testing.T) {
	profile := ProviderProfile{
		Name:      "qg",
		CatalogID: "qg",
		BaseURL:   "https://api.quebragalho.dev/v1",
		APIKeyEnv: "QUEBRAGALHO_API_KEY",
	}
	env := map[string]string{
		"QG_API_KEY":          "sk-qg-short",
		"QUEBRAGALHO_API_KEY": "sk-qg-long",
	}
	got := profile.EnvAPIKey(func(key string) string { return env[key] })
	if got != "sk-qg-short" {
		t.Fatalf("EnvAPIKey() = %q, want QG_API_KEY to win for the gateway profile", got)
	}
}

func TestEnvAPIKeyFallsBackToNamedEnv(t *testing.T) {
	profile := ProviderProfile{
		Name:      "qg",
		CatalogID: "qg",
		BaseURL:   "https://api.quebragalho.dev/v1",
		APIKeyEnv: "QUEBRAGALHO_API_KEY",
	}
	env := map[string]string{"QUEBRAGALHO_API_KEY": "sk-qg-long"}
	got := profile.EnvAPIKey(func(key string) string { return env[key] })
	if got != "sk-qg-long" {
		t.Fatalf("EnvAPIKey() = %q, want QUEBRAGALHO_API_KEY fallback", got)
	}
}

// The QG gateway key must never be attached to a profile pointed at another
// endpoint: exporting QG_API_KEY in a shell that also runs a custom
// OpenAI-compatible provider would otherwise ship the gateway credential to a
// third-party host.
func TestEnvAPIKeyNeverFeedsForeignProfile(t *testing.T) {
	profile := ProviderProfile{
		Name:         "custom",
		ProviderKind: ProviderKindOpenAICompatible,
		BaseURL:      "https://proxy.example/v1",
		APIKeyEnv:    "OPENAI_API_KEY",
	}
	env := map[string]string{
		"QG_API_KEY":     "sk-qg-short",
		"OPENAI_API_KEY": "sk-foreign",
	}
	got := profile.EnvAPIKey(func(key string) string { return env[key] })
	if got != "sk-foreign" {
		t.Fatalf("EnvAPIKey() = %q, want the profile's own named env", got)
	}
	if got == "sk-qg-short" {
		t.Fatal("EnvAPIKey() leaked QG_API_KEY to a foreign endpoint")
	}

	onlyQG := map[string]string{"QG_API_KEY": "sk-qg-short"}
	if got := profile.EnvAPIKey(func(key string) string { return onlyQG[key] }); got != "" {
		t.Fatalf("EnvAPIKey() = %q, want empty when only QG_API_KEY is set for a foreign profile", got)
	}
}

func TestEnvAPIKeyAcceptsQGVarAsNamedEnv(t *testing.T) {
	profile := ProviderProfile{
		Name:      "qg",
		CatalogID: "qg",
		BaseURL:   "https://api.quebragalho.dev/v1",
		APIKeyEnv: "QG_API_KEY",
	}
	env := map[string]string{"QG_API_KEY": "sk-qg-short"}
	got := profile.EnvAPIKey(func(key string) string { return env[key] })
	if got != "sk-qg-short" {
		t.Fatalf("EnvAPIKey() = %q, want the named QG_API_KEY value", got)
	}
}

func TestEnvAPIKeyEmptyWhenNothingSet(t *testing.T) {
	profile := ProviderProfile{
		Name:      "qg",
		CatalogID: "qg",
		BaseURL:   "https://api.quebragalho.dev/v1",
		APIKeyEnv: "QUEBRAGALHO_API_KEY",
	}
	if got := profile.EnvAPIKey(func(string) string { return "" }); got != "" {
		t.Fatalf("EnvAPIKey() = %q, want empty", got)
	}
}

func TestResolvePrefersQGAPIKeyOverGatewayEnv(t *testing.T) {
	path := writeConfig(t, `{
		"activeProvider": "qg",
		"providers": [{
			"name": "qg",
			"catalogID": "qg"
		}]
	}`)

	resolved, err := Resolve(ResolveOptions{
		ProjectConfigPath: path,
		Env: map[string]string{
			"QG_API_KEY":          "sk-qg-short",
			"QUEBRAGALHO_API_KEY": "sk-qg-long",
		},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Provider.APIKey != "sk-qg-short" {
		t.Fatalf("APIKey = %q, want QG_API_KEY to win", resolved.Provider.APIKey)
	}
}

func TestResolveFlagAPIKeyBeatsEnv(t *testing.T) {
	path := writeConfig(t, `{
		"activeProvider": "qg",
		"providers": [{
			"name": "qg",
			"catalogID": "qg"
		}]
	}`)

	resolved, err := Resolve(ResolveOptions{
		ProjectConfigPath: path,
		Env: map[string]string{
			"QG_API_KEY":          "sk-qg-short",
			"QUEBRAGALHO_API_KEY": "sk-qg-long",
		},
		Overrides: Overrides{Provider: ProviderProfile{APIKey: "sk-flag"}},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Provider.APIKey != "sk-flag" {
		t.Fatalf("APIKey = %q, want --api-key override to win", resolved.Provider.APIKey)
	}
}

// The environment beats the stored credential: a stored key is only consulted
// later, when the resolver left the profile keyless.
func TestResolveEnvKeyBeatsStoredMarker(t *testing.T) {
	path := writeConfig(t, `{
		"activeProvider": "qg",
		"providers": [{
			"name": "qg",
			"catalogID": "qg",
			"apiKeyStored": true
		}]
	}`)

	resolved, err := Resolve(ResolveOptions{
		ProjectConfigPath: path,
		Env:               map[string]string{"QG_API_KEY": "sk-qg-short"},
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Provider.APIKey != "sk-qg-short" {
		t.Fatalf("APIKey = %q, want env key before the stored credential", resolved.Provider.APIKey)
	}
	if !resolved.Provider.APIKeyStored {
		t.Fatal("APIKeyStored marker lost, want it preserved for the store lookup")
	}
}
