// Testes de aceitação do cache de prefixo (cache alto sem perder qualidade),
// escritos a partir do comportamento esperado e não da implementação.
//
// Superfície: agent.Run com provider dublê que captura o CompletionRequest
// real. Nenhum símbolo novo. Os dois são guardas de qualidade (verdes no dev);
// o teste-ouro do prefixo mora no TUI (internal/tui/accept_cache_prefix_test.go).
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func acceptCacheHermetic(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("QG_CRED_STORAGE", "file")
	t.Setenv("QG_TOOL_OUTPUT_CEILING_TOKENS", "")
}

// acceptCacheWorkspace cria um workspace com git, AGENTS.md e um arquivo de
// código. O store de grants fica fora do workspace para não entrar no repomap.
func acceptCacheWorkspace(t *testing.T) (string, *sandbox.Engine) {
	t.Helper()
	acceptCacheHermetic(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	acceptWrite(t, dir, ".git/HEAD", "ref: refs/heads/main\n")
	acceptWrite(t, dir, "AGENTS.md", "# Guidelines v1\n")
	acceptWrite(t, dir, "main.go", "package main\n")
	store, err := sandbox.NewGrantStore(sandbox.StoreOptions{FilePath: filepath.Join(t.TempDir(), "grants.json")})
	if err != nil {
		t.Fatal(err)
	}
	return dir, sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: dir, Store: store})
}

func acceptWrite(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// acceptApprovedPrefix é único: o system já cita "go test" na política de
// confirmação, então um comando comum não provaria de onde veio o texto.
const acceptApprovedPrefix = "qgaccept probe"

func acceptGrantProbe(t *testing.T, engine *sandbox.Engine) {
	t.Helper()
	if _, err := engine.GrantCommandPrefix(sandbox.CommandPrefixInput{
		ToolName: "exec_command", Prefix: strings.Fields(acceptApprovedPrefix),
	}); err != nil {
		t.Fatal(err)
	}
}

func acceptCacheOptions(dir string) Options {
	return Options{
		Cwd:             dir,
		SessionID:       "accept-cache-session",
		ProviderName:    "test-provider",
		Model:           "claude-sonnet",
		ReasoningEffort: "high",
		MaxTurns:        6,
	}
}

// Aprovações que já existiam antes de a conversa começar podem ficar no
// system, congeladas, e nada do que o modelo recebe é removido. Guarda: falha
// se a aprovação sair do system sem chegar ao sufixo.
func TestAcceptApprovalFromBeforeConversationReachesModel(t *testing.T) {
	dir, engine := acceptCacheWorkspace(t)
	acceptGrantProbe(t, engine)
	opts := acceptCacheOptions(dir)
	opts.Sandbox = engine
	p := &mockProvider{turns: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: "ok"},
		{Type: qgruntime.StreamEventDone},
	}}}
	if _, err := Run(context.Background(), "rode os testes", p, opts); err != nil {
		t.Fatal(err)
	}
	for _, m := range p.requests[0].Messages {
		if strings.Contains(m.Content, acceptApprovedPrefix) {
			return
		}
	}
	t.Fatalf("qualidade: a aprovação %q concedida antes da conversa não chegou ao modelo (nem no system nem no sufixo)", acceptApprovedPrefix)
}

// acceptBulkTool devolve uma saída grande, não comprimível, abaixo dos tetos
// de hoje (16.000 tokens ~ 64.000 bytes). Cada linha é única.
type acceptBulkTool struct{ output string }

func (acceptBulkTool) Name() string        { return "accept_bulk" }
func (acceptBulkTool) Description() string { return "returns a large deterministic payload" }
func (acceptBulkTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", Properties: map[string]tools.PropertySchema{}}
}
func (acceptBulkTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow, Reason: "test"}
}
func (tool acceptBulkTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: tool.output}
}

// Invariante de qualidade: o cache nunca pode cortar ou encurtar saída de
// ferramenta nem baixar o effort. Guarda de regressão: falha se o teto de
// saída cair ou o effort mudar.
func TestAcceptQualityToolOutputUncutAndEffortKept(t *testing.T) {
	acceptCacheHermetic(t)
	var b strings.Builder
	for i := 0; b.Len() < 60_000; i++ {
		fmt.Fprintf(&b, "linha %05d: resultado deterministico da ferramenta\n", i)
	}
	payload := b.String()
	registry := tools.NewRegistry()
	registry.Register(acceptBulkTool{output: payload})
	p := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "accept_bulk"},
			{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-1", ArgumentsFragment: `{}`},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "ok"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	opts := acceptCacheOptions(t.TempDir())
	opts.Registry = registry
	if _, err := Run(context.Background(), "use a ferramenta", p, opts); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 2 {
		t.Fatalf("esperava 2 requests, veio %d", len(p.requests))
	}
	found := false
	for _, m := range p.requests[1].Messages {
		if m.Role == qgruntime.MessageRoleTool && strings.Contains(m.Content, payload) {
			found = true
		}
	}
	if !found {
		t.Fatalf("qualidade: a saída de %d bytes da ferramenta não chegou inteira ao modelo", len(payload))
	}
	for i, r := range p.requests {
		if r.ReasoningEffort != "high" {
			t.Fatalf("qualidade: request %d saiu com effort %q, esperado \"high\"", i+1, r.ReasoningEffort)
		}
	}
}
