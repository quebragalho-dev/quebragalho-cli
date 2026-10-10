package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestProviderCommandOpensOnboardingWizard(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.input.SetValue("/provider")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected /provider to open the onboarding wizard without starting a run")
	}
	if next.providerWizard == nil {
		t.Fatal("expected provider wizard to be open")
	}
	if next.providerWizard.step != providerWizardStepMethod {
		t.Fatalf("wizard step = %v, want connect-method chooser", next.providerWizard.step)
	}
	if len(next.transcript) != len(m.transcript) {
		t.Fatalf("/provider should not append transcript output when opening wizard")
	}
	// The wizard opens on the connect-method chooser: the API-key path is the
	// only one, and it must not advertise removed multi-provider flows.
	view := plainRender(t, next.View())
	for _, want := range []string{
		"Provider setup",
		"How do you want to connect?",
		"Paste an API key",
	} {
		assertContains(t, view, want)
	}
	assertNotContains(t, view, "Sign in with OAuth")
	assertNotContains(t, view, "20+")

	// Choosing the API-key method reveals the provider catalog — QG only.
	next.providerWizard.selectedMethod = len(providerWizardMethodOptions()) - 1
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	listView := plainRender(t, next.View())
	for _, want := range []string{"Choose provider", "Quebra Galho"} {
		assertContains(t, listView, want)
	}
	assertNotContains(t, listView, "OpenRouter")
}

func TestProviderWizardStartsAtFirstProvider(t *testing.T) {
	m := newModel(context.Background(), Options{
		ProviderName: "ollama-cloud",
		ModelName:    "cogito-2.1:671b",
	})
	m.providerProfile = config.ProviderProfile{
		Name:      "ollama-cloud",
		CatalogID: "ollama-cloud",
		Model:     "cogito-2.1:671b",
	}

	next := openProviderWizardForTest(t, m)
	if next.providerWizard.selectedProvider != 0 {
		t.Fatalf("selected provider = %d, want first provider", next.providerWizard.selectedProvider)
	}
	if got, want := next.providerWizard.currentProvider().ID, next.providerWizard.providers[0].ID; got != want {
		t.Fatalf("current provider = %q, want first provider %q", got, want)
	}
}

func TestProviderWizardReplacesEmptyStateWordmark(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.width = 120
	m.height = 34
	m.input.SetValue("/provider")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	next.width = m.width
	next.height = m.height

	view := plainRender(t, next.View())
	assertContains(t, view, "Provider setup")
	assertNotContains(t, view, emptyStateTagline)
	assertNotContains(t, view, "███████")
}

func TestProviderWizardUsesRuntimeProviderCatalog(t *testing.T) {
	wizard := newModel(context.Background(), Options{}).newProviderWizard()
	got := map[string]bool{}
	for _, provider := range wizard.providers {
		got[provider.ID] = true
		if !providercatalog.RuntimeSupported(provider) {
			t.Fatalf("wizard included unsupported provider %q", provider.ID)
		}
	}

	for _, provider := range providercatalog.All() {
		if !providercatalog.RuntimeSupported(provider) {
			continue
		}
		if !got[provider.ID] {
			t.Fatalf("wizard omitted runtime catalog provider %q", provider.ID)
		}
	}
	for _, unsupported := range []string{"bedrock", "vertex"} {
		if got[unsupported] {
			t.Fatalf("wizard should not include unsupported provider %q", unsupported)
		}
	}
}

func TestProviderWizardModelsAreProviderScoped(t *testing.T) {
	descriptor, ok := providercatalog.Get("qg")
	if !ok {
		t.Fatal("qg missing from catalog")
	}
	models := providerWizardModelOptions(descriptor)
	got := map[string]bool{}
	for _, model := range models {
		got[model.ID] = true
	}
	// The model step lists only the selected provider's catalog — global
	// registry models from other providers must not leak in.
	for _, want := range []string{"muse-spark-1.3-contributor", "deepseek-v4-pro", "glm-5.3"} {
		if !got[want] {
			t.Fatalf("qg models missing %q; got %#v", want, providerWizardModelIDs(models))
		}
	}
	for _, notWant := range []string{"claude-sonnet-4.5", "gpt-4.1", "openai/gpt-4.1"} {
		if got[notWant] {
			t.Fatalf("qg models should not include %q; got %#v", notWant, providerWizardModelIDs(models))
		}
	}
}

func TestProviderWizardAdvancesProviderAPIKeyAndModelSteps(t *testing.T) {
	// Pin the credential state so the footer assertion does not depend on the
	// ambient environment.
	t.Setenv("QUEBRAGALHO_API_KEY", "")

	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)

	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")
	next := m
	if got := next.providerWizard.currentProvider().ID; got != "qg" {
		t.Fatalf("after down, selected provider = %q, want qg", got)
	}
	clearProviderAuthEnvForTest(t, next.providerWizard.currentProvider())

	updated, _ := next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("wizard step = %v, want credential", next.providerWizard.step)
	}
	view := plainRender(t, next.View())
	for _, want := range []string{
		"Paste API key",
		"QUEBRAGALHO_API_KEY",
		"Enter continue",
	} {
		assertContains(t, view, want)
	}
	assertNotContains(t, view, "providers add")

	updated, cmd := next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("wizard step = %v, want model", next.providerWizard.step)
	}
	if cmd == nil {
		t.Fatal("entering model step should start live model discovery")
	}
	view = plainRender(t, next.View())
	assertContains(t, view, "Checking available models")
	assertNotContains(t, view, "deepseek-v4-pro")

	updated, _ = next.Update(providerModelsDiscoveredMsg{
		providerID: "qg",
		token:      next.providerWizard.discoveryToken,
		models: []providermodeldiscovery.Model{{
			ID:          "deepseek-v4-pro",
			Description: "deepseek-v4-pro",
		}},
	})
	next = updated.(model)
	view = plainRender(t, next.View())
	for _, want := range []string{
		"Choose a model",
		"deepseek-v4-pro",
	} {
		assertContains(t, view, want)
	}

	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepDone {
		t.Fatalf("wizard step = %v, want done", next.providerWizard.step)
	}
	view = plainRender(t, next.View())
	for _, want := range []string{
		"Ready to connect",
		"Provider    Quebra Galho",
		"Model       deepseek-v4-pro",
		"Credential  QUEBRAGALHO_API_KEY env var",
		"Press Enter to save and start using this provider.",
	} {
		assertContains(t, view, want)
	}
	assertNotContains(t, view, "qg providers check")
}

