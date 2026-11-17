package providermodelcatalog

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

func TestModelsDoNotAliasMutableCatalogState(t *testing.T) {
	descriptor, ok := providercatalog.Get("qg")
	if !ok {
		t.Fatal("qg missing from catalog")
	}
	first := Models(descriptor)
	if len(first) == 0 {
		t.Fatal("Models(qg) returned no models")
	}
	first[0].ID = "mutated"
	first[0].Description = "mutated"

	second := Models(descriptor)
	if second[0].ID == "mutated" || second[0].Description == "mutated" {
		t.Fatal("Models returned aliased mutable catalog state")
	}
}

func TestDedupeModelsDoesNotAliasCapabilitySlices(t *testing.T) {
	models := []Model{{
		ID:               "reasoner",
		ReasoningEfforts: []string{"low", "high"},
	}}
	first := dedupeModels("reasoner", models)
	first[0].ReasoningEfforts[0] = "mutated"

	second := dedupeModels("reasoner", models)
	if got, want := second[0].ReasoningEfforts[0], "low"; got != want {
		t.Fatalf("reasoning efforts alias source state: got %q, want %q", got, want)
	}
}

func modelIDs(models []Model) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}
