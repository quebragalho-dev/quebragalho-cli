package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestHelpCommandRendersGroupedSections(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.input.SetValue("/help")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected /help to be handled without starting an agent run")
	}
	text := transcriptText(next.transcript)
	for _, want := range []string{
		"Comandos",
		"Modelo",
		"Sessão",
		"Execução",
		"Ferramentas",
		"Meta",
		"  /model [list|id]",
		"  /permissions",
		"dica: escreva um texto para perguntar ao QG",
	} {
		assertContains(t, text, want)
	}
	assertNotContains(t, text, "hint: escreva um texto")
	assertNotContains(t, text, "Commands:\n/provider")
}

func TestProviderAndConfigCommandsUseStableStatusOutput(t *testing.T) {
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		ProviderProfile: config.ProviderProfile{
			Name:         "openai",
			ProviderKind: config.ProviderKindOpenAI,
			BaseURL:      config.OpenAIBaseURL,
			APIKey:       "sk-sensitive",
			Model:        "gpt-4.1",
		},
		AgentOptions:  agent.Options{MaxTurns: 42},
		RecapsEnabled: true,
	})

	m.input.SetValue("/provider status")
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd != nil {
		t.Fatal("expected /provider to be handled without starting an agent run")
	}
	providerText := transcriptText(next.transcript)
	for _, want := range []string{"qg login", "QG_API_KEY", "QUEBRAGALHO_API_KEY"} {
		assertContains(t, providerText, want)
	}
	assertNotContains(t, providerText, "sk-sensitive")

	next.input.SetValue("/config")
	updated, cmd = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if cmd != nil {
		t.Fatal("expected /config to be handled without starting an agent run")
	}
	if next.picker == nil || next.picker.kind != pickerConfig {
		t.Fatal("expected bare /config to open the settings panel")
	}
	if got := transcriptText(next.transcript); strings.Contains(got, "sk-sensitive") {
		t.Fatalf("/config panel leaked credential into transcript: %q", got)
	}
}

func TestProviderCommandRedactsCredentialBearingBaseURL(t *testing.T) {
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		ProviderProfile: config.ProviderProfile{
			Name:         "openai",
			ProviderKind: config.ProviderKindOpenAI,
			BaseURL:      "https://user:super-secret@proxy.local/v1?api_key=query-secret",
			APIKey:       "query-secret",
			Model:        "gpt-4.1",
		},
	})
	m.input.SetValue("/provider status")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected /provider to be handled without starting an agent run")
	}
	text := transcriptText(next.transcript)
	for _, unwanted := range []string{"super-secret", "query-secret", "user:super-secret@"} {
		assertNotContains(t, text, unwanted)
	}
	assertContains(t, text, "qg login")
}

func TestToolsCommandRendersCommandCard(t *testing.T) {
	m := newModel(context.Background(), Options{
		Registry: tools.NewRegistry(),
	})
	m.input.SetValue("/tools")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected /tools to be handled without starting an agent run")
	}
	emptyText := transcriptText(next.transcript)
	for _, want := range []string{
		"Ferramentas",
		"0 registradas | nenhuma ferramenta disponível",
		"Registro",
		"registradas  0",
		"ações: /mcp manage servers | /permissions manage access",
	} {
		assertContains(t, emptyText, want)
	}
	assertNotContains(t, emptyText, "status: warning")
	assertNotContains(t, emptyText, "registered tools:")

	registry := tools.NewRegistry()
	registry.Register(tools.NewScopedReadFileTool(".", nil))
	m = newModel(context.Background(), Options{
		Registry: registry,
	})
	m.input.SetValue("/tools")

	updated, cmd = m.Update(testKey(tea.KeyEnter))
	next = updated.(model)

	if cmd != nil {
		t.Fatal("expected /tools to be handled without starting an agent run")
	}
	toolsText := transcriptText(next.transcript)
	for _, want := range []string{
		"Ferramentas",
		"1 registrada | catálogo registrado",
		"Registro",
		"registradas  1",
		"Disponíveis",
		"- read_file",
		"ações: /mcp manage servers | /permissions manage access",
	} {
		assertContains(t, toolsText, want)
	}
	assertNotContains(t, toolsText, "status: ok")
	assertNotContains(t, toolsText, "registered tools:")
}

func TestToolsCommandCardHandlesNilRegistry(t *testing.T) {
	text := model{}.toolsText()

	for _, want := range []string{
		"Ferramentas",
		"0 registradas | nenhuma ferramenta disponível",
		"registradas  0",
	} {
		assertContains(t, text, want)
	}
}

func TestToolsCommandShowsFullSortedCatalog(t *testing.T) {
	registry := tools.NewRegistry()
	for _, name := range []string{
		"write_file",
		"read_file",
		"grep",
		"glob",
		"edit_file",
		"apply_patch",
		"bash",
		"web_search",
		"web_fetch",
	} {
		registry.Register(commandTestMCPTool{name: name, description: name + " tool"})
	}

	m := newModel(context.Background(), Options{
		Registry: registry,
	})
	m.input.SetValue("/tools")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected /tools to be handled without starting an agent run")
	}
	text := transcriptText(next.transcript)
	for _, want := range []string{
		"9 registradas | catálogo registrado",
		"- apply_patch",
		"- bash",
		"- edit_file",
		"- glob",
		"- grep",
		"- read_file",
		"- web_fetch",
		"- web_search",
		"- write_file",
	} {
		assertContains(t, text, want)
	}
	assertNotContains(t, text, "... 1 more")

	previous := -1
	for _, want := range []string{
		"- apply_patch",
		"- bash",
		"- edit_file",
		"- glob",
		"- grep",
		"- read_file",
		"- web_fetch",
		"- web_search",
		"- write_file",
	} {
		current := strings.Index(text, want)
		if current < 0 {
			t.Fatalf("expected tools output to contain %q, got:\n%s", want, text)
		}
		if current <= previous {
			t.Fatalf("expected tools output to keep sorted order at %q, got:\n%s", want, text)
		}
		previous = current
	}
}

