package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

type setupStage int

const (
	setupStageWelcome setupStage = iota
	setupStageMethod
	setupStageProvider
	setupStageCredentials
	setupStageModel
	setupStageSafety
	setupStageReady
)

type setupState struct {
	visible        bool
	required       bool
	configPath     string
	providers      []SetupProviderOption // active list
	allProviders   []SetupProviderOption // full catalog
	selected       int
	selectedMethod int
	stage          setupStage
	err            string
	apiKey         textinput.Model
	models         []providerWizardModel
	modelIndex     int
	modelQuery     string
	modelForID     string
	modelLoad      bool
	modelErr       string
	modelSrc       string
	modelGen       uint64
}

type setupModelsDiscoveredMsg struct {
	providerID       string
	gen              uint64
	redactionSecrets []string
	models           []providermodeldiscovery.Model
	err              error
}

func newSetupState(options SetupOptions) setupState {
	providers := append([]SetupProviderOption{}, options.Providers...)
	if len(providers) == 0 {
		providers = []SetupProviderOption{{
			ID:           "openai",
			Name:         "OpenAI",
			DefaultModel: "gpt-4.1",
			EnvVar:       "OPENAI_API_KEY",
			RequiresAuth: true,
		}}
	}
	apiKey := textinput.New()
	apiKey.Prompt = ""
	apiKey.Placeholder = "paste key or leave blank"
	apiKey.EchoMode = textinput.EchoPassword
	apiKey.EchoCharacter = '*'
	// Bubble's Ctrl+V binding reads the clipboard itself. Keep it disabled so
	// terminal bracketed paste (Paste: true) is the single paste path.
	apiKey.KeyMap.Paste.SetEnabled(false)
	apiKey.Focus()
	return setupState{
		visible:      options.Visible,
		required:     options.Required,
		configPath:   strings.TrimSpace(options.ConfigPath),
		providers:    providers,
		allProviders: providers,
		apiKey:       apiKey,
	}
}

func (m model) handleSetupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	m.clearMouseSelection()
	if m.setupCredentialInputActive() {
		return m.handleSetupCredentialKey(msg)
	}
	switch {
	case keyCtrl(msg, 'c'):
		return m, tea.Quit
	case keyIs(msg, tea.KeyEsc):
		if m.setup.stage > setupStageWelcome {
			prev := m.previousSetupStage()
			if prev == setupStageMethod {
				m = m.setupReturnToMethod()
			}
			m.setup.stage = prev
			m.setup.err = ""
			return m, nil
		}
		if m.setup.required {
			return m, tea.Quit
		}
		return m.exitSetupToChat()
	case keyIs(msg, tea.KeyLeft):
		if m.setup.stage > setupStageWelcome {
			prev := m.previousSetupStage()
			if prev == setupStageMethod {
				m = m.setupReturnToMethod()
			}
			m.setup.stage = prev
			m.setup.err = ""
		}
		return m, nil
	case keyIs(msg, tea.KeyEnter):
		if m.setup.stage == setupStageMethod || m.setup.stage == setupStageProvider || m.setup.stage == setupStageModel || m.setup.stage == setupStageReady {
			return m.advanceSetup()
		}
		return m, nil
	case keyIs(msg, tea.KeySpace):
		if m.setup.stage < setupStageReady && m.setup.stage != setupStageProvider && m.setup.stage != setupStageModel {
			return m.advanceSetup()
		}
		return m, nil
	case keyIs(msg, tea.KeyUp) || keyCtrl(msg, 'p'):
		switch m.setup.stage {
		case setupStageMethod:
			m.moveSetupMethod(-1)
		case setupStageProvider:
			m.moveSetupProvider(-1)
		case setupStageModel:
			m.moveSetupModel(-1)
		}
		return m, nil
	case keyIs(msg, tea.KeyDown) || keyCtrl(msg, 'n'):
		switch m.setup.stage {
		case setupStageMethod:
			m.moveSetupMethod(1)
		case setupStageProvider:
			m.moveSetupProvider(1)
		case setupStageModel:
			m.moveSetupModel(1)
		}
		return m, nil
	case keyText(msg) != "":
		if m.setup.stage == setupStageModel {
			m.appendSetupModelQuery(keyRunes(msg))
			return m, nil
		}
		switch keyText(msg) {
		case "q":
			return m, tea.Quit
		case "k":
			if m.setup.stage == setupStageProvider {
				m.moveSetupProvider(-1)
			}
		case "j":
			if m.setup.stage == setupStageProvider {
				m.moveSetupProvider(1)
			}
		}
		return m, nil
	case keyBackspace(msg):
		if m.setup.stage == setupStageModel {
			m.deleteSetupModelQueryRune()
		}
		return m, nil
	case keyCtrl(msg, 'u'):
		if m.setup.stage == setupStageModel {
			m.setup.modelQuery = ""
			m.setup.modelIndex = 0
		}
		return m, nil
	}

	switch msg.String() {
	case " ":
		if m.setup.stage < setupStageReady && m.setup.stage != setupStageProvider && m.setup.stage != setupStageModel {
			return m.advanceSetup()
		}
	case "q":
		return m, tea.Quit
	case "k":
		switch m.setup.stage {
		case setupStageProvider:
			m.moveSetupProvider(-1)
		case setupStageModel:
			m.moveSetupModel(-1)
		}
	case "j":
		switch m.setup.stage {
		case setupStageProvider:
			m.moveSetupProvider(1)
		case setupStageModel:
			m.moveSetupModel(1)
		}
	}
	return m, nil
}

