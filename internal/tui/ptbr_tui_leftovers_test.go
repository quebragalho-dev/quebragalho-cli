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
	if agent.ReasonShellNeedsUnsandboxed != "This command needs to run outside the sandbox." {
		t.Fatalf("canonical constant changed: %q", agent.ReasonShellNeedsUnsandboxed)
	}
	if got := agent.DisplayPermissionReason(agent.ReasonShellNeedsUnsandboxed); got != wantPT {
		t.Fatalf("shared mapping = %q, want %q", got, wantPT)
	}

	for _, width := range []int{50, 80, 120} {
		request := agent.PermissionRequest{
			ToolName:   "exec_command",
			SideEffect: "shell",
			Reason:     agent.ReasonShellNeedsUnsandboxed,
			AvailableDecisions: []agent.PermissionDecisionAction{
				agent.PermissionDecisionAllow,
				agent.PermissionDecisionDeny,
			},
		}
		card, _ := renderFocusedPermissionPrompt(request, 0, false, "", width)
		flat := strings.Join(strings.Fields(plainRender(t, card)), " ")
		if !strings.Contains(flat, wantPT) {
			t.Fatalf("width %d: permission card missing %q:\n%s", width, wantPT, card)
		}
		if strings.Contains(flat, agent.ReasonShellNeedsUnsandboxed) {
			t.Fatalf("width %d: permission card leaked canonical English:\n%s", width, card)
		}
	}
}

// /model and /profile cards render Portuguese titles at 50 and 80 columns.
func TestPTBRModelAndProfileCards(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "gpt-test", ProviderName: "qg"})
	cards := []struct{ name, text, wantPT, wantEN string }{
		{"model", m.modelText(""), "Ativo", "Active"},
		{"profile", model{}.profileText(), "Estado", "State"},
	}
	for _, c := range cards {
		for _, width := range []int{50, 80} {
			got := plainRender(t, m.renderRow(transcriptRow{kind: rowSystem, text: c.text}, width, buildRowContext(nil)))
			if !strings.Contains(got, c.wantPT) {
				t.Fatalf("%s at %d missing %q:\n%s", c.name, width, c.wantPT, got)
			}
			if strings.Contains(got, c.wantEN) {
				t.Fatalf("%s at %d leaked %q:\n%s", c.name, width, c.wantEN, got)
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
		if got := effortDisplayLabel(value); got != want {
			t.Fatalf("effortDisplayLabel(%q) = %q, want %q", value, got, want)
		}
		card := plainRender(t, model{reasoningEffort: modelregistry.ReasoningEffort(value)}.effortStatusCard(value, "detalhe"))
		if !strings.Contains(card, want) {
			t.Fatalf("effort card for %q missing %q:\n%s", value, want, card)
		}
		if strings.Contains(card, value) {
			t.Fatalf("effort card for %q leaked English label:\n%s", value, card)
		}
	}
	for input, want := range map[string]string{
		"nenhum": "none", "mínimo": "minimal", "minimo": "minimal", "ultra": "ultra",
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
