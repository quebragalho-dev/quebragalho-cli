package tui

import (
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

const maxProviderWizardProvidersVisible = 10
const maxProviderWizardModelsVisible = 10
const providerWizardMinWidth = 48
const providerWizardProviderWidth = 64
const providerWizardMediumWidth = 86
const providerWizardModelWidth = 92

type providerWizardStep int

const (
	providerWizardStepMethod providerWizardStep = iota
	providerWizardStepProvider
	providerWizardStepManageKey
	providerWizardStepCredential
	providerWizardStepModel
	providerWizardStepDone
	// Manager steps (provider_manager.go): the list-first /provider surface.
	// They live on the same state struct so every overlay gate, key route, and
	// mouse hook that already checks m.providerWizard covers them for free.
	providerWizardStepManage
	providerWizardStepEditMenu
	providerWizardStepEditValue
)

// providerWizardMethodOption is a row in the "How do you want to connect?" step.
type providerWizardMethodOption struct {
	label    string
	subtitle string
}

// providerWizardMethodOptions returns the connect-method rows. QG authenticates
// with a plain API key, so key paste is the only path.
func providerWizardMethodOptions() []providerWizardMethodOption {
	return []providerWizardMethodOption{{
		label:    "Paste an API key / browse providers",
		subtitle: "Paste your " + providercatalog.KeyEnvHint + " and connect the Quebra Galho provider.",
	}}
}

type providerWizardModel struct {
	ID          string
	Description string
	Meta        string
}

type providerWizardState struct {
	step             providerWizardStep
	providers        []providercatalog.Descriptor
	selectedProvider int
	models           []providerWizardModel
	selectedModel    int
	providerSearch   string
	modelSearch      string
	baseURL          string
	profileName      string
	apiKey           string
	err              string
	modelSource      string
	modelLoading     bool
	modelLoadError   string
	discoveryToken   int
	selectedMethod   int
	// Manage-key step: shown when the selected provider already has a stored key.
	manageKeyCursor    int
	manageProviderName string
	// Manager state (provider_manager.go): the list-first /provider surface.
	manage           bool
	manageRows       []providerManagerRow
	manageCursor     int
	manageDeleting   bool
	manageStatus     string
	manageCredGen    int
	manageActiveName string
	// Edit state: field-level editor for one saved profile.
	editOriginal config.ProviderProfile
	editDraft    config.ProviderProfile
	editCursor   int
	editField    providerEditField
	editBuffer   string
}

func (m model) newProviderWizard() *providerWizardState {
	providers := providerWizardProviders()
	wizard := &providerWizardState{
		step:             providerWizardStepMethod,
		providers:        providers,
		selectedProvider: 0,
	}
	wizard.refreshModels()
	return wizard
}

func providerWizardProviders() []providercatalog.Descriptor {
	providers := []providercatalog.Descriptor{}
	for _, descriptor := range providercatalog.All() {
		if !providercatalog.RuntimeSupported(descriptor) {
			continue
		}
		providers = append(providers, descriptor)
	}
	return providers
}

func (wizard *providerWizardState) currentProvider() providercatalog.Descriptor {
	if wizard == nil {
		return providercatalog.Descriptor{}
	}
	if wizard.step == providerWizardStepProvider {
		providers := wizard.filteredProviders()
		if len(providers) == 0 {
			return providercatalog.Descriptor{}
		}
		wizard.selectedProvider = clampInt(wizard.selectedProvider, 0, len(providers)-1)
		return providers[wizard.selectedProvider]
	}
	if len(wizard.providers) == 0 {
		return providercatalog.Descriptor{}
	}
	wizard.selectedProvider = clampInt(wizard.selectedProvider, 0, len(wizard.providers)-1)
	return wizard.providers[wizard.selectedProvider]
}

func (wizard *providerWizardState) currentModel() providerWizardModel {
	if wizard == nil {
		return providerWizardModel{}
	}
	wizard.refreshModels()
	models := wizard.filteredModels()
	if len(models) == 0 {
		return providerWizardModel{Description: "no matching models"}
	}
	wizard.selectedModel = clampInt(wizard.selectedModel, 0, len(models)-1)
	return models[wizard.selectedModel]
}

func (wizard *providerWizardState) move(delta int) {
	if wizard == nil {
		return
	}
	switch wizard.step {
	case providerWizardStepMethod:
		options := providerWizardMethodOptions()
		if len(options) == 0 {
			return
		}
		wizard.selectedMethod = ((wizard.selectedMethod+delta)%len(options) + len(options)) % len(options)
	case providerWizardStepProvider:
		providers := wizard.filteredProviders()
		if len(providers) == 0 {
			return
		}
		wizard.selectedProvider = ((wizard.selectedProvider+delta)%len(providers) + len(providers)) % len(providers)
		wizard.selectedModel = 0
		wizard.modelSearch = ""
		wizard.apiKey = ""
		wizard.err = ""
		wizard.modelSource = ""
		wizard.modelLoading = false
		wizard.modelLoadError = ""
		wizard.refreshModels()
	case providerWizardStepModel:
		wizard.refreshModels()
		models := wizard.filteredModels()
		if len(models) == 0 {
			return
		}
		wizard.selectedModel = ((wizard.selectedModel+delta)%len(models) + len(models)) % len(models)
	}
}

func (wizard *providerWizardState) advance() {
	if wizard == nil {
		return
	}
	switch wizard.step {
	case providerWizardStepMethod:
		options := providerWizardMethodOptions()
		wizard.selectedMethod = clampInt(wizard.selectedMethod, 0, maxInt(0, len(options)-1))
		wizard.err = ""
		wizard.providers = providerWizardProviders()
		wizard.selectedProvider = 0
		wizard.refreshModels()
		wizard.step = providerWizardStepProvider
	case providerWizardStepProvider:
		if len(wizard.filteredProviders()) == 0 {
			return
		}
		// Resolve the selected provider's index in the full list before clearing
		// the search — selectedProvider is an index into the filtered slice, and
		// clearing the search swaps to the full list.
		if selected := wizard.currentProvider(); selected.ID != "" {
			for i, p := range wizard.providers {
				if p.ID == selected.ID {
					wizard.selectedProvider = i
					break
				}
			}
		}
		wizard.providerSearch = ""
		wizard.refreshModels()
		wizard.err = ""
		if providerWizardNeedsCredential(wizard.currentProvider()) {
			wizard.step = providerWizardStepCredential
		} else {
			wizard.step = providerWizardStepModel
		}
	case providerWizardStepCredential:
		wizard.err = ""
		wizard.step = providerWizardStepModel
	case providerWizardStepModel:
		wizard.err = ""
		if wizard.modelLoading {
			wizard.err = "Models are still loading."
			return
		}
		wizard.refreshModels()
		if len(wizard.filteredModels()) == 0 {
			wizard.err = "choose a matching model before continuing"
			return
		}
		wizard.step = providerWizardStepDone
	case providerWizardStepDone:
		wizard.step = providerWizardStepProvider
	}
}

func (wizard *providerWizardState) retreat() {
	if wizard == nil {
		return
	}
	wizard.err = ""
	switch wizard.step {
	case providerWizardStepProvider:
		wizard.step = providerWizardStepMethod
	case providerWizardStepCredential:
		wizard.step = providerWizardStepProvider
	case providerWizardStepModel:
		if providerWizardNeedsCredential(wizard.currentProvider()) {
			wizard.step = providerWizardStepCredential
		} else {
			wizard.step = providerWizardStepProvider
		}
	case providerWizardStepDone:
		wizard.step = providerWizardStepModel
	}
}

func (wizard *providerWizardState) refreshModels() {
	if wizard == nil {
		return
	}
	provider := wizard.currentProvider()
	if wizard.modelSource != "" && wizard.modelSource != "fallback" {
		wizard.selectedModel = clampInt(wizard.selectedModel, 0, maxInt(0, len(wizard.models)-1))
		return
	}
	models := providerWizardModelOptions(provider)
	if sameProviderWizardModels(wizard.models, models) {
		wizard.selectedModel = clampInt(wizard.selectedModel, 0, maxInt(0, len(models)-1))
		return
	}
	wizard.models = models
	wizard.selectedModel = 0
	wizard.modelSource = "fallback"
}

func sameProviderWizardModels(a, b []providerWizardModel) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index].ID != b[index].ID {
			return false
		}
	}
	return true
}