func (m model) handleSetupPaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.setupCredentialInputActive():
		previousAPIKey := m.setup.apiKey.Value()
		var cmd tea.Cmd
		m.setup.apiKey, cmd = m.setup.apiKey.Update(msg)
		if m.setup.apiKey.Value() != previousAPIKey {
			m.setup.modelGen++
			m.resetSetupModels()
		}
		m.setup.err = ""
		return m, cmd
	case m.setup.stage == setupStageModel:
		m.appendSetupModelQuery([]rune(msg.Content))
	}
	return m, nil
}

func (m model) handleSetupMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// A right-click pastes the clipboard into the focused setup field, mirroring
	// handleMouse (mouse.go) so paste behaves identically in setup mode — routePaste
	// targets the active setup input. Without this branch, setup swallows the
	// right-click and paste never fires.
	if mouseRightPress(msg) {
		return m, pasteFromClipboardCmd()
	}
	if mouseLeftPress(msg) {
		switch m.setup.stage {
		case setupStageProvider:
			if target, ok := m.selectSetupProviderAtMouse(msg); ok {
				if m.repeatMouseSelection(target) {
					m.clearMouseSelection()
					return m.advanceSetup()
				}
				m.lastMouseSelection = target
				return m, nil
			}
		case setupStageModel:
			if target, ok := m.selectSetupModelAtMouse(msg); ok {
				if m.repeatMouseSelection(target) {
					m.clearMouseSelection()
					return m.advanceSetup()
				}
				m.lastMouseSelection = target
				return m, nil
			}
		}
	}

	switch {
	case mouseWheelUp(msg):
		m.clearMouseSelection()
		switch m.setup.stage {
		case setupStageProvider:
			m.moveSetupProvider(-1)
		case setupStageModel:
			m.moveSetupModel(-1)
		}
	case mouseWheelDown(msg):
		m.clearMouseSelection()
		switch m.setup.stage {
		case setupStageProvider:
			m.moveSetupProvider(1)
		case setupStageModel:
			m.moveSetupModel(1)
		}
	}
	return m, nil
}

func (m *model) selectSetupProviderAtMouse(msg tea.MouseMsg) (mouseSelectionTarget, bool) {
	if len(m.setup.providers) == 0 {
		return mouseSelectionTarget{}, false
	}
	width := chatWidth(m.width)
	height := normalizedStartupHeight(m.height)
	rowWidth := setupProviderBlockWidth(width, m.setup.providers)
	if !setupBlockContainsMouseX(mouseX(msg), width, rowWidth) {
		return mouseSelectionTarget{}, false
	}
	maxVisible := setupProviderMaxVisible(height, len(m.setup.providers))
	if maxVisible == 0 {
		return mouseSelectionTarget{}, false
	}
	content := m.setupProviderLines(width, height)
	top := setupContentTop(height, len(content), m.setup.err != "")
	row := mouseY(msg) - top - 2
	if row < 0 || row >= maxVisible {
		return mouseSelectionTarget{}, false
	}
	start := selectableListStart(len(m.setup.providers), maxVisible, m.setup.selected)
	index := start + row
	if index < 0 || index >= len(m.setup.providers) {
		return mouseSelectionTarget{}, false
	}
	m.setup.selected = index
	m.setup.apiKey.SetValue("")
	m.setup.modelGen++
	m.resetSetupModels()
	return mouseSelectionTarget{Scope: "first-run-provider", Value: m.setup.providers[index].ID, Index: index}, true
}

func (m *model) selectSetupModelAtMouse(msg tea.MouseMsg) (mouseSelectionTarget, bool) {
	if m.setup.modelLoad {
		return mouseSelectionTarget{}, false
	}
	m.ensureSetupModels()
	models := m.setupFilteredModels()
	if len(models) == 0 {
		return mouseSelectionTarget{}, false
	}
	width := chatWidth(m.width)
	height := normalizedStartupHeight(m.height)
	rowWidth := setupModelBlockWidth(width, m.setup.models)
	if !setupBlockContainsMouseX(mouseX(msg), width, rowWidth) {
		return mouseSelectionTarget{}, false
	}
	maxVisible := setupModelMaxVisible(height, len(models))
	if maxVisible == 0 {
		return mouseSelectionTarget{}, false
	}
	m.setup.modelIndex = clampInt(m.setup.modelIndex, 0, len(models)-1)
	content := m.setupModelLines(width, height)
	top := setupContentTop(height, len(content), m.setup.err != "")
	rowStart := 4
	if m.setupModelStatus() != "" {
		rowStart++
	}
	row := mouseY(msg) - top - rowStart
	if row < 0 || row >= maxVisible {
		return mouseSelectionTarget{}, false
	}
	start := selectableListStart(len(models), maxVisible, m.setup.modelIndex)
	index := start + row
	if index < 0 || index >= len(models) {
		return mouseSelectionTarget{}, false
	}
	m.setup.modelIndex = index
	return mouseSelectionTarget{Scope: "first-run-model", Value: models[index].ID, Index: index}, true
}

