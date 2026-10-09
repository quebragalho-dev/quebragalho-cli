package modelregistry

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodelcatalog"
)

// DefaultRegistry must mirror the curated QG fallback exactly: the 19 models
// served by https://api.quebragalho.dev/v1, all tagged ProviderQG, with no
// legacy claude/gemini/gpt entries left over from the multi-provider catalog.
func TestDefaultRegistryMirrorsCuratedQGCatalog(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry: %v", err)
	}
	descriptor, ok := providercatalog.Get(providercatalog.QGID)
	if !ok {
		t.Fatal("qg descriptor missing from catalog")
	}
	curated := providermodelcatalog.Models(descriptor)
	if len(curated) != 19 {
		t.Fatalf("curated QG fallback has %d models, want 19", len(curated))
	}
	got := registry.List(ListOptions{IncludeDeprecated: true})
	if len(got) != len(curated) {
		t.Fatalf("registry has %d entries, want %d (the curated QG catalog)", len(got), len(curated))
	}
	curatedIDs := map[string]bool{}
	for _, model := range curated {
		curatedIDs[model.ID] = true
	}
	for _, entry := range got {
		if !curatedIDs[entry.ID] {
			t.Fatalf("registry entry %q is not part of the curated QG catalog", entry.ID)
		}
		if entry.Provider != ProviderQG {
			t.Fatalf("registry entry %q provider = %q, want qg", entry.ID, entry.Provider)
		}
		if entry.APIModel != entry.ID {
			t.Fatalf("registry entry %q api model = %q, want the bare served id", entry.ID, entry.APIModel)
		}
		if entry.ContextLimits.ContextWindow <= 0 {
			t.Fatalf("registry entry %q needs a positive context window, got %#v", entry.ID, entry.ContextLimits)
		}
		if !entry.AllowsProvider(ProviderOpenAICompatible) {
			t.Fatalf("registry entry %q must allow the openai-compatible transport", entry.ID)
		}
	}
	for _, banned := range []string{"claude-sonnet-4.5", "gpt-4.1", "gemini-2.5-pro", "gpt-4-turbo"} {
		if _, ok := registry.Get(banned); ok {
			t.Fatalf("legacy model %q must not resolve from the QG registry", banned)
		}
	}
}
