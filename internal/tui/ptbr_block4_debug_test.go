package tui

import (
	"strings"
	"testing"
)

// /debug mostra o estado da execução em português, sem vazar o texto antigo.
func TestPTBRDebugCommand(t *testing.T) {
	idle := (model{}).debugText()
	for _, want := range []string{
		"Depuração",
		"Execução",
		"estado da execução: ociosa",
		"execução ativa: 0",
		"permissão pendente: não",
	} {
		if !strings.Contains(idle, want) {
			t.Fatalf("/debug idle missing %q:\n%s", want, idle)
		}
	}

	busy := (model{pending: true, activeRunID: 7}).debugText()
	for _, want := range []string{"estado da execução: em execução", "execução ativa: 7"} {
		if !strings.Contains(busy, want) {
			t.Fatalf("/debug busy missing %q:\n%s", want, busy)
		}
	}
	if strings.Contains(busy, "estado da execução: ociosa") {
		t.Fatalf("/debug busy kept idle state:\n%s", busy)
	}

	for _, unwanted := range []string{
		"\nDebug", "Runtime", "run state", "active run", "pending permission",
		"idle", "running", "yes", "no",
	} {
		if strings.Contains(idle, unwanted) || strings.Contains(busy, unwanted) {
			t.Fatalf("/debug leaked English %q:\nidle:\n%s\nbusy:\n%s", unwanted, idle, busy)
		}
	}
}