func setupContentTop(height int, contentLines int, hasError bool) int {
	if hasError {
		contentLines += 2
	}
	return maxInt(0, (height-contentLines-3)/2)
}

func (m model) setupStages() []setupStage {
	stages := []setupStage{setupStageWelcome, setupStageMethod}
	stages = append(stages, setupStageProvider)
	stages = append(stages, setupStageCredentials, setupStageModel, setupStageSafety, setupStageReady)
	return stages
}

// setupReturnToMethod returns to the connect-method chooser.
func (m model) setupReturnToMethod() model {
	m.setup.providers = m.setup.allProviders
	m.setup.selected = 0
	m.resetSetupModels()
	return m
}

// setupMethodOptions returns the connect-method choices for this run.
func (m model) setupMethodOptions() []providerWizardMethodOption {
	return providerWizardMethodOptions()
}

func (m *model) moveSetupMethod(delta int) {
	options := m.setupMethodOptions()
	if len(options) == 0 {
		return
	}
	m.setup.selectedMethod = ((m.setup.selectedMethod+delta)%len(options) + len(options)) % len(options)
}

func (m model) nextSetupStage() setupStage {
	stages := m.setupStages()
	for index, stage := range stages {
		if stage == m.setup.stage {
			if index+1 < len(stages) {
				return stages[index+1]
			}
			return stage
		}
	}
	return m.setup.stage
}

func (m model) previousSetupStage() setupStage {
	stages := m.setupStages()
	for index, stage := range stages {
		if stage == m.setup.stage {
			if index > 0 {
				return stages[index-1]
			}
			return stage
		}
	}
	return m.setup.stage
}

func setupBlockContainsMouseX(x int, width int, blockWidth int) bool {
	if blockWidth <= 0 {
		return false
	}
	left := maxInt(0, (width-blockWidth)/2)
	return x >= left && x < left+blockWidth
}

func (m model) advanceSetup() (tea.Model, tea.Cmd) {
	if m.setup.stage < setupStageReady {
		if m.setup.stage == setupStageMethod {
			options := m.setupMethodOptions()
			m.setup.selectedMethod = clamp(m.setup.selectedMethod, 0, maxInt(0, len(options)-1))
			m.setup.providers = m.setup.allProviders
			m.setup.selected = 0
			m.resetSetupModels()
			m.setup.stage = setupStageProvider
			m.setup.err = ""
			return m, nil
		}
		if m.setup.stage == setupStageProvider {
			m.setup.apiKey.SetValue("")
		}
		if m.setup.stage == setupStageModel && m.setup.modelLoad {
			m.setup.err = "Models are still loading."
			return m, nil
		}
		if m.setup.stage == setupStageModel && m.setupCurrentModel().ID == "" {
			m.setup.err = "Choose a matching model before continuing."
			return m, nil
		}
		m.setup.stage = m.nextSetupStage()
		m.setup.err = ""
		if m.setup.stage == setupStageModel {
			m.resetSetupModels()
			m.setup.modelErr = ""
			m.setup.modelGen++
			cmd := m.setupModelDiscoveryCmd(m.setup.modelGen)
			m.setup.modelLoad = cmd != nil
			return m, cmd
		}
		return m, nil
	}
	return m.completeSetup()
}

func (m *model) moveSetupProvider(delta int) {
	if len(m.setup.providers) == 0 {
		return
	}
	m.setup.selected = ((m.setup.selected+delta)%len(m.setup.providers) + len(m.setup.providers)) % len(m.setup.providers)
	m.setup.apiKey.SetValue("")
	m.setup.modelGen++
	m.resetSetupModels()
}

func (m model) completeSetup() (tea.Model, tea.Cmd) {
	option := m.setupProvider()
	if option.ID == "" {
		m.setup.err = "No provider option is available."
		return m, nil
	}
	if m.setupSave == nil {
		return m.exitSetupToChat()
	}

	apiKey := m.setupCredentialAPIKey(option)
	result, err := m.setupSave(SetupSelection{
		CatalogID: option.ID,
		Model:     m.setupCurrentModel().ID,
		APIKey:    apiKey,
	})
	if err != nil {
		m.setup.err = err.Error()
		return m, nil
	}

	if result.ConfigPath != "" {
		m.setup.configPath = result.ConfigPath
	}
	if result.Provider.Name != "" {
		m.providerProfile = result.Provider
		m.providerName = result.Provider.Name
		m.modelName = result.Provider.Model
		// Export QG_PROVIDER alongside the committed profile fields (and the
		// config setupSave already persisted as active). Unlike command_center's
		// switch — which commits everything only after a successful build — setup
		// commits config + profile unconditionally here, so the env must match
		// them unconditionally too: applyEnv makes QG_PROVIDER WIN over config,
		// so a stale value from an earlier /model switch left in place would send
		// spawned children to the OLD provider even though config now names the
		// new one. That is the exact D3 gap this closes.
		config.SetActiveProviderEnv(result.Provider.Name)
		if m.newProvider != nil {
			if provider, providerErr := m.newProvider(result.Provider); providerErr == nil {
				m.provider = provider
			}
		}
	}

	return m.exitSetupToChat()
}

