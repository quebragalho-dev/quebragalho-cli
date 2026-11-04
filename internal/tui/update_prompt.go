package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// UpdateChoice is the operator's answer on the new-version startup screen.
type UpdateChoice int

const (
	// UpdateInstall installs the new version and reopens qg.
	UpdateInstall UpdateChoice = iota + 1
	// UpdateSkip starts the current version and stops asking until a newer one shows up.
	UpdateSkip
	// UpdateQuit leaves without starting. Esc and Ctrl+C mean the same.
	UpdateQuit
	// UpdateInstallFailed proceeds to the current version after a failed
	// install. Unlike Skip it records no refusal: the next launch asks again.
	UpdateInstallFailed
)

func (choice UpdateChoice) String() string {
	switch choice {
	case UpdateInstall:
		return "install"
	case UpdateSkip:
		return "skip"
	case UpdateQuit:
		return "quit"
	case UpdateInstallFailed:
		return "install-failed"
	default:
		return "unknown"
	}
}

// updatePromptOptions are the four actions on the startup screen, in order.
var updatePromptOptions = []string{
	"Instalar e iniciar",
	"Não instalar e iniciar",
	"Ler changelog",
	"Sair",
}

// RunUpdatePrompt shows the new-version startup screen and returns the chosen
// action. It draws on the current terminal (no alt screen) and never touches
// the network: the version is decided by the caller from stored state.
// Choosing install runs install in the background while the screen shows a
// progress state; a failed install shows the failure in the card before
// returning. A program error is returned as-is; the caller must fail open
// (proceed).
func RunUpdatePrompt(ctx context.Context, current, latest, changelogURL string, openURL func(string) error, install func(ctx context.Context) error) (UpdateChoice, error) {
	programOpts := []tea.ProgramOption{tea.WithContext(ctx)}
	if noColorRequested(os.Getenv) {
		programOpts = append(programOpts, tea.WithColorProfile(colorprofile.Ascii))
	}
	program := tea.NewProgram(newUpdatePromptModel(current, latest, changelogURL, openURL, install, ctx), programOpts...)
	final, err := program.Run()
	if err != nil {
		return 0, err
	}
	model, ok := final.(updatePromptModel)
	if !ok {
		return 0, fmt.Errorf("update prompt ended with %T, want updatePromptModel", final)
	}
	if !model.done {
		return UpdateQuit, nil
	}
	return model.choice, nil
}

// updatePromptNoColor mirrors run.go: the NO_COLOR env forces the Ascii
// profile (no color, bold kept) instead of the detected one. It exists so
// tests can pin the contract without spawning a Bubble Tea program.
func updatePromptNoColor(getenv func(string) string) bool {
	return noColorRequested(getenv)
}

// updatePromptThemeMode is the last mode resolved by ApplyUpdatePromptTheme.
// The prompt program reads it at construction so it can reapply the same
// mode when the terminal reports its background color.
var updatePromptThemeMode themeMode

type updatePromptModel struct {
	ctx           context.Context
	current       string
	latest        string
	changelogURL  string
	openURL       func(string) error
	install       func(ctx context.Context) error
	themeMode     themeMode
	focus         int
	width         int
	height        int
	ascii         bool
	browserSent   bool
	browserFail   bool
	installing    bool
	spinner       int
	failed        bool
	failureReason string
	done          bool
	choice        UpdateChoice
}

func newUpdatePromptModel(current, latest, changelogURL string, openURL func(string) error, install func(ctx context.Context) error, ctx context.Context) updatePromptModel {
	if ctx == nil {
		ctx = context.Background()
	}
	return updatePromptModel{
		ctx:          ctx,
		current:      current,
		latest:       latest,
		changelogURL: changelogURL,
		openURL:      openURL,
		install:      install,
		themeMode:    updatePromptThemeMode,
		ascii:        updatePromptASCII(),
	}
}

// updatePromptASCII reports whether the terminal should get the ASCII fallback
// box drawing (">", "->", "+", "-", "|") instead of Unicode. An explicit
// non-UTF-8 locale opts out; anything else (including unset) keeps Unicode.
func updatePromptASCII() bool {
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			lowered := strings.ToLower(value)
			return !strings.Contains(lowered, "utf-8") && !strings.Contains(lowered, "utf8")
		}
	}
	return false
}

