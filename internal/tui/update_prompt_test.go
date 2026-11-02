package tui

import (
	"context"
	"errors"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

var errUpdatePromptTest = errors.New("no browser here")

var noopUpdateInstall = func(context.Context) error { return nil }

func updatePromptModelForTest(current, latest, changelog string, openURL func(string) error) updatePromptModel {
	return newUpdatePromptModel(current, latest, changelog, openURL, noopUpdateInstall, context.Background())
}

func strippedUpdatePromptView(model updatePromptModel, width, height int) string {
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return ansi.Strip(updated.(updatePromptModel).View().Content)
}

func updatePromptViewLines(t *testing.T, model updatePromptModel, width, height int) []string {
	t.Helper()
	return strings.Split(strippedUpdatePromptView(model, width, height), "\n")
}

func driveUpdatePromptKeys(t *testing.T, model updatePromptModel, keys []tea.KeyPressMsg) updatePromptModel {
	t.Helper()
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	model = updated.(updatePromptModel)
	for _, key := range keys {
		if model.done || model.installing || model.failed {
			break
		}
		updated, _ := model.Update(key)
		model = updated.(updatePromptModel)
	}
	return model
}

// finishUpdateInstall runs the install command the prompt returned and feeds
// the completion back, like Bubble Tea would.
func finishUpdateInstall(t *testing.T, model updatePromptModel, cmd tea.Cmd) updatePromptModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected an install command")
	}
	msg, ok := cmd().(updatePromptInstallDoneMsg)
	if !ok {
		t.Fatalf("install command returned %T, want the install result", msg)
	}
	updated, _ := model.Update(msg)
	return updated.(updatePromptModel)
}

func TestUpdatePromptChoices(t *testing.T) {
	noop := func(string) error { return nil }
	for _, test := range []struct {
		name   string
		keys   []tea.KeyPressMsg
		choice UpdateChoice
	}{
		{name: "digit two skips", keys: []tea.KeyPressMsg{testKeyText("2")}, choice: UpdateSkip},
		{name: "digit four quits", keys: []tea.KeyPressMsg{testKeyText("4")}, choice: UpdateQuit},
		{name: "escape quits", keys: []tea.KeyPressMsg{testKey(tea.KeyEsc)}, choice: UpdateQuit},
		{name: "up from first selects quit", keys: []tea.KeyPressMsg{testKey(tea.KeyUp), testKey(tea.KeyEnter)}, choice: UpdateQuit},
		{name: "down wraps to first then enter installs", keys: []tea.KeyPressMsg{testKey(tea.KeyDown), testKey(tea.KeyDown), testKey(tea.KeyDown), testKey(tea.KeyDown), testKey(tea.KeyEnter)}, choice: UpdateInstall},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := driveUpdatePromptKeys(t, updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", noop), test.keys)
			if test.choice == UpdateInstall {
				if !model.installing || model.done {
					t.Fatal("choosing install must start installing, not finish")
				}
				return
			}
			if !model.done {
				t.Fatal("prompt did not finish")
			}
			if model.choice != test.choice {
				t.Fatalf("choice = %v, want %v", model.choice, test.choice)
			}
		})
	}
}

func TestUpdatePromptInstallRunsAndChooses(t *testing.T) {
	installed := 0
	model := updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", nil)
	model.install = func(context.Context) error {
		installed++
		return nil
	}
	model = driveUpdatePromptKeys(t, model, []tea.KeyPressMsg{testKey(tea.KeyEnter)})
	if !model.installing || model.done {
		t.Fatal("enter on install must start installing, not finish")
	}
	if view := strippedUpdatePromptView(model, 120, 0); !strings.Contains(view, "Instalando a versão 0.0.11") {
		t.Fatalf("installing state must announce the install, got:\n%s", view)
	}
	// Extra keys while installing are swallowed: no duplicate installs.
	updated, _ := model.Update(testKey(tea.KeyEnter))
	model = updated.(updatePromptModel)
	updated, _ = model.Update(testKey(tea.KeyEsc))
	model = updated.(updatePromptModel)
	if !model.installing || model.done {
		t.Fatal("input during install must be ignored")
	}
	model = finishUpdateInstall(t, model, updatePromptInstallCmd(model.ctx, model.install))
	if !model.done || model.choice != UpdateInstall {
		t.Fatalf("install success must choose install, got done=%v choice=%v", model.done, model.choice)
	}
	if installed != 1 {
		t.Fatalf("install ran %d times, want exactly once", installed)
	}
}