func (m *model) resetSetupModels() {
	option := m.setupProvider()
	provider := setupProviderDescriptor(option)
	models := providerWizardModelOptions(provider)
	m.setup.models = models
	m.setup.modelIndex = 0
	m.setup.modelQuery = ""
	m.setup.modelForID = provider.ID
	m.setup.modelLoad = false
	m.setup.modelErr = ""
	m.setup.modelSrc = "fallback"
}

func (m model) setupModelDiscoveryCmd(gen uint64) tea.Cmd {
	option := m.setupProvider()
	provider := setupProviderDescriptor(option)
	if provider.ID == "" || !providercatalog.RuntimeSupported(provider) {
		return nil
	}
	pastedKey := m.setupCredentialAPIKey(option)
	baseSecrets := []string{m.setup.apiKey.Value()}
	discover := m.discoverProviderModels
	if discover == nil {
		discover = func(ctx context.Context, profile config.ProviderProfile) ([]providermodeldiscovery.Model, error) {
			return providermodeldiscovery.DiscoverCatalog(ctx, provider, profile, providermodeldiscovery.Options{})
		}
	}
	providerID := provider.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 12*time.Second)
		defer cancel()
		profile := providerWizardDiscoveryProfile(provider, pastedKey, "")
		secrets := append(append([]string{}, baseSecrets...), pastedKey, profile.APIKey)
		models, err := discover(ctx, profile)
		return setupModelsDiscoveredMsg{providerID: providerID, gen: gen, redactionSecrets: secrets, models: models, err: err}
	}
}

func (m model) applySetupModelsDiscovered(msg setupModelsDiscoveredMsg) model {
	if !m.setup.visible || m.setup.stage != setupStageModel || m.setupProviderDescriptor().ID != msg.providerID || m.setup.modelGen != msg.gen {
		return m
	}
	m.setup.modelLoad = false
	m.setup.err = ""
	if msg.err != nil {
		m.setup.modelErr = redaction.RedactString(msg.err.Error(), redaction.Options{ExtraSecretValues: msg.redactionSecrets})
		m.setup.modelSrc = "fallback"
		if len(m.setup.models) == 0 {
			m.resetSetupModels()
		}
		return m
	}
	models := providerWizardModelsFromDiscovery(msg.models)
	if len(models) == 0 {
		m.setup.modelErr = "models endpoint returned no model ids"
		m.setup.modelSrc = "fallback"
		if len(m.setup.models) == 0 {
			m.resetSetupModels()
		}
		return m
	}
	currentID := m.setupCurrentModel().ID
	m.setup.models = models
	m.setup.modelIndex = 0
	m.setup.modelSrc = providerWizardModelsSource(msg.models)
	if m.setup.modelSrc == "" {
		m.setup.modelSrc = "live"
	}
	m.setup.modelErr = ""
	if currentID != "" {
		for index, model := range m.setupFilteredModels() {
			if model.ID == currentID {
				m.setup.modelIndex = index
				break
			}
		}
	}
	return m
}

func (m model) setupProviderDescriptor() providercatalog.Descriptor {
	return setupProviderDescriptor(m.setupProvider())
}

func setupProviderDescriptor(option SetupProviderOption) providercatalog.Descriptor {
	if descriptor, ok := providercatalog.Get(option.ID); ok {
		return descriptor
	}
	descriptor := providercatalog.Descriptor{
		ID:           strings.TrimSpace(option.ID),
		Name:         strings.TrimSpace(option.Name),
		DefaultModel: strings.TrimSpace(option.DefaultModel),
		RequiresAuth: option.RequiresAuth,
		Local:        option.Local,
		AuthEnvVars:  cleanSetupEnvVar(option.EnvVar),
	}
	return descriptor
}

func cleanSetupEnvVar(envVar string) []string {
	if envVar = strings.TrimSpace(envVar); envVar != "" {
		return []string{envVar}
	}
	return nil
}

func (m model) setupCurrentModel() providerWizardModel {
	m.ensureSetupModels()
	models := m.setupFilteredModels()
	if len(models) == 0 {
		return providerWizardModel{Description: "no matching models"}
	}
	index := clamp(m.setup.modelIndex, 0, len(models)-1)
	return models[index]
}