func TestProviderWizardSupportsLeftAndGuardedRightNavigation(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)

	updated, _ := m.Update(testKey(tea.KeyRight))
	next := updated.(model)
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("right from provider step = %v, want credential", next.providerWizard.step)
	}

	updated, _ = next.Update(testKey(tea.KeyLeft))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepProvider {
		t.Fatalf("left from credential step = %v, want provider", next.providerWizard.step)
	}

	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	clearProviderAuthEnvForTest(t, next.providerWizard.currentProvider())
	updated, _ = next.Update(testKey(tea.KeyRight))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("right from empty credential step = %v, want credential", next.providerWizard.step)
	}

	updated, _ = next.Update(testKeyText("sk-test"))
	next = updated.(model)
	updated, cmd := next.Update(testKey(tea.KeyRight))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("right from entered credential step = %v, want model", next.providerWizard.step)
	}
	if cmd == nil {
		t.Fatal("right from entered credential should start live model discovery")
	}

	updated, _ = next.Update(testKey(tea.KeyRight))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("right while loading step = %v, want model", next.providerWizard.step)
	}

	updated, _ = next.Update(providerModelsDiscoveredMsg{
		providerID: next.providerWizard.currentProvider().ID,
		token:      next.providerWizard.discoveryToken,
		models: []providermodeldiscovery.Model{{
			ID:          next.providerWizard.currentProvider().DefaultModel,
			Description: next.providerWizard.currentProvider().DefaultModel,
		}},
	})
	next = updated.(model)
	updated, _ = next.Update(testKey(tea.KeyRight))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepDone {
		t.Fatalf("right from model step = %v, want ready", next.providerWizard.step)
	}

	updated, _ = next.Update(testKey(tea.KeyLeft))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("left from ready step = %v, want model", next.providerWizard.step)
	}
}

func TestProviderWizardRightAllowsExistingCredentialEnv(t *testing.T) {
	t.Setenv("QUEBRAGALHO_API_KEY", "sk-env")
	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)

	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")
	next := m
	if got := next.providerWizard.currentProvider().ID; got != "qg" {
		t.Fatalf("after down, selected provider = %q, want qg", got)
	}
	updated, _ := next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("enter from provider step = %v, want credential", next.providerWizard.step)
	}

	updated, cmd := next.Update(testKey(tea.KeyRight))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("right with env credential step = %v, want model", next.providerWizard.step)
	}
	if cmd == nil {
		t.Fatal("right with env credential should start live model discovery")
	}
}

func TestProviderWizardAcceptsPastedAPIKeyWithoutRenderingSecret(t *testing.T) {
	const secret = "AIza-secret-123"
	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)
	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("wizard step = %v, want credential", next.providerWizard.step)
	}

	updated, _ = next.Update(testPaste(secret))
	next = updated.(model)
	if next.providerWizard.apiKey != secret {
		t.Fatalf("wizard api key was not captured from paste")
	}
	view := plainRender(t, next.View())
	for _, want := range []string{"Paste API key", "api key >", "saved in your user config"} {
		assertContains(t, view, want)
	}
	assertNotContains(t, view, secret)
}

func TestProviderWizardAppliesPastedKeyToCurrentSession(t *testing.T) {
	const secret = "AIza-secret-123"
	var captured config.ProviderProfile
	m := newModel(context.Background(), Options{
		NewProvider: func(profile config.ProviderProfile) (qgruntime.Provider, error) {
			captured = profile
			return &fakeProvider{}, nil
		},
	})
	m = openProviderWizardForTest(t, m)
	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	updated, _ = next.Update(testPaste(secret))
	next = updated.(model)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("wizard step = %v, want model", next.providerWizard.step)
	}
	next = finishProviderWizardModelDiscoveryForTest(t, next)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepDone {
		t.Fatalf("wizard step = %v, want done", next.providerWizard.step)
	}
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)

	if next.providerWizard != nil {
		t.Fatal("successful provider apply should close the wizard")
	}
	if captured.CatalogID != "qg" || captured.ProviderKind != config.ProviderKindOpenAICompatible {
		t.Fatalf("captured profile provider = %#v, want qg", captured)
	}
	if captured.APIKey != secret {
		t.Fatalf("captured API key = %q, want pasted secret", captured.APIKey)
	}
	if captured.APIKeyEnv != "" {
		t.Fatalf("captured APIKeyEnv = %q, want empty when using pasted key", captured.APIKeyEnv)
	}
	if next.providerProfile.APIKey != secret || next.providerName != "qg" {
		t.Fatalf("model provider state was not updated: provider=%q profile=%#v", next.providerName, next.providerProfile)
	}
}

