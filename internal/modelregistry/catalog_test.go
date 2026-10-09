package modelregistry

import (
	"strings"
	"testing"
)

func TestDefaultRegistryCoversM1ModelCatalog(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry returned error: %v", err)
	}

	models := registry.List(ListOptions{})
	if len(models) < 10 {
		t.Fatalf("default registry has %d active/preview models, want at least 10", len(models))
	}
	for _, model := range models {
		if model.Status == ModelStatusDeprecated {
			t.Fatalf("List without include deprecated returned deprecated model %q", model.ID)
		}
		if model.ID == "" || model.DisplayName == "" || model.APIModel == "" {
			t.Fatalf("model should expose complete identity metadata: %#v", model)
		}
		// MaxOutputTokens may be 0 ("no cap advertised"); the window must be positive.
		if model.ContextLimits.ContextWindow <= 0 {
			t.Fatalf("model should expose a positive context window: %#v", model)
		}
		if !model.Supports(ModelCapabilityChat) || !model.Supports(ModelCapabilityStreaming) {
			t.Fatalf("model %q should expose chat and streaming capabilities", model.ID)
		}
		if model.Cost.Currency != "USD" || model.Cost.Unit != "per_1m_tokens" {
			t.Fatalf("model %q should expose USD token pricing: %#v", model.ID, model.Cost)
		}
		if !strings.HasPrefix(model.Cost.Source, "https://") {
			t.Fatalf("model %q should expose source URL: %#v", model.ID, model.Cost)
		}
	}

	providers := map[ProviderKind]bool{}
	for _, model := range models {
		providers[model.Provider] = true
	}
	if len(providers) != 1 || !providers[ProviderQG] {
		t.Fatalf("default registry providers = %#v, want only qg", providers)
	}
	if deprecated := registry.List(ListOptions{IncludeDeprecated: true}); len(deprecated) != len(models) {
		t.Fatalf("include-deprecated list has %d rows, want %d (no legacy deprecated entries)", len(deprecated), len(models))
	}
	if DefaultModelID != "muse-spark-1.3-contributor" {
		t.Fatalf("DefaultModelID = %q, want muse-spark-1.3-contributor", DefaultModelID)
	}

	muse, err := registry.Require("muse-spark")
	if err != nil {
		t.Fatalf("muse-spark alias should resolve: %v", err)
	}
	if muse.APIModel != "muse-spark-1.3-contributor" {
		t.Fatalf("muse APIModel = %q, want the bare served id", muse.APIModel)
	}
	if muse.ContextLimits.ContextWindow != 1_000_000 {
		t.Fatalf("muse limits = %#v, want 1M context", muse.ContextLimits)
	}
	if muse.ContextLimits.MaxOutputTokens != 0 {
		t.Fatalf("muse max output = %d, want 0 (unknown cap)", muse.ContextLimits.MaxOutputTokens)
	}
}

func TestDefaultRegistryResolvesAliasesAndStableFilters(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry returned error: %v", err)
	}

	if id, ok := registry.ResolveID(" QG:MUSE-SPARK-1.3-CONTRIBUTOR "); !ok || id != "muse-spark-1.3-contributor" {
		t.Fatalf("ResolveID(qg alias) = %q/%v, want muse-spark-1.3-contributor/true", id, ok)
	}
	if id, ok := registry.ResolveID("muse-spark"); !ok || id != "muse-spark-1.3-contributor" {
		t.Fatalf("ResolveID(muse-spark) = %q/%v, want muse-spark-1.3-contributor/true", id, ok)
	}
	if _, ok := registry.ResolveID("gpt-latest"); ok {
		t.Fatal("registry should not expose speculative latest aliases")
	}

	qgModels := registry.ListByProvider(ProviderQG)
	if len(qgModels) == 0 {
		t.Fatal("ListByProvider(qg) returned no models")
	}
	for _, model := range qgModels {
		if model.Provider != ProviderQG {
			t.Fatalf("ListByProvider(qg) returned %q", model.Provider)
		}
	}
	if legacy := registry.ListByProvider(ProviderOpenAI); len(legacy) != 0 {
		t.Fatalf("ListByProvider(openai) = %#v, want empty in the single-provider catalog", legacy)
	}

	visionModels := registry.ListByCapability(ModelCapabilityVision)
	if len(visionModels) == 0 {
		t.Fatal("expected at least one vision model")
	}
	for _, model := range visionModels {
		if !model.Supports(ModelCapabilityVision) {
			t.Fatalf("ListByCapability(vision) returned model without vision: %#v", model)
		}
	}
	if !registry.SupportsCapability("deepseek-v4-pro", ModelCapabilityReasoning) {
		t.Fatal("deepseek-v4-pro should expose reasoning capability")
	}
}

