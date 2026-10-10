package modelregistry

import (
	"fmt"
	"strings"
)

const DefaultModelID = "muse-spark-1.3-contributor"

// FallbackContextWindow is the assumed context window (max input tokens) for a
// model that isn't in the curated registry and wasn't resolved from live provider
// discovery — e.g. proxy/custom models like the GPT-5 Codex, xAI, or Ollama-cloud
// variants. A positive value here is what enables agent-loop compaction (both the
// proactive ~80% trigger and the reactive post-overflow recovery) for those models;
// without it compaction stays disabled and long sessions overflow. It is only a
// backstop: an exact window from the registry or live discovery always wins.
const FallbackContextWindow = 200_000

// AgentContextWindow turns a resolved context window into the value used to enable
// agent-loop compaction: the resolved window when known, else FallbackContextWindow
// so compaction is enabled even for uncatalogued models. Kept separate from the
// raw resolver so display gauges still show nothing (not a guessed denominator)
// when the real window is unknown.
func AgentContextWindow(resolved int) int {
	if resolved <= 0 {
		return FallbackContextWindow
	}
	return resolved
}

// sourceLastVerified records the last live observation of GET /v1/models
// (ids, context labels, vision/reasoning flags, per-1M prices).
const sourceLastVerified = "2026-10-09"

const qgPricingSource = "https://api.quebragalho.dev/v1"

type ListOptions struct {
	IncludeDeprecated bool
	Provider          ProviderKind
	Capability        ModelCapability
}

func DefaultRegistry() (Registry, error) {
	return NewRegistry(DefaultModelEntries())
}

func DefaultModelEntries() []ModelEntry {
	// The 19 models served by https://api.quebragalho.dev/v1, observed via
	// GET /v1/models on 2026-10-09: context windows come from context_label
	// (live context_length was null), prices are the live per-1M rates, and
	// MaxOutputTokens stays 0 because the API publishes no output cap.
	entries := []ModelEntry{
		qgModel("muse-spark-1.3-contributor", "Muse Spark", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.04, OutputPerMillion: 0.08, CachedInputPerMillion: 0.001}, []string{"muse-spark"}),
		qgModel("deepseek-v4-pro", "DeepSeek V4 Pro", 1_000_000, []ModelCapability{ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.32, OutputPerMillion: 0.97, CachedInputPerMillion: 0.033}, nil),
		qgModel("deepseek-v4.1-flash", "DeepSeek V4.1 Flash", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.14, OutputPerMillion: 0.56, CachedInputPerMillion: 0.009}, nil),
		qgModel("glm-5.3", "GLM 5.3", 1_000_000, []ModelCapability{ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.54, OutputPerMillion: 1.70, CachedInputPerMillion: 0.08}, nil),
		qgModel("glm-5.3-flash", "GLM 5.3 Flash", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.05, OutputPerMillion: 0.17, CachedInputPerMillion: 0.009}, nil),
		qgModel("gpt-5.6-luna", "GPT 5.6 Luna", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.055, OutputPerMillion: 0.33, CachedInputPerMillion: 0.009}, nil),
		qgModel("gpt-5.6-sol", "GPT 5.6 Sol", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 1.20, OutputPerMillion: 7.20, CachedInputPerMillion: 0.12}, nil),
		qgModel("gpt-6-astra", "GPT 6 Astra", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 4.00, OutputPerMillion: 20.00, CachedInputPerMillion: 0.40}, nil),
		qgModel("gpt-6-luna", "GPT 6 Luna", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.03, OutputPerMillion: 0.15, CachedInputPerMillion: 0.003}, nil),
		qgModel("gpt-6-sol", "GPT 6 Sol", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.40, OutputPerMillion: 2.00, CachedInputPerMillion: 0.04}, nil),
		qgModel("gpt-6.1-sol", "GPT 6.1 Sol", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.80, OutputPerMillion: 4.00, CachedInputPerMillion: 0.04}, nil),
		qgModel("grok-4.7", "Grok 4.7", 500_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.80, OutputPerMillion: 2.40, CachedInputPerMillion: 0.08}, nil),
		qgModel("hy4", "Hy4", 1_000_000, []ModelCapability{ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.33, OutputPerMillion: 1.00, CachedInputPerMillion: 0.033}, nil),
		qgModel("kimi-k3", "Kimi K3", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 1.19, OutputPerMillion: 6.20, CachedInputPerMillion: 0.15}, nil),
		qgModel("mimo-v2.6-flash", "Mimo V2.6 Flash", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.028, OutputPerMillion: 0.056, CachedInputPerMillion: 0.00056}, nil),
		qgModel("mimo-v2.6-pro", "Mimo V2.6 Pro", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.28, OutputPerMillion: 0.56, CachedInputPerMillion: 0.00241}, nil),
		qgModel("mimo-v2.6-pro-ultraspeed", "Mimo V2.6 Pro Ultraspeed", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 3.05, OutputPerMillion: 6.09, CachedInputPerMillion: 0.0252}, nil),
		qgModel("qwen3.8-max", "Qwen 3.8 Max", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.80, OutputPerMillion: 2.40, CachedInputPerMillion: 0.10}, nil),
		qgModel("qwen3.8-omni-flash", "Qwen 3.8 Omni Flash", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.06, OutputPerMillion: 0.188, CachedInputPerMillion: 0.0099}, nil),
	}
	decorateModelDepth(entries)
	return cloneModelEntries(entries)
}

