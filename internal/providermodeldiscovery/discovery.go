package providermodeldiscovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodelcatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providers/providerio"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

type Model struct {
	ID                     string
	Description            string
	ContextWindow          int
	ToolCall               bool
	Reasoning              bool
	ReasoningEfforts       []string
	DefaultReasoningEffort string
	ServiceTiers           []string
	DefaultServiceTier     string
	InputModalities        []string
	OutputModalities       []string
	InputCost              float64
	OutputCost             float64
	Tags                   []string
	Source                 string
}

type Options struct {
	HTTPClient *http.Client
	UserAgent  string
}

// DiscoverCatalog returns the models available for the single QG provider: the
// live list from GET /v1/models when the profile can probe it with a
// credential, else the fixed offline catalog. Known non-coding live ids are
// dropped, and a live response that yields nothing usable falls back to the
// fixed catalog so the picker never collapses to an empty list (M11).
func DiscoverCatalog(ctx context.Context, provider providercatalog.Descriptor, profile config.ProviderProfile, options Options) ([]Model, error) {
	var liveErr error
	if modelDiscoveryAllowed(profile) && (!provider.RequiresAuth ||
		discoveryHasCredential(profile)) {
		var liveModels []Model
		liveModels, liveErr = Discover(ctx, profile, options)
		if liveErr == nil {
			usable := make([]Model, 0, len(liveModels))
			for _, model := range liveModels {
				if model.ID == "" || providermodelcatalog.IsKnownNonCodingModelID(model.ID) {
					continue
				}
				usable = append(usable, model)
			}
			if len(usable) > 0 {
				return usable, nil
			}
			// Live probe returned 200 but no usable model ids: fall through to
			// the fixed catalog instead of returning an empty list that collapses
			// the picker (M11).
		}
	}
	if curated := modelsFromCatalog(providermodelcatalog.Models(provider)); len(curated) > 0 {
		return curated, nil
	}
	if liveErr != nil {
		return nil, liveErr
	}
	return nil, fmt.Errorf("no provider models discovered")
}

// discoveryHasCredential reports whether the profile carries a usable credential
// for an authenticated /models probe. A profile may authenticate via a raw
// auth-header value instead of APIKey, so treat either as present — consistent
// with config credential checks and qgcommands ProviderSnapshot.APIKeySet.
func discoveryHasCredential(profile config.ProviderProfile) bool {
	return strings.TrimSpace(profile.APIKey) != "" || strings.TrimSpace(profile.AuthHeaderValue) != ""
}

func Discover(ctx context.Context, profile config.ProviderProfile, options Options) ([]Model, error) {
	switch discoveryProviderKind(profile) {
	case config.ProviderKindOpenAI, config.ProviderKindOpenAICompatible:
		return discoverOpenAIModels(ctx, profile, options)
	default:
		return nil, fmt.Errorf("qg: single provider only supports OpenAI-compatible discovery (got %q)", discoveryProviderKind(profile))
	}
}

func discoverOpenAIModels(ctx context.Context, profile config.ProviderProfile, options Options) ([]Model, error) {
	endpoint, err := modelsEndpoint(profile.BaseURL)
	if err != nil {
		return nil, err
	}
	return fetchProviderModels(ctx, endpoint, profile, options, providerio.AuthHeaders{
		APIKey:            profile.APIKey,
		DefaultAuthHeader: "Authorization",
		DefaultAuthScheme: "Bearer",
		AuthHeader:        profile.AuthHeader,
		AuthScheme:        profile.AuthScheme,
		AuthHeaderValue:   profile.AuthHeaderValue,
		CustomHeaders:     providerio.CopyHeaders(profile.CustomHeaders),
	}, nil)
}

func fetchProviderModels(ctx context.Context, endpoint string, profile config.ProviderProfile, options Options, auth providerio.AuthHeaders, configure func(*http.Request)) ([]Model, error) {
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := providerio.SendWithRetry(ctx, client, http.MethodGet, endpoint, nil, func(request *http.Request) {
		request.Header.Set("Accept", "application/json")
		if configure != nil {
			configure(request)
		}
		providerio.ApplyAuthHeaders(request, auth)
	}, 1)
	if err != nil {
		return nil, redactDiscoveryError(err, profile)
	}
	defer response.Body.Close()

	// Full provider catalogs run ~0.5MB and grow; keep headroom for growth.
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, redactDiscoveryError(err, profile)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, redactDiscoveryError(fmt.Errorf("models endpoint returned %s: %s", response.Status, strings.TrimSpace(string(body))), profile)
	}

	models, err := parseModelsResponse(body)
	if err != nil {
		return nil, redactDiscoveryError(err, profile)
	}
	return models, nil
}

func modelDiscoveryAllowed(profile config.ProviderProfile) bool {
	switch discoveryProviderKind(profile) {
	case config.ProviderKindOpenAI, config.ProviderKindOpenAICompatible:
		return true
	default:
		return false
	}
}

func discoveryProviderKind(profile config.ProviderProfile) config.ProviderKind {
	kind := config.ProviderKind(strings.TrimSpace(strings.ToLower(string(profile.ProviderKind))))
	if kind == "" {
		kind = config.ProviderKind(strings.TrimSpace(strings.ToLower(profile.Provider)))
	}
	return kind
}