func TestProviderWizardPersistsPastedKeyToUserConfig(t *testing.T) {
	const secret = "ollama-secret-123"
	// Encrypted-file backend in the temp config dir keeps the test off the real keychain.
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	configPath := filepath.Join(t.TempDir(), "qg", "config.json")
	var captured config.ProviderProfile
	m := newModel(context.Background(), Options{
		UserConfigPath: configPath,
		NewProvider: func(profile config.ProviderProfile) (qgruntime.Provider, error) {
			captured = profile
			return &fakeProvider{}, nil
		},
	})
	m = openProviderWizardForTest(t, m)
	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	updated, _ = next.Update(testPaste(secret))
	next = updated.(model)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	next = finishProviderWizardModelDiscoveryForTest(t, next)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	_ = updated.(model)

	if captured.APIKey != secret {
		t.Fatalf("captured APIKey = %q, want pasted secret", captured.APIKey)
	}
	persisted := readProviderWizardConfigFixture(t, configPath)
	if persisted.ActiveProvider != "qg" {
		t.Fatalf("active provider = %q, want ollama-cloud", persisted.ActiveProvider)
	}
	if len(persisted.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(persisted.Providers))
	}
	profile := persisted.Providers[0]
	if profile.Name != "qg" || profile.CatalogID != "qg" {
		t.Fatalf("persisted provider identity = %#v, want ollama-cloud", profile)
	}
	// Capture flip: the secret lives in the credential store, not config.json.
	if profile.APIKey != "" || profile.APIKeyEnv != "" {
		t.Fatalf("config must not persist the key: APIKey %q APIKeyEnv %q", profile.APIKey, profile.APIKeyEnv)
	}
	if !profile.APIKeyStored {
		t.Fatal("expected APIKeyStored marker in persisted config")
	}
	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if key, ok, _ := store.Get("qg"); !ok || key != secret {
		t.Fatalf("stored key = %q,%v; want the pasted secret in the credential store", key, ok)
	}
}

func TestProviderWizardUsesAPIKeyEnvForCurrentSessionWithoutPersistingSecret(t *testing.T) {
	const secret = "qg-env-secret"
	t.Setenv("QUEBRAGALHO_API_KEY", secret)
	configPath := filepath.Join(t.TempDir(), "qg", "config.json")
	var captured config.ProviderProfile
	m := newModel(context.Background(), Options{
		UserConfigPath: configPath,
		NewProvider: func(profile config.ProviderProfile) (qgruntime.Provider, error) {
			captured = profile
			return &fakeProvider{}, nil
		},
	})
	m = openProviderWizardForTest(t, m)
	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	next = finishProviderWizardModelDiscoveryForTest(t, next)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)

	if captured.APIKey != secret {
		t.Fatalf("runtime APIKey = %q, want env secret", captured.APIKey)
	}
	if captured.APIKeyEnv != "QUEBRAGALHO_API_KEY" {
		t.Fatalf("runtime APIKeyEnv = %q, want OLLAMA_API_KEY", captured.APIKeyEnv)
	}
	if next.providerProfile.APIKey != "" {
		t.Fatalf("session providerProfile persisted APIKey = %q, want empty", next.providerProfile.APIKey)
	}
	if next.providerProfile.APIKeyEnv != "QUEBRAGALHO_API_KEY" {
		t.Fatalf("session providerProfile APIKeyEnv = %q, want OLLAMA_API_KEY", next.providerProfile.APIKeyEnv)
	}
	persisted := readProviderWizardConfigFixture(t, configPath)
	if len(persisted.Providers) != 1 {
		t.Fatalf("providers length = %d, want 1", len(persisted.Providers))
	}
	profile := persisted.Providers[0]
	if profile.APIKey != "" {
		t.Fatalf("persisted APIKey = %q, want empty", profile.APIKey)
	}
	if profile.APIKeyEnv != "QUEBRAGALHO_API_KEY" {
		t.Fatalf("persisted APIKeyEnv = %q, want OLLAMA_API_KEY", profile.APIKeyEnv)
	}
}

func TestProviderWizardUsesLiveDiscoveredModels(t *testing.T) {
	var captured config.ProviderProfile
	m := newModel(context.Background(), Options{
		DiscoverProviderModels: func(ctx context.Context, profile config.ProviderProfile) ([]providermodeldiscovery.Model, error) {
			captured = profile
			return []providermodeldiscovery.Model{{ID: "live-b"}, {ID: "live-a"}}, nil
		},
	})
	m = openProviderWizardForTest(t, m)
	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")
	clearProviderAuthEnvForTest(t, m.providerWizard.currentProvider())

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("wizard step = %v, want credential", next.providerWizard.step)
	}
	updated, cmd = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("wizard step = %v, want model", next.providerWizard.step)
	}
	if cmd == nil {
		t.Fatal("entering model step should start live model discovery")
	}
	view := plainRender(t, next.View())
	assertContains(t, view, "Checking available models")
	assertNotContains(t, view, "muse-spark-1.3-contributor")

	updated, _ = next.Update(testKey(tea.KeyEnter))
	waiting := updated.(model)
	if waiting.providerWizard.step != providerWizardStepModel {
		t.Fatalf("enter while loading step = %v, want model", waiting.providerWizard.step)
	}
	assertContains(t, plainRender(t, waiting.View()), "Models are still loading.")

	msg := cmd()
	updated, _ = next.Update(msg)
	next = updated.(model)

	if captured.CatalogID != "qg" {
		t.Fatalf("discovery profile = %#v, want qg", captured)
	}
	if got := providerWizardModelIDs(next.providerWizard.models); strings.Join(got, ",") != "live-b,live-a" {
		t.Fatalf("wizard models = %#v, want live discovered models", got)
	}
	view = plainRender(t, next.View())
	assertNotContains(t, view, "models: live")
	assertContains(t, view, "search > \u258cmodel name...")
	assertNotContains(t, view, "gpt-4.1")
}