// decorateModelDepth layers registry-depth metadata (fuzzy match patterns,
// default reasoning efforts, deprecation fallbacks, and escalation targets)
// onto the base catalog entries. Upgrade targets follow ascending input price
// within a model family (live rates observed 2026-10-09), so escalate_model
// moves a struggling session up one tier instead of across vendors.
func decorateModelDepth(entries []ModelEntry) {
	depth := map[string]struct {
		defaultEffort ReasoningEffort
		patterns      []string
		deprecation   *DeprecationRule
		upgradeTarget string
	}{
		"muse-spark-1.3-contributor": {
			patterns: []string{`(?i)^muse[^a-z0-9]*spark$`},
		},
		"deepseek-v4.1-flash": {upgradeTarget: "deepseek-v4-pro"},
		"glm-5.3-flash":       {upgradeTarget: "glm-5.3"},
		"gpt-5.6-luna":        {upgradeTarget: "gpt-5.6-sol"},
		"gpt-6-luna":          {upgradeTarget: "gpt-6-sol"},
		"gpt-6-sol":           {upgradeTarget: "gpt-6.1-sol"},
		"gpt-6.1-sol":         {upgradeTarget: "gpt-6-astra"},
		"mimo-v2.6-flash":     {upgradeTarget: "mimo-v2.6-pro"},
		"mimo-v2.6-pro":       {upgradeTarget: "mimo-v2.6-pro-ultraspeed"},
		"qwen3.8-omni-flash":  {upgradeTarget: "qwen3.8-max"},
	}
	for index := range entries {
		extra, ok := depth[entries[index].ID]
		if !ok {
			continue
		}
		if extra.defaultEffort != "" {
			entries[index].DefaultReasoningEffort = extra.defaultEffort
		}
		if len(extra.patterns) > 0 {
			entries[index].MatchPatterns = append(entries[index].MatchPatterns, extra.patterns...)
		}
		if extra.deprecation != nil {
			entries[index].Deprecation = extra.deprecation.Clone()
		}
		if extra.upgradeTarget != "" {
			entries[index].UpgradeTargetID = extra.upgradeTarget
		}
	}
}

func (registry Registry) List(options ListOptions) []ModelEntry {
	models := make([]ModelEntry, 0, len(registry.models))
	for _, model := range registry.models {
		if !options.IncludeDeprecated && model.Status == ModelStatusDeprecated {
			continue
		}
		if options.Provider != "" && model.Provider != options.Provider {
			continue
		}
		if options.Capability != "" && !model.Supports(options.Capability) {
			continue
		}
		models = append(models, cloneModelEntry(model))
	}
	return models
}

func (registry Registry) ListByProvider(provider ProviderKind) []ModelEntry {
	return registry.List(ListOptions{Provider: provider})
}

func (registry Registry) ListByCapability(capability ModelCapability) []ModelEntry {
	return registry.List(ListOptions{Capability: capability})
}

func (registry Registry) ResolveID(pattern string) (string, bool) {
	model, ok := registry.Get(pattern)
	if !ok {
		return "", false
	}
	return model.ID, true
}

func (registry Registry) Require(pattern string) (ModelEntry, error) {
	model, ok := registry.Get(pattern)
	if !ok {
		return ModelEntry{}, fmt.Errorf("unknown QG model %q", pattern)
	}
	return model, nil
}

func (registry Registry) SupportsCapability(pattern string, capability ModelCapability) bool {
	model, ok := registry.Get(pattern)
	return ok && model.Supports(capability)
}

func (registry Registry) ReasoningEfforts(pattern string) []ReasoningEffort {
	if model, ok := registry.Get(pattern); ok {
		return append([]ReasoningEffort{}, effectiveReasoningEfforts(model)...)
	}
	// Unknown model not in the curated catalog — e.g. a GPT-5 / Codex / o-series
	// variant served via a gateway or a custom OpenAI-compatible endpoint.
	// Infer from the name so /effort still shows controls for it.
	return reasoningEffortsForModelName(pattern)
}