func (m *model) ensureSetupModels() {
	option := m.setupProvider()
	providerID := setupProviderDescriptor(option).ID
	if len(m.setup.models) > 0 && m.setup.modelForID == providerID {
		return
	}
	m.resetSetupModels()
}

func (m model) setupFilteredModels() []providerWizardModel {
	query := strings.ToLower(strings.TrimSpace(m.setup.modelQuery))
	if query == "" {
		return append([]providerWizardModel{}, m.setup.models...)
	}
	models := make([]providerWizardModel, 0, len(m.setup.models))
	for _, model := range m.setup.models {
		if model.matches(query) {
			models = append(models, model)
		}
	}
	return models
}

func (m *model) moveSetupModel(delta int) {
	if m.setup.modelLoad {
		return
	}
	m.ensureSetupModels()
	models := m.setupFilteredModels()
	if len(models) == 0 {
		return
	}
	m.setup.modelIndex = ((m.setup.modelIndex+delta)%len(models) + len(models)) % len(models)
}

func (m *model) appendSetupModelQuery(runes []rune) {
	if m.setup.modelLoad {
		return
	}
	for _, r := range runes {
		if r < 32 || r == 127 {
			continue
		}
		m.setup.modelQuery += string(r)
	}
	m.setup.modelIndex = 0
	m.setup.err = ""
}

func (m *model) deleteSetupModelQueryRune() {
	if m.setup.modelLoad {
		return
	}
	if m.setup.modelQuery == "" {
		return
	}
	runes := []rune(m.setup.modelQuery)
	m.setup.modelQuery = string(runes[:len(runes)-1])
	m.setup.modelIndex = 0
	m.setup.err = ""
}

func (m model) exitSetupToChat() (tea.Model, tea.Cmd) {
	m.setup.visible = false
	m.headerPrinted = false
	m.flushQueue = nil
	m.printInFlight = false
	return m, nil
}

func (m model) setupCredentialInputActive() bool {
	return m.setup.stage == setupStageCredentials && setupProviderAcceptsAPIKey(m.setupProvider())
}
func (m model) handleSetupCredentialKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyCtrl(msg, 'c'):
		return m, tea.Quit
	case keyIs(msg, tea.KeyEsc) || keyIs(msg, tea.KeyLeft):
		m.setup.stage = m.previousSetupStage()
		m.setup.err = ""
		return m, nil
	case keyIs(msg, tea.KeyEnter):
		return m.advanceSetup()
	case keyIs(msg, tea.KeyUp) || keyIs(msg, tea.KeyDown):
		return m, nil
	}
	previousAPIKey := m.setup.apiKey.Value()
	var cmd tea.Cmd
	m.setup.apiKey, cmd = m.setup.apiKey.Update(msg)
	if m.setup.apiKey.Value() != previousAPIKey {
		m.setup.modelGen++
		m.resetSetupModels()
	}
	m.setup.err = ""
	return m, cmd
}

func setupProviderAcceptsAPIKey(option SetupProviderOption) bool {
	return option.RequiresAuth && !option.Local
}
func (m model) setupCredentialAPIKey(option SetupProviderOption) string {
	if !setupProviderAcceptsAPIKey(option) {
		return ""
	}
	return strings.TrimSpace(m.setup.apiKey.Value())
}
func (m model) setupProvider() SetupProviderOption {
	if len(m.setup.providers) == 0 {
		return SetupProviderOption{}
	}
	index := clamp(m.setup.selected, 0, len(m.setup.providers)-1)
	return m.setup.providers[index]
}

// setupErrorAffordance returns a faint one-line recovery hint tailored to the
// current setup stage, so a first-run error points the user at the inline fix
// instead of leaving them staring at a red line. Empty for stages whose footer
// already makes recovery obvious. Each hint names only keys the stage's footer
// confirms, so the guidance is always accurate.
func (m model) setupErrorAffordance() string {
	switch m.setup.stage {
	case setupStageCredentials:
		if m.setupCredentialInputActive() {
			return "→ re-enter the key above, then Enter to retry (← to go back)"
		}
	case setupStageModel:
		if !m.setup.modelLoad {
			return "→ pick another model with ↑/↓, or type to search"
		}
	}
	return ""
}

func (m model) setupView(width int) string {
	if width <= 0 {
		width = defaultStartupWidth
	}
	height := normalizedStartupHeight(m.height)
	content := m.setupStageLines(width, height)
	if m.setup.err != "" {
		content = append(content, "", qgTheme.red.Render("error: "+m.setup.err))
		if hint := m.setupErrorAffordance(); hint != "" {
			content = append(content, qgTheme.faint.Render(hint))
		}
	}
	progress := m.setupProgressText()
	footer := m.setupFooter()

	topGap := maxInt(0, (height-len(content)-3)/2)
	bottomGap := maxInt(0, height-topGap-len(content)-2)
	lines := make([]string, 0, height)
	for i := 0; i < topGap; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, centerSetupLines(content, width)...)
	for i := 0; i < bottomGap; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, centerLine(fitStyledLine(progress, width), width))
	lines = append(lines, centerLine(fitStyledLine(footer, width), width))
	return strings.Join(lines, "\n")
}

