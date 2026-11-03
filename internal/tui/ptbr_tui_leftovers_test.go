package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// The escalated-shell reason stays canonical up to the approval; the card
// renders Portuguese through the shared agent mapping.
func TestPTBRPermissionEscalationReasonCard(t *testing.T) {
	const wantPT = "Este comando precisa rodar fora do ambiente isolado."

	for _, tc := range []struct{ reason, wantPT string }{
		{agent.ReasonShellNeedsUnsandboxed, "Este comando precisa rodar fora do ambiente isolado."},
		{"destructive shell command requires approval", "Comando destrutivo precisa de aprovação."},
	} {
		for _, width := range []int{50, 80, 120} {
			request := agent.PermissionRequest{
				ToolName:   "exec_command",
				SideEffect: "shell",
				Reason:     tc.reason,
				AvailableDecisions: []agent.PermissionDecisionAction{
					agent.PermissionDecisionAllow,
					agent.PermissionDecisionDeny,
				},
			}
			card, _ := renderFocusedPermissionPrompt(request, 0, false, "", width)
			flat := strings.Join(strings.Fields(plainRender(t, card)), " ")
			if !strings.Contains(flat, tc.wantPT) {
				t.Fatalf("width %d: permission card missing %q:\n%s", width, tc.wantPT, card)
			}
			if strings.Contains(flat, tc.reason) {
				t.Fatalf("width %d: permission card leaked canonical English:\n%s", width, card)
			}
		}
	}
}

// /model and /profile cards render Portuguese at 50 and 80 columns.
func TestPTBRModelAndProfileCards(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "gpt-test", ProviderName: "qg"})
	pmRestore := model{}
	pmRestore.agentOptions.Profile = &agent.ProfilePolicy{Escalate: &agent.PostureEscalation{MaxTurns: 80}}
	pmKeep := model{}
	pmKeep.agentOptions.Profile = &agent.ProfilePolicy{Escalate: &agent.PostureEscalation{}}
	cards := []struct {
		name, text string
		pts, ens   []string
	}{
		{"model", m.modelText(""), []string{"Ativo", "modelo:", "provedor:", "nível: automático"}, []string{"Active", "model:", "provider:", "effort:"}},
		{"profile", pmRestore.profileText(), []string{"Estado", "escalonamento:", "nota:"}, []string{"State", "escalation:", "note:"}},
		{"profile-keep", pmKeep.profileText(), []string{"Estado", "escalonamento:", "nota:"}, []string{"State", "escalation:", "note:"}},
	}
	for _, c := range cards {
		for _, width := range []int{50, 80} {
			got := plainRender(t, m.renderRow(transcriptRow{kind: rowSystem, text: c.text}, width, buildRowContext(nil)))
			for _, want := range c.pts {
				if !strings.Contains(got, want) {
					t.Fatalf("%s at %d missing %q:\n%s", c.name, width, want, got)
				}
			}
			for _, unwanted := range c.ens {
				if strings.Contains(got, unwanted) {
					t.Fatalf("%s at %d leaked %q:\n%s", c.name, width, unwanted, got)
				}
			}
		}
	}

	// The full /model hint fits at 120 columns.
	wide := plainRender(t, m.renderRow(transcriptRow{kind: rowSystem, text: m.modelText("")}, 120, buildRowContext(nil)))
	if !strings.Contains(wide, "para trocar o modelo") {
		t.Fatalf("model hint missing Portuguese at 120:\n%s", wide)
	}
	if strings.Contains(wide, "to inspect models") || strings.Contains(wide, "to switch this TUI session") {
		t.Fatalf("model hint kept English at 120:\n%s", wide)
	}

	// Full escalation sentences are pinned on the raw card text: the 50/80
	// renders cut these long lines exactly like the base did.
	for _, tc := range []struct {
		text     string
		pts, ens []string
	}{
		{pmRestore.profileText(),
			[]string{
				"escalonamento: armado — uma vez, após falhas seguidas de ferramenta, um ciclo de autocorreção que falha ou uma mutação de risco crítico; restaura o orçamento de turnos para 80",
				"nota: o sinal de conclusão incerta só existe sem TUI (o portão de conclusão nunca roda no modo interativo), então não dispara aqui",
			},
			[]string{"escalation:", "note:", "restores the turn budget", "cannot fire", "headless-only"}},
		{pmKeep.profileText(),
			[]string{"mantém o orçamento de turnos fixado"},
			[]string{"keeps the pinned turn budget"}},
	} {
		for _, want := range tc.pts {
			if !strings.Contains(tc.text, want) {
				t.Fatalf("profile text missing %q:\n%s", want, tc.text)
			}
		}
		for _, unwanted := range tc.ens {
			if strings.Contains(tc.text, unwanted) {
				t.Fatalf("profile text kept %q:\n%s", unwanted, tc.text)
			}
		}
	}

	// The /model switch summary keeps the note when persistence fails.
	root := t.TempDir()
	blockedParent := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedParent, []byte("block config directory creation"), 0o600); err != nil {
		t.Fatal(err)
	}
	sw := newModel(context.Background(), Options{
		UserConfigPath:  filepath.Join(blockedParent, "config.json"),
		ProviderName:    "qg",
		ModelName:       "muse-spark-1.3-contributor",
		Provider:        &fakeProvider{},
		ProviderProfile: config.ProviderProfile{Name: "qg", CatalogID: "qg", Model: "muse-spark-1.3-contributor", APIKeyEnv: "QUEBRAGALHO_API_KEY", Provider: string(config.ProviderKindOpenAICompatible)},
		NewProvider:     func(config.ProviderProfile) (qgruntime.Provider, error) { return &fakeProvider{}, nil },
	})
	_, text, _, _ := sw.handleModelCommandScope("muse-spark-1.3-contributor", true)
	if !strings.Contains(text, "não salvo (") {
		t.Fatalf("switch summary missing não salvo: %q", text)
	}
	if strings.Contains(text, "not saved") {
		t.Fatalf("switch summary kept English: %q", text)
	}
}