func (m updatePromptModel) Init() tea.Cmd {
	// Read the terminal background only to keep a selected palette legible,
	// like the main TUI: the first frame uses the dark guess applied before
	// the program started, and corrects itself when the terminal answers.
	return tea.RequestBackgroundColor
}

// updatePromptInstallDoneMsg reports the result of the background install.
type updatePromptInstallDoneMsg struct {
	err error
}

// updatePromptSpinnerMsg advances the installing indicator.
type updatePromptSpinnerMsg struct{}

// updatePromptFailureDoneMsg ends the failure display and opens the current version.
type updatePromptFailureDoneMsg struct{}

// updateInstallSpinnerFrames cycles while installing; the ASCII set keeps the
// card aligned on terminals without Unicode.
func updateInstallSpinnerFrames(ascii bool) []string {
	if ascii {
		return []string{"-", "\\", "|", "/"}
	}
	return []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
}

const (
	updatePromptSpinnerInterval = 100 * time.Millisecond
	// updatePromptFailureDelay shows the failure card before moving on, so a
	// fast terminal does not flash it past unreadably.
	updatePromptFailureDelay = 1500 * time.Millisecond
)

func (m updatePromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		// Reapply the resolved mode against the real terminal background,
		// like the main TUI: a light terminal switches a system theme to the
		// light palette without repainting the terminal canvas.
		applyTheme(m.themeMode, msg.IsDark())
		return m, nil
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		return m, nil
	case updatePromptInstallDoneMsg:
		if !m.installing {
			return m, nil
		}
		m.installing = false
		if msg.err != nil {
			m.failed = true
			m.browserSent = false
			m.browserFail = false
			m.failureReason = msg.err.Error()
			return m, tea.Tick(updatePromptFailureDelay, func(time.Time) tea.Msg {
				return updatePromptFailureDoneMsg{}
			})
		}
		m.chooseUpdateOption(UpdateInstall)
		return m, tea.Quit
	case updatePromptSpinnerMsg:
		if !m.installing {
			return m, nil
		}
		m.spinner++
		return m, updatePromptSpinnerTick()
	case updatePromptFailureDoneMsg:
		if !m.failed {
			return m, nil
		}
		m.chooseUpdateOption(UpdateInstallFailed)
		return m, tea.Quit
	case tea.KeyPressMsg:
		return m.handleUpdatePromptKey(msg)
	}
	return m, nil
}

func updatePromptSpinnerTick() tea.Cmd {
	return tea.Tick(updatePromptSpinnerInterval, func(time.Time) tea.Msg {
		return updatePromptSpinnerMsg{}
	})
}

// updatePromptInstallCmd runs the install without blocking the screen and
// reports its outcome back to the model.
func updatePromptInstallCmd(ctx context.Context, install func(ctx context.Context) error) tea.Cmd {
	return func() tea.Msg {
		if install == nil {
			return updatePromptInstallDoneMsg{err: errors.New("instalação indisponível")}
		}
		return updatePromptInstallDoneMsg{err: install(ctx)}
	}
}

// sanitizeUpdateReason collapses an install error to one line, capping its
// length so the failure card stays readable.
func sanitizeUpdateReason(reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	if runes := []rune(reason); len(runes) > 120 {
		reason = string(runes[:120]) + "…"
	}
	return reason
}

// updateInstallFailureLines renders "Não foi possível atualizar" with the
// sanitized reason on one line when it fits, or the reason-free two-line form
// on narrow cards.
func updateInstallFailureLines(inner int, reason string) []string {
	reason = sanitizeUpdateReason(reason)
	if reason != "" {
		if single := "Não foi possível atualizar: " + reason + ". Iniciando a versão atual."; lipgloss.Width(single) <= inner {
			return []string{single}
		}
	}
	return []string{"Não foi possível atualizar.", "Iniciando a versão atual."}
}