func (m model) setupStageLines(width int, height int) []string {
	switch m.setup.stage {
	case setupStageMethod:
		return m.setupMethodLines(width)
	case setupStageProvider:
		return m.setupProviderLines(width, height)
	case setupStageCredentials:
		return m.setupCredentialLines(width)
	case setupStageModel:
		return m.setupModelLines(width, height)
	case setupStageSafety:
		return []string{
			qgTheme.ink.Bold(true).Render("Safety"),
			"",
			"QG asks before running shell commands or changing files.",
			"Unsafe mode stays off unless you explicitly enable it.",
			"",
			qgTheme.faint.Render("Default: ask before risky work."),
		}
	case setupStageReady:
		return m.setupReadyLines(width)
	default:
		return []string{
			qgTheme.accent.Render("Welcome to QG"),
			"",
			qgTheme.ink.Render("A terminal agent for changing real code."),
			qgTheme.faint.Render("Plan changes, edit with approval, run checks, and resume sessions."),
		}
	}
}

type setupReadyRow struct {
	label string
	value string
}

func (m model) setupReadyLines(width int) []string {
	option := m.setupProvider()
	model := m.setupCurrentModel()
	providerName := displayValue(option.Name, option.ID)

	rows := []setupReadyRow{{label: "provider", value: providerName}}
	rows = append(rows,
		setupReadyRow{label: "model", value: displayValue(model.ID, option.DefaultModel)},
		setupReadyRow{label: "credentials", value: m.setupCredentialSummary(option)},
		setupReadyRow{label: "config", value: shortenPath(displayValue(m.setup.configPath, "user config"))},
	)

	lines := []string{
		qgTheme.ink.Bold(true).Render("Ready"),
		"",
		"QG will save this setup and open chat.",
		"",
	}

	labelWidth := 0
	for _, row := range rows {
		labelWidth = maxInt(labelWidth, lipgloss.Width(row.label)+1)
	}
	rowWidth := setupReadyRowsWidth(width, labelWidth, rows)
	for _, row := range rows {
		label := fmt.Sprintf("%*s", labelWidth, row.label+":")
		line := "  " + qgTheme.faint.Render(label) + "  " + qgTheme.ink.Render(row.value)
		lines = append(lines, padSetupLine(line, rowWidth))
	}

	lines = append(lines,
		"",
		qgTheme.faint.Render("Later, use /provider, /doctor, or /help anytime."),
	)
	return lines
}

func setupReadyRowsWidth(terminalWidth int, labelWidth int, rows []setupReadyRow) int {
	available := maxInt(28, minInt(terminalWidth-8, 86))
	target := 0
	for _, row := range rows {
		target = maxInt(target, 4+labelWidth+lipgloss.Width(row.value))
	}
	return minInt(target, available)
}

func (m model) setupModelLines(width int, height int) []string {
	m.ensureSetupModels()
	if m.setup.modelLoad {
		return m.setupModelLoadingLines(width)
	}
	rowWidth := setupModelBlockWidth(width, m.setup.models)
	models := m.setupFilteredModels()
	maxVisible := setupModelMaxVisible(height, len(models))
	start := selectableListStart(len(models), maxVisible, m.setup.modelIndex)
	lines := []string{
		padSetupLine("  "+qgTheme.ink.Bold(true).Render("Choose a model"), rowWidth),
		blankSetupBlockLine(rowWidth),
		padSetupLine("  "+m.setupModelSearchLine(rowWidth-2), rowWidth),
	}
	if status := m.setupModelStatus(); status != "" {
		lines = append(lines, padSetupLine("  "+qgTheme.faint.Render(status), rowWidth))
	}
	lines = append(lines, blankSetupBlockLine(rowWidth))
	if len(models) == 0 {
		lines = append(lines, padSetupLine("  "+qgTheme.faint.Render("No matching models"), rowWidth))
		return lines
	}
	visibleModels := models[start : start+maxVisible]
	for offset, model := range visibleModels {
		lines = append(lines, m.setupModelRow(rowWidth, start+offset, model))
	}
	detail := setupModelSelectedDetail(m.setupCurrentModel())
	lines = append(lines,
		blankSetupBlockLine(rowWidth),
		padSetupLine("  "+qgTheme.faint.Render(detail), rowWidth),
	)
	return lines
}
func (m model) setupModelLoadingLines(width int) []string {
	rowWidth := setupModelLoadingBlockWidth(width)
	return []string{
		padSetupLine("  "+qgTheme.ink.Bold(true).Render("Choose a model"), rowWidth),
		blankSetupBlockLine(rowWidth),
		padSetupLine("  "+qgTheme.faint.Render("Checking available models..."), rowWidth),
		padSetupLine("  "+qgTheme.faint.Render("Built-in models will be used if discovery fails."), rowWidth),
	}
}

func setupTextInputBlockWidth(terminalWidth int, values ...string) int {
	available := maxInt(34, minInt(terminalWidth-8, 72))
	target := 42
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			target = maxInt(target, lipgloss.Width("  "+value)+8)
		}
	}
	return minInt(target, available)
}

