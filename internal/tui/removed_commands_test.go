package tui

import (
	"testing"
)

// Comandos removidos não podem reaparecer na lista: /fast saiu sem
// substituto (o QG não tem modo rápido).
func TestRemovedCommandsAbsent(t *testing.T) {
	for _, name := range []string{"/fast"} {
		if _, ok := resolveCommand(name); ok {
			t.Fatalf("%s voltou à lista de comandos, want ausente", name)
		}
	}
	for _, c := range commandDefinitions {
		if c.name == "/fast" {
			t.Fatalf("commandDefinitions ainda contém /fast: %#v", c)
		}
	}
	if statuslineFieldKnown("fast") {
		t.Fatal("statusline ainda registra o campo \"fast\"")
	}
}