func TestProviderWizardIgnoresStaleDiscoveryForSameProvider(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)
	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")
	clearProviderAuthEnvForTest(t, m.providerWizard.currentProvider())

	// First walk into the model step (through the credential step).
	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("wizard step = %v, want credential", next.providerWizard.step)
	}
	updated, cmd := next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("wizard step = %v, want model", next.providerWizard.step)
	}
	if cmd == nil {
		t.Fatal("first model entry should start discovery")
	}
	staleToken := next.providerWizard.discoveryToken

	updated, _ = next.Update(testKey(tea.KeyLeft))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("left from model step = %v, want credential", next.providerWizard.step)
	}
	updated, _ = next.Update(testKey(tea.KeyLeft))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepProvider {
		t.Fatalf("left from credential step = %v, want provider", next.providerWizard.step)
	}

	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	updated, cmd = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if cmd == nil {
		t.Fatal("second model entry should start discovery")
	}
	currentToken := next.providerWizard.discoveryToken
	if currentToken == staleToken {
		t.Fatalf("discovery token did not advance: %d", currentToken)
	}

	updated, _ = next.Update(providerModelsDiscoveredMsg{
		providerID: "qg",
		token:      staleToken,
		models: []providermodeldiscovery.Model{{
			ID: "stale-model",
		}},
	})
	next = updated.(model)
	if got := providerWizardModelIDs(next.providerWizard.models); containsString(got, "stale-model") {
		t.Fatalf("stale discovery response applied: %#v", got)
	}
	if !next.providerWizard.modelLoading {
		t.Fatal("stale discovery response should not clear the active loading state")
	}

	updated, _ = next.Update(providerModelsDiscoveredMsg{
		providerID: "qg",
		token:      currentToken,
		models: []providermodeldiscovery.Model{{
			ID: "fresh-model",
		}},
	})
	next = updated.(model)
	if got := providerWizardModelIDs(next.providerWizard.models); !containsString(got, "fresh-model") {
		t.Fatalf("fresh discovery response did not apply: %#v", got)
	}
}

func TestProviderWizardKeepsFallbackModelsWhenLiveDiscoveryFails(t *testing.T) {
	m := newModel(context.Background(), Options{
		DiscoverProviderModels: func(context.Context, config.ProviderProfile) ([]providermodeldiscovery.Model, error) {
			return nil, errors.New("offline")
		},
	})
	m = openProviderWizardForTest(t, m)
	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")
	clearProviderAuthEnvForTest(t, m.providerWizard.currentProvider())

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("wizard step = %v, want credential", next.providerWizard.step)
	}
	updated, cmd := next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("wizard step = %v, want model", next.providerWizard.step)
	}
	if cmd == nil {
		t.Fatal("entering model step should start live model discovery")
	}
	view := plainRender(t, next.View())
	assertContains(t, view, "Checking available models")
	assertNotContains(t, view, "muse-spark-1.3-contributor")

	updated, _ = next.Update(cmd())
	next = updated.(model)

	if got := providerWizardModelIDs(next.providerWizard.models); !containsString(got, "muse-spark-1.3-contributor") {
		t.Fatalf("wizard models = %#v, want fallback model muse-spark-1.3-contributor", got)
	}
	view = plainRender(t, next.View())
	assertContains(t, view, "Using built-in model list")
	assertNotContains(t, view, "offline")
}

func TestProviderWizardRendersDiscoveredModelMetadata(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)
	m.providerWizard.selectedProvider = providerWizardProviderIndex(t, m.providerWizard, "qg")
	m.providerWizard.step = providerWizardStepModel

	next := m.applyProviderModelsDiscovered(providerModelsDiscoveredMsg{
		providerID: "qg",
		models: []providermodeldiscovery.Model{{
			ID:            "gpt-4.1",
			Description:   "GPT-4.1",
			ContextWindow: 1048576,
			ToolCall:      true,
			Reasoning:     true,
			InputCost:     2,
			OutputCost:    8,
			Source:        "models.dev",
		}},
	})

	view := plainRender(t, next.View())
	assertNotContains(t, view, "models: models.dev")
	assertNotContains(t, view, "models: OpenGateway")
	assertContains(t, view, "GPT-4.1")
	assertContains(t, view, "1M ctx")
	assertContains(t, view, "tools")
	assertContains(t, view, "reasoning")
}

func TestProviderWizardEscClosesWizard(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)

	updated, _ := m.Update(testKey(tea.KeyEsc))
	next := updated.(model)
	if next.providerWizard != nil {
		t.Fatal("Esc should close provider wizard")
	}
}