func TestUpdatePromptInstallFailureShowsCardThenProceeds(t *testing.T) {
	model := updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", nil)
	model.install = func(context.Context) error { return errors.New("network down") }
	model = driveUpdatePromptKeys(t, model, []tea.KeyPressMsg{testKeyText("1")})
	updated, _ := model.Update(updatePromptInstallDoneMsg{err: errors.New("network down")})
	model = updated.(updatePromptModel)
	if model.done || !model.failed {
		t.Fatal("a failed install must show the failure card, not finish")
	}
	view := strippedUpdatePromptView(model, 120, 0)
	if !strings.Contains(view, "Não foi possível atualizar") || !strings.Contains(view, "network down") || !strings.Contains(view, "Iniciando a versão atual.") {
		t.Fatalf("failure card must name the cause, got:\n%s", view)
	}
	updated, _ = model.Update(updatePromptFailureDoneMsg{})
	model = updated.(updatePromptModel)
	if !model.done || model.choice != UpdateInstallFailed {
		t.Fatalf("after the failure pause, want failed-install, got done=%v choice=%v", model.done, model.choice)
	}
}

func TestUpdatePromptEnterOnChangelogOpensWithoutChoosing(t *testing.T) {
	var opened []string
	model := updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", func(url string) error {
		opened = append(opened, url)
		return nil
	})
	model = driveUpdatePromptKeys(t, model, []tea.KeyPressMsg{testKey(tea.KeyDown), testKey(tea.KeyDown), testKey(tea.KeyEnter)})
	if model.done {
		t.Fatal("opening the changelog must not finish the prompt")
	}
	if model.focus != 2 {
		t.Fatalf("focus = %d, want it to stay on the changelog option", model.focus)
	}
	if len(opened) != 1 || opened[0] != "https://example.com/CHANGELOG.md" {
		t.Fatalf("openURL calls = %#v, want exactly the changelog link", opened)
	}
}

func TestUpdatePromptChangelogOpensBrowser(t *testing.T) {
	var opened []string
	model := updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", func(url string) error {
		opened = append(opened, url)
		return nil
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	model = updated.(updatePromptModel)
	updated, _ = model.Update(testKeyText("3"))
	model = updated.(updatePromptModel)

	if len(opened) != 1 || opened[0] != "https://example.com/CHANGELOG.md" {
		t.Fatalf("openURL calls = %#v, want exactly the changelog link", opened)
	}
	if model.done {
		t.Fatal("opening the changelog must keep the prompt open")
	}
	if model.focus != 2 {
		t.Fatalf("focus = %d, want it to stay on the changelog option", model.focus)
	}
	if view := ansi.Strip(model.View().Content); !strings.Contains(view, "Changelog enviado ao navegador.") {
		t.Fatalf("view must confirm the handoff, got:\n%s", view)
	}
}

func TestUpdatePromptChangelogFailureShowsLink(t *testing.T) {
	model := updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", func(string) error {
		return errUpdatePromptTest
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	model = updated.(updatePromptModel)
	updated, _ = model.Update(testKeyText("3"))
	model = updated.(updatePromptModel)

	if model.done {
		t.Fatal("a failed browser open must keep the prompt open")
	}
	view := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"Não foi possível abrir o navegador.",
		"Copie o link abaixo.",
		"https://example.com/CHANGELOG.md",
		"Instalar e iniciar",
		"Não instalar e iniciar",
		"Ler changelog",
		"Sair",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("view must contain %q, got:\n%s", want, view)
		}
	}
}

