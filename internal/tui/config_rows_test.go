package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// Every /config row label in panel order: the budget, windowing and
// indicators below are all expressed over these 13 rows.
var configRowsPanelLabels = []string{
	"Provedor",
	"Modelo",
	"Nível de raciocínio",
	"Resumir conversa a partir de",
	"Modo de permissão",
	"Controle do computador",
	"Modo de voz",
	"Modelo de ditado",
	"Tema",
	"Barra de status",
	"Estilo de resposta",
	"Resumo ao voltar",
	"Servidores MCP",
	"Chave de API",
}

func renderConfigRowsPanel(t *testing.T, width, height, selected int) string {
	t.Helper()
	m := configPanelTestModel()
	m.width = width
	m.height = height
	m.picker = m.newConfigPicker()
	m.picker.selected = selected
	return plainRender(t, m.pickerOverlay(width))
}

// renderConfigRowsView renders the whole frame like the real binary does: an
// empty conversation in alt-screen mode, where the open panel shares the body
// with the title bar above and the composer/status footer below.
func renderConfigRowsView(t *testing.T, width, height, selected int) string {
	t.Helper()
	m := configPanelTestModel()
	m.width = width
	m.height = height
	m.altScreen = true
	m.picker = m.newConfigPicker()
	m.picker.selected = selected
	return plainRender(t, m.transcriptView())
}

func assertConfigRowsNoOverflow(t *testing.T, width int, got string) {
	t.Helper()
	for _, line := range strings.Split(got, "\n") {
		if lipgloss.Width(line) > width {
			t.Fatalf("width %d overflow (%d cells): %q", width, lipgloss.Width(line), line)
		}
	}
}

func assertConfigRowsLineCount(t *testing.T, height int, got string) {
	t.Helper()
	if n := len(strings.Split(strings.TrimSuffix(got, "\n"), "\n")); n > height {
		t.Fatalf("height %d view has %d lines:\n%s", height, n, got)
	}
}

// A tall terminal fits the whole panel: all 13 rows, the Shift+Tab hint from
// the permission row's Detail, and no paging indicator.
func TestConfigPanelShowsAllRowsOnTallTerminal(t *testing.T) {
	got := renderConfigRowsPanel(t, 120, 40, 0)
	for _, label := range configRowsPanelLabels {
		if !strings.Contains(got, label) {
			t.Fatalf("120x40 should show row %q, got:\n%s", label, got)
		}
	}
	if strings.Contains(got, "mais ") {
		t.Fatalf("120x40 fits every row, should not page, got:\n%s", got)
	}
	if !strings.Contains(got, "Shift+Tab alterna") {
		t.Fatalf("120x40 should keep the Shift+Tab hint, got:\n%s", got)
	}
	assertConfigRowsNoOverflow(t, 120, got)
}

// A tall terminal fits the whole frame too: panel plus app chrome.
func TestConfigPanelFullViewOnTallTerminal(t *testing.T) {
	got := renderConfigRowsView(t, 120, 40, 0)
	for _, label := range configRowsPanelLabels {
		if !strings.Contains(got, label) {
			t.Fatalf("120x40 view should show row %q, got:\n%s", label, got)
		}
	}
	if strings.Contains(got, "mais ") {
		t.Fatalf("120x40 view fits every row, should not page, got:\n%s", got)
	}
	assertConfigRowsLineCount(t, 40, got)
	assertConfigRowsNoOverflow(t, 120, got)
}

// A short terminal keeps the panel chrome on screen: title, search, the
// selected row and the footer, with a "↓ mais N" tail for the hidden rows.
func TestConfigPanelFullViewOnShortTerminal(t *testing.T) {
	got := renderConfigRowsView(t, 80, 24, 0)
	for _, want := range []string{"Configurações", "buscar >", "❯ Provedor", "Esc fechar", "↓ mais 8"} {
		if !strings.Contains(got, want) {
			t.Fatalf("80x24 view should show %q, got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Chave de API") {
		t.Fatalf("80x24 view should hide trailing rows, got:\n%s", got)
	}
	if strings.Contains(got, "↑ mais") {
		t.Fatalf("80x24 view at the top should not show ↑ mais, got:\n%s", got)
	}
	assertConfigRowsLineCount(t, 24, got)
	assertConfigRowsNoOverflow(t, 80, got)

	// Scrolled to the last row: it is visible and an "↑ mais" head names the
	// hidden rows above, without losing the panel chrome.
	got = renderConfigRowsView(t, 80, 24, len(configRowsPanelLabels)-1)
	for _, want := range []string{"Configurações", "buscar >", "Chave de API", "↑ mais", "Esc fechar"} {
		if !strings.Contains(got, want) {
			t.Fatalf("80x24 scrolled view should show %q, got:\n%s", want, got)
		}
	}
	assertConfigRowsLineCount(t, 24, got)
	assertConfigRowsNoOverflow(t, 80, got)
}

// Rolling through every row on a short terminal never switches panel modes:
// search and footer stay on screen from the first row to the last, with the
// title and the selection always visible and the frame within the height.
func TestConfigPanelKeepsFullModeWhileScrollingShortTerminal(t *testing.T) {
	for sel := 0; sel < len(configRowsPanelLabels); sel++ {
		got := renderConfigRowsView(t, 80, 16, sel)
		for _, want := range []string{"Configurações", "buscar >", "Esc fechar", configRowsPanelLabels[sel]} {
			if !strings.Contains(got, want) {
				t.Fatalf("80x16 sel=%d should show %q in full mode, got:\n%s", sel, want, got)
			}
		}
		assertConfigRowsLineCount(t, 16, got)
		assertConfigRowsNoOverflow(t, 80, got)
	}
}

// Going down to the last row and back up keeps the title and the selected
// row on screen at every step, on any height: the window always contains the
// selection and the overlay always fits the frame body.
func TestConfigPanelSelectionAlwaysVisibleTinyTerminals(t *testing.T) {
	for _, height := range []int{16, 12} {
		var order []int
		for sel := 0; sel < len(configRowsPanelLabels); sel++ {
			order = append(order, sel)
		}
		for sel := len(configRowsPanelLabels) - 2; sel >= 0; sel-- {
			order = append(order, sel)
		}
		for _, sel := range order {
			got := renderConfigRowsView(t, 80, height, sel)
			if !strings.Contains(got, "Configurações") {
				t.Fatalf("80x%d sel=%d lost the panel title, got:\n%s", height, sel, got)
			}
			if want := configRowsPanelLabels[sel]; !strings.Contains(got, want) {
				t.Fatalf("80x%d sel=%d hides the selection %q, got:\n%s", height, sel, want, got)
			}
			assertConfigRowsLineCount(t, height, got)
			assertConfigRowsNoOverflow(t, 80, got)
		}
	}
}

// A very short terminal still shows the title, the selection and paging.
func TestConfigPanelFullViewOnTinyTerminal(t *testing.T) {
	got := renderConfigRowsView(t, 80, 16, 0)
	for _, want := range []string{"Configurações", "buscar >", "Provedor", "↓ mais", "Esc fechar"} {
		if !strings.Contains(got, want) {
			t.Fatalf("80x16 view should show %q, got:\n%s", want, got)
		}
	}
	assertConfigRowsLineCount(t, 16, got)
	assertConfigRowsNoOverflow(t, 80, got)
}