func modelsEndpoint(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("provider base URL is required for model discovery")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid provider base URL %q", baseURL)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/models"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

type modelsResponseItem struct {
	ID                 string   `json:"id"`
	Slug               string   `json:"slug"`
	DisplayName        string   `json:"display_name"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Visibility         string   `json:"visibility"`
	ContextWindow      int      `json:"context_window"`
	ContextWindowAlt   int      `json:"contextWindow"`
	ContextLength      int      `json:"context_length"`
	MaxContextLength   int      `json:"max_context_length"`
	Free               bool     `json:"free"`
	IsFree             bool     `json:"is_free"`
	ToolCall           bool     `json:"tool_call"`
	ToolCallCamel      bool     `json:"toolCall"`
	Tools              bool     `json:"tools"`
	Reasoning          bool     `json:"reasoning"`
	ReasoningEfforts   []string `json:"reasoning_efforts"`
	DefaultReasoning   string   `json:"default_reasoning_level"`
	SupportedReasoning []struct {
		Effort string `json:"effort"`
	} `json:"supported_reasoning_levels"`
	ServiceTiers []struct {
		ID string `json:"id"`
	} `json:"service_tiers"`
	AdditionalSpeedTiers []string `json:"additional_speed_tiers"`
	DefaultServiceTier   string   `json:"default_service_tier"`
	SupportedParameters  []string `json:"supported_parameters"`
}

type modelsResponse struct {
	Data   []modelsResponseItem `json:"data"`
	Models []modelsResponseItem `json:"models"`
}

func parseModelsResponse(body []byte) ([]Model, error) {
	var payload modelsResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}
	seen := map[string]bool{}
	items := payload.Data
	if len(items) == 0 {
		items = payload.Models
	}
	models := make([]Model, 0, len(items))
	for _, item := range items {
		id := firstNonEmptyDiscovery(item.ID, item.Slug)
		if id == "" || seen[id] {
			continue
		}
		// Some catalogs include hidden/internal entries alongside picker models
		// (visibility set to anything other than "list"). Generic
		// OpenAI-compatible responses omit visibility entirely, so the filter
		// only applies when the field is present.
		if visibility := strings.ToLower(strings.TrimSpace(item.Visibility)); visibility != "" && visibility != "list" {
			continue
		}
		seen[id] = true
		description := firstNonEmptyDiscovery(
			item.DisplayName,
			item.Name,
			item.Description,
		)
		if (item.Free || item.IsFree || strings.HasSuffix(strings.ToLower(id), ":free")) &&
			description != "" &&
			!strings.Contains(strings.ToLower(description), "free") {
			description = description + " (free)"
		}
		contextWindow := firstPositiveDiscovery(
			item.ContextWindow,
			item.ContextWindowAlt,
			item.ContextLength,
			item.MaxContextLength,
		)
		toolCall := item.ToolCall || item.ToolCallCamel || item.Tools ||
			discoveryContainsFold(item.SupportedParameters, "tools")
		reasoning := item.Reasoning ||
			discoveryContainsFold(item.SupportedParameters, "reasoning") ||
			discoveryContainsFold(item.SupportedParameters, "reasoning_effort") ||
			discoveryContainsFold(item.SupportedParameters, "include_reasoning")
		efforts := reasoningEfforts(item)
		serviceTiers := modelServiceTiers(item)
		models = append(models, Model{
			ID:                     id,
			Description:            description,
			ContextWindow:          contextWindow,
			ToolCall:               toolCall,
			Reasoning:              reasoning || len(efforts) > 0,
			ReasoningEfforts:       efforts,
			DefaultReasoningEffort: strings.TrimSpace(item.DefaultReasoning),
			ServiceTiers:           serviceTiers,
			DefaultServiceTier:     normalizeServiceTier(item.DefaultServiceTier),
			Source:                 "live",
		})
	}
	sort.SliceStable(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	if len(models) == 0 {
		return nil, fmt.Errorf("models endpoint returned no model ids")
	}
	return models, nil
}

func modelServiceTiers(item modelsResponseItem) []string {
	values := append([]string{}, item.AdditionalSpeedTiers...)
	for _, tier := range item.ServiceTiers {
		values = append(values, tier.ID)
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeServiceTier(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func normalizeServiceTier(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "fast" {
		return "priority"
	}
	return value
}

func reasoningEfforts(item modelsResponseItem) []string {
	efforts := append([]string{}, item.ReasoningEfforts...)
	for _, level := range item.SupportedReasoning {
		efforts = append(efforts, level.Effort)
	}
	seen := map[string]bool{}
	result := make([]string, 0, len(efforts))
	for _, effort := range efforts {
		effort = strings.ToLower(strings.TrimSpace(effort))
		if effort == "" || seen[effort] {
			continue
		}
		seen[effort] = true
		result = append(result, effort)
	}
	return result
}

func modelsFromCatalog(models []providermodelcatalog.Model) []Model {
	result := make([]Model, 0, len(models))
	for _, model := range models {
		result = append(result, Model{
			ID:                     model.ID,
			Description:            model.Description,
			ContextWindow:          model.ContextWindow,
			ToolCall:               model.ToolCall,
			Reasoning:              model.Reasoning,
			ReasoningEfforts:       append([]string{}, model.ReasoningEfforts...),
			DefaultReasoningEffort: model.DefaultReasoningEffort,
			ServiceTiers:           append([]string{}, model.ServiceTiers...),
			DefaultServiceTier:     model.DefaultServiceTier,
			InputModalities:        append([]string{}, model.InputModalities...),
			OutputModalities:       append([]string{}, model.OutputModalities...),
			InputCost:              model.InputCost,
			OutputCost:             model.OutputCost,
			Tags:                   append([]string{}, model.Tags...),
			Source:                 model.Source,
		})
	}
	return result
}

func discoveryContainsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}

func firstNonEmptyDiscovery(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func firstPositiveDiscovery(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func redactDiscoveryError(err error, profile config.ProviderProfile) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s", redaction.RedactString(err.Error(), redaction.Options{ExtraSecretValues: []string{
		profile.APIKey,
		profile.AuthHeaderValue,
	}}))
}