func providerWizardNeedsCredential(provider providercatalog.Descriptor) bool {
	return provider.RequiresAuth && !provider.Local && len(provider.AuthEnvVars) > 0
}
func (m model) handleProviderWizardKey(msg tea.KeyMsg) (model, tea.Cmd) {
	if m.providerWizard == nil {
		return m, nil
	}
	// A manager-launched add flow honors the manager's "Esc walks back one
	// level" contract on EVERY step (not just the first): Esc returns to the
	// provider list instead of destroying the overlay — and with it the user's
	// cursor and status — from a step deep in the flow.
	if m.providerWizard.manage && !m.providerWizard.managerStep() && keyIs(msg, tea.KeyEsc) {
		wizard := m.providerWizard
		wizard.err = ""
		wizard.step = providerWizardStepManage
		return m, nil
	}
	// Manager steps (list / edit) own their keys entirely.
	if m.providerWizard.managerStep() {
		return m.handleProviderManagerKey(msg)
	}
	// Manage-key step: keep/replace/remove a provider's stored key.
	if m.providerWizard.step == providerWizardStepManageKey {
		switch {
		case keyIs(msg, tea.KeyEsc) || keyIs(msg, tea.KeyLeft):
			m.providerWizard.step = providerWizardStepProvider
			m.providerWizard.err = ""
			return m, nil
		case keyIs(msg, tea.KeyUp):
			m.providerWizard.manageKeyCursor = (m.providerWizard.manageKeyCursor + 2) % 3
			return m, nil
		case keyIs(msg, tea.KeyDown) || keyIs(msg, tea.KeyTab):
			m.providerWizard.manageKeyCursor = (m.providerWizard.manageKeyCursor + 1) % 3
			return m, nil
		case keyIs(msg, tea.KeyEnter):
			return m.applyManageKeyChoice()
		}
		return m, nil
	}
	if m.providerWizard.step == providerWizardStepProvider {
		switch {
		case keyText(msg) != "":
			m.providerWizard.appendProviderSearch(keyRunes(msg))
			return m, nil
		case keyBackspace(msg):
			m.providerWizard.deleteProviderSearchRune()
			return m, nil
		case keyCtrl(msg, 'u'):
			m.providerWizard.providerSearch = ""
			m.providerWizard.selectedProvider = 0
			return m, nil
		}
	}
	if m.providerWizard.step == providerWizardStepCredential {
		switch {
		case keyIs(msg, tea.KeyEsc):
			m.providerWizard = nil
			return m, nil
		case keyText(msg) != "":
			m.providerWizard.appendAPIKey(keyRunes(msg))
			return m, nil
		case keyBackspace(msg):
			m.providerWizard.deleteAPIKeyRune()
			return m, nil
		case keyCtrl(msg, 'u'):
			m.providerWizard.apiKey = ""
			return m, nil
		case keyIs(msg, tea.KeyLeft):
			m.providerWizard.retreat()
			return m, nil
		case keyIs(msg, tea.KeyRight):
			if m.providerWizard.canAdvanceWithRight() {
				return m.advanceProviderWizard()
			}
			return m, nil
		case keyIs(msg, tea.KeyEnter):
			return m.advanceProviderWizard()
		}
		return m, nil
	}
	if m.providerWizard.step == providerWizardStepModel {
		switch {
		case keyText(msg) != "":
			m.providerWizard.appendModelSearch(keyRunes(msg))
			return m, nil
		case keyBackspace(msg):
			m.providerWizard.deleteModelSearchRune()
			return m, nil
		case keyCtrl(msg, 'u'):
			m.providerWizard.modelSearch = ""
			m.providerWizard.selectedModel = 0
			return m, nil
		}
	}
	switch {
	case keyIs(msg, tea.KeyEsc):
		m.providerWizard = nil
	case keyIs(msg, tea.KeyUp):
		m.providerWizard.move(-1)
	case keyIs(msg, tea.KeyDown) || keyIs(msg, tea.KeyTab):
		m.providerWizard.move(1)
	case keyIs(msg, tea.KeyLeft):
		m.providerWizard.retreat()
	case keyIs(msg, tea.KeyRight):
		if m.providerWizard.canAdvanceWithRight() {
			return m.advanceProviderWizard()
		}
	case keyIs(msg, tea.KeyEnter):
		if m.providerWizard.step == providerWizardStepDone {
			return m.applyProviderWizard()
		}
		return m.advanceProviderWizard()
	}
	return m, nil
}