func TestProviderWizardModelStepUsesFriendlyNamesAndStaysCompact(t *testing.T) {
	wizard := &providerWizardState{
		step:        providerWizardStepModel,
		modelSource: "models.dev",
		models:      providerWizardManyModelsForTest(18),
	}
	wizard.models[0] = providerWizardModel{
		ID:          "x-ai/grok-4.3",
		Description: "Grok 4.3",
		Meta:        "1M ctx · tools · reasoning",
	}

	view := plainRender(t, strings.Join(wizard.renderModelStep(84), "\n"))
	assertContains(t, view, "search > \u258cmodel name...")
	assertContains(t, view, "\u258c")
	assertNotContains(t, view, "SearchÃ")
	assertNotContains(t, view, "Searchâ")
	assertContains(t, view, "Grok 4.3")
	assertContains(t, view, "x-ai/grok-4.3 · 1M ctx · tools · reasoning")
	assertNotContains(t, view, "❯ x-ai/grok-4.3")
	assertNotContains(t, view, "more models")
	assertNotContains(t, view, "Model 17")
}

func TestProviderWizardModelSearchFiltersAndAppliesRawModelID(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.providerWizard = &providerWizardState{
		step:        providerWizardStepModel,
		modelSource: "models.dev",
		providers: []providercatalog.Descriptor{{
			ID:                  "openrouter",
			Name:                "OpenRouter",
			Transport:           providercatalog.TransportOpenAICompatible,
			DefaultBaseURL:      "https://openrouter.ai/api/v1",
			DefaultModel:        "openai/gpt-4.1",
			AuthEnvVars:         []string{"OPENROUTER_API_KEY"},
			RequiresAuth:        true,
			SupportedAPIFormats: []providercatalog.APIFormat{providercatalog.APIFormatOpenAIChatCompletions},
		}},
		models: []providerWizardModel{
			{ID: "openai/gpt-4.1", Description: "GPT-4.1", Meta: "1M ctx · tools"},
			{ID: "deepseek/deepseek-chat", Description: "DeepSeek Chat", Meta: "64K ctx · tools"},
			{ID: "deepseek/deepseek-v3.2", Description: "DeepSeek V3.2", Meta: "128K ctx · tools"},
		},
	}

	updated, _ := m.Update(testKeyText("deep"))
	next := updated.(model)

	if next.providerWizard.modelSearch != "deep" {
		t.Fatalf("model search = %q, want deep", next.providerWizard.modelSearch)
	}
	if got := next.providerWizard.currentModel().ID; got != "deepseek/deepseek-chat" {
		t.Fatalf("current model ID = %q, want raw OpenRouter ID", got)
	}
	view := plainRender(t, next.View())
	assertContains(t, view, "DeepSeek Chat")
	assertContains(t, view, "DeepSeek V3.2")
	assertNotContains(t, view, "GPT-4.1")
}

func TestProviderWizardBlocksAdvanceWhenModelSearchHasNoMatches(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.providerWizard = &providerWizardState{
		step: providerWizardStepModel,
		providers: []providercatalog.Descriptor{{
			ID:                  "openrouter",
			Name:                "OpenRouter",
			Transport:           providercatalog.TransportOpenAICompatible,
			DefaultBaseURL:      "https://openrouter.ai/api/v1",
			DefaultModel:        "openai/gpt-4.1",
			AuthEnvVars:         []string{"OPENROUTER_API_KEY"},
			RequiresAuth:        true,
			SupportedAPIFormats: []providercatalog.APIFormat{providercatalog.APIFormatOpenAIChatCompletions},
		}},
		models: []providerWizardModel{
			{ID: "openai/gpt-4.1", Description: "GPT-4.1"},
			{ID: "deepseek/deepseek-chat", Description: "DeepSeek Chat"},
		},
	}

	updated, _ := m.Update(testKeyText("nomatch"))
	next := updated.(model)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)

	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("wizard advanced to %v, want model step", next.providerWizard.step)
	}
	if next.providerWizard.err == "" {
		t.Fatal("expected model search error")
	}
	if got := next.providerWizard.currentModel().ID; got != "" {
		t.Fatalf("current model ID = %q, want empty when search has no matches", got)
	}
	view := plainRender(t, next.View())
	assertContains(t, view, "no matching models")
	assertContains(t, view, "choose a matching model")
}

func openProviderWizardForTest(t *testing.T, m model) model {
	t.Helper()
	m.input.SetValue("/provider")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if next.providerWizard == nil {
		t.Fatal("expected provider wizard to be open")
	}
	// The wizard now opens on the connect-method chooser. These tests exercise the
	// API-key / browse path, so select that method and advance into the provider
	// list (the OAuth path is covered by the OAuth-specific tests).
	if next.providerWizard.step == providerWizardStepMethod {
		options := providerWizardMethodOptions()
		next.providerWizard.selectedMethod = len(options) - 1 // last = "browse / API key"
		u2, _ := next.Update(testKey(tea.KeyEnter))
		next = u2.(model)
		if next.providerWizard == nil || next.providerWizard.step != providerWizardStepProvider {
			t.Fatalf("expected provider step after choosing API-key method, got %#v", next.providerWizard)
		}
	}
	return next
}

func finishProviderWizardModelDiscoveryForTest(t *testing.T, m model) model {
	t.Helper()
	if m.providerWizard == nil {
		t.Fatal("expected provider wizard to be open")
	}
	if m.providerWizard.step != providerWizardStepModel {
		t.Fatalf("wizard step = %v, want model", m.providerWizard.step)
	}
	provider := m.providerWizard.currentProvider()
	modelID := firstProviderDisplayValue(provider.DefaultModel, "test-model")
	updated, _ := m.Update(providerModelsDiscoveredMsg{
		providerID: provider.ID,
		token:      m.providerWizard.discoveryToken,
		models: []providermodeldiscovery.Model{{
			ID:          modelID,
			Description: modelID,
		}},
	})
	return updated.(model)
}

