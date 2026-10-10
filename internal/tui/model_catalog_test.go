package tui

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
)

func TestModelContextWindowUsesCachedCatalog(t *testing.T) {
	registry := mustTestModelRegistry(t, testModelEntry("custom-long-context", 12345, []modelregistry.ModelCapability{
		modelregistry.ModelCapabilityChat,
	}))
	m := model{modelCatalog: registry}

	if got := m.modelContextWindow("custom-long-context"); got != 12345 {
		t.Fatalf("modelContextWindow = %d, want 12345", got)
	}
}

func TestModelSupportsVisionTUITrustsCachedCatalog(t *testing.T) {
	registry := mustTestModelRegistry(t, testModelEntry("gpt-5-text-only", 12345, []modelregistry.ModelCapability{
		modelregistry.ModelCapabilityChat,
	}))
	m := model{modelName: "gpt-5-text-only", modelCatalog: registry}

	if m.modelSupportsVisionTUI() {
		t.Fatal("catalog-known text-only model must not fall through to vision name heuristic")
	}
}

func TestModelSupportsVisionTUIChecksDiscoveredBeforeHeuristic(t *testing.T) {
	registry := mustTestModelRegistry(t, testModelEntry("custom-known", 12345, []modelregistry.ModelCapability{
		modelregistry.ModelCapabilityChat,
	}))
	m := model{
		modelName:    "gpt-5-text-only",
		modelCatalog: registry,
		modelPickerLiveByProvider: map[string][]providermodeldiscovery.Model{
			"custom": {{ID: "gpt-5-text-only", InputModalities: []string{"text"}}},
		},
	}

	if m.modelSupportsVisionTUI() {
		t.Fatal("discovered text-only model must not fall through to vision name heuristic")
	}
}

func TestModelSupportsVisionTUIFallsBackWhenLiveMetadataOmitsModalities(t *testing.T) {
	registry := mustTestModelRegistry(t, testModelEntry("custom-known", 12345, []modelregistry.ModelCapability{
		modelregistry.ModelCapabilityChat,
	}))
	m := model{
		modelName:    "gpt-5.6-sol",
		modelCatalog: registry,
		modelPickerLiveByProvider: map[string][]providermodeldiscovery.Model{
			"chatgpt": {{ID: "gpt-5.6-sol"}},
		},
	}

	if !m.modelSupportsVisionTUI() {
		t.Fatal("live discovery without modality metadata must fall back to the model capability heuristic")
	}
}

func BenchmarkModelContextWindowLookup(b *testing.B) {
	cachedRegistry, err := modelregistry.DefaultRegistry()
	if err != nil {
		b.Fatalf("load default registry: %v", err)
	}

	b.Run("default_registry_each_call", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			registry, err := modelregistry.DefaultRegistry()
			if err != nil {
				b.Fatalf("load default registry: %v", err)
			}
			entry, ok := registry.Resolve("gpt-4.1")
			if !ok || entry.ContextLimits.ContextWindow <= 0 {
				b.Fatal("expected gpt-4.1 context window")
			}
		}
	})

	b.Run("cached_registry", func(b *testing.B) {
		m := model{modelName: "gpt-4.1", modelCatalog: cachedRegistry}
		b.ReportAllocs()
		for b.Loop() {
			if got := m.modelContextWindow(m.modelName); got <= 0 {
				b.Fatal("expected gpt-4.1 context window")
			}
		}
	})
}

func mustTestModelRegistry(t *testing.T, entries ...modelregistry.ModelEntry) modelregistry.Registry {
	t.Helper()
	registry, err := modelregistry.NewRegistry(entries)
	if err != nil {
		t.Fatalf("create test model registry: %v", err)
	}
	return registry
}

func testModelEntry(id string, contextWindow int, capabilities []modelregistry.ModelCapability) modelregistry.ModelEntry {
	return modelregistry.ModelEntry{
		ID:            id,
		DisplayName:   id,
		APIModel:      id,
		Provider:      modelregistry.ProviderOpenAI,
		APIProviders:  []modelregistry.ProviderKind{modelregistry.ProviderOpenAI},
		ContextLimits: modelregistry.ContextLimits{ContextWindow: contextWindow, MaxOutputTokens: 1024},
		Capabilities:  capabilities,
		Cost: modelregistry.ModelCost{
			Currency:           "USD",
			Unit:               "per_1m_tokens",
			InputPerMillion:    1,
			OutputPerMillion:   1,
			Source:             "test",
			SourceLastVerified: "2026-01-01",
		},
		Status:  modelregistry.ModelStatusActive,
		Aliases: []string{id},
	}
}

// The TUI /model list must read THIS model's catalog and show only QG rows:
// legacy-provider entries (even synthetic ones) never surface in the list.
func TestModelListTextShowsOnlyQGCatalogModels(t *testing.T) {
	openaiEntry := testModelEntry("openai-synthetic", 128_000, []modelregistry.ModelCapability{
		modelregistry.ModelCapabilityChat,
	})
	qgEntry := testModelEntry("qg-synthetic", 200_000, []modelregistry.ModelCapability{
		modelregistry.ModelCapabilityChat,
	})
	qgEntry.Provider = modelregistry.ProviderQG
	qgEntry.APIProviders = []modelregistry.ProviderKind{
		modelregistry.ProviderQG,
		modelregistry.ProviderOpenAICompatible,
	}
	reg, err := modelregistry.NewRegistry([]modelregistry.ModelEntry{openaiEntry, qgEntry})
	if err != nil {
		t.Fatalf("build mixed registry: %v", err)
	}
	m := model{modelCatalog: reg, modelName: "qg-synthetic", providerName: "qg"}

	text := m.modelListText()
	if !strings.Contains(text, "qg-synthetic") {
		t.Fatalf("model list must include the qg entry from the model catalog, got %q", text)
	}
	if strings.Contains(text, "openai-synthetic") {
		t.Fatalf("model list must filter out non-qg entries, got %q", text)
	}
}