func (m model) handleProviderWizardPaste(content string) (model, tea.Cmd) {
	if m.providerWizard == nil {
		return m, nil
	}
	switch m.providerWizard.step {
	case providerWizardStepProvider:
		m.providerWizard.appendProviderSearch([]rune(content))
	case providerWizardStepCredential:
		m.providerWizard.appendAPIKey([]rune(content))
	case providerWizardStepModel:
		m.providerWizard.appendModelSearch([]rune(content))
	case providerWizardStepEditValue:
		for _, r := range content {
			if r == '\n' || r == '\r' || r == '\t' {
				continue
			}
			m.providerWizard.editBuffer += string(r)
		}
	}
	return m, nil
}

func (wizard *providerWizardState) canAdvanceWithRight() bool {
	if wizard == nil {
		return false
	}
	switch wizard.step {
	case providerWizardStepMethod:
		return len(providerWizardMethodOptions()) > 0
	case providerWizardStepProvider:
		return strings.TrimSpace(wizard.currentProvider().ID) != ""
	case providerWizardStepCredential:
		return wizard.credentialReadyForRight()
	case providerWizardStepModel:
		if wizard.modelLoading {
			return false
		}
		wizard.refreshModels()
		return len(wizard.filteredModels()) > 0
	default:
		return false
	}
}

func (wizard *providerWizardState) credentialReadyForRight() bool {
	if strings.TrimSpace(wizard.apiKey) != "" {
		return true
	}
	provider := wizard.currentProvider()
	if !providerWizardNeedsCredential(provider) {
		return true
	}
	for _, env := range provider.AuthEnvVars {
		if strings.TrimSpace(os.Getenv(strings.TrimSpace(env))) != "" {
			return true
		}
	}
	return false
}

func (wizard *providerWizardState) appendAPIKey(runes []rune) {
	for _, r := range runes {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			continue
		}
		wizard.apiKey += string(r)
	}
	wizard.err = ""
}

