package cli

import (
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

func TestResolveInteractiveSetupFallsBackToUsableProvider(t *testing.T) {
	resolved := config.ResolvedConfig{
		Provider: config.ProviderProfile{Name: "qg", CatalogID: "qg", ProviderKind: config.ProviderKindOpenAICompatible},
		Providers: []config.ProviderProfile{
			{Name: "qg", CatalogID: "qg", ProviderKind: config.ProviderKindOpenAICompatible},
			{Name: "backup", ProviderKind: config.ProviderKindOpenAICompatible, APIKey: "sk-b"},
		},
	}
	got, onboarding := resolveInteractiveSetup(resolved)
	if onboarding {
		t.Fatal("a usable saved provider must satisfy the launch instead of onboarding")
	}
	if got.ActiveProvider != "backup" {
		t.Fatalf("ActiveProvider = %q, want backup", got.ActiveProvider)
	}
}
