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
// (ids, vision/reasoning flags, per-1M prices).
const sourceLastVerified = "2026-11-02"

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
	// The 18 models served by https://api.quebragalho.dev/v1, observed via
	// GET /v1/models on 2026-11-02: prices are the live standard per-1M USD
	// rates (promo offers ignored), vision/reasoning come from the live
	// capabilities flags, and MaxOutputTokens stays 0 because the API
	// publishes no output cap. Live context_length was null, so chat models
	// keep the previously observed 1M window and jev keeps its reported 32k.
	entries := []ModelEntry{
		qgModel("muse-spark-1.3-contributor", "Muse Spark", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.09, OutputPerMillion: 0.18, CachedInputPerMillion: 0.0018}, []string{"muse-spark"}),
		qgModel("muse-wonder", "Muse Wonder", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 2.40, OutputPerMillion: 12.00, CachedInputPerMillion: 0.12}, nil),
		qgModel("claude-haiku-5.5", "Claude Haiku 5.5", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.25, OutputPerMillion: 1.25, CachedInputPerMillion: 0.025}, nil),
		qgModel("deepseek-v4-pro", "DeepSeek V4 Pro", 1_000_000, []ModelCapability{ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.32, OutputPerMillion: 0.97, CachedInputPerMillion: 0.033}, nil),
		qgModel("deepseek-v4.1-flash", "DeepSeek V4.1 Flash", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.14, OutputPerMillion: 0.56, CachedInputPerMillion: 0.0028}, nil),
		qgModel("gemini-4-argon", "Gemini 4 Argon", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 1.60, OutputPerMillion: 8.00, CachedInputPerMillion: 0.08}, nil),
		qgModel("glm-5.2", "GLM 5.2", 1_000_000, []ModelCapability{ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.54, OutputPerMillion: 1.70, CachedInputPerMillion: 0.08}, nil),
		qgModel("glm-5.3", "GLM 5.3", 1_000_000, []ModelCapability{ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.54, OutputPerMillion: 1.70, CachedInputPerMillion: 0.08}, nil),
		qgModel("glm-5.3-flash", "GLM 5.3 Flash", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.05, OutputPerMillion: 0.17, CachedInputPerMillion: 0.009}, nil),
		qgModel("gpt-6-luna", "GPT 6 Luna", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.03, OutputPerMillion: 0.15, CachedInputPerMillion: 0.003}, nil),
		qgModel("gpt-6.1-terra", "GPT 6.1 Terra", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.40, OutputPerMillion: 2.00, CachedInputPerMillion: 0.02}, nil),
		qgModel("grok-4.7", "Grok 4.7", 500_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.80, OutputPerMillion: 2.40, CachedInputPerMillion: 0.08}, nil),
		qgModel("jev-1.13", "Jev 1.13", 32_000, nil, ModelCost{InputPerMillion: 0.0378, OutputPerMillion: 0, CachedInputPerMillion: 0}, nil),
		qgModel("jev-1.14", "Jev 1.14", 32_000, nil, ModelCost{InputPerMillion: 0.042, OutputPerMillion: 0, CachedInputPerMillion: 0}, nil),
		qgModel("kimi-k3", "Kimi K3", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 1.19, OutputPerMillion: 6.20, CachedInputPerMillion: 0.15}, nil),
		qgModel("kimi-k3.1-pro", "Kimi K3.1 Pro", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.80, OutputPerMillion: 4.00, CachedInputPerMillion: 0.08}, nil),
		qgModel("mimo-v2.6-flash", "Mimo V2.6 Flash", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.07, OutputPerMillion: 0.14, CachedInputPerMillion: 0.0014}, nil),
		qgModel("qwen3.8-omni-flash", "Qwen 3.8 Omni Flash", 1_000_000, []ModelCapability{ModelCapabilityVision, ModelCapabilityReasoning}, ModelCost{InputPerMillion: 0.063, OutputPerMillion: 0.198, CachedInputPerMillion: 0.0102}, nil),
	}
	decorateModelDepth(entries)
	return cloneModelEntries(entries)
}

// decorateModelDepth layers registry-depth metadata (fuzzy match patterns,
// default reasoning efforts, deprecation fallbacks, and escalation targets)
// onto the base catalog entries. Upgrade targets follow ascending input price
// within a model family (live rates observed 2026-11-02), so escalate_model
// moves a struggling session up one tier instead of across vendors.
func decorateModelDepth(entries []ModelEntry) {
	depth := map[string]struct {
		defaultEffort ReasoningEffort
		patterns      []string
		deprecation   *DeprecationRule
		upgradeTarget string
		efforts       []ReasoningEffort
	}{
		"muse-spark-1.3-contributor": {
			patterns: []string{`(?i)^muse[^a-z0-9]*spark$`},
		},
		"deepseek-v4.1-flash": {upgradeTarget: "deepseek-v4-pro"},
		"glm-5.2": {
			upgradeTarget: "glm-5.3",
			// Minimal measured accepted on the live gateway (Sonda E2E);
			// smallest tier also minimizes title cost.
			efforts: []ReasoningEffort{ReasoningEffortMinimal},
		},
		"glm-5.3-flash": {upgradeTarget: "glm-5.3"},
		"gpt-6-luna": {
			upgradeTarget: "gpt-6.1-terra",
			// Minimal measured accepted on the live gateway (Sonda E2E).
			efforts: []ReasoningEffort{ReasoningEffortMinimal},
		},
		"kimi-k3.1-pro": {upgradeTarget: "kimi-k3"},
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
		if len(extra.efforts) > 0 {
			entries[index].ReasoningEfforts = append([]ReasoningEffort{}, extra.efforts...)
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