func (wizard *providerWizardState) deleteAPIKeyRune() {
	if wizard.apiKey == "" {
		return
	}
	runes := []rune(wizard.apiKey)
	wizard.apiKey = string(runes[:len(runes)-1])
	wizard.err = ""
}
func (wizard *providerWizardState) appendModelSearch(runes []rune) {
	for _, r := range runes {
		if unicode.IsControl(r) {
			continue
		}
		wizard.modelSearch += string(r)
	}
	wizard.selectedModel = 0
}

func (wizard *providerWizardState) deleteModelSearchRune() {
	if wizard.modelSearch == "" {
		return
	}
	runes := []rune(wizard.modelSearch)
	wizard.modelSearch = string(runes[:len(runes)-1])
	wizard.selectedModel = 0
}

func (wizard *providerWizardState) appendProviderSearch(runes []rune) {
	for _, r := range runes {
		if unicode.IsControl(r) {
			continue
		}
		wizard.providerSearch += string(r)
	}
	wizard.selectedProvider = 0
}

func (wizard *providerWizardState) deleteProviderSearchRune() {
	if wizard.providerSearch == "" {
		return
	}
	runes := []rune(wizard.providerSearch)
	wizard.providerSearch = string(runes[:len(runes)-1])
	wizard.selectedProvider = 0
}

func (wizard *providerWizardState) filteredProviders() []providercatalog.Descriptor {
	if wizard == nil {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(wizard.providerSearch))
	if query == "" {
		return append([]providercatalog.Descriptor{}, wizard.providers...)
	}
	providers := make([]providercatalog.Descriptor, 0, len(wizard.providers))
	for _, provider := range wizard.providers {
		if providerMatchesQuery(provider, query) {
			providers = append(providers, provider)
		}
	}
	return providers
}

func providerMatchesQuery(provider providercatalog.Descriptor, query string) bool {
	if query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{provider.ID, provider.Name, strings.Join(provider.Aliases, " ")}, " "))
	return strings.Contains(haystack, query)
}

func (m model) applyProviderWizard() (model, tea.Cmd) {
	wizard := m.providerWizard
	if wizard == nil {
		return m, nil
	}
	provider := wizard.currentProvider()
	modelChoice := wizard.currentModel()
	profile := providerWizardProfile(provider, modelChoice.ID, wizard.apiKey, wizard.baseURL, wizard.profileName)
	runtimeProfile := providerWizardRuntimeProfile(profile)

	// Build and persist into LOCALS first, committing live state only once BOTH
	// succeed. A persist failure (read-only config, disk full) must not leave the
	// chat running on the new provider while the status line, m.providerProfile,
	// and the QG_PROVIDER export (which pins spawned children) still point at
	// the old one — parent and children would silently run on different providers.
	var nextProvider qgruntime.Provider
	if m.newProvider != nil {
		built, err := m.newProvider(runtimeProfile)
		if err != nil {
			wizard.err = redaction.RedactString(err.Error(), redaction.Options{ExtraSecretValues: []string{profile.APIKey, runtimeProfile.APIKey}})
			return m, nil
		}
		nextProvider = built
	}
	keyStoreFellBack := false
	if strings.TrimSpace(m.userConfigPath) != "" {
		// Capture flip: move the freshly entered key into the encrypted credential
		// store before persisting, so config.json never holds the cleartext. The
		// provider was already built above from runtimeProfile, which has the key.
		secret := profile.APIKey
		profile, keyStoreFellBack = config.SecureProviderProfile(profile, m.userConfigPath)
		if _, err := config.UpsertProvider(m.userConfigPath, profile, true); err != nil {
			wizard.err = redaction.RedactString(err.Error(), redaction.Options{ExtraSecretValues: []string{secret, profile.APIKey}})
			return m, nil // nothing committed to live state yet
		}
	}

	// Both succeeded — commit the live provider, profile, model, and the child
	// env export together, so they can never disagree.
	if nextProvider != nil {
		m.provider = nextProvider
	}
	m.providerProfile = profile
	m.providerName = profile.Name
	m.modelName = profile.Model
	m.streamUsage = qgruntime.Usage{}
	// Keep the in-memory saved list in sync so the provider manager and /model
	// picker show the new profile without a restart.
	m.savedProviders = upsertSavedProviderProfile(m.savedProviders, profile)
	// Keep sub-agent child processes on the same provider we just switched to —
	// same as the /model and /provider switch paths (command_center.go). Without
	// this, a QG_PROVIDER exported by an earlier switch stays pointing at the
	// OLD provider and wins over config in every spawned child (applyEnv), so
	// specialists/swarm members run on the wrong provider's credentials.
	config.SetActiveProviderEnv(profile.Name)
	m.providerWizard = nil
	if keyStoreFellBack {
		m = m.showTransientNoticeInline("Credential store unavailable — API key saved in plaintext in "+m.userConfigPath, transientNoticeWarning)
	}
	return m, nil
}

