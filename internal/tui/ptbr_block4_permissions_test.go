package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

// /permissions rende o modo e as autorizações em português, sem vazar o texto antigo.
func TestPTBRPermissionsCommand(t *testing.T) {
	store, err := sandbox.NewGrantStore(sandbox.StoreOptions{FilePath: filepath.Join(t.TempDir(), "ptbr-permissions.json")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Grant(sandbox.GrantInput{ToolName: "bash", Decision: sandbox.GrantAllow, Reason: "rotina de build"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Grant(sandbox.GrantInput{ToolName: "read_file", Decision: sandbox.GrantAllow, Reason: "leitura de rotina"}); err != nil {
		t.Fatal(err)
	}

	m := newModel(context.Background(), Options{PermissionMode: agent.PermissionModeAsk, SandboxStore: store})
	m.input.SetValue("/permissions")
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd != nil {
		t.Fatal("expected /permissions to be handled without starting an agent run")
	}
	got := transcriptText(next.transcript)
	for _, want := range []string{
		"Permissões",
		"ask permissões",
		"2 autorizações persistentes",
		"Estado",
		"modo  ask",
		"Autorizações",
		"bash [allow]",
		"read_file [allow]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("/permissions missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{
		"Permissions", "permissions", "no persistent grants", "persistent grant",
		"State", "mode  ask", "Grants", "none", "grants error", "error: ",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("/permissions leaked English %q:\n%s", unwanted, got)
		}
	}

	// Singular e erro de leitura também falam português.
	single, err := sandbox.NewGrantStore(sandbox.StoreOptions{FilePath: filepath.Join(t.TempDir(), "ptbr-permissions-one.json")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := single.Grant(sandbox.GrantInput{ToolName: "bash", Decision: sandbox.GrantAllow}); err != nil {
		t.Fatal(err)
	}
	one := model{permissionMode: agent.PermissionModeAuto, sandboxStore: single}.permissionsText()
	if !strings.Contains(one, "1 autorização persistente") || strings.Contains(one, "1 persistent grant") {
		t.Fatalf("singular grant count wrong:\n%s", one)
	}

	broken := model{permissionMode: agent.PermissionModeAsk}.permissionsTextWithStore(&stubGrantStore{listErr: errors.New("falha simulada")})
	if !strings.Contains(broken, "erro: falha simulada") || strings.Contains(broken, "error: ") {
		t.Fatalf("grant error prefix wrong:\n%s", broken)
	}
}