func TestUpdatePromptInitialView120(t *testing.T) {
	lines := updatePromptViewLines(t, updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", nil), 120, 0)
	if len(lines) != 15 {
		t.Fatalf("120-col card has %d lines, want 15", len(lines))
	}
	margin := strings.Repeat(" ", 18)
	want := []string{
		margin + "╭" + "──" + " QG · ATUALIZAÇÃO " + strings.Repeat("─", 62) + "╮",
		margin + "│ " + strings.Repeat(" ", 80) + " │",
		margin + "│ Nova versão disponível" + strings.Repeat(" ", 80-22) + " │",
		margin + "│ 0.0.10 → 0.0.11" + strings.Repeat(" ", 80-15) + " │",
		margin + "│ " + strings.Repeat(" ", 80) + " │",
		margin + "│ Atualize agora ou continue com a versão atual." + strings.Repeat(" ", 80-46) + " │",
		margin + "│ " + strings.Repeat(" ", 80) + " │",
		margin + "│ ❯ 1  Instalar e iniciar" + strings.Repeat(" ", 80-23) + " │",
		margin + "│   2  Não instalar e iniciar" + strings.Repeat(" ", 80-27) + " │",
		margin + "│   3  Ler changelog" + strings.Repeat(" ", 80-18) + " │",
		margin + "│   4  Sair" + strings.Repeat(" ", 80-9) + " │",
		margin + "│ " + strings.Repeat(" ", 80) + " │",
		margin + "│ " + strings.Repeat("─", 80) + " │",
		margin + "│ ↑/↓ mover   Enter confirmar   1–4 escolher   Esc sair" + strings.Repeat(" ", 80-53) + " │",
		margin + "╰" + strings.Repeat("─", 82) + "╯",
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d:\n got %q\nwant %q", i, lines[i], want[i])
		}
	}
}

func TestUpdatePromptInitialView80(t *testing.T) {
	lines := updatePromptViewLines(t, updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", nil), 80, 0)
	if len(lines) != 15 {
		t.Fatalf("80-col card has %d lines, want 15", len(lines))
	}
	margin := strings.Repeat(" ", 2)
	want := []string{
		margin + "╭" + "──" + " QG · ATUALIZAÇÃO " + strings.Repeat("─", 54) + "╮",
		margin + "│ " + strings.Repeat(" ", 72) + " │",
		margin + "│ Nova versão disponível" + strings.Repeat(" ", 72-22) + " │",
		margin + "│ 0.0.10 → 0.0.11" + strings.Repeat(" ", 72-15) + " │",
		margin + "│ " + strings.Repeat(" ", 72) + " │",
		margin + "│ Atualize agora ou continue com a versão atual." + strings.Repeat(" ", 72-46) + " │",
		margin + "│ " + strings.Repeat(" ", 72) + " │",
		margin + "│ ❯ 1  Instalar e iniciar" + strings.Repeat(" ", 72-23) + " │",
		margin + "│   2  Não instalar e iniciar" + strings.Repeat(" ", 72-27) + " │",
		margin + "│   3  Ler changelog" + strings.Repeat(" ", 72-18) + " │",
		margin + "│   4  Sair" + strings.Repeat(" ", 72-9) + " │",
		margin + "│ " + strings.Repeat(" ", 72) + " │",
		margin + "│ " + strings.Repeat("─", 72) + " │",
		margin + "│ ↑/↓ mover · Enter confirmar · 1–4 escolher · Esc sair" + strings.Repeat(" ", 72-53) + " │",
		margin + "╰" + strings.Repeat("─", 74) + "╯",
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d:\n got %q\nwant %q", i, lines[i], want[i])
		}
	}
}

func TestUpdatePromptInitialView50(t *testing.T) {
	lines := updatePromptViewLines(t, updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", nil), 50, 0)
	margin := strings.Repeat(" ", 2)
	dashes := strings.Repeat("─", 46)
	want := []string{
		margin + "QG · ATUALIZAÇÃO",
		margin + dashes,
		margin,
		margin + "Nova versão disponível",
		margin + "0.0.10 → 0.0.11",
		margin,
		margin + "Atualize agora ou continue com a versão atual.",
		margin,
		margin + "❯ 1  Instalar e iniciar",
		margin + "  2  Não instalar e iniciar",
		margin + "  3  Ler changelog",
		margin + "  4  Sair",
		margin,
		margin + dashes,
		margin + "↑/↓ mover · Enter confirmar",
		margin + "1–4 escolher · Esc sair",
	}
	if len(lines) != len(want) {
		t.Fatalf("50-col card has %d lines, want %d:\n%q", len(lines), len(want), strings.Join(lines, "\n"))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d:\n got %q\nwant %q", i, lines[i], want[i])
		}
	}
}