func (m updatePromptModel) handleUpdatePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.done || m.installing || m.failed {
		// No duplicate activations while installing and no dismissal of the
		// failure card: both states resolve on their own timer or completion.
		return m, nil
	}
	switch {
	case keyIs(msg, tea.KeyUp):
		m.focus = (m.focus + len(updatePromptOptions) - 1) % len(updatePromptOptions)
		return m, nil
	case keyIs(msg, tea.KeyDown):
		m.focus = (m.focus + 1) % len(updatePromptOptions)
		return m, nil
	case keyIs(msg, tea.KeyEnter):
		return m.activateUpdateOption(m.focus)
	case keyIs(msg, tea.KeyEsc) || keyCtrl(msg, 'c'):
		m.chooseUpdateOption(UpdateQuit)
		return m, tea.Quit
	case keyText(msg) != "" && !keyAlt(msg) && !keyHasMod(msg, tea.ModCtrl):
		switch keyText(msg) {
		case "1", "2", "3", "4":
			return m.activateUpdateOption(int(keyText(msg)[0] - '1'))
		}
	}
	return m, nil
}

func (m updatePromptModel) activateUpdateOption(index int) (tea.Model, tea.Cmd) {
	m.focus = index
	switch index {
	case 0:
		// Install runs in the background while the card shows progress; the
		// choice is only reported once it finishes.
		m.installing = true
		m.spinner = 0
		m.browserSent = false
		m.browserFail = false
		return m, tea.Batch(updatePromptSpinnerTick(), updatePromptInstallCmd(m.ctx, m.install))
	case 1:
		m.chooseUpdateOption(UpdateSkip)
		return m, tea.Quit
	case 2:
		if m.openURL == nil {
			m.browserFail = true
			m.browserSent = false
			return m, nil
		}
		if err := m.openURL(m.changelogURL); err != nil {
			m.browserFail = true
			m.browserSent = false
			return m, nil
		}
		m.browserFail = false
		m.browserSent = true
		return m, nil
	default:
		m.chooseUpdateOption(UpdateQuit)
		return m, tea.Quit
	}
}

func (m *updatePromptModel) chooseUpdateOption(choice UpdateChoice) {
	m.choice = choice
	m.done = true
}

// updateBoxRunes is the frame and cursor drawing set. The ASCII fallback
// keeps the layout byte-identical in width on terminals without Unicode.
type updateBoxRunes struct {
	topLeft     string
	topRight    string
	bottomLeft  string
	bottomRight string
	horizontal  string
	vertical    string
	cursor      string
	arrow       string
}

func updatePromptRunes(ascii bool) updateBoxRunes {
	if ascii {
		return updateBoxRunes{topLeft: "+", topRight: "+", bottomLeft: "+", bottomRight: "+", horizontal: "-", vertical: "|", cursor: ">", arrow: "->"}
	}
	return updateBoxRunes{topLeft: "╭", topRight: "╮", bottomLeft: "╰", bottomRight: "╯", horizontal: "─", vertical: "│", cursor: "❯", arrow: "→"}
}

func (m updatePromptModel) View() tea.View {
	return tea.NewView(m.renderUpdatePrompt())
}

func (m updatePromptModel) cardInnerWidth() int {
	width := m.width
	if width <= 0 {
		width = 80
	}
	cardWidth := minInt(84, width-4)
	if cardWidth < 4 {
		cardWidth = 4
	}
	if cardWidth < 60 {
		return maxInt(1, width-4)
	}
	return cardWidth - 4
}

func (m updatePromptModel) renderUpdatePrompt() string {
	runes := updatePromptRunes(m.ascii)
	width := m.width
	if width <= 0 {
		width = 80
	}
	cardWidth := minInt(84, width-4)
	if cardWidth < 4 {
		cardWidth = 4
	}
	var lines []string
	if cardWidth >= 60 {
		lines = m.borderedUpdateCard(runes, cardWidth)
		margin := strings.Repeat(" ", maxInt(0, (width-cardWidth)/2))
		for index, line := range lines {
			lines[index] = margin + line
		}
	} else {
		lines = m.plainUpdateCard(runes, width-4)
		// The link below the card stays at column zero so copies stay clean;
		// everything else carries the two-space margin. The link is appended
		// after this loop, so no exception is needed here.
		margin := "  "
		for index, line := range lines {
			lines[index] = margin + line
		}
	}
	if m.browserFail {
		lines = append(lines, "", m.changelogURL)
	}
	if m.height > len(lines) {
		top := (m.height - len(lines)) / 2
		pad := make([]string, 0, top+(m.height-len(lines)-top))
		for range top {
			pad = append(pad, "")
		}
		lines = append(pad, lines...)
		for len(lines) < m.height {
			lines = append(lines, "")
		}
	}
	return strings.Join(lines, "\n")
}

