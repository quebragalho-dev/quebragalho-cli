package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

// acceptDriftWorkspace monta um repo com diretriz na raiz e no subdiretório
// de trabalho. O store de grants fica fora do workspace.
func acceptDriftWorkspace(t *testing.T) (string, *sandbox.Engine) {
	t.Helper()
	acceptCacheHermetic(t)
	root := t.TempDir()
	for _, dir := range []string{".git", "sub"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	acceptWrite(t, root, ".git/HEAD", "ref: refs/heads/main\n")
	acceptWrite(t, root, "AGENTS.md", "# Regras da raiz\n\nUse gofmt.\n")
	acceptWrite(t, root, "sub/QG.md", "# Regras do sub\n\nRode make lint.\n")
	acceptWrite(t, root, "sub/main.go", "package main\n")
	store, err := sandbox.NewGrantStore(sandbox.StoreOptions{FilePath: filepath.Join(t.TempDir(), "grants.json")})
	if err != nil {
		t.Fatal(err)
	}
	return root, sandbox.NewEngine(sandbox.EngineOptions{WorkspaceRoot: root, Store: store})
}

// acceptDriftTurn roda um turno da mesma conversa, encadeado como o TUI faz.
func acceptDriftTurn(t *testing.T, opts Options, prompt string, history []qgruntime.Message) Result {
	t.Helper()
	p := &mockProvider{turns: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: "ok"},
		{Type: qgruntime.StreamEventDone},
	}}}
	opts.Continuation = history
	result, err := Run(context.Background(), prompt, p, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) == 0 || result.Messages[0].Role != qgruntime.MessageRoleSystem {
		t.Fatalf("o turno deveria devolver o histórico começando pelo system")
	}
	return result
}

func acceptDriftLastUser(result Result) string {
	for i := len(result.Messages) - 1; i >= 0; i-- {
		if result.Messages[i].Role == qgruntime.MessageRoleUser {
			return result.Messages[i].Content
		}
	}
	return ""
}

func acceptDriftGrant(t *testing.T, engine *sandbox.Engine, prefix ...string) {
	t.Helper()
	if _, err := engine.GrantCommandPrefix(sandbox.CommandPrefixInput{ToolName: "exec_command", Prefix: prefix}); err != nil {
		t.Fatal(err)
	}
}

// Uma diretriz que some no meio da conversa (arquivo apagado ou ausente no
// branch novo) continua no system congelado. A nota do sufixo precisa dizer
// que ela não vale mais, ou o modelo segue uma regra removida.
func TestAcceptCacheDriftNoteReportsRemovedGuideline(t *testing.T) {
	root, engine := acceptDriftWorkspace(t)
	opts := acceptCacheOptions(filepath.Join(root, "sub"))
	opts.Sandbox = engine
	first := acceptDriftTurn(t, opts, "primeira pergunta", nil)
	if !strings.Contains(first.Messages[0].Content, "## Project guidelines (sub/QG.md)") {
		t.Fatal("pré-condição: o system do 1º turno deveria trazer a diretriz de sub/QG.md")
	}
	if err := os.Remove(filepath.Join(root, "sub", "QG.md")); err != nil {
		t.Fatal(err)
	}
	second := acceptDriftTurn(t, opts, "segunda pergunta", first.Messages)
	if second.Messages[0].Content != first.Messages[0].Content {
		t.Fatal("o system não pode mudar dentro da conversa")
	}
	note := acceptDriftLastUser(second)
	lower := strings.ToLower(note)
	if !strings.Contains(note, "sub/QG.md") || (!strings.Contains(lower, "remov") && !strings.Contains(lower, "no longer")) {
		t.Fatalf("a nota do sufixo não avisa que a diretriz de sub/QG.md foi removida:\n%s", note)
	}
	if !strings.HasSuffix(note, "segunda pergunta") {
		t.Fatalf("a nota perdeu o prompt do turno:\n%s", note)
	}
}

