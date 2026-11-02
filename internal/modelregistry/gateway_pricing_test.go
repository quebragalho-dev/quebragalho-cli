package modelregistry

import (
	"testing"
)

func TestMuseSparkMatchesGatewayPricing(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	entry, err := registry.Require("muse-spark-1.3-contributor")
	if err != nil {
		t.Fatal(err)
	}
	// Live GET /v1/models 2026-11-02: input 0.09, output 0.18, cached 0.0018.
	if entry.Cost.InputPerMillion != 0.09 || entry.Cost.OutputPerMillion != 0.18 || entry.Cost.CachedInputPerMillion != 0.0018 {
		t.Fatalf("muse-spark cost = %v/%v/%v, want 0.09/0.18/0.0018 (gateway live)",
			entry.Cost.InputPerMillion, entry.Cost.OutputPerMillion, entry.Cost.CachedInputPerMillion)
	}
}
