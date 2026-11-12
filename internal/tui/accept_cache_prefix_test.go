package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func acceptCacheHermeticTUI(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, t.TempDir())
	}
	t.Setenv("QG_CRED_STORAGE", "file")
}

func acceptCacheWrite(t *testing.T, dir, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// acceptCacheApprovedPrefix é único: o system já cita "go test" na política de
// confirmação.
const acceptCacheApprovedPrefix = "qgaccept probe"

func acceptCacheOutsideSystem(req qgruntime.CompletionRequest, token string) bool {
	for _, message := range req.Messages {
		if message.Role != qgruntime.MessageRoleSystem && strings.Contains(message.Content, token) {
			return true
		}
	}
	return false
}

// SPEC 1.6, teste-ouro obrigatório, pelo caminho real do TUI (composer ->
// agente -> provider), com a decisão (b): dentro da conversa os bytes do
// system nunca mudam. A aprovação de comando chega no meio do 1º run, como no
// fluxo real de permissão; depois vêm troca de branch, edição do AGENTS.md e
// arquivo novo no repomap. system+tools ficam idênticos e cada informação nova
// chega fora do system e fica no histórico. Depois o contrário: /clear
// reconstrói o prefixo com o workspace atual.
func TestAcceptCacheGoldenPrefixTUI(t *testing.T) {
	acceptCacheHermeticTUI(t)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	acceptCacheWrite(t, dir, ".git/HEAD", "ref: refs/heads/main\n")
	acceptCacheWrite(t, dir, "AGENTS.md", "# Guidelines v1\n")
	acceptCacheWrite(t, dir, "main.go", "package main\n")
	grants, err := sandbox.NewGrantStore(sandbox.StoreOptions{FilePath: filepath.Join(t.TempDir(), "grants.json")})
	if err != nil {
		t.Fatal(err)
	}
	engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: dir, Store: grants})
	registry := tools.NewRegistry()
	registry.Register(continuationEchoTool{})
	toolCall := []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "read_it"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-1", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
		{Type: qgruntime.StreamEventDone},
	}
	provider := &scriptedProvider{scripts: append([][]qgruntime.StreamEvent{toolCall}, textScripts("a1", "a2", "a3", "a4", "a5", "a6")...)}
	granted := false
	provider.beforeCall = func(index int) {
		// A 1ª chamada pede a ferramenta; a aprovação "sempre" é gravada antes
		// de o agente montar o 2º request do mesmo run.
		if index == 0 && !granted {
			granted = true
			if _, err := engine.GrantCommandPrefix(sandbox.CommandPrefixInput{ToolName: "exec_command", Prefix: strings.Fields(acceptCacheApprovedPrefix)}); err != nil {
				t.Error(err)
			}
		}
	}
	m := newModel(context.Background(), Options{
		Cwd:          dir,
		ProviderName: "test",
		ModelName:    "test-model",
		Provider:     provider,
		Registry:     registry,
		SessionStore: testSessionStore(t),
		AgentOptions: agent.Options{Sandbox: engine},
	})
	writer := m.snapshotWriter
	t.Cleanup(func() {
		if writer != nil {
			writer.Flush(10 * time.Second)
		}
	})

	m = runTextTurn(t, m, "q1")
	m = runTextTurn(t, m, "q2")
	acceptCacheWrite(t, dir, ".git/HEAD", "ref: refs/heads/feature-cache\n")
	m = runTextTurn(t, m, "q3")
	acceptCacheWrite(t, dir, "AGENTS.md", "# Guidelines v2\n")
	m = runTextTurn(t, m, "q4")
	acceptCacheWrite(t, dir, "novo.go", "package main\n")
	m = runTextTurn(t, m, "q5")

	reqs := provider.requests
	if !granted || len(reqs) != 6 {
		t.Fatalf("roteiro: aprovação no meio do run = %v, requests = %d (esperado 6)", granted, len(reqs))
	}
	first := reqs[0]
	firstTools, _ := json.Marshal(first.Tools)
	if len(first.Tools) == 0 {
		t.Fatal("o request deveria anunciar ferramentas")
	}
	for i, req := range reqs[1:] {
		if req.Messages[0].Content != first.Messages[0].Content {
			t.Fatalf("SPEC 1.1/1.6 + decisão (b): o system do request %d mudou dentro da conversa", i+2)
		}
		if got, _ := json.Marshal(req.Tools); string(got) != string(firstTools) {
			t.Fatalf("SPEC 1.1/1.4: as definições de tools do request %d mudaram", i+2)
		}
	}
	// A aprovação obtida no 1º run chega fora do system até o turno seguinte e
	// continua no histórico.
	for _, req := range []int{3, 6} {
		if !acceptCacheOutsideSystem(reqs[req-1], acceptCacheApprovedPrefix) {
			t.Fatalf("SPEC 1.3 + decisão (b): a aprovação obtida no meio do 1º run não está no sufixo/histórico do request %d", req)
		}
	}
	for _, c := range []struct {
		req    int
		prompt string
		want   string
	}{
		{4, "q3", "feature-cache"},
		{5, "q4", "Guidelines v2"},
		{6, "q5", "novo.go"},
	} {
		last := lastUserMessage(reqs[c.req-1]).Content
		if !strings.Contains(last, c.want) {
			t.Fatalf("SPEC 1.2/1.6: a informação nova %q não está no sufixo do request %d: %q", c.want, c.req, last)
		}
		if !strings.HasSuffix(last, c.prompt) {
			t.Fatalf("o sufixo do request %d perdeu o prompt do turno: %q", c.req, last)
		}
	}

	m.input.SetValue("/clear")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	m = runTextTurn(t, m, "q6")
	if len(provider.requests) != 7 {
		t.Fatalf("esperava 7 requests, veio %d", len(provider.requests))
	}
	fresh := provider.requests[6].Messages[0].Content
	if !strings.Contains(fresh, "Guidelines v2") || strings.Contains(fresh, "Guidelines v1") || !strings.Contains(fresh, "feature-cache") {
		t.Fatal("SPEC 1.6 (contrário): depois do /clear o prefixo deve ser reconstruído com o AGENTS.md e o branch atuais")
	}
}