// Uma diretriz num diretório com parênteses (como os route groups do
// Next.js) que não mudou não pode ser anunciada como removida. Se o arquivo
// some, a nota cita o caminho inteiro.
func TestAcceptCacheDriftNoteParenthesizedGuideline(t *testing.T) {
	for _, removed := range []bool{false, true} {
		acceptCacheHermetic(t)
		root := t.TempDir()
		dir := filepath.Join(root, "app", "(auth)")
		for _, path := range []string{filepath.Join(root, ".git"), dir} {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		acceptWrite(t, root, ".git/HEAD", "ref: refs/heads/main\n")
		acceptWrite(t, root, "app/(auth)/QG.md", "# Regras do grupo\n\nRode make lint.\n")
		acceptWrite(t, root, "app/(auth)/page.go", "package page\n")
		opts := acceptCacheOptions(dir)
		first := acceptDriftTurn(t, opts, "primeira pergunta", nil)
		if !strings.Contains(first.Messages[0].Content, "## Project guidelines (app/(auth)/QG.md)") {
			t.Fatal("pré-condição: o system do 1º turno deveria trazer a diretriz de app/(auth)/QG.md")
		}
		if removed {
			if err := os.Remove(filepath.Join(dir, "QG.md")); err != nil {
				t.Fatal(err)
			}
		}
		note := acceptDriftLastUser(acceptDriftTurn(t, opts, "segunda pergunta", first.Messages))
		if !removed && note != "segunda pergunta" {
			t.Fatalf("nada mudou no workspace, mas o turno ganhou nota:\n%s", note)
		}
		if removed && !strings.Contains(note, "app/(auth)/QG.md") {
			t.Fatalf("a nota não cita a diretriz removida pelo caminho inteiro:\n%s", note)
		}
	}
}

// Uma aprovação nova mais ampla ("go test") cujo texto está contido numa
// aprovação congelada ("go test -race") precisa chegar inteira à nota.
func TestAcceptCacheDriftNoteKeepsBroaderApproval(t *testing.T) {
	root, engine := acceptDriftWorkspace(t)
	acceptDriftGrant(t, engine, "go", "test", "-race")
	opts := acceptCacheOptions(filepath.Join(root, "sub"))
	opts.Sandbox = engine
	first := acceptDriftTurn(t, opts, "primeira pergunta", nil)
	acceptDriftGrant(t, engine, "go", "test")
	acceptDriftGrant(t, engine, "go", "vet")
	second := acceptDriftTurn(t, opts, "pode rodar a suíte?", first.Messages)
	note := acceptDriftLastUser(second)
	approved := func(command string) bool {
		for _, line := range strings.Split(note, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "- ") && strings.HasSuffix(line, " "+command) {
				return true
			}
		}
		return false
	}
	if !approved("go vet") {
		t.Fatalf("pré-condição: a aprovação nova de go vet deveria estar na nota:\n%s", note)
	}
	if !approved("go test") {
		t.Fatalf("a aprovação nova de go test sumiu da nota (mascarada por go test -race):\n%s", note)
	}
}

// Fixa os bytes das diretrizes na 1ª chamada: uma seção por arquivo, no mesmo
// formato de antes do fatiamento. Qualquer mudança aqui desloca o prefixo
// cacheado de toda conversa nova.
func TestAcceptCacheFirstCallGuidelineBytes(t *testing.T) {
	root, _ := acceptDriftWorkspace(t)
	first := acceptDriftTurn(t, acceptCacheOptions(filepath.Join(root, "sub")), "oi", nil)
	const want = "</environment>\n\n" +
		"## Project guidelines (AGENTS.md)\n\n# Regras da raiz\n\nUse gofmt.\n\n" +
		"## Project guidelines (sub/QG.md)\n\n# Regras do sub\n\nRode make lint.\n\n" +
		"## Repo map\n\nRepo: sub\n"
	if system := first.Messages[0].Content; !strings.Contains(system, want) {
		at := strings.Index(system, "</environment>")
		if at < 0 {
			at = 0
		}
		end := min(len(system), at+len(want)+200)
		t.Fatalf("os bytes das diretrizes na 1ª chamada mudaram.\nesperado:\n%q\nveio:\n%q", want, system[at:end])
	}
}