func setupModelMaxVisible(height int, total int) int {
	if total <= 0 {
		return 0
	}
	maxVisible := height - 12
	if maxVisible < 5 {
		maxVisible = 5
	}
	if maxVisible > 18 {
		maxVisible = 18
	}
	if maxVisible > total {
		return total
	}
	return maxVisible
}

func setupModelLoadingBlockWidth(terminalWidth int) int {
	available := maxInt(34, minInt(terminalWidth-8, 72))
	target := maxInt(lipgloss.Width("  Choose a model"), lipgloss.Width("  Built-in models will be used if discovery fails."))
	return minInt(maxInt(target, 42), available)
}

func setupModelBlockWidth(terminalWidth int, models []providerWizardModel) int {
	available := maxInt(34, minInt(terminalWidth-8, 72))
	target := lipgloss.Width("  Choose a model")
	target = maxInt(target, lipgloss.Width("  search > model name..."))
	for _, model := range models {
		target = maxInt(target, 4+lipgloss.Width(model.displayLabel()))
		if detail := setupModelSelectedDetail(model); detail != "" {
			target = maxInt(target, lipgloss.Width("  "+detail))
		}
	}
	target = maxInt(target, 42)
	return minInt(target, available)
}

func (m model) setupModelSearchLine(width int) string {
	query := strings.TrimSpace(m.setup.modelQuery)
	prompt := qgTheme.userPrompt.Render("search > ")
	cursor := qgTheme.accent.Render("▌")
	if query == "" {
		return fitStyledLine(prompt+cursor+qgTheme.faint.Render("model name..."), width)
	}
	return fitStyledLine(prompt+qgTheme.ink.Render(query)+cursor, width)
}

func (m model) setupModelStatus() string {
	if m.setup.modelLoad {
		return "Refreshing available models"
	}
	if m.setup.modelErr != "" {
		return "Using built-in model list"
	}
	return ""
}

func (m model) setupModelRow(width int, index int, model providerWizardModel) string {
	selected := index == m.setup.modelIndex
	marker := "  "
	style := qgTheme.ink
	if selected {
		marker = "❯ "
		style = qgTheme.accent.Bold(true)
	}
	left := marker + style.Render(model.displayLabel())
	return padSetupLine(left, width)
}

func setupModelSelectedDetail(model providerWizardModel) string {
	parts := []string{}
	if secondary := strings.TrimSpace(model.secondaryText()); secondary != "" && !providerWizardGenericModelDescription(secondary) {
		parts = append(parts, secondary)
	}
	if meta := strings.TrimSpace(model.Meta); meta != "" {
		parts = append(parts, meta)
	}
	return strings.Join(parts, " · ")
}

func (m model) setupMethodLines(width int) []string {
	options := m.setupMethodOptions()
	rowWidth := setupMethodBlockWidth(width, options)
	idx := clamp(m.setup.selectedMethod, 0, maxInt(0, len(options)-1))
	lines := []string{
		padSetupLine("  "+qgTheme.ink.Bold(true).Render("How do you want to connect?"), rowWidth),
		blankSetupBlockLine(rowWidth),
	}
	for index, option := range options {
		marker := "  "
		style := qgTheme.ink
		if index == idx {
			marker = "❯ "
			style = qgTheme.accent.Bold(true)
		}
		lines = append(lines, padSetupLine(marker+style.Render(option.label), rowWidth))
		lines = append(lines, padSetupLine("    "+qgTheme.faint.Render(option.subtitle), rowWidth))
	}
	return lines
}

func setupMethodBlockWidth(terminalWidth int, options []providerWizardMethodOption) int {
	available := maxInt(34, minInt(terminalWidth-8, 86))
	target := lipgloss.Width("  How do you want to connect?")
	for _, option := range options {
		target = maxInt(target, 2+lipgloss.Width(option.label))
		target = maxInt(target, 4+lipgloss.Width(option.subtitle))
	}
	return minInt(maxInt(target, 42), available)
}

func (m model) setupProviderLines(width int, height int) []string {
	rowWidth := setupProviderBlockWidth(width, m.setup.providers)
	maxVisible := setupProviderMaxVisible(height, len(m.setup.providers))
	start := selectableListStart(len(m.setup.providers), maxVisible, m.setup.selected)
	visibleProviders := m.setup.providers[start : start+maxVisible]
	lines := []string{
		padSetupLine("  "+qgTheme.ink.Bold(true).Render("Choose a provider"), rowWidth),
		blankSetupBlockLine(rowWidth),
	}
	for index, option := range visibleProviders {
		absoluteIndex := start + index
		marker := "  "
		style := qgTheme.ink
		if absoluteIndex == m.setup.selected {
			marker = "❯ "
			style = qgTheme.accent.Bold(true)
		}
		label := displayValue(option.Name, option.ID)
		if option.Recommended {
			label = "★ " + label
		}
		line := marker + style.Render(label)
		if option.Recommended {
			line += qgTheme.faint.Render("  (recommended)")
		}
		lines = append(lines, padSetupLine(line, rowWidth))
	}
	return lines
}
func setupProviderMaxVisible(height int, total int) int {
	if total <= 0 {
		return 0
	}
	maxVisible := height - 8
	if maxVisible < 6 {
		maxVisible = 6
	}
	if maxVisible > total {
		return total
	}
	return maxVisible
}