func providerWizardManyModelsForTest(count int) []providerWizardModel {
	models := make([]providerWizardModel, 0, count)
	for index := 0; index < count; index++ {
		models = append(models, providerWizardModel{
			ID:          fmt.Sprintf("provider/model-%02d", index),
			Description: fmt.Sprintf("Model %02d", index),
			Meta:        "tools",
		})
	}
	return models
}

func providerWizardProviderIndex(t *testing.T, wizard *providerWizardState, id string) int {
	t.Helper()
	for index, provider := range wizard.providers {
		if provider.ID == id {
			return index
		}
	}
	t.Fatalf("provider %q not found in wizard providers", id)
	return 0
}

func clearProviderAuthEnvForTest(t *testing.T, provider providercatalog.Descriptor) {
	t.Helper()
	for _, env := range provider.AuthEnvVars {
		env = strings.TrimSpace(env)
		if env != "" {
			t.Setenv(env, "")
		}
	}
}

func providerWizardModelIDs(models []providerWizardModel) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestWizardProviderStoredKey(t *testing.T) {
	m := model{savedProviders: []config.ProviderProfile{
		{Name: "acme", CatalogID: "acme-cloud", APIKeyStored: true},
		{Name: "nokey"},
	}}
	if name, ok := m.wizardProviderStoredKey(providercatalog.Descriptor{Name: "acme"}); !ok || name != "acme" {
		t.Fatalf("match by name = %q,%v", name, ok)
	}
	if name, ok := m.wizardProviderStoredKey(providercatalog.Descriptor{ID: "acme-cloud"}); !ok || name != "acme" {
		t.Fatalf("match by catalog id = %q,%v", name, ok)
	}
	if _, ok := m.wizardProviderStoredKey(providercatalog.Descriptor{Name: "nokey"}); ok {
		t.Fatal("provider without a stored key must not match")
	}
}

