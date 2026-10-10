package tui

import (
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"testing"
)

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

func providerWizardModelIDs(models []providerWizardModel) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestModelLabelsStayDistinctWithProseDescriptions(t *testing.T) {
	for _, id := range []string{"gpt-oss-120b", "gpt-oss-20b"} {
		model := providerWizardModel{ID: id, Description: "Open-weight GPT model for self-hosted reasoning and instruction-following workloads"}
		if got := model.displayLabel(); got != id {
			t.Fatalf("label = %q, want %q", got, id)
		}
	}
	model := providerWizardModel{ID: "x-ai/grok-4.3", Description: "Grok 4.3"}
	if got := model.displayLabel(); got != "Grok 4.3" {
		t.Fatalf("friendly label lost: %q", got)
	}
}
