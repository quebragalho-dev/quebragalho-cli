package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// autoCompactPanelModel builds a key-driven session model with the panel open:
// charTestModel for a working Update path, an unknown model name for the 200k
// fallback window, and a scratch user config when the test persists.
func autoCompactPanelModel(t *testing.T, limit int, withConfig bool) (model, string) {
	t.Helper()
	m := charTestModel(t, time.Now())
	path := ""
	if withConfig {
		path = filepath.Join(t.TempDir(), "config.json")
	}
	m.userConfigPath = path
	m.modelName = "autocompact-test-unknown"
	m.autoCompactLimit = limit
	return m.openAutoCompactPrompt(), path
}

func updateAutoCompactPrompt(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	updated, _ := m.Update(msg)
	got, ok := updated.(model)
	if !ok {
		t.Fatal("Update did not return a TUI model")
	}
	return got
}

func TestAutoCompactPanelRenders(t *testing.T) {
	m := charTestModel(t, time.Now())
	updated, _ := m.dispatchCommand(parseCommand("/autocompact"))
	got, ok := updated.(model)
	if !ok {
		t.Fatal("dispatchCommand did not return a TUI model")
	}
	if got.autoCompactPrompt == nil {
		t.Fatal("bare /autocompact should open the panel")
	}
	for _, width := range []int{100, 60} {
		plain := plainRender(t, got.autoCompactPromptOverlay(width))
		for _, want := range []string{
			"Auto-compact",
			"Compacta a conversa sozinho ao chegar perto do",
			"limite escolhido, mesmo que o modelo aceite mais.",
			"As mensagens antigas viram um resumo.",
			"Exemplo: /autocompact 400k",
			"Atual: automático",
			"(compacta em 140k tokens neste modelo)",
			"↑/↓ 50k em 50k · número, off ou auto",
			"Enter salva · Esc cancela",
		} {
			if !strings.Contains(plain, want) {
				t.Fatalf("overlay at width %d misses %q:\n%s", width, want, plain)
			}
		}
	}
	// Every explanatory line must survive the minimum overlay width.
	for _, line := range []string{
		"Compacta a conversa sozinho ao chegar perto do",
		"limite escolhido, mesmo que o modelo aceite mais.",
		"As mensagens antigas viram um resumo.",
		"Exemplo: /autocompact 400k",
	} {
		if w := lipgloss.Width(line); w > 52 {
			t.Fatalf("explanation line is %d cells, want at most 52: %q", w, line)
		}
	}
}

func TestAutoCompactPanelTypingApplies(t *testing.T) {
	m, path := autoCompactPanelModel(t, 0, true)
	for _, key := range []string{"4", "0", "0", "k"} {
		m = updateAutoCompactPrompt(t, m, testKeyText(key))
	}
	if m.autoCompactPrompt == nil || m.autoCompactPrompt.input != "400k" {
		t.Fatalf("typed input = %+v, want open prompt with 400k", m.autoCompactPrompt)
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyEnter))
	if m.autoCompactPrompt != nil {
		t.Fatal("Enter should close the panel")
	}
	if m.autoCompactLimit != 400000 {
		t.Fatalf("autoCompactLimit = %d, want 400000", m.autoCompactLimit)
	}
	if prefs := readAutoCompactFile(t, path); prefs["autoCompactLimit"] != 400000.0 {
		t.Fatalf("persisted autoCompactLimit = %v, want 400000", prefs["autoCompactLimit"])
	}
	if !strings.Contains(m.transientNotice.text, "Auto-compact: 400k tokens") {
		t.Fatalf("applied notice = %q", m.transientNotice.text)
	}
}