// reasoningEffortsForModelName infers reasoning-effort controls from a model name
// for known reasoning families not present in the catalog. It returns nil for
// non-reasoning models (e.g. GPT-4.1 / GPT-4o), so /effort stays empty there.
func reasoningEffortsForModelName(name string) []ReasoningEffort {
	n := strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexByte(n, ':'); i >= 0 { // drop a "provider:" qualifier
		n = n[i+1:]
	}
	// Gateway ids also carry a slash-form "vendor/" qualifier (e.g. the
	// OpenGateway's tencent/hy3, github's openai/o3-mini) — match on the bare name.
	if i := strings.LastIndexByte(n, '/'); i >= 0 {
		n = n[i+1:]
	}
	switch {
	case strings.HasPrefix(n, "gpt-5") || strings.HasPrefix(n, "gpt5"):
		// GPT-5 / Codex (gpt-5.x) add a "minimal" tier below low.
		return []ReasoningEffort{ReasoningEffortMinimal, ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh}
	case strings.Contains(n, "codex") || isOSeriesModelName(n):
		return []ReasoningEffort{ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh}
	case strings.HasPrefix(n, "hy3") || strings.Contains(n, "hunyuan"):
		// Tencent Hunyuan reasoning models (served via OpenAI-compatible gateways).
		// The client forwards reasoning_effort; whether the upstream honors it is
		// the gateway's translation concern — unknown fields are ignored, so the
		// worst case is a silent no-op rather than a 400.
		return []ReasoningEffort{ReasoningEffortLow, ReasoningEffortMedium, ReasoningEffortHigh}
	case strings.Contains(n, "deepseek"):
		// DeepSeek V4 (deepseek-v4-flash / deepseek-v4-pro) exposes thinking-mode
		// effort control through the OpenAI-compatible reasoning_effort parameter.
		// The API accepts low/high/max and maps medium/xhigh onto high, so only
		// the three distinct tiers are advertised.
		return []ReasoningEffort{ReasoningEffortLow, ReasoningEffortHigh, ReasoningEffortMax}
	default:
		return nil
	}
}

// isOSeriesModelName reports whether name is an OpenAI o-series reasoning model
// (o1/o3/o4, optionally with a -mini/-pro suffix), without matching unrelated
// names that merely start with the same letters.
func isOSeriesModelName(n string) bool {
	for _, prefix := range []string{"o1", "o3", "o4"} {
		if n == prefix || strings.HasPrefix(n, prefix+"-") || strings.HasPrefix(n, prefix+"mini") {
			return true
		}
	}
	return false
}

func (registry Registry) RequireProvider(pattern string, provider ProviderKind) (ModelEntry, error) {
	model, err := registry.Require(pattern)
	if err != nil {
		return ModelEntry{}, err
	}
	if model.Provider != provider {
		return ModelEntry{}, fmt.Errorf("qg model %s belongs to %s, not %s", model.ID, model.Provider, provider)
	}
	return model, nil
}

// qgModel builds a curated entry for a model served by the QG API. Every entry
// is active, has no advertised output cap (0 = unknown; the factory omits
// max_tokens), and can be reached over both the qg primary kind and the
// openai-compatible transport the endpoint actually speaks.
func qgModel(id string, displayName string, window int, extraCapabilities []ModelCapability, cost ModelCost, extraAliases []string) ModelEntry {
	cost.Currency = "USD"
	cost.Unit = "per_1m_tokens"
	cost.Source = qgPricingSource
	cost.SourceLastVerified = sourceLastVerified
	aliases := append([]string{"qg:" + id}, extraAliases...)
	return ModelEntry{
		ID:            id,
		DisplayName:   displayName,
		APIModel:      id,
		Provider:      ProviderQG,
		APIProviders:  []ProviderKind{ProviderQG, ProviderOpenAICompatible},
		ContextLimits: ContextLimits{ContextWindow: window},
		Capabilities:  withBaseCapabilities(extraCapabilities...),
		Cost:          cost,
		Status:        ModelStatusActive,
		Aliases:       aliases,
		Description:   "QG model served by " + qgPricingSource + ".",
	}
}

func withBaseCapabilities(extra ...ModelCapability) []ModelCapability {
	capabilities := []ModelCapability{
		ModelCapabilityChat,
		ModelCapabilityStreaming,
		ModelCapabilityToolCalling,
		ModelCapabilitySystemPrompt,
	}
	return append(capabilities, extra...)
}

func cloneModelEntries(entries []ModelEntry) []ModelEntry {
	cloned := make([]ModelEntry, len(entries))
	for index, entry := range entries {
		cloned[index] = cloneModelEntry(entry)
	}
	return cloned
}

func cloneModelEntry(entry ModelEntry) ModelEntry {
	entry.APIProviders = append([]ProviderKind{}, entry.APIProviders...)
	entry.ReasoningEfforts = append([]ReasoningEffort{}, entry.ReasoningEfforts...)
	entry.Capabilities = append([]ModelCapability{}, entry.Capabilities...)
	entry.Aliases = append([]string{}, entry.Aliases...)
	entry.MatchPatterns = append([]string{}, entry.MatchPatterns...)
	entry.Deprecation = entry.Deprecation.Clone()
	entry.Cost.Tiers = append([]ModelCostTier{}, entry.Cost.Tiers...)
	entry.Cost.Notes = append([]string{}, entry.Cost.Notes...)
	return entry
}