// wizardProviderStoredKey reports the saved provider name that has a key in the
// credential store matching the wizard-selected descriptor, so the wizard can offer
// keep/replace/remove instead of forcing a new key entry.
func (m model) wizardProviderStoredKey(provider providercatalog.Descriptor) (string, bool) {
	for _, profile := range m.savedProviders {
		if !profile.APIKeyStored {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(profile.Name), strings.TrimSpace(provider.Name)) ||
			strings.EqualFold(strings.TrimSpace(profile.CatalogID), strings.TrimSpace(provider.ID)) ||
			strings.EqualFold(strings.TrimSpace(profile.Name), strings.TrimSpace(provider.ID)) {
			return profile.Name, true
		}
	}
	return "", false
}

// applyManageKeyChoice acts on the keep/replace/remove selection. Keep closes the
// wizard (nothing changes); Replace routes to credential entry (overwrites on save);
// Remove deletes the stored key and its marker.
func (m model) applyManageKeyChoice() (model, tea.Cmd) {
	wizard := m.providerWizard
	if wizard == nil {
		return m, nil
	}
	name := strings.TrimSpace(wizard.manageProviderName)
	switch wizard.manageKeyCursor {
	case 1: // Replace
		wizard.apiKey = ""
		wizard.err = ""
		wizard.step = providerWizardStepCredential
		return m, nil
	case 2: // Remove
		if strings.TrimSpace(m.userConfigPath) != "" {
			if store, err := config.ProviderKeyStoreAt(filepath.Dir(m.userConfigPath)); err == nil {
				_, _ = store.Delete(name)
			}
			_, _ = config.ClearProviderKeyStored(m.userConfigPath, name)
		} else {
			_, _ = config.ForgetProviderKey(name)
		}
		m.providerWizard = nil
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "Provider\nRemoved the stored key for " + name + ". Re-add it any time with /provider."})
		return m, nil
	default: // Keep
		m.providerWizard = nil
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "Provider\nKept the saved key for " + name + "."})
		return m, nil
	}
}

func providerWizardRuntimeProfile(profile config.ProviderProfile) config.ProviderProfile {
	runtimeProfile := profile
	if strings.TrimSpace(runtimeProfile.APIKey) == "" && strings.TrimSpace(runtimeProfile.APIKeyEnv) != "" {
		runtimeProfile.APIKey = strings.TrimSpace(os.Getenv(runtimeProfile.APIKeyEnv))
	}
	return runtimeProfile
}

func (m model) providerWizardOverlay(width int) string {
	if m.providerWizard == nil {
		return ""
	}
	return m.providerWizard.render(width, m.spinnerGlyph())
}

// spinner is the shared global run spinner (spinnerGlyph()), same as everywhere.
func (wizard *providerWizardState) render(width int, spinner string) string {
	if wizard == nil {
		return ""
	}
	overlayWidth := providerWizardOverlayWidth(width, wizard.step)
	innerWidth := maxInt(20, overlayWidth-4)

	lines := []string{
		qgTheme.faint.Render(providerWizardStepLine(wizard)),
		qgTheme.line.Render(strings.Repeat("─", innerWidth)),
	}
	if wizard.err != "" {
		lines = append(lines, qgTheme.red.Render("error: "+wizard.err), "")
	}
	switch wizard.step {
	case providerWizardStepMethod:
		lines = append(lines, wizard.renderMethodStep(innerWidth)...)
	case providerWizardStepProvider:
		lines = append(lines, wizard.renderProviderStep(innerWidth)...)
	case providerWizardStepManageKey:
		lines = append(lines, wizard.renderManageKeyStep(innerWidth)...)
	case providerWizardStepCredential:
		lines = append(lines, wizard.renderCredentialStep(innerWidth)...)
	case providerWizardStepModel:
		lines = append(lines, wizard.renderModelStep(innerWidth)...)
	case providerWizardStepDone:
		lines = append(lines, wizard.renderDoneStep(innerWidth)...)
	case providerWizardStepManage:
		lines = append(lines, wizard.renderManageStep(innerWidth)...)
	case providerWizardStepEditMenu:
		lines = append(lines, wizard.renderEditMenuStep(innerWidth)...)
	case providerWizardStepEditValue:
		lines = append(lines, wizard.renderEditValueStep(innerWidth)...)
	}
	lines = append(lines,
		qgTheme.line.Render(strings.Repeat("─", innerWidth)),
		qgTheme.faint.Render(wizard.footer()),
	)

	title := "Provider setup"
	if wizard.managerStep() {
		title = "Providers"
	}
	block := styledBlockFillTitle(overlayWidth, title, lines, qgTheme.lineStrong, lipgloss.NewStyle())
	if width > overlayWidth {
		return indentBlock(block, (width-overlayWidth)/2)
	}
	return block
}

func (wizard *providerWizardState) footer() string {
	text := wizard.footerText()
	// Inside a manager-launched add flow, Esc walks back to the provider list
	// (see handleProviderWizardKey) — the hint must match.
	if wizard.manage && !wizard.managerStep() {
		text = strings.ReplaceAll(text, "Esc close", "Esc back")
	}
	return text
}

