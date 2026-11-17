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

func assertConfigRowsNoOverflow(t *testing.T, width int, got string) {
	t.Helper()
	for _, line := range strings.Split(got, "\n") {
		if lipgloss.Width(line) > width {
			t.Fatalf("width %d overflow (%d cells): %q", width, lipgloss.Width(line), line)
		}
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

// A short terminal shows a window with a faint "↓ mais N" tail; moving the
// selection to the last row scrolls it into view with an "↑ mais" head.
func TestConfigPanelPaginatesOnShortTerminal(t *testing.T) {
	got := renderConfigRowsPanel(t, 80, 24, 0)
	if strings.Contains(got, "Chave de API") {
		t.Fatalf("80x24 should hide trailing rows, got:\n%s", got)
	}
	if !strings.Contains(got, "↓ mais 2") {
		t.Fatalf("80x24 should report the 2 hidden rows, got:\n%s", got)
	}
	if strings.Contains(got, "↑ mais") {
		t.Fatalf("80x24 at the top should not show ↑ mais, got:\n%s", got)
	}
	if !strings.Contains(got, "Shift+Tab alterna") {
		t.Fatalf("80x24 should keep the Shift+Tab hint, got:\n%s", got)
	}
	assertConfigRowsNoOverflow(t, 80, got)

	got = renderConfigRowsPanel(t, 80, 24, len(configRowsPanelLabels)-1)
	if !strings.Contains(got, "Chave de API") {
		t.Fatalf("80x24 scrolled to the end should show Chave de API, got:\n%s", got)
	}
	if !strings.Contains(got, "↑ mais") {
		t.Fatalf("80x24 scrolled to the end should show ↑ mais, got:\n%s", got)
	}
	assertConfigRowsNoOverflow(t, 80, got)
}