func TestAutoCompactPanelArrowSteps(t *testing.T) {
	m, _ := autoCompactPanelModel(t, 0, false)
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyUp))
	if m.autoCompactPrompt.input != "50k" {
		t.Fatalf("Up from empty = %q, want 50k", m.autoCompactPrompt.input)
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyUp))
	if m.autoCompactPrompt.input != "100k" {
		t.Fatalf("Up from 50k = %q, want 100k", m.autoCompactPrompt.input)
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyDown))
	if m.autoCompactPrompt.input != "50k" {
		t.Fatalf("Down from 100k = %q, want 50k", m.autoCompactPrompt.input)
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyDown))
	if m.autoCompactPrompt.input != "50k" {
		t.Fatalf("Down from 50k = %q, want to stay 50k", m.autoCompactPrompt.input)
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyRight))
	if m.autoCompactPrompt.input != "100k" {
		t.Fatalf("Right behaves as Up: %q, want 100k", m.autoCompactPrompt.input)
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyLeft))
	if m.autoCompactPrompt.input != "50k" {
		t.Fatalf("Left behaves as Down: %q, want 50k", m.autoCompactPrompt.input)
	}
}

func TestStepAutoCompactLimit(t *testing.T) {
	for _, tc := range []struct {
		current int
		up      bool
		want    int
	}{
		{0, true, 50_000},
		{0, false, 50_000},
		{120_000, true, 150_000},
		{150_000, true, 200_000},
		{2_000_000, true, 2_000_000},
		{120_000, false, 100_000},
		{100_000, false, 50_000},
		{50_000, false, 50_000},
		{30_000, false, 50_000},
	} {
		if got := stepAutoCompactLimit(tc.current, tc.up); got != tc.want {
			t.Errorf("stepAutoCompactLimit(%d, up=%v) = %d, want %d", tc.current, tc.up, got, tc.want)
		}
	}
}

func TestAutoCompactPanelOffAndAuto(t *testing.T) {
	m, _ := autoCompactPanelModel(t, 0, false)
	for _, key := range []string{"o", "f", "f"} {
		m = updateAutoCompactPrompt(t, m, testKeyText(key))
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyEnter))
	if m.autoCompactPrompt != nil {
		t.Fatal("Enter should close the panel")
	}
	if m.autoCompactLimit != config.AutoCompactOff {
		t.Fatalf("autoCompactLimit = %d, want off", m.autoCompactLimit)
	}

	m, _ = autoCompactPanelModel(t, 0, false)
	for _, key := range []string{"a", "u", "t", "o"} {
		m = updateAutoCompactPrompt(t, m, testKeyText(key))
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyEnter))
	if m.autoCompactLimit != 0 {
		t.Fatalf("autoCompactLimit = %d, want 0", m.autoCompactLimit)
	}
}

func TestAutoCompactPanelRejectsInvalid(t *testing.T) {
	m, path := autoCompactPanelModel(t, 400000, true)
	for _, key := range []string{"b", "a", "n", "a", "n", "a"} {
		m = updateAutoCompactPrompt(t, m, testKeyText(key))
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyEnter))
	if m.autoCompactPrompt == nil {
		t.Fatal("invalid Enter must keep the panel open")
	}
	if m.autoCompactPrompt.err != "Valor inválido: número a partir de 20k, off ou auto." {
		t.Fatalf("error line = %q", m.autoCompactPrompt.err)
	}
	// The rejection line must reach the screen in full even at the minimum
	// overlay width.
	for _, width := range []int{100, 56} {
		plain := plainRender(t, m.autoCompactPromptOverlay(width))
		if !strings.Contains(plain, "Valor inválido: número a partir de 20k, off ou auto.") {
			t.Fatalf("overlay at width %d should show the whole error line:\n%s", width, plain)
		}
	}
	if m.autoCompactLimit != 400000 {
		t.Fatalf("invalid input changed the limit to %d", m.autoCompactLimit)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid input must not write the config file")
	}
	m = updateAutoCompactPrompt(t, m, testKeyText("x"))
	if m.autoCompactPrompt.err != "" {
		t.Fatalf("the next key should clear the error, got %q", m.autoCompactPrompt.err)
	}
}

