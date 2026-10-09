package tui

import (
	"context"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

type providerModelsDiscoveredMsg struct {
	providerID string
	token      int
	models     []providermodeldiscovery.Model
	err        error
	// secrets are redacted from any surfaced error (credentials used to
	// authenticate discovery, which must never be logged or shown).
	secrets []string
}

func (m model) advanceProviderWizard() (model, tea.Cmd) {
	if m.providerWizard == nil {
		return m, nil
	}
	// A provider that already has a key in the credential store: offer
	// keep/replace/remove before re-entering credentials.
	if m.providerWizard.step == providerWizardStepProvider {
		if name, ok := m.wizardProviderStoredKey(m.providerWizard.currentProvider()); ok {
			m.providerWizard.manageProviderName = name
			m.providerWizard.manageKeyCursor = 0
			m.providerWizard.err = ""
			m.providerWizard.step = providerWizardStepManageKey
			return m, nil
		}
	}
	previous := m.providerWizard.step
	m.providerWizard.advance()
	if m.providerWizard.step == providerWizardStepModel && previous != providerWizardStepModel {
		return m, m.providerModelDiscoveryCmd()
	}
	return m, nil
}

func (m model) providerModelDiscoveryCmd() tea.Cmd {
	wizard := m.providerWizard
	if wizard == nil {
		return nil
	}
	provider := wizard.currentProvider()
	if !providerWizardCatalogDiscoveryAllowed(provider) {
		return nil
	}
	pastedKey := wizard.apiKey
	discover := m.discoverProviderModels
	if discover == nil {
		discover = func(ctx context.Context, profile config.ProviderProfile) ([]providermodeldiscovery.Model, error) {
			return providermodeldiscovery.DiscoverCatalog(ctx, provider, profile, providermodeldiscovery.Options{})
		}
	}

	wizard.modelLoading = true
	wizard.modelLoadError = ""
	wizard.discoveryToken++
	token := wizard.discoveryToken
	providerID := provider.ID
	baseURL := wizard.baseURL
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 12*time.Second)
		defer cancel()
		profile := providerWizardDiscoveryProfile(provider, pastedKey, baseURL)
		models, err := discover(ctx, profile)
		return providerModelsDiscoveredMsg{providerID: providerID, token: token, models: models, err: err, secrets: []string{pastedKey, profile.APIKey}}
	}
}

func (m model) applyProviderModelsDiscovered(msg providerModelsDiscoveredMsg) model {
	wizard := m.providerWizard
	if wizard == nil || wizard.step != providerWizardStepModel || wizard.currentProvider().ID != msg.providerID || msg.token != wizard.discoveryToken {
		return m
	}
	wizard.modelLoading = false
	if msg.err != nil {
		wizard.modelLoadError = redaction.RedactString(msg.err.Error(), redaction.Options{ExtraSecretValues: append([]string{wizard.apiKey}, msg.secrets...)})
		wizard.modelSource = "fallback"
		wizard.refreshModels()
		return m
	}
	models := providerWizardModelsFromDiscovery(msg.models)
	if len(models) == 0 {
		wizard.modelLoadError = "models endpoint returned no model ids"
		wizard.modelSource = "fallback"
		wizard.refreshModels()
		return m
	}
	wizard.models = models
	wizard.selectedModel = 0
	wizard.modelSource = providerWizardModelsSource(msg.models)
	if wizard.modelSource == "" {
		wizard.modelSource = "live"
	}
	wizard.modelLoadError = ""
	return m
}

func providerWizardModelsFromDiscovery(models []providermodeldiscovery.Model) []providerWizardModel {
	result := make([]providerWizardModel, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		result = append(result, providerWizardModel{
			ID:          id,
			Description: firstProviderDisplayValue(model.Description, "live model"),
			Meta:        providerWizardModelMeta(model.ContextWindow, model.ToolCall, model.Reasoning, model.InputCost, model.OutputCost, model.Tags),
		})
	}
	return result
}

func providerWizardModelsSource(models []providermodeldiscovery.Model) string {
	for _, model := range models {
		if source := strings.TrimSpace(model.Source); source != "" {
			return source
		}
	}
	return ""
}

func providerWizardDiscoveryProfile(provider providercatalog.Descriptor, apiKey string, baseURL string) config.ProviderProfile {
	profile := providerWizardProfile(provider, provider.DefaultModel, apiKey, baseURL, "")
	if strings.TrimSpace(profile.APIKey) == "" && strings.TrimSpace(profile.APIKeyEnv) != "" {
		profile.APIKey = strings.TrimSpace(os.Getenv(profile.APIKeyEnv))
	}
	return profile
}

func providerWizardCatalogDiscoveryAllowed(provider providercatalog.Descriptor) bool {
	return providercatalog.RuntimeSupported(provider)
}
