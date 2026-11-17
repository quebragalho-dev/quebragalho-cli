package tui

import (
	"testing"
)

// Comandos removidos não podem reaparecer na lista: /fast saiu sem
// substituto (o QG não tem modo rápido); /turns saiu e o orçamento de
// turnos passou a vir só do config/flag/env (sem ajuste pela TUI);
// /profile saiu com os perfis de execução (sem substituto na TUI).
func TestRemovedCommandsAbsent(t *testing.T) {
	for _, name := range []string{"/fast", "/turns", "/profile"} {
		if _, ok := resolveCommand(name); ok {
			t.Fatalf("%s voltou à lista de comandos, want ausente", name)
		}
	}
	for _, c := range commandDefinitions {
		if c.name == "/fast" || c.name == "/turns" || c.name == "/profile" {
			t.Fatalf("commandDefinitions ainda contém %s: %#v", c.name, c)
		}
	}
	if statuslineFieldKnown("fast") {
		t.Fatal("statusline ainda registra o campo \"fast\"")
	}
}