func TestContextAndPermissionsCommandsRenderProductState(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(tools.NewScopedReadFileTool(".", nil))

	store, err := sandbox.NewGrantStore(sandbox.StoreOptions{FilePath: filepath.Join(t.TempDir(), "sandbox-grants.json")})
	if err != nil {
		t.Fatalf("NewGrantStore returned error: %v", err)
	}
	if _, err := store.Grant(sandbox.GrantInput{
		ToolName: "bash",
		Decision: sandbox.GrantAllow,
		Reason:   "sk-proj-sensitive-credential-value approved shell",
	}); err != nil {
		t.Fatalf("Grant returned error: %v", err)
	}

	m := newModel(context.Background(), Options{
		Cwd:            `D:\codings\Opensource\QG`,
		ProviderName:   "openai",
		ModelName:      "gpt-4.1",
		Registry:       registry,
		SandboxStore:   store,
		PermissionMode: agent.PermissionModeAsk,
	})

	m.input.SetValue("/context")
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd != nil {
		t.Fatal("expected /context to be handled without starting an agent run")
	}
	contextText := transcriptText(next.transcript)
	for _, want := range []string{
		"Contexto",
		"runtime go | ask permissões | 1 ferramenta",
		"Execução",
		"cwd             D:\\codings\\Opensource\\QG",
		"provedor        openai",
		"modelo          gpt-4.1",
		"Sessão",
		"ativa        nenhuma",
		"compactação  ociosa",
		"Ferramentas",
		"registradas  1",
		"ações: /permissions manage access | /tools inspect catalog",
	} {
		assertContains(t, contextText, want)
	}
	assertNotContains(t, contextText, "status: ok")
	assertNotContains(t, contextText, "permission mode:")

	next.input.SetValue("/permissions")
	updated, cmd = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if cmd != nil {
		t.Fatal("expected /permissions to be handled without starting an agent run")
	}
	permissionText := transcriptText(next.transcript)
	for _, want := range []string{
		"Permissões",
		"ask permissões",
		"1 autorização persistente",
		"Estado",
		"modo  ask",
		"Autorizações",
		"bash [allow]",
		"[REDACTED]",
	} {
		assertContains(t, permissionText, want)
	}
	assertNotContains(t, permissionText, "sk-proj-sensitive-credential-value")
	assertNotContains(t, permissionText, "status: ok")
	assertNotContains(t, permissionText, "Permission mode:")
}

// stubGrantStore is a minimal test double for sandbox.GrantStore. It only
// implements List, which is enough to exercise the error path of
// permissionsText().
type stubGrantStore struct {
	listErr error
}

func (store *stubGrantStore) List() ([]sandbox.Grant, error) {
	return nil, store.listErr
}

func TestPermissionsCommandCardHandlesNilStoreAndEmptyGrants(t *testing.T) {
	nilStore := model{permissionMode: agent.PermissionModeAuto}.permissionsText()
	for _, want := range []string{
		"Permissões",
		"auto permissões",
		"autorizações indisponíveis",
		"modo  auto",
		"autorizações persistentes: indisponíveis",
	} {
		assertContains(t, nilStore, want)
	}
	assertNotContains(t, nilStore, "status: warning")

	store, err := sandbox.NewGrantStore(sandbox.StoreOptions{FilePath: filepath.Join(t.TempDir(), "empty-grants.json")})
	if err != nil {
		t.Fatalf("NewGrantStore returned error: %v", err)
	}
	emptyText := model{permissionMode: agent.PermissionModeAsk, sandboxStore: store}.permissionsText()
	for _, want := range []string{
		"Permissões",
		"ask permissões",
		"nenhuma autorização persistente",
		"modo  ask",
		"nenhuma",
	} {
		assertContains(t, emptyText, want)
	}
	assertNotContains(t, emptyText, "status: info")

	errStore := &stubGrantStore{listErr: errors.New("storage failure")}
	errText := model{permissionMode: agent.PermissionModeAsk}.permissionsTextWithStore(errStore)
	for _, want := range []string{
		"Permissões",
		"ask permissões",
		"erro de autorizações",
		"modo  ask",
		"erro: storage failure",
	} {
		assertContains(t, errText, want)
	}
	assertNotContains(t, errText, "status: blocked")
}

func TestContextCommandCardHandlesNilRegistryAndStableStyle(t *testing.T) {
	text := model{}.contextText()

	for _, want := range []string{
		"Contexto",
		"0 ferramentas",
		"estilo          concise",
		"raiz         desconhecida",
	} {
		assertContains(t, text, want)
	}
}

func TestCompactCommandAvoidsShellOnlyPlaceholder(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.input.SetValue("/compact")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected /compact to be handled without starting an agent run")
	}
	text := transcriptText(next.transcript)
	for _, want := range []string{"Compact", "status: warning", "requested: yes", "visible transcript rows:"} {
		assertContains(t, text, want)
	}
	if strings.Contains(text, "not wired") || strings.Contains(text, "future compaction backend") {
		t.Fatalf("compact output should describe product state, got %q", text)
	}
}
