package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// /config sem argumento abre o painel único de configurações em português.
func TestPTBRConfigCommand(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "muse-spark-1.3-contributor", ProviderName: "qg"})
	m.input.SetValue("/config")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd != nil {
		t.Fatal("expected /config to be handled without starting an agent run")
	}
	if next.picker == nil || next.picker.kind != pickerConfig {
		t.Fatalf("expected bare /config to open the settings panel, got %+v", next.picker)
	}
	if strings.TrimSpace(next.picker.title) != "Configurações" {
		t.Fatalf("panel title wrong: %q", next.picker.title)
	}
	got := transcriptText(next.transcript)
	for _, unwanted := range []string{
		"\nConfig\n", "Runtime", "max turns", "permission mode", "recaps: off",
		"recaps: on", "Provider", "provider:", "model:", "api key", "not set",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("/config leaked English %q", unwanted)
		}
	}

	// Os dois ramos de escrita continuam falando português.
	if _, text := m.handleConfigCommand("recaps on"); !strings.Contains(text, "recaps: ativado") {
		t.Fatalf("recaps on wrong:\n%s", text)
	}
	if _, text := m.handleConfigCommand("bogus"); !strings.Contains(text, "Ajuste desconhecido") || !strings.Contains(text, "uso: recaps on|off") {
		t.Fatalf("unknown setting wrong:\n%s", text)
	} else if strings.Contains(text, "Unknown setting") || strings.Contains(text, "use: recaps") {
		t.Fatalf("unknown setting leaked English:\n%s", text)
	}
}