func TestDefaultRegistryReasoningAndProviderAssertions(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry returned error: %v", err)
	}

	// Catalog entries declare no fixed effort list, so /effort falls back to the
	// name-based heuristic: DeepSeek V4 exposes low/high/max on the API.
	efforts := registry.ReasoningEfforts("deepseek-v4-pro")
	wantEfforts := []ReasoningEffort{ReasoningEffortLow, ReasoningEffortHigh, ReasoningEffortMax}
	if len(efforts) != len(wantEfforts) {
		t.Fatalf("ReasoningEfforts length = %d, want %d: %#v", len(efforts), len(wantEfforts), efforts)
	}
	for index, effort := range wantEfforts {
		if efforts[index] != effort {
			t.Fatalf("ReasoningEfforts[%d] = %q, want %q", index, efforts[index], effort)
		}
	}
	if efforts := registry.ReasoningEfforts("qwen3.8-max"); len(efforts) != 0 {
		t.Fatalf("qwen3.8-max reasoning efforts = %#v, want empty", efforts)
	}

	model, err := registry.RequireProvider("muse-spark-1.3-contributor", ProviderQG)
	if err != nil {
		t.Fatalf("RequireProvider(qg) returned error: %v", err)
	}
	if model.ID != "muse-spark-1.3-contributor" {
		t.Fatalf("RequireProvider returned %q", model.ID)
	}
	if _, err := registry.RequireProvider("muse-spark-1.3-contributor", ProviderAnthropic); err == nil {
		t.Fatal("RequireProvider should reject provider mismatches")
	} else if !strings.Contains(err.Error(), "belongs to qg") {
		t.Fatalf("provider mismatch error = %q, want belongs to qg", err.Error())
	}
	if _, err := registry.Require("unknown-model"); err == nil {
		t.Fatal("Require should reject unknown models")
	} else if !strings.Contains(err.Error(), "unknown QG model") {
		t.Fatalf("unknown model error = %q, want unknown QG model", err.Error())
	}
}

func TestDefaultRegistryResolvesFuzzyMatchPatterns(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry returned error: %v", err)
	}
	cases := map[string]string{
		"muse spark": "muse-spark-1.3-contributor",
		"MUSE SPARK": "muse-spark-1.3-contributor",
	}
	for input, want := range cases {
		model, ok := registry.Resolve(input)
		if !ok || model.ID != want {
			t.Fatalf("Resolve(%q) = %q/%v, want %q", input, model.ID, ok, want)
		}
	}
}

// The deprecation machinery must keep working even though the QG catalog ships
// no deprecated rows: a deprecated entry redirects to its fallback with a notice.
func TestRegistryDeprecatedModelsRedirect(t *testing.T) {
	deprecated := syntheticEntry("old-model")
	deprecated.Status = ModelStatusDeprecated
	deprecated.Deprecation = &DeprecationRule{
		FallbackID: "new-model",
		WarningMsg: "old-model is deprecated; using new-model instead",
	}
	replacement := syntheticEntry("new-model")
	registry, err := NewRegistry([]ModelEntry{deprecated, replacement})
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}

	model, notice, ok := registry.ResolveWithFallback("old-model")
	if !ok {
		t.Fatal("ResolveWithFallback(old-model) failed")
	}
	if model.ID != "new-model" {
		t.Fatalf("ResolveWithFallback(old-model) = %q, want new-model", model.ID)
	}
	if notice == "" {
		t.Fatal("ResolveWithFallback(old-model) should return a deprecation notice")
	}
}

// A default effort must be one of the model's supported efforts; Validate
// enforces it so EffectiveReasoningEffort can never hand back an unsupported tier.
func TestRegistryDefaultReasoningEffortMustBeSupported(t *testing.T) {
	valid := syntheticEntry("valid-effort")
	valid.ReasoningEfforts = []ReasoningEffort{ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh}
	valid.DefaultReasoningEffort = ReasoningEffortMedium
	registry, err := NewRegistry([]ModelEntry{valid})
	if err != nil {
		t.Fatalf("entry with supported default effort must validate: %v", err)
	}
	model, err := registry.Require("valid-effort")
	if err != nil {
		t.Fatalf("Require(valid-effort) returned error: %v", err)
	}
	if model.DefaultReasoningEffort != ReasoningEffortMedium {
		t.Fatalf("default effort = %q, want medium", model.DefaultReasoningEffort)
	}

	unsupported := syntheticEntry("unsupported-effort")
	unsupported.DefaultReasoningEffort = ReasoningEffortHigh
	if _, err := NewRegistry([]ModelEntry{unsupported}); err == nil {
		t.Fatal("default effort outside the supported set must be rejected")
	} else if !strings.Contains(err.Error(), "not in the model's supported efforts") {
		t.Fatalf("validation error = %q, want unsupported-default message", err.Error())
	}
}

func TestRegistryUpgradeTargets(t *testing.T) {
	base := syntheticEntry("base-model")
	base.UpgradeTargetID = "strong-model"
	strong := syntheticEntry("strong-model")
	registry, err := NewRegistry([]ModelEntry{base, strong})
	if err != nil {
		t.Fatalf("NewRegistry returned error: %v", err)
	}

	target, ok := registry.UpgradeTarget("base-model")
	if !ok {
		t.Fatal("UpgradeTarget(base-model) returned no target, want strong-model")
	}
	if target.ID != "strong-model" {
		t.Fatalf("UpgradeTarget(base-model) = %q, want strong-model", target.ID)
	}
	if target, ok := registry.UpgradeTarget("strong-model"); ok {
		t.Fatalf("top-tier model should have no upgrade target, got %q", target.ID)
	}

	// The seeded field is present on the decorated entry, not only via the method.
	entry, err := registry.Require("base-model")
	if err != nil {
		t.Fatalf("Require(base-model) returned error: %v", err)
	}
	if entry.UpgradeTargetID != "strong-model" {
		t.Fatalf("base-model UpgradeTargetID = %q, want strong-model", entry.UpgradeTargetID)
	}
}

// syntheticEntry builds a valid single-provider entry for machinery tests that
// must not depend on the shipped QG catalog contents.
func syntheticEntry(id string) ModelEntry {
	entry := validModelEntry()
	entry.ID = id
	entry.APIModel = id
	entry.Aliases = []string{id}
	entry.Provider = ProviderQG
	return entry
}