func TestUpdatePromptBrowserFailView80(t *testing.T) {
	model := updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", func(string) error {
		return errUpdatePromptTest
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 0})
	model = updated.(updatePromptModel)
	updated, _ = model.Update(testKeyText("3"))
	model = updated.(updatePromptModel)
	view := ansi.Strip(model.View().Content)
	lines := strings.Split(view, "\n")
	// Eighteen card lines plus a blank line and the bare link outside the frame.
	if len(lines) != 20 {
		t.Fatalf("fail view has %d lines, want 20 (18 card + blank + link):\n%s", len(lines), view)
	}
	if got := lines[18]; got != "" {
		t.Fatalf("line after the frame = %q, want a blank line", got)
	}
	if got := lines[19]; got != "https://example.com/CHANGELOG.md" {
		t.Fatalf("link line = %q, want the bare URL with no indent", got)
	}
	// The frame itself ends at line 18; the link must not be inside a bordered line.
	for _, line := range lines[:18] {
		if strings.Contains(line, "https://") {
			t.Fatalf("link must sit outside the frame, got:\n%s", view)
		}
	}
	foundFail := 0
	for _, line := range lines {
		if strings.Contains(line, "Não foi possível abrir o navegador.") || strings.Contains(line, "Copie o link abaixo.") {
			foundFail++
		}
	}
	if foundFail != 2 {
		t.Fatalf("fail state lines missing, got:\n%s", view)
	}
}

func TestUpdatePromptFocusFollowsKeys(t *testing.T) {
	model := updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", nil)
	model = driveUpdatePromptKeys(t, model, []tea.KeyPressMsg{testKey(tea.KeyDown)})
	view := strippedUpdatePromptView(model, 80, 0)
	if !strings.Contains(view, "│ ❯ 2  Não instalar e iniciar") {
		t.Fatalf("focus did not move to option 2, got:\n%s", view)
	}
	if strings.Contains(view, "❯ 1") {
		t.Fatalf("cursor must leave option 1, got:\n%s", view)
	}
}

func TestUpdatePromptCentersVertically(t *testing.T) {
	view := strippedUpdatePromptView(updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", nil), 120, 31)
	lines := strings.Split(view, "\n")
	leading := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			leading++
			continue
		}
		break
	}
	// 15-line card in a 31-line viewport: 8 blank lines on top.
	if leading != 8 {
		t.Fatalf("leading blank lines = %d, want 8", leading)
	}
}

func TestApplyUpdatePromptThemeLightUsesLightAccent(t *testing.T) {
	previousTheme, previousMode := qgTheme, updatePromptThemeMode
	t.Cleanup(func() { qgTheme, updatePromptThemeMode = previousTheme, previousMode })
	// "light" is a retired saved preference: it migrates to system. The first
	// frame assumes a dark terminal (like the main TUI), then the background
	// probe corrects it to the #54700a light accent.
	ApplyUpdatePromptTheme("", "", "light")
	model := updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", nil)
	if model.Init() == nil {
		t.Fatal("prompt Init must request the terminal background color")
	}
	updated, _ := model.Update(tea.BackgroundColorMsg{Color: color.White})
	model = updated.(updatePromptModel)
	if got := colorHex(t, qgTheme.accentColor); got != "#54700a" {
		t.Fatalf("QG_THEME=light on a light terminal accent = %q, want #54700a", got)
	}
}

func TestApplyUpdatePromptThemeFlagBeatsEnv(t *testing.T) {
	previousTheme, previousMode := qgTheme, updatePromptThemeMode
	t.Cleanup(func() { qgTheme, updatePromptThemeMode = previousTheme, previousMode })
	ApplyUpdatePromptTheme("dracula", "", "light")
	if got := colorHex(t, qgTheme.accentColor); got != "#bd93f9" {
		t.Fatalf("--theme dracula accent = %q, want #bd93f9", got)
	}
}

func TestUpdatePromptNoColorContract(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"1", true},
		{"yes", true},
	} {
		if got := updatePromptNoColor(func(string) string { return tc.value }); got != tc.want {
			t.Fatalf("NO_COLOR=%q -> %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestUpdatePromptAsciiFallback(t *testing.T) {
	t.Setenv("LANG", "C")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")
	model := updatePromptModelForTest("0.0.10", "0.0.11", "https://example.com/CHANGELOG.md", nil)
	view := strippedUpdatePromptView(model, 80, 0)
	for _, want := range []string{"+-- QG", "--+", "> 1  Instalar e iniciar", "  2  Não instalar e iniciar", "| Nova versão", "0.0.10 -> 0.0.11", "Up/Down mover - Enter confirmar - 1-4 escolher - Esc sair"} {
		if !strings.Contains(view, want) {
			t.Fatalf("ascii fallback must contain %q, got:\n%s", want, view)
		}
	}
	if strings.Contains(view, "│") || strings.Contains(view, "❯") || strings.Contains(view, "→") || strings.Contains(view, "─") {
		t.Fatalf("ascii fallback must not use box drawing, got:\n%s", view)
	}
}