func TestAutoCompactPanelDismiss(t *testing.T) {
	m, _ := autoCompactPanelModel(t, 400000, false)
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyEsc))
	if m.autoCompactPrompt != nil {
		t.Fatal("Esc should close the panel")
	}
	if m.autoCompactLimit != 400000 {
		t.Fatalf("Esc changed the limit to %d", m.autoCompactLimit)
	}
	if m.transientNotice.text != "Auto-compact: nada alterado." {
		t.Fatalf("dismiss notice = %q", m.transientNotice.text)
	}

	empty, _ := autoCompactPanelModel(t, 0, false)
	empty = updateAutoCompactPrompt(t, empty, testKey(tea.KeyEnter))
	if empty.autoCompactPrompt != nil {
		t.Fatal("Enter on an empty input should close the panel")
	}
	if empty.transientNotice.text != "Auto-compact: nada alterado." {
		t.Fatalf("empty-Enter notice = %q", empty.transientNotice.text)
	}

	ctrlc, _ := autoCompactPanelModel(t, 0, false)
	ctrlc.exitConfirmActive = false
	ctrlc = updateAutoCompactPrompt(t, ctrlc, testKeyCtrl('c'))
	if ctrlc.autoCompactPrompt != nil {
		t.Fatal("Ctrl+C should close the panel")
	}
	if ctrlc.exitConfirmActive {
		t.Fatal("Ctrl+C on the panel must not arm the exit confirmation")
	}
}

func TestAutoCompactPanelOwnsKeysAndPaste(t *testing.T) {
	m, _ := autoCompactPanelModel(t, 0, false)
	before := m.input.Value()
	m = updateAutoCompactPrompt(t, m, testKeyText("z"))
	if m.input.Value() != before {
		t.Fatalf("composer received the key: %q", m.input.Value())
	}
	if m.autoCompactPrompt.input != "z" {
		t.Fatalf("panel input = %q, want z", m.autoCompactPrompt.input)
	}
	m = updateAutoCompactPrompt(t, m, testPaste("400k"))
	if m.autoCompactPrompt.input != "z400k" {
		t.Fatalf("pasted panel input = %q, want z400k", m.autoCompactPrompt.input)
	}
	if m.input.Value() != before {
		t.Fatalf("composer received the paste: %q", m.input.Value())
	}
}

func TestAutoCompactPanelPrefillsPositiveLimit(t *testing.T) {
	m, _ := autoCompactPanelModel(t, 400000, false)
	if m.autoCompactPrompt.input != "400k" {
		t.Fatalf("prefilled input = %q, want 400k", m.autoCompactPrompt.input)
	}
	off, _ := autoCompactPanelModel(t, config.AutoCompactOff, false)
	if off.autoCompactPrompt.input != "" {
		t.Fatalf("off should start empty, got %q", off.autoCompactPrompt.input)
	}
}

func TestAutoCompactPanelBlocksModal(t *testing.T) {
	m, _ := autoCompactPanelModel(t, 0, false)
	if m.noBlockingModal() {
		t.Fatal("noBlockingModal must be false while the panel is open")
	}
}

// The current setting is never cut: each of its two lines fits the minimum
// overlay width in every form, including the clamped one.
func TestAutoCompactPanelShowsSettingInFullAtMinimumWidth(t *testing.T) {
	for _, tc := range []struct {
		name  string
		limit int
		want  []string
	}{
		{"automatic", 0, []string{"Atual: automático", "(compacta em 140k tokens neste modelo)"}},
		{"limit the window cannot hold", 400000, []string{"Atual: 400k tokens", "(neste modelo compacta em 140k; a janela é de 200k)"}},
		{"limit that fits", 100000, []string{"Atual: 100k tokens"}},
		{"off", config.AutoCompactOff, []string{"Atual: desligado", "(só compacta se o provedor recusar por excesso)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := charTestModel(t, time.Now())
			m.autoCompactLimit = tc.limit
			m = m.openAutoCompactPrompt()
			plain := plainRender(t, m.autoCompactPromptOverlay(pickerOverlayMinWidth))
			for _, want := range tc.want {
				if !strings.Contains(plain, want) {
					t.Fatalf("overlay at the minimum width misses %q:\n%s", want, plain)
				}
			}
			if strings.Contains(plain, "…") {
				t.Fatalf("overlay at the minimum width cuts a line:\n%s", plain)
			}
		})
	}
}

