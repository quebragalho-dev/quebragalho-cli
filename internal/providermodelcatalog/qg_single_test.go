package providermodelcatalog

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

func TestQGModelsFixedFallback(t *testing.T) {
	d, ok := providercatalog.Get("qg")
	if !ok {
		t.Fatal("qg missing from catalog")
	}
	models := Models(d)
	if len(models) == 0 {
		t.Fatal("Models(qg) returned no models")
	}
	if models[0].ID != "muse-spark-1.3-contributor" {
		t.Fatalf("Models(qg)[0].ID = %q, want muse-spark-1.3-contributor", models[0].ID)
	}
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
	}
	for _, want := range []string{
		"muse-spark-1.3-contributor",
		"deepseek-v4-pro",
		"deepseek-v4.1-flash",
		"glm-5.3",
		"glm-5.3-flash",
		"gpt-5.6-luna",
		"gpt-5.6-sol",
		"gpt-6-astra",
		"gpt-6-luna",
		"gpt-6-sol",
		"gpt-6.1-sol",
		"grok-4.7",
		"hy4",
		"kimi-k3",
		"mimo-v2.6-flash",
		"mimo-v2.6-pro",
		"mimo-v2.6-pro-ultraspeed",
		"qwen3.8-max",
		"qwen3.8-omni-flash",
	} {
		if !ids[want] {
			t.Fatalf("Models(qg) missing %q; got %#v", want, modelIDs(models))
		}
	}
	if len(models) != 19 {
		t.Fatalf("Models(qg) returned %d models, want 19", len(models))
	}
}