func (wizard *providerWizardState) footerText() string {
	canRight := wizard.canAdvanceWithRight()
	switch wizard.step {
	case providerWizardStepManage:
		if wizard.manageDeleting {
			return "Enter/y delete   Esc/n cancel"
		}
		if len(wizard.manageRows) == 0 {
			return "a add   Esc close"
		}
		return "↑/↓ move   Enter activate   a add   e edit   d delete   Esc close"
	case providerWizardStepEditMenu:
		return "↑/↓ move   Enter edit field / save   Esc back"
	case providerWizardStepEditValue:
		return "type to edit   Enter apply   Ctrl+U clear   Esc back"
	case providerWizardStepMethod:
		return "↑/↓ move   Enter/→ continue   Esc close"
	case providerWizardStepProvider:
		if canRight {
			return "↑/↓ move   Enter/→ continue   ← back   Esc close"
		}
		return "↑/↓ move   Enter continue   ← back   Esc close"
	case providerWizardStepManageKey:
		return "↑/↓ move   Enter select   Esc back"
	case providerWizardStepModel:
		if canRight {
			return "↑/↓ move   Enter/→ continue   ← back   Esc close"
		}
		return "↑/↓ move   Enter continue   ← back   Esc close"
	case providerWizardStepDone:
		return "Enter save   ← back   Esc close"
	default:
		if canRight {
			return "Enter/→ continue   ← back   Esc close"
		}
		return "Enter continue   ← back   Esc close"
	}
}

func providerWizardOverlayWidth(width int, step providerWizardStep) int {
	if width <= 0 {
		return providerWizardProviderWidth
	}
	target := providerWizardMediumWidth
	switch step {
	case providerWizardStepProvider:
		target = providerWizardProviderWidth
	case providerWizardStepModel, providerWizardStepManage:
		target = providerWizardModelWidth
	}
	target = minInt(target, width)
	if target < providerWizardMinWidth {
		return width
	}
	return target
}

func providerWizardStepLine(wizard *providerWizardState) string {
	if wizard == nil {
		return ""
	}
	if wizard.managerStep() {
		switch wizard.step {
		case providerWizardStepManage:
			return pluralCount(len(wizard.manageRows), "saved provider")
		default:
			return "edit provider"
		}
	}
	step := wizard.step
	type stepLabel struct {
		step  providerWizardStep
		label string
	}
	steps := []stepLabel{
		{providerWizardStepMethod, "1 method"},
		{providerWizardStepProvider, "2 provider"},
	}
	switch {
	default:
		steps = append(steps,
			stepLabel{providerWizardStepCredential, "3 key"},
			stepLabel{providerWizardStepModel, "4 model"},
			stepLabel{providerWizardStepDone, "5 ready"},
		)
	}
	parts := make([]string, 0, len(steps))
	for _, item := range steps {
		if item.step == step {
			parts = append(parts, "["+item.label+"]")
		} else {
			parts = append(parts, item.label)
		}
	}
	return strings.Join(parts, "  ")
}

// renderMethodStep renders the "How do you want to connect?" chooser.
func (wizard *providerWizardState) renderMethodStep(width int) []string {
	options := providerWizardMethodOptions()
	wizard.selectedMethod = clampInt(wizard.selectedMethod, 0, maxInt(0, len(options)-1))
	lines := []string{qgTheme.accent.Render("How do you want to connect?")}
	for index, option := range options {
		surface := transparentSurface
		marker := surface(qgTheme.faintest).Render("  ")
		if index == wizard.selectedMethod {
			surface = qgTheme.onSel
			marker = surface(qgTheme.accent).Render("❯ ")
		}
		lines = append(lines, fitStyledLine(marker+surface(qgTheme.ink).Render(option.label), width))
		lines = append(lines, fitStyledLine("    "+qgTheme.faint.Render(option.subtitle), width))
	}
	return lines
}

// renderManageKeyStep shows keep/replace/remove for a provider that already has a
// key in the encrypted credential store.
func (wizard *providerWizardState) renderManageKeyStep(width int) []string {
	name := strings.TrimSpace(wizard.manageProviderName)
	if name == "" {
		name = wizard.currentProvider().Name
	}
	options := []struct{ label, subtitle string }{
		{"Keep current key", "A key is already saved (encrypted) for " + name + "."},
		{"Replace key", "Enter a new key; it overwrites the stored one."},
		{"Remove key", "Delete the stored key for " + name + "."},
	}
	wizard.manageKeyCursor = clampInt(wizard.manageKeyCursor, 0, len(options)-1)
	lines := []string{qgTheme.accent.Render(name + " already has a saved key")}
	for index, option := range options {
		surface := transparentSurface
		marker := surface(qgTheme.faintest).Render("  ")
		if index == wizard.manageKeyCursor {
			surface = qgTheme.onSel
			marker = surface(qgTheme.accent).Render("❯ ")
		}
		lines = append(lines, fitStyledLine(marker+surface(qgTheme.ink).Render(option.label), width))
		lines = append(lines, fitStyledLine("    "+qgTheme.faint.Render(option.subtitle), width))
	}
	return lines
}