// The panel input is capped: typed and pasted text beyond 12 runes is dropped.
func TestAutoCompactPanelCapsInputLength(t *testing.T) {
	m, _ := autoCompactPanelModel(t, 0, false)
	for range 15 {
		m = updateAutoCompactPrompt(t, m, testKeyText("9"))
	}
	if got := m.autoCompactPrompt.input; got != "999999999999" {
		t.Fatalf("typed input = %q, want 12 runes", got)
	}
	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyBackspace))
	m = updateAutoCompactPrompt(t, m, testPaste("12345678901234567890"))
	if got := m.autoCompactPrompt.input; got != "999999999991" {
		t.Fatalf("pasted input = %q, want the cap to hold", got)
	}
}

func TestAutoCompactPanelWarnsBelow50kAndKeeps20kMinimum(t *testing.T) {
	m, path := autoCompactPanelModel(t, 0, true)
	for _, key := range []string{"2", "0", "k"} {
		m = updateAutoCompactPrompt(t, m, testKeyText(key))
	}
	for _, width := range []int{100, pickerOverlayMinWidth} {
		plain := plainRender(t, m.autoCompactPromptOverlay(width))
		for _, want := range []string{
			"Abaixo de 50k, o QG vai compactar quase todo",
			"turno e gastar mais chamadas.",
		} {
			if !strings.Contains(plain, want) {
				t.Fatalf("overlay at width %d with 20k should warn %q:\n%s", width, want, plain)
			}
		}
		if strings.Contains(plain, "…") {
			t.Fatalf("overlay at width %d cuts a line:\n%s", width, plain)
		}
	}

	// Prefilled session limit below 50k also warns on open, and stepping up to 50k clears it.
	prefilled, _ := autoCompactPanelModel(t, 35_000, false)
	if !strings.Contains(plainRender(t, prefilled.autoCompactPromptOverlay(100)), "gastar mais chamadas") {
		t.Fatalf("prefilled 35k should warn on open:\n%s", plainRender(t, prefilled.autoCompactPromptOverlay(100)))
	}
	at50k := updateAutoCompactPrompt(t, prefilled, testKey(tea.KeyUp))
	if strings.Contains(plainRender(t, at50k.autoCompactPromptOverlay(100)), "quase todo") {
		t.Fatalf("50k must not show the below-50k warning:\n%s", plainRender(t, at50k.autoCompactPromptOverlay(100)))
	}

	m = updateAutoCompactPrompt(t, m, testKey(tea.KeyEnter))
	if m.autoCompactPrompt != nil {
		t.Fatal("Enter on 20k must still apply and close the panel")
	}
	if m.autoCompactLimit != 20_000 {
		t.Fatalf("autoCompactLimit = %d, want 20000", m.autoCompactLimit)
	}
	if prefs := readAutoCompactFile(t, path); prefs["autoCompactLimit"] != 20000.0 {
		t.Fatalf("persisted autoCompactLimit = %v, want 20000", prefs["autoCompactLimit"])
	}

	// Below 20k is still rejected.
	belowMin, _ := autoCompactPanelModel(t, 0, false)
	for _, key := range []string{"1", "9", "k"} {
		belowMin = updateAutoCompactPrompt(t, belowMin, testKeyText(key))
	}
	belowMin = updateAutoCompactPrompt(t, belowMin, testKey(tea.KeyEnter))
	if belowMin.autoCompactPrompt == nil || belowMin.autoCompactPrompt.err == "" {
		t.Fatalf("19k must still be rejected below the 20k minimum: %+v", belowMin.autoCompactPrompt)
	}
}
