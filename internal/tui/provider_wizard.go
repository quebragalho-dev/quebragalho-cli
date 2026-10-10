package tui

import (
	"maps"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

type providerWizardModel struct {
	ID          string
	Description string
	Meta        string
}

const (
	providerWizardMaxModelNameChars = 28
	providerWizardMaxModelNameWords = 4
)

func providerWizardDescriptionIsDisplayName(description string) bool {
	trimmed := strings.TrimSpace(description)
	if trimmed == "" || providerWizardGenericModelDescription(trimmed) {
		return false
	}
	if len(trimmed) > providerWizardMaxModelNameChars {
		return false
	}
	return len(strings.Fields(trimmed)) <= providerWizardMaxModelNameWords
}

func (model providerWizardModel) displayLabel() string {
	if providerWizardDescriptionIsDisplayName(model.Description) {
		return strings.TrimSpace(model.Description)
	}
	if id := strings.TrimSpace(model.ID); id != "" {
		return id
	}
	// ID-less sentinel rows built in currentModel ("model name required",
	// "no matching models") would otherwise render blank.
	return strings.TrimSpace(model.Description)
}

func (model providerWizardModel) secondaryText() string {
	if model.displayLabel() != model.ID {
		return model.ID
	}
	return model.Description
}

func (model providerWizardModel) matches(query string) bool {
	if query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{model.ID, model.Description, model.Meta}, " "))
	return strings.Contains(haystack, query)
}

func providerWizardGenericModelDescription(description string) bool {
	switch strings.ToLower(strings.TrimSpace(description)) {
	case "", "catalog default", "catalog model", "custom endpoint model", "live model":
		return true
	default:
		return strings.HasSuffix(strings.ToLower(strings.TrimSpace(description)), " model")
	}
}

func maskedProviderWizardKey(value string) string {
	count := len([]rune(value))
	if count == 0 {
		return ""
	}
	if count > 24 {
		count = 24
	}
	return strings.Repeat("*", count)
}

func providerWizardProfile(provider providercatalog.Descriptor, model string, apiKey string, baseURL string, profileName string) config.ProviderProfile {
	resolvedBaseURL := firstProviderDisplayValue(strings.TrimSpace(baseURL), provider.DefaultBaseURL)
	profile := config.ProviderProfile{
		Name:         providerWizardDisplayName(provider, baseURL, profileName),
		ProviderKind: providerWizardProviderKind(provider),
		CatalogID:    provider.ID,
		BaseURL:      resolvedBaseURL,
		APIFormat:    providerWizardAPIFormat(provider),
		Model:        firstProviderDisplayValue(model, provider.DefaultModel),
	}
	// Catalog custom headers belong to the catalog endpoint: the resolver only
	// applies them when the base URL is the default, so a profile built against a
	// staging/proxy/custom override must not bake them in either — otherwise
	// attribution leaks to an arbitrary host.
	if sameProviderBaseURL(resolvedBaseURL, provider.DefaultBaseURL) {
		profile.CustomHeaders = maps.Clone(provider.CustomHeaders)
	}
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		profile.APIKey = apiKey
	} else if env := firstProviderDisplayValue(provider.AuthEnvVars...); provider.RequiresAuth && env != "" {
		// A blank credential step means the profile reads the catalog's env var
		// at runtime rather than storing a pasted key.
		profile.APIKeyEnv = env
	}
	return profile
}

func sameProviderBaseURL(left string, right string) bool {
	return strings.EqualFold(
		strings.TrimRight(strings.TrimSpace(left), "/"),
		strings.TrimRight(strings.TrimSpace(right), "/"),
	)
}

func providerWizardDisplayName(provider providercatalog.Descriptor, baseURL string, profileName string) string {
	if name := strings.TrimSpace(profileName); name != "" {
		return name
	}
	return provider.ID
}

func providerWizardProviderKind(provider providercatalog.Descriptor) config.ProviderKind {
	switch provider.Transport {
	case providercatalog.TransportOpenAI:
		return config.ProviderKindOpenAI
	case providercatalog.TransportAnthropic:
		return config.ProviderKindAnthropic
	case providercatalog.TransportAnthropicCompatible:
		return config.ProviderKindAnthropicCompat
	case providercatalog.TransportGoogle:
		return config.ProviderKindGoogle
	case providercatalog.TransportOpenAICompatible:
		return config.ProviderKindOpenAICompatible
	default:
		return config.ProviderKind(strings.ToLower(string(provider.Transport)))
	}
}

func providerWizardAPIFormat(provider providercatalog.Descriptor) string {
	if provider.Transport == providercatalog.TransportOpenAI || provider.Transport == providercatalog.TransportOpenAICompatible {
		return string(providercatalog.APIFormatOpenAIChatCompletions)
	}
	if len(provider.SupportedAPIFormats) == 0 {
		return ""
	}
	return string(provider.SupportedAPIFormats[0])
}