// borderedUpdateCard renders the framed card. inner is the content width
// between the one-space padding on each side.
func (m updatePromptModel) borderedUpdateCard(runes updateBoxRunes, cardWidth int) []string {
	inner := cardWidth - 4
	bar := strings.Repeat(runes.horizontal, inner)
	title := " " + brandWordmarkText() + " · " + qgTheme.amber.Bold(true).Render("ATUALIZAÇÃO") + " "
	top := qgTheme.lineStrong.Render(runes.topLeft+strings.Repeat(runes.horizontal, 2)) + title + qgTheme.lineStrong.Render(strings.Repeat(runes.horizontal, maxInt(0, inner-lipgloss.Width(title)))+runes.topRight)
	bottom := qgTheme.lineStrong.Render(runes.bottomLeft + strings.Repeat(runes.horizontal, cardWidth-2) + runes.bottomRight)
	line := func(content string) string {
		fitted := fitStyledLine(content, inner)
		gap := qgTheme.ink.Render(strings.Repeat(" ", maxInt(0, inner-lipgloss.Width(fitted))))
		return qgTheme.lineStrong.Render(runes.vertical+" ") + fitted + gap + qgTheme.lineStrong.Render(" "+runes.vertical)
	}
	blank := line("")
	lines := []string{
		top,
		blank,
		line(qgTheme.ink.Bold(true).Render("Nova versão disponível")),
		line(qgTheme.muted.Render(m.current) + " " + qgTheme.lineStrong.Render(runes.arrow) + " " + qgTheme.accent.Render(m.latest)),
		blank,
		line(m.explainerLine(runes)),
		blank,
	}
	focused := qgTheme.onSel
	for index, label := range updatePromptOptions {
		number := qgTheme.faint.Bold(true).Render(fmt.Sprintf("%d", index+1))
		text := qgTheme.ink.Bold(true).Render(label)
		row := "  " + number + "  " + text
		if index == m.focus {
			number = focused(qgTheme.ink.Bold(true)).Render(fmt.Sprintf("%d", index+1))
			text = focused(qgTheme.ink.Bold(true)).Render(label)
			row = focused(qgTheme.accent).Render(runes.cursor) + focused(qgTheme.ink).Render(" ") + number + focused(qgTheme.ink).Render("  ") + text
			row += focused(qgTheme.ink).Render(strings.Repeat(" ", maxInt(0, inner-lipgloss.Width(row))))
		}
		lines = append(lines, line(row))
	}
	lines = append(lines, blank)
	if m.browserSent {
		lines = append(lines, line(qgTheme.muted.Render("Changelog enviado ao navegador.")))
	}
	if m.browserFail {
		lines = append(lines, line(qgTheme.amber.Render("Não foi possível abrir o navegador.")))
		lines = append(lines, line(qgTheme.muted.Render("Copie o link abaixo.")))
		lines = append(lines, blank)
	}
	if m.failed {
		for _, failureLine := range m.failureLines(inner) {
			lines = append(lines, line(failureLine))
		}
		lines = append(lines, blank)
	}
	lines = append(lines, line(qgTheme.line.Render(bar)))
	lines = append(lines, line(updatePromptHelp(m.ascii, inner)))
	lines = append(lines, bottom)
	return lines
}

