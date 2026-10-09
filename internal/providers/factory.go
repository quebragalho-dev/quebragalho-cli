package providers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providers/openai"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providers/providerio"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// Options configures provider construction.
type Options struct {
	UserAgent     string
	HTTPClient    *http.Client
	ModelRegistry *modelregistry.Registry
}

// New creates the single QG runtime provider (OpenAI-compatible,
// https://api.quebragalho.dev/v1). Any non OpenAI-compatible provider kind is
// rejected: Etapa 2 removes Anthropic/Google/Codex branches.
func New(profile config.ProviderProfile, options Options) (qgruntime.Provider, error) {
	resolved, err := resolveProfile(profile, options)
	if err != nil {
		return nil, err
	}
	if resolved.providerKind != config.ProviderKindOpenAI && resolved.providerKind != config.ProviderKindOpenAICompatible {
		return nil, fmt.Errorf("qg: single provider only supports %q (got %q)", config.ProviderKindOpenAICompatible, resolved.providerKind)
	}
	return openai.New(openai.Options{
		APIKey:                profile.APIKey,
		BaseURL:               resolved.baseURL,
		Model:                 resolved.apiModel,
		AuthHeader:            profile.AuthHeader,
		AuthScheme:            profile.AuthScheme,
		AuthHeaderValue:       profile.AuthHeaderValue,
		CustomHeaders:         providerio.CopyHeaders(profile.CustomHeaders),
		MaxTokens:             resolved.maxOutputTokens,
		HTTPClient:            options.HTTPClient,
		UserAgent:             options.UserAgent,
		ParseThinkTags:        parseThinkTagsForProfile(profile, resolved),
		DisablePromptCacheKey: resolved.providerKind == config.ProviderKindOpenAICompatible,
	})
}

// NewTurnSessionProvider builds the default TurnSessionProvider for a resolved
// profile: it constructs the provider exactly as New does, then wraps it with a
// ProviderCapabilities projection computed from the same model-registry entry
// New already resolves. The default session's Stream is the provider's
// StreamCompletion, so runtime behavior is identical to using New directly.
func NewTurnSessionProvider(profile config.ProviderProfile, options Options) (qgruntime.TurnSessionProvider, error) {
	provider, err := New(profile, options)
	if err != nil {
		return nil, err
	}
	caps, err := resolveCapabilities(profile, options)
	if err != nil {
		return nil, err
	}
	return qgruntime.NewProviderTurnSessionProvider(provider, caps), nil
}

// resolveCapabilities projects the resolved profile's model-registry entry into
// the flat qgruntime.ProviderCapabilities (qgruntime cannot reference
// modelregistry types — modelregistry imports qgruntime, so a typed field
// would form an import cycle). A model absent from the registry yields only the
// resolved API model id and max-output tokens; the rest stays zero (unknown).
func resolveCapabilities(profile config.ProviderProfile, options Options) (qgruntime.ProviderCapabilities, error) {
	resolved, err := resolveProfile(profile, options)
	if err != nil {
		return qgruntime.ProviderCapabilities{}, err
	}
	caps := qgruntime.ProviderCapabilities{
		Model:           resolved.apiModel,
		MaxOutputTokens: resolved.maxOutputTokens,
	}
	registry, err := defaultRegistry(options.ModelRegistry)
	if err != nil {
		return qgruntime.ProviderCapabilities{}, err
	}
	if entry, ok := registry.Get(strings.TrimSpace(profile.Model)); ok {
		caps.ContextWindow = entry.ContextLimits.ContextWindow
		caps.SupportsVision = entry.Supports(modelregistry.ModelCapabilityVision)
		caps.SupportsReasoning = entry.Supports(modelregistry.ModelCapabilityReasoning)
		caps.SupportsPromptCache = entry.Supports(modelregistry.ModelCapabilityPromptCache)
		// Read efforts through Registry.ReasoningEfforts (not the raw entry) so
		// the projection matches what the /effort picker and the run-time
		// resolver advertise — including the name-based fallback for catalog
		// entries that enumerate no efforts of their own.
		for _, effort := range registry.ReasoningEfforts(entry.ID) {
			caps.ReasoningEfforts = append(caps.ReasoningEfforts, string(effort))
		}
	}
	return caps, nil
}

func parseThinkTagsForProfile(profile config.ProviderProfile, resolved resolvedProfile) bool {
	if profile.ParseThinkTags != nil {
		return *profile.ParseThinkTags
	}
	if resolved.providerKind != config.ProviderKindOpenAICompatible {
		return false
	}
	return modelMayEmitThinkTags(resolved.apiModel)
}

func modelMayEmitThinkTags(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, marker := range []string{
		"deepseek-r1",
		"deepseek-reasoner",
		"gpt-oss",
		"glm-z1",
		"kimi-k2-thinking",
		"magistral",
		"minimax-m2",
		"minimax-m3",
		"nemotron",
		"qwen3",
		"qwq",
		"reasoner",
		"reasoning",
		"thinking",
	} {
		if strings.Contains(model, marker) {
			return true
		}
	}
	return false
}

