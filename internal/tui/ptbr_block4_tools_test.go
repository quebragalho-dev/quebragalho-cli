package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// /tools lista o catálogo em português, sem vazar o texto antigo.
func TestPTBRToolsCommand(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewScopedReadFileTool(".", nil))
	m := newModel(context.Background(), Options{Registry: registry})
	m.input.SetValue("/tools")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd != nil {
		t.Fatal("expected /tools to be handled without starting an agent run")
	}
	got := transcriptText(next.transcript)
	for _, want := range []string{
		"Ferramentas",
		"1 registrada | catálogo registrado",
		"Registro",
		"registradas  1",
		"Disponíveis",
		"- read_file",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("/tools missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"\nTools", "registered |", "registered catalog", "Registry",
		"registered  1", "Available", "no tools available",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("/tools leaked English %q:\n%s", unwanted, got)
		}
	}

	// Sem ferramentas, o cartão vazio também fala português.
	empty := model{}.toolsText()
	for _, want := range []string{"Ferramentas", "0 registradas", "nenhuma ferramenta disponível"} {
		if !strings.Contains(empty, want) {
			t.Fatalf("empty /tools missing %q:\n%s", want, empty)
		}
	}
	for _, unwanted := range []string{"Tools", "0 registered", "no tools available"} {
		if strings.Contains(empty, unwanted) {
			t.Fatalf("empty /tools leaked English %q:\n%s", unwanted, empty)
		}
	}
}