func setupProviderBlockWidth(terminalWidth int, providers []SetupProviderOption) int {
	available := maxInt(24, minInt(terminalWidth-8, 44))
	target := maxInt(lipgloss.Width("  2/6"), lipgloss.Width("  Choose a provider"))
	for _, provider := range providers {
		label := displayValue(provider.Name, provider.ID)
		if provider.Recommended {
			label = "★ " + label + "  (recommended)"
		}
		target = maxInt(target, 2+lipgloss.Width(label))
	}
	target = maxInt(target, 32)
	return minInt(target, available)
}

func blankSetupBlockLine(width int) string {
	if width <= 0 {
		return ""
	}
	return strings.Repeat(" ", width)
}

func (m model) setupCredentialLines(width int) []string {
	option := m.setupProvider()
	lines := []string{
		qgTheme.ink.Bold(true).Render("Credentials"),
		"",
	}
	if option.Local || !option.RequiresAuth {
		lines = append(lines,
			displayValue(option.Name, option.ID)+" does not need an API key.",
			"Start the local server before sending a prompt.",
		)
		return lines
	}
	envVar := displayValue(option.EnvVar, "the provider API key env var")
	lines = append(lines,
		"Paste your "+displayValue(option.Name, option.ID)+" API key",
		"or leave blank to use "+envVar+" from your shell.",
		"",
		m.setupAPIKeyInputLine(width),
		"",
		qgTheme.faint.Render("Saved keys stay in your user config."),
		qgTheme.faint.Render("Blank uses "+envVar+" from your shell."),
	)
	return lines
}

func (m model) setupAPIKeyInputLine(width int) string {
	input := m.setup.apiKey
	if strings.TrimSpace(input.Value()) == "" {
		return qgTheme.faint.Render(input.Placeholder)
	}
	contentWidth := lipgloss.Width(input.Value())
	if contentWidth == 0 {
		contentWidth = lipgloss.Width(input.Placeholder)
	}
	input.SetWidth(minInt(maxInt(contentWidth, 1), maxInt(1, width-lipgloss.Width(input.Prompt))))
	return input.View()
}

func (m model) setupCredentialSummary(option SetupProviderOption) string {
	if !setupProviderAcceptsAPIKey(option) {
		return "not required"
	}
	if m.setupCredentialAPIKey(option) != "" {
		return "saved API key"
	}
	return "env var " + displayValue(option.EnvVar, "provider API key")
}

func (m model) setupFooter() string {
	switch m.setup.stage {
	case setupStageMethod:
		return qgTheme.faint.Render("↑/↓ choose   ") + qgTheme.accent.Render("Enter") + qgTheme.faint.Render(" continue   q quit")
	case setupStageReady:
		return qgTheme.accent.Render("Enter") + qgTheme.faint.Render(" to save and start chat")
	case setupStageCredentials:
		if m.setupCredentialInputActive() {
			return qgTheme.faint.Render("paste key optional   ") + qgTheme.accent.Render("Enter") + qgTheme.faint.Render(" continue   left back")
		}
		return qgTheme.accent.Render("Space") + qgTheme.faint.Render(" to continue")
	case setupStageProvider:
		return qgTheme.faint.Render("↑/↓ choose   ") + qgTheme.accent.Render("Enter") + qgTheme.faint.Render(" continue   q quit")
	case setupStageModel:
		if m.setup.modelLoad {
			return qgTheme.faint.Render("checking models...")
		}
		return qgTheme.faint.Render("↑/↓ choose   type search   ") + qgTheme.accent.Render("Enter") + qgTheme.faint.Render(" continue")
	case setupStageWelcome:
		return qgTheme.accent.Render("Space") + qgTheme.faint.Render(" to set up QG")
	default:
		return qgTheme.accent.Render("Space") + qgTheme.faint.Render(" to continue")
	}
}

func centerSetupLines(lines []string, width int) []string {
	fitted := make([]string, 0, len(lines))
	for _, line := range lines {
		fitted = append(fitted, centerLine(fitStyledLine(line, width), width))
	}
	return fitted
}

func (m model) setupProgressText() string {
	stages := m.setupStages()
	position := 0
	for index, stage := range stages {
		if stage == m.setup.stage {
			position = index
			break
		}
	}
	return qgTheme.faint.Render(fmt.Sprintf("%d/%d", position+1, len(stages)))
}

func padSetupLine(line string, width int) string {
	if width <= 0 {
		return line
	}
	if pad := width - lipgloss.Width(line); pad > 0 {
		return line + strings.Repeat(" ", pad)
	}
	return fitStyledLine(line, width)
}
