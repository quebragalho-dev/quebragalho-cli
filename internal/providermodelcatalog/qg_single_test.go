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
		"gemini-4-argon",
		"glm-5.2",
		"glm-5.3",
		"glm-5.3-flash",
		"muse-wonder",
		"claude-haiku-5.5",
		"gpt-6-luna",
		"gpt-6.1-terra",
		"grok-4.7",
		"jev-1.13",
		"jev-1.14",
		"kimi-k3",
		"kimi-k3.1-pro",
		"mimo-v2.6-flash",
		"qwen3.8-omni-flash",
	} {
		if !ids[want] {
			t.Fatalf("Models(qg) missing %q; got %#v", want, modelIDs(models))
		}
	}
	if len(models) != 18 {
		t.Fatalf("Models(qg) returned %d models, want 18", len(models))
	}
}