func TestProviderWizardManageKeyRemove(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	configPath := filepath.Join(t.TempDir(), "qg", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"providers":[{"name":"acme","apiKeyStored":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("acme", "sk-secret"); err != nil {
		t.Fatal(err)
	}

	m := newModel(context.Background(), Options{UserConfigPath: configPath})
	m.providerWizard = &providerWizardState{step: providerWizardStepManageKey, manageProviderName: "acme", manageKeyCursor: 2}
	next, _ := m.applyManageKeyChoice()
	if next.providerWizard != nil {
		t.Fatal("remove should close the wizard")
	}
	if _, ok, _ := store.Get("acme"); ok {
		t.Fatal("remove should delete the key from the credential store")
	}
}

func TestProviderWizardManageKeyReplaceAndKeep(t *testing.T) {
	m := newModel(context.Background(), Options{UserConfigPath: filepath.Join(t.TempDir(), "config.json")})

	m.providerWizard = &providerWizardState{step: providerWizardStepManageKey, manageProviderName: "acme", manageKeyCursor: 1}
	next, _ := m.applyManageKeyChoice()
	if next.providerWizard == nil || next.providerWizard.step != providerWizardStepCredential {
		t.Fatal("replace should route to the credential step")
	}

	m.providerWizard = &providerWizardState{step: providerWizardStepManageKey, manageProviderName: "acme", manageKeyCursor: 0}
	next, _ = m.applyManageKeyChoice()
	if next.providerWizard != nil {
		t.Fatal("keep should close the wizard without changes")
	}
}
func TestAdvanceProviderWizardNamedShowsManageKey(t *testing.T) {
	m := model{savedProviders: []config.ProviderProfile{
		{Name: "openai", CatalogID: "openai", APIKeyStored: true},
	}}
	m.providerWizard = &providerWizardState{
		step: providerWizardStepProvider,
		providers: []providercatalog.Descriptor{
			{ID: "openai", Name: "OpenAI", Transport: providercatalog.TransportOpenAI},
		},
		selectedProvider: 0,
	}

	next, _ := m.advanceProviderWizard()
	if next.providerWizard == nil {
		t.Fatal("wizard should not be nil after advancing from provider step")
	}
	if next.providerWizard.step != providerWizardStepManageKey {
		t.Fatalf("named provider should route to ManageKey step, got step: %v", next.providerWizard.step)
	}
}

func readProviderWizardConfigFixture(t *testing.T, path string) config.FileConfig {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg config.FileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Applying the wizard switches the live provider, so it must export
// QG_PROVIDER exactly like the /model and /provider switch paths — a stale
// value from an earlier switch would otherwise win over config in every
// spawned child (applyEnv) and pin specialists/swarm members to the OLD
// provider's credentials.
func TestApplyProviderWizardExportsActiveProviderEnv(t *testing.T) {
	t.Setenv(config.ActiveProviderEnv, "stale-previous-provider")
	m := newModel(context.Background(), Options{})
	// Isolate the SUCCESS path's persist to a temp config (the default empty
	// path would skip persist; a future default must never reach the real user
	// config), and stub the build so the full commit sequence runs.
	m.userConfigPath = filepath.Join(t.TempDir(), "config.json")
	m.newProvider = func(config.ProviderProfile) (qgruntime.Provider, error) {
		return &fakeProvider{}, nil
	}
	m.providerWizard = &providerWizardState{
		step:        providerWizardStepModel,
		profileName: "acme-wizard",
		providers: []providercatalog.Descriptor{{
			ID:                  "openrouter",
			Name:                "OpenRouter",
			Transport:           providercatalog.TransportOpenAICompatible,
			DefaultBaseURL:      "https://openrouter.ai/api/v1",
			DefaultModel:        "openai/gpt-4.1",
			AuthEnvVars:         []string{"OPENROUTER_API_KEY"},
			RequiresAuth:        true,
			SupportedAPIFormats: []providercatalog.APIFormat{providercatalog.APIFormatOpenAIChatCompletions},
		}},
		models: []providerWizardModel{{ID: "openai/gpt-4.1", Description: "GPT-4.1"}},
	}

	updated, _ := m.applyProviderWizard()
	next := updated

	if next.providerName == "" {
		t.Fatal("wizard apply should have set a provider name")
	}
	if got := os.Getenv(config.ActiveProviderEnv); got != next.providerName {
		t.Fatalf("%s = %q after wizard apply, want %q (children would spawn on the stale provider)", config.ActiveProviderEnv, got, next.providerName)
	}
}

// On a config PERSIST failure, applyProviderWizard must leave live state fully
// unchanged — the chat must NOT already be running on the new provider while the
// status line and the QG_PROVIDER export (which pins spawned children) still
// point at the old one. Build and persist are staged into locals; nothing is
// committed unless both succeed.
func TestApplyProviderWizardPersistFailureLeavesLiveStateUnchanged(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "encrypted-file") // never touch the real OS keychain: apiKey is secured before the persist fails
	t.Setenv(config.ActiveProviderEnv, "old-provider")

	// A config path whose parent is a regular FILE, so writeConfigFile's MkdirAll
	// fails and UpsertProvider returns an error.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	brokenConfigPath := filepath.Join(blocker, "config.json")

	oldProvider := &fakeProvider{}
	newProvider := &fakeProvider{}
	m := newModel(context.Background(), Options{})
	m.provider = oldProvider
	m.providerProfile = config.ProviderProfile{Name: "old-provider"}
	m.providerName = "old-provider"
	m.userConfigPath = brokenConfigPath
	m.newProvider = func(config.ProviderProfile) (qgruntime.Provider, error) { return newProvider, nil }
	m.providerWizard = &providerWizardState{
		step:        providerWizardStepModel,
		profileName: "acme-new",
		providers: []providercatalog.Descriptor{{
			ID:                  "openrouter",
			Name:                "OpenRouter",
			Transport:           providercatalog.TransportOpenAICompatible,
			DefaultBaseURL:      "https://openrouter.ai/api/v1",
			DefaultModel:        "openai/gpt-4.1",
			AuthEnvVars:         []string{"OPENROUTER_API_KEY"},
			RequiresAuth:        true,
			SupportedAPIFormats: []providercatalog.APIFormat{providercatalog.APIFormatOpenAIChatCompletions},
		}},
		models: []providerWizardModel{{ID: "openai/gpt-4.1", Description: "GPT-4.1"}},
		apiKey: "sk-new",
	}

	updated, _ := m.applyProviderWizard()
	next := updated

	if next.providerWizard == nil || next.providerWizard.err == "" {
		t.Fatal("a persist failure must surface a wizard error and keep the wizard open")
	}
	if next.provider != oldProvider {
		t.Fatal("live provider must NOT be swapped when the persist fails")
	}
	if next.providerName != "old-provider" {
		t.Fatalf("providerName = %q, want it unchanged on persist failure", next.providerName)
	}
	if got := os.Getenv(config.ActiveProviderEnv); got != "old-provider" {
		t.Fatalf("%s = %q, want it unchanged on persist failure (children would diverge from parent)", config.ActiveProviderEnv, got)
	}
}

func TestProviderSearchFiltersByNameIDAndAlias(t *testing.T) {
	wizard := &providerWizardState{
		step: providerWizardStepProvider,
		providers: []providercatalog.Descriptor{
			{ID: "openai", Name: "OpenAI", Aliases: []string{"chatgpt"}},
			{ID: "anthropic", Name: "Anthropic", Aliases: []string{"claude"}},
			{ID: "google", Name: "Google", Aliases: []string{"gemini"}},
			{ID: "groq", Name: "Groq", Aliases: []string{}},
		},
	}

	tests := []struct {
		query string
		want  []string
	}{
		{"openai", []string{"openai"}},
		{"anthropic", []string{"anthropic"}},
		{"claude", []string{"anthropic"}},
		{"gemini", []string{"google"}},
		{"chatgpt", []string{"openai"}},
		{"groq", []string{"groq"}},
		{"GROQ", []string{"groq"}},
		{"nomatch", nil},
		{"", []string{"openai", "anthropic", "google", "groq"}},
	}

	for _, tt := range tests {
		t.Run("query="+tt.query, func(t *testing.T) {
			wizard.providerSearch = tt.query
			got := wizard.filteredProviders()
			if len(got) != len(tt.want) {
				t.Fatalf("filteredProviders(%q) = %d results, want %d", tt.query, len(got), len(tt.want))
			}
			for i, id := range tt.want {
				if got[i].ID != id {
					t.Fatalf("filteredProviders(%q)[%d] = %q, want %q", tt.query, i, got[i].ID, id)
				}
			}
		})
	}
}

func TestProviderSearchResetsSelectedProviderOnEdit(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)

	// Move to a later provider, then type a search query.
	m.providerWizard.selectedProvider = 3
	updated, _ := m.Update(testKeyText("anth"))
	next := updated.(model)
	if next.providerWizard.selectedProvider != 0 {
		t.Fatalf("selectedProvider after typing = %d, want 0 (reset on edit)", next.providerWizard.selectedProvider)
	}
}

func TestProviderSearchFilteredAdvanceSelectsCorrectProvider(t *testing.T) {
	wizard := &providerWizardState{
		step: providerWizardStepProvider,
		providers: []providercatalog.Descriptor{
			{ID: "openai", Name: "OpenAI", AuthEnvVars: []string{"OPENAI_API_KEY"}, RequiresAuth: true},
			{ID: "anthropic", Name: "Anthropic", AuthEnvVars: []string{"ANTHROPIC_API_KEY"}, RequiresAuth: true},
			{ID: "google", Name: "Google", AuthEnvVars: []string{"GOOGLE_API_KEY"}, RequiresAuth: true},
			{ID: "groq", Name: "Groq", AuthEnvVars: []string{"GROQ_API_KEY"}, RequiresAuth: true},
		},
		selectedProvider: 0,
	}

	// Type a search that matches exactly "groq".
	wizard.providerSearch = "groq"
	filtered := wizard.filteredProviders()
	if len(filtered) != 1 || filtered[0].ID != "groq" {
		t.Fatalf("expected exactly groq in filtered results, got %v", providerWizardIDs(filtered))
	}

	// currentProvider() via filtered list should be groq.
	if got := wizard.currentProvider().ID; got != "groq" {
		t.Fatalf("currentProvider() = %q, want groq", got)
	}
}

func TestProviderSearchEmptyMatchEnterIsNoop(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)

	// Type a query that matches nothing.
	updated, _ := m.Update(testKeyText("zzzzz"))
	next := updated.(model)
	if len(next.providerWizard.filteredProviders()) != 0 {
		t.Fatal("expected qg filtered providers")
	}

	// Enter should be a no-op.
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if next.providerWizard.step != providerWizardStepProvider {
		t.Fatalf("wizard step = %v, want provider (empty match Enter should be no-op)", next.providerWizard.step)
	}
	if next.providerWizard.providerSearch != "zzzzz" {
		t.Fatalf("providerSearch = %q, want query preserved on noop", next.providerWizard.providerSearch)
	}
}

func TestProviderSearchClearRestoresFullList(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = openProviderWizardForTest(t, m)

	initialCount := len(m.providerWizard.filteredProviders())
	if initialCount == 0 {
		t.Fatal("expected providers in the wizard")
	}

	// Type a search query.
	updated, _ := m.Update(testKeyText("open"))
	next := updated.(model)
	filtered := next.providerWizard.filteredProviders()
	if len(filtered) >= initialCount {
		t.Fatalf("search should narrow results: got %d, want < %d", len(filtered), initialCount)
	}

	// Ctrl+U clears the search.
	updated, _ = next.Update(testKeyCtrl('u'))
	next = updated.(model)
	restored := next.providerWizard.filteredProviders()
	if len(restored) != initialCount {
		t.Fatalf("after clear, filtered count = %d, want %d", len(restored), initialCount)
	}
}

func providerWizardIDs(descriptors []providercatalog.Descriptor) []string {
	ids := make([]string, len(descriptors))
	for i, d := range descriptors {
		ids[i] = d.ID
	}
	return ids
}

// A provider that returns prose blurbs instead of display names must not
// collapse distinct models into identical rows. Reproduces the reported picker
// where two "Open-weight GPT model..." and two "Kimi multimodal agent model..."
// rows were indistinguishable, with the ID visible only for the highlighted one.
func TestProviderWizardModelRowsStayDistinctWithProseDescriptions(t *testing.T) {
	wizard := &providerWizardState{
		step:        providerWizardStepModel,
		modelSource: "models.dev",
		models: []providerWizardModel{
			{ID: "gpt-oss-120b", Description: "Open-weight GPT model for self-hosted reasoning and instruction-following workloads"},
			{ID: "gpt-oss-20b", Description: "Open-weight GPT model for self-hosted reasoning and instruction-following workloads"},
			{ID: "kimi-k2-thinking", Description: "Kimi multimodal agent model for visual understanding, coding, and planning"},
			{ID: "kimi-k2-instruct", Description: "Kimi multimodal agent model for visual understanding, coding, and planning"},
			{ID: "x-ai/grok-4.3", Description: "Grok 4.3"},
		},
	}
	view := plainRender(t, strings.Join(wizard.renderModelStep(96), "\n"))

	// every prose-described model is identified by its unique ID
	for _, id := range []string{"gpt-oss-120b", "gpt-oss-20b", "kimi-k2-thinking", "kimi-k2-instruct"} {
		if !strings.Contains(view, id) {
			t.Errorf("model %q is not identifiable in the list:\n%s", id, view)
		}
	}
	// a genuine display name is still preferred over the raw id
	if !strings.Contains(view, "Grok 4.3") {
		t.Errorf("friendly display name was lost:\n%s", view)
	}
	// the prose blurb must not be a row label
	if strings.Contains(view, "❯ Open-weight GPT model") {
		t.Errorf("prose blurb still used as a row label:\n%s", view)
	}
}
