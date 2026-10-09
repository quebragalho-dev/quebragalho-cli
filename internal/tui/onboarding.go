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
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
)

type setupStage int

const (
	setupStageWelcome setupStage = iota
	setupStageCredentials
	setupStageModel
)

type setupState struct {
	visible      bool
	required     bool
	configPath   string
	providers    []SetupProviderOption // QG-only choice set (kept for the save path)
	allProviders []SetupProviderOption // full catalog
	selected     int
	stage        setupStage
	err          string
	apiKey       textinput.Model
	// keyCheck is true while the pasted key is being validated against the
	// gateway; the credential screen accepts no input until the result lands.
	keyCheck   bool
	validate   func(context.Context, string) error
	models     []providerWizardModel
	modelIndex int
	modelQuery string
	modelForID string
	modelGen   uint64
}

// setupKeyCheckMsg carries the async GET /v1/models validation result back to
// the credential screen; gen binds it to the key that was submitted.
type setupKeyCheckMsg struct {
	gen uint64
	err error
}

func newSetupState(options SetupOptions) setupState {
	providers := append([]SetupProviderOption{}, options.Providers...)
	if len(providers) == 0 {
		providers = []SetupProviderOption{{
			ID:           providercatalog.QGID,
			Name:         "Quebra Galho",
			DefaultModel: modelregistry.DefaultModelID,
			EnvVar:       "QUEBRAGALHO_API_KEY",
			RequiresAuth: true,
		}}
	}
	apiKey := textinput.New()
	apiKey.Prompt = ""
	apiKey.Placeholder = "paste your Quebra Galho API key"
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
		validate:     options.ValidateAPIKey,
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
	case keyIs(msg, tea.KeyEsc), keyIs(msg, tea.KeyLeft):
		if m.setup.stage > setupStageWelcome {
			m.setup.stage = m.previousSetupStage()
			m.setup.err = ""
			return m, nil
		}
		if m.setup.required {
			return m, tea.Quit
		}
		return m.exitSetupToChat()
	case keyIs(msg, tea.KeyEnter):
		if m.setup.stage == setupStageModel {
			return m.advanceSetup()
		}
		return m, nil
	case keyIs(msg, tea.KeySpace):
		switch m.setup.stage {
		case setupStageWelcome:
			return m.advanceSetup()
		case setupStageModel:
			m.appendSetupModelQuery(keyRunes(msg))
		}
		return m, nil
	case keyIs(msg, tea.KeyUp) || keyCtrl(msg, 'p'):
		if m.setup.stage == setupStageModel {
			m.moveSetupModel(-1)
		}
		return m, nil
	case keyIs(msg, tea.KeyDown) || keyCtrl(msg, 'n'):
		if m.setup.stage == setupStageModel {
			m.moveSetupModel(1)
		}
		return m, nil
	case keyText(msg) != "":
		if m.setup.stage == setupStageModel {
			m.appendSetupModelQuery(keyRunes(msg))
			return m, nil
		}
		if keyText(msg) == "q" && m.setup.stage == setupStageWelcome {
			return m, tea.Quit
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
	return m, nil
}

func (m model) handleSetupPaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.setupCredentialInputActive():
		if m.setup.keyCheck {
			return m, nil
		}
		var cmd tea.Cmd
		m.setup.apiKey, cmd = m.setup.apiKey.Update(msg)
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
	// targets the active setup input.
	if mouseRightPress(msg) {
		return m, pasteFromClipboardCmd()
	}
	if mouseLeftPress(msg) && m.setup.stage == setupStageModel {
		if target, ok := m.selectSetupModelAtMouse(msg); ok {
			if m.repeatMouseSelection(target) {
				m.clearMouseSelection()
				return m.advanceSetup()
			}
			m.lastMouseSelection = target
			return m, nil
		}
	}
	switch {
	case mouseWheelUp(msg):
		m.clearMouseSelection()
		if m.setup.stage == setupStageModel {
			m.moveSetupModel(-1)
		}
	case mouseWheelDown(msg):
		m.clearMouseSelection()
		if m.setup.stage == setupStageModel {
			m.moveSetupModel(1)
		}
	}
	return m, nil
}
func (m *model) selectSetupModelAtMouse(msg tea.MouseMsg) (mouseSelectionTarget, bool) {
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
	return []setupStage{setupStageWelcome, setupStageCredentials, setupStageModel}
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
	switch m.setup.stage {
	case setupStageWelcome:
		m.setup.stage = setupStageCredentials
		m.setup.err = ""
		return m, nil
	case setupStageCredentials:
		if m.setup.keyCheck {
			return m, nil
		}
		if strings.TrimSpace(m.setup.apiKey.Value()) == "" {
			m.setup.err = "Paste your Quebra Galho API key to continue."
			return m, nil
		}
		if m.setup.validate == nil {
			m.setup.stage = setupStageModel
			m.setup.err = ""
			m.resetSetupModels()
			return m, nil
		}
		m.setup.keyCheck = true
		m.setup.err = ""
		return m, m.setupKeyCheckCmd()
	case setupStageModel:
		if m.setupCurrentModel().ID == "" {
			m.setup.err = "Choose a matching model before continuing."
			return m, nil
		}
		return m.completeSetup()
	}
	return m, nil
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

	if result.KeyStoreFallback {
		m = m.showTransientNoticeInline("Credential store unavailable — API key saved in plaintext in "+result.ConfigPath, transientNoticeWarning)
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
}

// setupKeyCheckCmd validates the pasted key against GET /v1/models off the UI
// thread; gen binds the result to the key that was submitted so a stale reply
// cannot move a screen the user already left.
func (m model) setupKeyCheckCmd() tea.Cmd {
	validate := m.setup.validate
	if validate == nil {
		return nil
	}
	key := strings.TrimSpace(m.setup.apiKey.Value())
	gen := m.setup.modelGen
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return setupKeyCheckMsg{gen: gen, err: validate(ctx, key)}
	}
}