func (wizard *providerWizardState) renderProviderStep(width int) []string {
	header := "Choose provider"
	lines := []string{qgTheme.accent.Render(header)}
	lines = append(lines, wizard.renderProviderSearch(width))
	providers := wizard.filteredProviders()
	if len(providers) == 0 {
		lines = append(lines, qgTheme.faint.Render("  no matching providers"))
		return lines
	}
	maxVisible := minInt(maxProviderWizardProvidersVisible, len(providers))
	start := selectableListStart(len(providers), maxVisible, wizard.selectedProvider)
	for offset, provider := range providers[start : start+maxVisible] {
		lines = append(lines, wizard.renderSelectableProvider(width, start+offset, provider))
	}
	return lines
}

func (wizard *providerWizardState) renderProviderSearch(width int) string {
	query := strings.TrimSpace(wizard.providerSearch)
	return providerWizardInputLine("search > ", query, "provider name, id, or alias...", width)
}

func (wizard *providerWizardState) renderSelectableProvider(width int, index int, provider providercatalog.Descriptor) string {
	selected := index == wizard.selectedProvider
	surface := transparentSurface
	marker := surface(qgTheme.faintest).Render("  ")
	if selected {
		surface = qgTheme.onSel
		marker = surface(qgTheme.accent).Render("❯ ")
	}
	name := provider.Name
	if provider.Recommended {
		name = "★ " + name
	}
	left := marker + surface(qgTheme.ink).Render(name)
	if badge := providerWizardBadge(provider); badge != "" {
		left += surface(qgTheme.faint).Render("   " + badge)
	}
	return fitStyledLine(left, width)
}

// providerWizardBadge is the faint hint shown next to a provider row.
func providerWizardBadge(provider providercatalog.Descriptor) string {
	if provider.Recommended {
		return "(recommended)"
	}
	return ""
}
func (wizard *providerWizardState) renderCredentialStep(width int) []string {
	provider := wizard.currentProvider()

	env := firstProviderDisplayValue(provider.AuthEnvVars...)
	value := qgTheme.accent.Render("▌") + qgTheme.faint.Render("paste key here")
	if wizard.apiKey != "" {
		value = qgTheme.ink.Render(maskedProviderWizardKey(wizard.apiKey)) + qgTheme.accent.Render("▌")
	}
	input := qgTheme.userPrompt.Render("api key > ") + value
	lines := []string{
		qgTheme.accent.Render("Paste API key"),
		qgTheme.ink.Render(providerWizardCredentialInstruction(env)),
		input,
		qgTheme.faint.Render("Pasted keys are hidden and saved in your user config."),
	}
	return lines
}

func providerWizardCredentialInstruction(env string) string {
	if env = strings.TrimSpace(env); env != "" {
		return "Paste a key, or leave blank to use " + env + "."
	}
	return "Paste a key, or leave blank to use your shell env."
}

func (wizard *providerWizardState) renderModelStep(width int) []string {
	if wizard.modelLoading {
		return wizard.renderModelLoadingStep(width)
	}
	lines := []string{qgTheme.accent.Render("Choose a model")}
	if status := wizard.modelStatusText(); status != "" {
		lines = append(lines, qgTheme.faint.Render(status))
	}
	lines = append(lines, wizard.renderModelSearch(width))
	wizard.refreshModels()
	models := wizard.filteredModels()
	if len(models) == 0 {
		lines = append(lines, qgTheme.faint.Render("  no matching models"))
		return lines
	}
	maxVisible := minInt(maxProviderWizardModelsVisible, len(models))
	wizard.selectedModel = clampInt(wizard.selectedModel, 0, len(models)-1)
	start := selectableListStart(len(models), maxVisible, wizard.selectedModel)
	for offset, model := range models[start : start+maxVisible] {
		lines = append(lines, wizard.renderSelectableModel(width, start+offset, model))
	}
	if detail := providerWizardModelDetail(wizard.currentModel()); detail != "" {
		lines = append(lines, fitStyledLine(qgTheme.faint.Render("  "+detail), width))
	}
	return lines
}

func (wizard *providerWizardState) renderModelLoadingStep(width int) []string {
	return []string{
		qgTheme.accent.Render("Choose a model"),
		"",
		fitStyledLine(qgTheme.faint.Render("Checking available models..."), width),
		fitStyledLine(qgTheme.faint.Render("Built-in models will be used if discovery fails."), width),
	}
}

func (wizard *providerWizardState) renderModelSearch(width int) string {
	query := strings.TrimSpace(wizard.modelSearch)
	return providerWizardInputLine("search > ", query, "model name...", width)
}

