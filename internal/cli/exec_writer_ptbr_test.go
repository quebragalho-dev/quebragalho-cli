package cli

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
)

// The human exec text branch translates canonical messages; JSON and
// stream-json keep them byte-identical. These tests pin both sides.
func TestHumanExecMessageTable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"unsafe flag", "Unsafe permissions are active for this run because --dangerously-skip-permissions was passed.", "Permissões sem confirmação ativas nesta execução porque --dangerously-skip-permissions foi passado."},
		{"cancelled", "run cancelled by signal", "execução cancelada por sinal"},
		{"incomplete default", "run stopped with work unfinished", "execução parada com trabalho inacabado"},
		{"truncation length", "Response was cut off at the output token limit and may be incomplete. Raise the model's max output tokens or ask qg to continue.", "A resposta foi cortada no limite de tokens de saída e pode estar incompleta. Aumente o máximo de tokens de saída do modelo ou peça ao qg para continuar."},
		{"truncation filter", "Response was withheld or cut by the provider's content filter and may be incomplete.", "A resposta foi retida ou cortada pelo filtro de conteúdo do provedor e pode estar incompleta."},
		{"scratch plural", "This run left 2 new scratch/debug-like files untracked in git: a.py, b.py. Review before committing/staging.", "Esta execução deixou 2 arquivos temporários sem rastrear no git: a.py, b.py. Revise antes de fazer commit."},
		{"scratch singular", "This run left 1 new scratch/debug-like file untracked in git: a.py. Review before committing/staging.", "Esta execução deixou 1 arquivo temporário sem rastrear no git: a.py. Revise antes de fazer commit."},
		{"provider error passes through", "auth error: bad key", "auth error: bad key"},
		{"dynamic truncation passes through", "Response ended early (stop) and may be incomplete.", "Response ended early (stop) and may be incomplete."},
		{"unknown passes through", "something else entirely", "something else entirely"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := humanExecMessage(tc.in); got != tc.want {
				t.Fatalf("humanExecMessage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestHumanPermissionText(t *testing.T) {
	if got := humanPermissionAction(agent.PermissionActionPrompt); got != "requer aprovação" {
		t.Fatalf("action = %q", got)
	}
	if got := humanPermissionReason("network access requires approval"); got != "O acesso à rede precisa de aprovação." {
		t.Fatalf("reason = %q", got)
	}
	if got := humanPermissionReason("some model-provided reason"); got != "some model-provided reason" {
		t.Fatalf("unknown reason must pass through, got %q", got)
	}
}

func TestWarningTextTranslatedButJSONKept(t *testing.T) {
	const canonical = "Unsafe permissions are active for this run because --dangerously-skip-permissions was passed."
	for _, format := range []execOutputFormat{execOutputText, execOutputJSON, execOutputStreamJSON} {
		var stdout, stderr strings.Builder
		writer := execEventWriter{stdout: &stdout, stderr: &stderr, format: format, runID: "r1", streamedText: &strings.Builder{}}
		writer.warning(canonical)
		if writer.err != nil {
			t.Fatalf("warning: %v", writer.err)
		}
		if format == execOutputText {
			if !strings.Contains(stderr.String(), "Permissões sem confirmação") {
				t.Fatalf("text warning = %q, want Portuguese", stderr.String())
			}
			if strings.Contains(stderr.String(), "Unsafe permissions are active") {
				t.Fatalf("text warning leaked English: %q", stderr.String())
			}
		} else {
			if !strings.Contains(stdout.String(), canonical) {
				t.Fatalf("%s warning must keep canonical message, got %q", format, stdout.String())
			}
		}
	}
}

func TestPermissionTextTranslatedButJSONKept(t *testing.T) {
	event := agent.PermissionEvent{
		ToolCallID: "call_1",
		ToolName:   "bash",
		Action:     agent.PermissionActionPrompt,
		Reason:     "network access requires approval",
		SideEffect: "network",
		Scope:      "example.com",
	}
	var textOut, textErr strings.Builder
	textWriter := execEventWriter{stdout: &textOut, stderr: &textErr, format: execOutputText, runID: "r1", streamedText: &strings.Builder{}}
	textWriter.permission(event)
	if textWriter.err != nil {
		t.Fatalf("permission: %v", textWriter.err)
	}
	got := textErr.String()
	for _, want := range []string{"[permission]", "bash", "requer aprovação", "O acesso à rede precisa de aprovação."} {
		if !strings.Contains(got, want) {
			t.Fatalf("text permission line = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "network access requires approval") {
		t.Fatalf("text permission line leaked canonical reason: %q", got)
	}
	var jsonOut strings.Builder
	jsonWriter := execEventWriter{stdout: &jsonOut, stderr: &strings.Builder{}, format: execOutputJSON, runID: "r1", streamedText: &strings.Builder{}}
	jsonWriter.permission(event)
	if jsonWriter.err != nil {
		t.Fatalf("permission json: %v", jsonWriter.err)
	}
	if !strings.Contains(jsonOut.String(), `"reason":"network access requires approval"`) {
		t.Fatalf("json permission must keep canonical reason, got %q", jsonOut.String())
	}
}
