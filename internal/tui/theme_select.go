package tui

import (
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// themeMode is the operator's palette preference.
type themeMode string

const (
	themeSystem themeMode = "system" // preserve the terminal canvas (default)
	themeAuto   themeMode = "auto"   // legacy alias for system
	themeDark   themeMode = "dark"   // migrated old saved preference
	themeLight  themeMode = "light"  // migrated old saved preference
)

// themeModes lists the values /theme presents, in picker order: `system` first,
// then named palettes. Former dark/light saved preferences migrate to System and
// no longer appear as choices.
var themeModes = append([]string{string(themeSystem)}, selectableThemeNames()...)

func selectableThemeNames() []string {
	names := make([]string, 0, len(themeRegistry))
	for _, entry := range themeRegistry {
		if entry.Name == string(themeDark) || entry.Name == string(themeLight) {
			continue
		}
		names = append(names, entry.Name)
	}
	return names
}

// resolveThemeMode picks the first accepted preference from candidates in
// precedence order — the caller passes them highest-first: the --theme flag, then
// QG_THEME, then the persisted config theme. A value is accepted if it is
// `system`, the legacy `auto` alias, or names a registered theme. Unrecognized/
// blank values are skipped, and an empty list (or all-unrecognized) falls back to
// system.
func resolveThemeMode(candidates ...string) themeMode {
	for _, v := range candidates {
		s := strings.ToLower(strings.TrimSpace(v))
		if s == "" {
			continue
		}
		if s == string(themeSystem) || s == string(themeAuto) || s == string(themeDark) || s == string(themeLight) {
			return themeSystem
		}
		if _, ok := lookupTheme(s); ok {
			return themeMode(s)
		}
	}
	return themeSystem
}

// validThemeMode reports whether s names a theme mode (for /theme validation):
// `system`, legacy `auto`, or any visible named palette.
func validThemeMode(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == string(themeSystem) || s == string(themeAuto) {
		return true
	}
	if s == string(themeDark) || s == string(themeLight) {
		return false
	}
	_, ok := lookupTheme(s)
	return ok
}

// ValidThemeArg reports whether s is an acceptable --theme / QG_THEME value.
// It accepts retired dark/light preferences so existing scripts and environment
// variables migrate to System, while validThemeMode keeps them out of the picker.
func ValidThemeArg(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == string(themeDark) || s == string(themeLight) {
		return true
	}
	return validThemeMode(s)
}

// ApplyUpdatePromptTheme resolves and applies the update prompt palette with
// exactly the main-TUI procedure: --theme flag, QG_THEME env, saved
// preference, then the terminal background. Call it right before showing the
// startup prompt so the first frame already matches the TUI theme. Like the
// main TUI (which starts with hasDarkBg true), the first frame assumes a dark
// terminal; the prompt program then asks the terminal for its background and
// corrects itself on BackgroundColorMsg. It returns the resolved mode so the
// program can reapply it when the terminal answers.
func ApplyUpdatePromptTheme(flagTheme, savedTheme, qgThemeEnv string) themeMode {
	mode := resolveThemeMode(flagTheme, strings.TrimSpace(qgThemeEnv), savedTheme)
	applyTheme(mode, true)
	updatePromptThemeMode = mode
	return mode
}

// applyTheme swaps the active palette (qgTheme) and the globals derived from it
// — the streaming-fade ramp and the static render cache — so a committed switch
// repaints every subsequent render. `system` (and legacy `auto`) preserve the
// terminal canvas. Named palettes adapt their contrast direction to the terminal
// background without painting it. Must run on the Bubble Tea update goroutine (or
// before the program starts), like every other qgTheme access.
func applyTheme(mode themeMode, terminalDark bool) themeMode {
	resolved, theme := themeForMode(mode, terminalDark)
	qgTheme = theme
	rebuildStreamingFadePalette()
	if defaultRenderCache != nil {
		defaultRenderCache.clear() // old-palette entries must not be reused
	}
	return resolved
}

// themeForMode resolves a candidate without mutating qgTheme. The /theme picker
// uses it to render a contained preview while the active UI remains untouched.
func themeForMode(mode themeMode, terminalDark bool) (themeMode, tuiTheme) {
	if mode == themeSystem || mode == themeAuto || mode == "" {
		return themeSystem, buildSystemThemeForTerminal(terminalDark)
	}
	if entry, ok := lookupTheme(string(mode)); ok {
		theme := buildTheme(paletteForTerminal(entry.Palette, entry.IsDark, terminalDark))
		if entry.IsDark == terminalDark {
			theme.codeTheme = codeThemeForMode(themeMode(entry.Name))
		}
		return themeMode(entry.Name), theme
	}
	return themeSystem, buildSystemThemeForTerminal(terminalDark)
}

// handleThemeCommand implements /theme [name]: `list` shows state, a registered
// theme name (or legacy `auto`) switches the active palette. Bare `/theme` opens
// the picker at the dispatch layer, so it never reaches here empty.
func (m model) handleThemeCommand(args string) (model, string) {
	arg := strings.ToLower(strings.TrimSpace(args))
	if arg == "" || arg == "list" {
		return m, m.themeStateText()
	}
	if !validThemeMode(arg) {
		return m, "Tema\nTema desconhecido: " + arg + " (use /theme sem argumento para escolher na lista)"
	}
	m.themeMode = resolveThemeMode(arg)
	active := string(applyTheme(m.themeMode, m.hasDarkBg))
	lines := []string{
		"Tema",
		"tema ativo: " + active,
		"A rolagem já impressa mantém as cores anteriores; a saída nova usa o novo tema.",
	}
	if note := m.persistThemePreference(); note != "" {
		lines = append(lines, note)
	}
	return m, strings.Join(lines, "\n")
}

// themeAppliedNotice returns the compact confirmation used after a successful
// theme switch. The full state card remains available through /theme list.
func (m model) themeAppliedNotice() string {
	if m.themeMode == themeSystem {
		return "Tema: Sistema"
	}
	if entry, ok := lookupTheme(string(m.themeMode)); ok {
		return "Tema: " + entry.Label
	}
	return "Tema: " + string(m.themeMode)
}

// persistThemePreference writes the committed theme to user config so it is applied
// again at startup (via Options.SavedTheme -> resolveThemeMode). Best-effort: returns
// a short note to surface on failure, or "" on success / when there is no config
// path (e.g. tests).
func (m model) persistThemePreference() string {
	if strings.TrimSpace(m.userConfigPath) == "" {
		return ""
	}
	if _, err := config.SetTheme(m.userConfigPath, string(m.themeMode)); err != nil {
		return "nota: não foi possível salvar a preferência de tema (" + err.Error() + ")"
	}
	return ""
}

// themeStateText renders the /theme state view.
func (m model) themeStateText() string {
	return renderCommandOutput(commandOutput{
		Title:  "Tema",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "Estado",
			Lines: []string{
				"tema ativo: " + m.themeDisplayLabel(),
				"disponíveis: " + strings.Join(themeModes, ", "),
			},
		}},
		Hints: []string{"use /theme sem argumento para abrir o seletor, ou /theme <nome> para trocar direto"},
	})
}

func (m model) themeDisplayLabel() string {
	if m.themeMode == themeSystem {
		return "Sistema"
	}
	if entry, ok := lookupTheme(string(m.themeMode)); ok {
		return entry.Label
	}
	return string(m.themeMode)
}