// applySetupKeyCheck lands the validation result: a rejection keeps the
// credential screen with a redacted error; success advances to the model list.
func (m model) applySetupKeyCheck(msg setupKeyCheckMsg) model {
	if !m.setup.visible || m.setup.stage != setupStageCredentials || m.setup.modelGen != msg.gen {
		return m
	}
	m.setup.keyCheck = false
	if msg.err != nil {
		m.setup.err = redaction.RedactString(msg.err.Error(), redaction.Options{ExtraSecretValues: []string{strings.TrimSpace(m.setup.apiKey.Value())}})
		return m
	}
	m.setup.stage = setupStageModel
	m.setup.err = ""
	m.resetSetupModels()
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
	m.ensureSetupModels()
	models := m.setupFilteredModels()
	if len(models) == 0 {
		return
	}
	m.setup.modelIndex = ((m.setup.modelIndex+delta)%len(models) + len(models)) % len(models)
}
func (m *model) appendSetupModelQuery(runes []rune) {
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
	case m.setup.keyCheck:
		// The gateway check owns the screen until its result lands.
		return m, nil
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
		m.setup.err = ""
	}
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
	// The wizard is QG-only: always pick the Quebra Galho entry so the model
	// list and save path never depend on a selection screen that no longer exists.
	for _, option := range m.setup.providers {
		if option.ID == providercatalog.QGID {
			return option
		}
	}
	if len(m.setup.providers) > 0 {
		return m.setup.providers[0]
	}
	return SetupProviderOption{}
}
func (m model) setupErrorAffordance() string {
	switch m.setup.stage {
	case setupStageCredentials:
		if m.setupCredentialInputActive() && !m.setup.keyCheck {
			return "→ re-enter the key above, then Enter to retry (← to go back)"
		}
	case setupStageModel:
		return "→ pick another model with ↑/↓, or type to search"
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
	case setupStageCredentials:
		return m.setupCredentialLines(width)
	case setupStageModel:
		return m.setupModelLines(width, height)
	default:
		return []string{
			qgTheme.accent.Render("Welcome to Quebra Galho"),
			"",
			qgTheme.ink.Render("A terminal agent for changing real code."),
			qgTheme.faint.Render("Plan changes, edit with approval, run checks, and resume sessions."),
			"",
			qgTheme.faint.Render("Re-enter your API key anytime with: qg login"),
		}
	}
}

func (m model) setupModelLines(width int, height int) []string {
	m.ensureSetupModels()
	rowWidth := setupModelBlockWidth(width, m.setup.models)
	models := m.setupFilteredModels()
	maxVisible := setupModelMaxVisible(height, len(models))
	start := selectableListStart(len(models), maxVisible, m.setup.modelIndex)
	lines := []string{
		padSetupLine("  "+qgTheme.ink.Bold(true).Render("Choose a model"), rowWidth),
		blankSetupBlockLine(rowWidth),
		padSetupLine("  "+m.setupModelSearchLine(rowWidth-2), rowWidth),
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

func blankSetupBlockLine(width int) string {
	if width <= 0 {
		return ""
	}
	return strings.Repeat(" ", width)
}

func (m model) setupCredentialLines(width int) []string {
	return []string{
		qgTheme.ink.Bold(true).Render("Your Quebra Galho API key"),
		"",
		"Paste the key, then Enter to validate it against the gateway.",
		"It is read as QG_API_KEY or QUEBRAGALHO_API_KEY from your shell later.",
		"",
		m.setupAPIKeyInputLine(width),
		"",
		qgTheme.faint.Render("Saved keys stay encrypted in your user config."),
		qgTheme.faint.Render("Re-enter it anytime with: qg login"),
	}
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

func (m model) setupFooter() string {
	switch m.setup.stage {
	case setupStageCredentials:
		if m.setup.keyCheck {
			return qgTheme.faint.Render("validating key...")
		}
		return qgTheme.faint.Render("paste key   ") + qgTheme.accent.Render("Enter") + qgTheme.faint.Render(" validate & continue   left back")
	case setupStageModel:
		return qgTheme.faint.Render("↑/↓ choose   type search   ") + qgTheme.accent.Render("Enter") + qgTheme.faint.Render(" continue")
	default:
		return qgTheme.accent.Render("Space") + qgTheme.faint.Render(" to set up QG")
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