// plainUpdateCard renders the borderless narrow layout. Every line is
// unindented here; View applies the two-space margin, except to the link
// below the card, which stays at column zero so copies stay clean.
func (m updatePromptModel) plainUpdateCard(runes updateBoxRunes, contentWidth int) []string {
	bar := strings.Repeat(runes.horizontal, contentWidth)
	title := brandWordmarkText() + " · " + qgTheme.amber.Bold(true).Render("ATUALIZAÇÃO")
	rule := qgTheme.lineStrong.Render(bar)
	lines := []string{
		title,
		rule,
		"",
		qgTheme.ink.Bold(true).Render("Nova versão disponível"),
		qgTheme.muted.Render(m.current) + " " + qgTheme.lineStrong.Render(runes.arrow) + " " + qgTheme.accent.Render(m.latest),
		"",
		m.explainerLine(runes),
		"",
	}
	focused := qgTheme.onSel
	for index, label := range updatePromptOptions {
		number := qgTheme.faint.Bold(true).Render(fmt.Sprintf("%d", index+1))
		text := qgTheme.ink.Bold(true).Render(label)
		row := "  " + number + "  " + text
		if index == m.focus {
			number = focused(qgTheme.ink.Bold(true)).Render(fmt.Sprintf("%d", index+1))
			text = focused(qgTheme.ink.Bold(true)).Render(label)
			row = focused(qgTheme.accent).Render(runes.cursor) + focused(qgTheme.ink).Render(" ") + number + focused(qgTheme.ink).Render("  ") + text
		}
		lines = append(lines, row)
	}
	lines = append(lines, "")
	if m.browserSent {
		lines = append(lines, qgTheme.muted.Render("Changelog enviado ao navegador."))
	}
	if m.browserFail {
		lines = append(lines, qgTheme.amber.Render("Não foi possível abrir o navegador."))
		lines = append(lines, qgTheme.muted.Render("Copie o link abaixo."))
		lines = append(lines, "")
	}
	if m.failed {
		lines = append(lines, m.failureLines(contentWidth)...)
		lines = append(lines, "")
	}
	lines = append(lines, rule)
	lines = append(lines, updatePromptNarrowHelp(m.ascii)...)
	return lines
}

// explainerLine renders the card's explanatory line: the install progress
// with an accent activity indicator while installing, the normal text
// otherwise.
func (m updatePromptModel) explainerLine(runes updateBoxRunes) string {
	if m.installing {
		frames := updateInstallSpinnerFrames(m.ascii)
		return qgTheme.accent.Render("Instalando a versão "+m.latest+"…") + " " + qgTheme.accent.Render(frames[m.spinner%len(frames)])
	}
	return qgTheme.muted.Render("Atualize agora ou continue com a versão atual.")
}

// failureLines renders the install failure, already styled, for the card the
// caller builds.
func (m updatePromptModel) failureLines(inner int) []string {
	out := make([]string, 0, len(updateInstallFailureLines(inner, m.failureReason)))
	for _, failureLine := range updateInstallFailureLines(inner, m.failureReason) {
		out = append(out, qgTheme.amber.Render(failureLine))
	}
	return out
}
func updatePromptHelp(ascii bool, inner int) string {
	key := qgTheme.ink.Render
	verb := qgTheme.faint.Render
	upDown, listSep, rangeDash := "↑/↓", "·", "–"
	if ascii {
		upDown, listSep, rangeDash = "Up/Down", "-", "-"
	}
	sep := verb(" " + listSep + " ")
	if inner >= 80 {
		sep = "   "
	}
	return key(upDown) + verb(" mover") + sep + key("Enter") + verb(" confirmar") + sep + key("1"+rangeDash+"4") + verb(" escolher") + sep + key("Esc") + verb(" sair")
}

func updatePromptNarrowHelp(ascii bool) []string {
	key := qgTheme.ink.Render
	verb := qgTheme.faint.Render
	upDown, listSep, rangeDash := "↑/↓", "·", "–"
	if ascii {
		upDown, listSep, rangeDash = "Up/Down", "-", "-"
	}
	sep := verb(" " + listSep + " ")
	return []string{
		key(upDown) + verb(" mover") + sep + key("Enter") + verb(" confirmar"),
		key("1"+rangeDash+"4") + verb(" escolher") + sep + key("Esc") + verb(" sair"),
	}
}