func providerWizardInputLine(promptText string, value string, placeholder string, width int) string {
	prompt := qgTheme.userPrompt.Render("search > ")
	cursor := qgTheme.accent.Render("▌")
	if promptText != "" {
		prompt = qgTheme.userPrompt.Render(promptText)
	}
	if value == "" {
		return fitStyledLine(prompt+cursor+qgTheme.faint.Render(placeholder), width)
	}
	return fitStyledLine(prompt+qgTheme.ink.Render(value)+cursor, width)
}
func (wizard *providerWizardState) modelStatusText() string {
	if wizard.modelLoadError != "" {
		return "Using built-in model list"
	}
	return ""
}

func (wizard *providerWizardState) renderSelectableModel(width int, index int, model providerWizardModel) string {
	selected := index == wizard.selectedModel
	surface := transparentSurface
	marker := surface(qgTheme.faintest).Render("  ")
	if selected {
		surface = qgTheme.onSel
		marker = surface(qgTheme.accent).Render("❯ ")
	}
	left := marker + surface(qgTheme.ink).Render(model.displayLabel())
	return fitStyledLine(left, width)
}

func providerWizardModelDetail(model providerWizardModel) string {
	parts := []string{}
	if secondary := strings.TrimSpace(model.secondaryText()); secondary != "" && !providerWizardGenericModelDescription(secondary) {
		parts = append(parts, secondary)
	}
	if meta := strings.TrimSpace(model.Meta); meta != "" {
		parts = append(parts, meta)
	}
	return strings.Join(parts, " · ")
}

func (wizard *providerWizardState) filteredModels() []providerWizardModel {
	if wizard == nil {
		return nil
	}
	query := strings.ToLower(strings.TrimSpace(wizard.modelSearch))
	if query == "" {
		return append([]providerWizardModel{}, wizard.models...)
	}
	models := make([]providerWizardModel, 0, len(wizard.models))
	for _, model := range wizard.models {
		if model.matches(query) {
			models = append(models, model)
		}
	}
	return models
}

// A model Description is only a good row label when it reads like a display
// NAME — "Grok 4.3" for x-ai/grok-4.3 — which is what the field is for, and why
// the row prefers it over an ugly raw ID.
//
// Providers do not all honour that. Aggregators commonly return a prose blurb
// instead ("Fast DeepSeek model for efficient chat, coding help, and agent
// loops"), which fails as a label twice over: it overflows the row, and because
// such blurbs are written per model FAMILY rather than per model, several
// DIFFERENT models collapse to byte-identical rows. A picker then offers two
// visually identical entries with the distinguishing ID visible only in the
// detail line of whichever one is highlighted.
//
// providerWizardGenericModelDescription cannot catch these: it rejects
// descriptions that are vague ("live model", "catalog default"), whereas these
// are specific — just shared and sentence-shaped. So the test is shape, not
// wording: a name is short and few-worded; anything longer is prose, and the ID
// (unique by construction) makes the better label.
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

func (wizard *providerWizardState) renderDoneStep(width int) []string {
	provider := wizard.currentProvider()
	model := wizard.currentModel()
	lines := []string{
		qgTheme.accent.Render("Ready to connect"),
		"",
		qgTheme.ink.Render("Provider    " + provider.Name),
	}
	lines = append(lines,
		qgTheme.ink.Render("Name        "+providerWizardDisplayName(provider, wizard.baseURL, wizard.profileName)),
		qgTheme.ink.Render("Model       "+model.ID),
		qgTheme.ink.Render("Credential  "+providerWizardCredentialLabel(provider, wizard.apiKey)),
		"",
		qgTheme.faint.Render("Press Enter to save and start using this provider."),
	)
	return lines
}

func providerWizardCredentialLabel(provider providercatalog.Descriptor, apiKey string) string {
	if strings.TrimSpace(apiKey) != "" {
		return "pasted key"
	}
	if env := firstProviderDisplayValue(provider.AuthEnvVars...); provider.RequiresAuth && env != "" {
		return env + " env var"
	}
	return "not required"
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

// sameProviderBaseURL reports whether two base URLs address the same endpoint,
// normalizing case and a trailing slash — mirroring the resolver's sameBaseURL so
// the wizard bakes catalog headers on exactly the endpoints the resolver would.
func sameProviderBaseURL(left string, right string) bool {
	return strings.EqualFold(
		strings.TrimRight(strings.TrimSpace(left), "/"),
		strings.TrimRight(strings.TrimSpace(right), "/"),
	)
}

// providerWizardEndpointError validates a Base URL typed into the /provider
// edit field (or anywhere else an endpoint is entered). HTTPS is required
// unless the host is loopback or a private network address.
func providerWizardEndpointError(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "enter an endpoint URL before continuing"
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "enter a valid endpoint URL including https://"
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "endpoint URL must start with http:// or https://"
	}
	if parsed.Scheme == "http" && !providerWizardIsLoopbackHost(parsed.Hostname()) {
		return "endpoint URL must use https:// unless it is local loopback or a private network address"
	}
	return ""
}

func providerWizardIsLoopbackHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
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
