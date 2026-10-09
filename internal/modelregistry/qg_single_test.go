package modelregistry

import "testing"

func TestQGDefaultModelIsMuseSpark(t *testing.T) {
	if DefaultModelID != "muse-spark-1.3-contributor" {
		t.Fatalf("DefaultModelID = %q, want muse-spark-1.3-contributor", DefaultModelID)
	}
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry: %v", err)
	}
	entry, err := registry.Require("muse-spark-1.3-contributor")
	if err != nil {
		t.Fatalf("Require(qg default): %v", err)
	}
	if !entry.AllowsProvider(ProviderOpenAICompatible) {
		t.Fatalf("qg default %q must allow openai-compatible transport", entry.ID)
	}
}