// Missing effort labels map to Portuguese; the stored value stays English.
func TestPTBREffortMissingLabels(t *testing.T) {
	for value, want := range map[string]string{"none": "nenhum", "minimal": "mínimo"} {
		card := plainRender(t, model{reasoningEffort: modelregistry.ReasoningEffort(value)}.effortStatusCard(value, "detalhe"))
		if !strings.Contains(card, want) {
			t.Fatalf("effort card for %q missing %q:\n%s", value, want, card)
		}
		if strings.Contains(card, value) {
			t.Fatalf("effort card for %q leaked English label:\n%s", value, card)
		}
	}
	for input, want := range map[string]string{
		"nenhum": "none", "mínimo": "minimal", "minimo": "minimal",
	} {
		if got := canonicalEffortInput(input); got != want {
			t.Fatalf("canonicalEffortInput(%q) = %q, want %q", input, got, want)
		}
	}
}

// Choosing the Portuguese label stores the canonical English value.
func TestPTBREffortCanonicalStoredEnglish(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "future-reasoner", ProviderName: "test-provider"})
	m.providerProfile.CatalogID = "test-provider"
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{
		"test-provider": {{ID: "future-reasoner", ReasoningEfforts: []string{"minimal", "low", "ultra"}}},
	}
	next, text := m.handleEffortCommand("mínimo")
	if next.reasoningEffort != modelregistry.ReasoningEffortMinimal {
		t.Fatalf("stored effort = %q, want minimal", next.reasoningEffort)
	}
	if !strings.Contains(text, "mínimo") {
		t.Fatalf("card must show mínimo, got %q", text)
	}
	if strings.Contains(text, "minimal") {
		t.Fatalf("card must not show raw minimal, got %q", text)
	}
}

// The step-explanation prompt asks for Brazilian Portuguese.
func TestPTBRPlanStepExplanationPrompt(t *testing.T) {
	system, _ := planStepExplanationPrompt(planStep{content: "Wire the login form", status: "pending"}, nil, nil)
	if !strings.Contains(system, "Brazilian Portuguese") {
		t.Fatalf("system prompt missing Brazilian Portuguese:\n%s", system)
	}
	if strings.Contains(system, "plain-English") {
		t.Fatalf("system prompt kept plain-English:\n%s", system)
	}
}

// One table for the whole canonical -> Portuguese permission map.
func TestPTBRDisplayPermissionReasonTable(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"destructive shell command requires approval", "Comando destrutivo precisa de aprovação."},
		{"The agent is requesting additional permissions.", "O agente está pedindo permissões adicionais."},
		{"tool requires approval before execution", "Ferramenta precisa de aprovação prévia."},
		{"cancelled in TUI", "cancelado na TUI"},
		{"run superseded", "execução substituída"},
		{"permission prompt unavailable", "pedido indisponível"},
		{"run cancelled", "execução cancelada"},
		{"blocked by a beforeTool hook", "bloqueado pelo hook beforeTool"},
		{"Reading /tmp/data.json requires access outside the workspace.", "Ler /tmp/data.json exige acesso fora da pasta de trabalho."},
		{"Writing to /tmp/out.txt requires access outside the workspace. Use /add-dir or --add-dir to allow writes there.", "Escrever em /tmp/out.txt exige acesso fora da pasta de trabalho. Use /add-dir ou --add-dir para permitir escrita lá."},
		{"Reading /tmp/um dir/relatório.json requires access outside the workspace.", "Ler /tmp/um dir/relatório.json exige acesso fora da pasta de trabalho."},
		{"Reading the docs requires approval", "Reading the docs requires approval"},
		{"some model-provided reason", "some model-provided reason"},
	} {
		if got := agent.DisplayPermissionReason(tc.in); got != tc.want {
			t.Fatalf("DisplayPermissionReason(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The grant-matched detail renders through the same map.
func TestPTBRGrantMatchedDetailRendersPortuguese(t *testing.T) {
	detail := permissionDetailText(agent.PermissionEvent{Action: agent.PermissionActionAllow, GrantMatched: true})
	if !strings.Contains(detail, "aprovado por permissão salva") {
		t.Fatalf("grant detail missing Portuguese: %q", detail)
	}
	if strings.Contains(detail, "approved by saved permission") {
		t.Fatalf("grant detail kept English: %q", detail)
	}
}

// A denied row renders the mapped reason without new cuts.
func TestPTBRDenyRowRendersPortuguese(t *testing.T) {
	for _, width := range []int{50, 80, 120} {
		event := &agent.PermissionEvent{ToolName: "bash", Action: agent.PermissionActionDeny, Reason: "run cancelled"}
		got := plainRender(t, renderPermissionRow(transcriptRow{tool: "bash", permission: event}, width))
		flat := strings.Join(strings.Fields(got), " ")
		if !strings.Contains(flat, "negado") {
			t.Fatalf("width %d: deny row lost negado:\n%s", width, got)
		}
		if !strings.Contains(flat, "execução cancelada") {
			t.Fatalf("width %d: deny row missing Portuguese:\n%s", width, got)
		}
		if strings.Contains(flat, "run cancelled") {
			t.Fatalf("width %d: deny row leaked canonical English:\n%s", width, got)
		}
		if strings.Contains(got, "…") {
			t.Fatalf("width %d: deny row cut the reason:\n%s", width, got)
		}
	}
}