type resolvedProfile struct {
	providerKind    config.ProviderKind
	apiModel        string
	baseURL         string
	maxOutputTokens int
}

// RuntimeMetadata describes the provider identity and concrete API model used
// after QG model aliases and provider-kind defaults are resolved.
type RuntimeMetadata struct {
	ProviderKind config.ProviderKind
	APIModel     string
}

// ResolveRuntimeMetadata returns the provider kind and API model that New would
// use for a profile, without constructing a network client.
func ResolveRuntimeMetadata(profile config.ProviderProfile, options Options) (RuntimeMetadata, error) {
	resolved, err := resolveProfile(profile, options)
	if err != nil {
		return RuntimeMetadata{}, err
	}
	return RuntimeMetadata{
		ProviderKind: resolved.providerKind,
		APIModel:     resolved.apiModel,
	}, nil
}

func resolveProfile(profile config.ProviderProfile, options Options) (resolvedProfile, error) {
	model := strings.TrimSpace(profile.Model)
	if model == "" {
		return resolvedProfile{}, fmt.Errorf("provider %s requires model", profile.Name)
	}
	providerKind, explicitProvider := explicitProviderKind(profile)
	registry, err := defaultRegistry(options.ModelRegistry)
	if err != nil {
		return resolvedProfile{}, err
	}

	// baseURL resolves in this order: profile override → catalog default.
	// The catalog default makes the chatgpt Codex backend Just Work when a
	// user runs `qg` with a `catalogID: chatgpt` profile; the user can
	// still pin their own URL (e.g. a self-hosted Codex gateway or a local
	// OAuth proxy) and it wins.
	baseURL := strings.TrimSpace(profile.BaseURL)
	if baseURL == "" {
		if descriptor, ok := providercatalog.Get(profile.CatalogID); ok {
			baseURL = strings.TrimSpace(descriptor.DefaultBaseURL)
		}
	}
	if baseURL == "" {
		baseURL = config.QGBaseURL
	}

	if entry, ok := registry.Get(model); ok {
		modelProvider := configKind(entry.Provider)
		// Adopt the registry entry's provider only when the caller did not pin one.
		// (The old `|| isImplicitOpenAI(...)` clause was dead: explicitProvider==true
		// means ProviderKind or Provider is set, but isImplicitOpenAI required both
		// empty, so it could never add a case.)
		if !explicitProvider {
			providerKind = modelProvider
		}
		if providerKind == config.ProviderKindOpenAICompatible {
			if !entry.AllowsProvider(modelregistry.ProviderOpenAICompatible) {
				return resolvedProfile{}, fmt.Errorf("qg model %s belongs to %s, not %s", entry.ID, entry.Provider, modelregistry.ProviderOpenAICompatible)
			}
		} else if providerKind == config.ProviderKindAnthropicCompat {
			if !entry.AllowsProvider(modelregistry.ProviderAnthropic) {
				return resolvedProfile{}, fmt.Errorf("qg model %s belongs to %s, not %s", entry.ID, entry.Provider, providerKind)
			}
		} else if providerKind != modelProvider {
			return resolvedProfile{}, fmt.Errorf("qg model %s belongs to %s, not %s", entry.ID, entry.Provider, providerKind)
		}
		return resolvedProfile{
			providerKind:    providerKind,
			apiModel:        entry.APIModel,
			baseURL:         baseURL,
			maxOutputTokens: entry.ContextLimits.MaxOutputTokens,
		}, nil
	}

	if providerKind == "" {
		providerKind = config.ProviderKindOpenAICompatible
	}
	return resolvedProfile{
		providerKind: providerKind,
		apiModel:     model,
		baseURL:      baseURL,
	}, nil
}

func explicitProviderKind(profile config.ProviderProfile) (config.ProviderKind, bool) {
	providerKind := config.ProviderKind(strings.TrimSpace(strings.ToLower(string(profile.ProviderKind))))
	if providerKind != "" {
		return providerKind, true
	}
	provider := strings.TrimSpace(strings.ToLower(profile.Provider))
	if provider != "" {
		return config.ProviderKind(provider), true
	}
	return "", false
}

func configKind(provider modelregistry.ProviderKind) config.ProviderKind {
	if provider == modelregistry.ProviderQG {
		// The single QG provider runs on the openai-compatible transport; a bare
		// cast would leak the registry-only "qg" kind into config kinds.
		return config.ProviderKindOpenAICompatible
	}
	return config.ProviderKind(provider)
}

func defaultRegistry(registry *modelregistry.Registry) (modelregistry.Registry, error) {
	if registry != nil {
		return *registry, nil
	}
	return modelregistry.DefaultRegistry()
}
