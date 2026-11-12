package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// cacheGoldenFixture builds a hermetic workspace: a .git/HEAD the prompt reads
// directly (no git binary), one AGENTS.md, and one tracked file.
func cacheGoldenFixture(t *testing.T, branch, guidelines string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(guidelines), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func marshalTools(t *testing.T, defs []qgruntime.ToolDefinition) string {
	t.Helper()
	data, err := json.Marshal(defs)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestCachePrefixStableAcrossWorkspaceDrift is the spec's golden test: approval
// of a command prefix, a branch switch, an AGENTS.md edit, and a new file
// mid-conversation must leave the request's system+tools bytes identical, with
// the new information carried as a SHORT suffix note.
func TestCachePrefixStableAcrossWorkspaceDrift(t *testing.T) {
	// Guidelines grandes de proposito (acima do teto de 8 KiB por arquivo):
	// so uma nota curta por assunto passa no limite abaixo.
	bigPadding := strings.Repeat("diretriz operacional complementar.\n", 300)
	dir := cacheGoldenFixture(t, "main", "NORMAS-v1: responda em portugues.\n"+bigPadding)
	store, err := sandbox.NewGrantStore(sandbox.StoreOptions{FilePath: filepath.Join(t.TempDir(), "grants.json")})
	if err != nil {
		t.Fatal(err)
	}
	engine := sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: dir, Store: store})
	base := Options{Cwd: dir, Sandbox: engine}
	frozen := buildSystemPromptParts(base).prompt

	first := textAnswerProvider("primeiro turno ok")
	result, err := Run(context.Background(), "primeira tarefa", first, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.requests) != 1 {
		t.Fatalf("turno 1: requests = %d, want 1", len(first.requests))
	}
	firstSystem := first.requests[0].Messages[0].Content
	firstTools := marshalTools(t, first.requests[0].Tools)

	// Mid-conversation drift: approval, branch switch, guideline edit, new file.
	if _, err := store.GrantCommandPrefix(sandbox.CommandPrefixInput{ToolName: "bash", Prefix: []string{"go", "test"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/feature-y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("NORMAS-v2: responda em portugues e cite arquivos.\n"+bigPadding), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	second := textAnswerProvider("segundo turno ok")
	_, err = Run(context.Background(), "segunda tarefa", second, Options{
		Cwd:          dir,
		Sandbox:      engine,
		Continuation: result.Messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.requests) != 1 {
		t.Fatalf("turno 2: requests = %d, want 1", len(second.requests))
	}
	got := second.requests[0]
	if got.Messages[0].Role != qgruntime.MessageRoleSystem {
		t.Fatalf("turno 2: primeira mensagem = %q, want system", got.Messages[0].Role)
	}
	if got.Messages[0].Content != firstSystem {
		t.Fatal("turno 2: system bytes mudaram apos drift no meio da conversa")
	}
	if got.Messages[0].Content != frozen {
		t.Fatal("turno 2: system diverge do congelado no turno 1")
	}
	if marshalTools(t, got.Tools) != firstTools {
		t.Fatal("turno 2: tools bytes mudaram apos drift no meio da conversa")
	}
	last := got.Messages[len(got.Messages)-1]
	if last.Role != qgruntime.MessageRoleUser {
		t.Fatalf("ultima mensagem = %q, want user", last.Role)
	}
	for _, want := range []string{"[context update]", "feature-y", "NORMAS-v2", "go test", "segunda tarefa"} {
		if !strings.Contains(last.Content, want) {
			t.Fatalf("nota de sufixo nao contem %q: %q", want, last.Content)
		}
	}
	// A nota e curta: so o conteudo novo, nunca o despejo integral das secoes.
	if len(last.Content) > 2048 {
		t.Fatalf("nota de sufixo tem %d bytes, want <= 2048 (curta, so o delta)", len(last.Content))
	}
	if strings.Contains(last.Content, "NORMAS-v1") {
		t.Fatalf("nota de sufixo repete o conteudo antigo das guidelines: %q", last.Content)
	}
}

// TestCachePrefixSuffixNotesFused requires loop-generated notes that land
// back-to-back (no assistant message between them) to ride as a single suffix
// user message instead of several.
func TestCachePrefixSuffixNotesFused(t *testing.T) {
	dir := t.TempDir()
	registry := tools.NewRegistry()
	registry.Register(changedFilesTool{})
	provider := &mockProvider{turns: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "fake_edit"},
			{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-1", ArgumentsFragment: `{}`},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
			{Type: qgruntime.StreamEventToolCallDropped},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "pronto"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	_, err := Run(context.Background(), "vai", provider, Options{
		Cwd:      dir,
		Registry: registry,
		FileDiagnostics: func(context.Context, string) string {
			return "main.go:1:1 error: boom"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(provider.requests))
	}
	second := provider.requests[1].Messages
	// O fim do request e UMA mensagem de usuario fundida (diagnostico do
	// turno anterior + aviso de chamada descartada), precedida do tool result.
	trail := 0
	for i := len(second) - 1; i >= 0 && second[i].Role == qgruntime.MessageRoleUser; i-- {
		trail++
	}
	if trail != 1 {
		t.Fatalf("request 2 termina com %d mensagens user seguidas, want 1 fundida", trail)
	}
	fused := second[len(second)-1].Content
	if !strings.Contains(fused, "missing a tool name") || !strings.Contains(fused, "boom") {
		t.Fatalf("mensagem fundida perdeu informacao: %q", fused)
	}
}

// TestCachePrefixContraryCases pins the allowed prefix changes: a fresh run
// (/clear, new session) rebuilds the system from the current workspace, so the
// prefix MAY differ from a previous conversation's frozen one.
func TestCachePrefixContraryCases(t *testing.T) {
	dir := cacheGoldenFixture(t, "main", "NORMAS-v1.\n")
	before := buildSystemPromptParts(Options{Cwd: dir}).prompt
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/feature-z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := buildSystemPromptParts(Options{Cwd: dir}).prompt
	if before == after {
		t.Fatal("run fresco apos troca de branch deveria refletir o branch novo")
	}
	if !strings.Contains(after, "feature-z") {
		t.Fatalf("run fresco nao menciona o branch novo: %q", after[len(after)-500:])
	}
}
