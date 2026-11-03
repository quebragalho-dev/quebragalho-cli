package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

// Item 1: the escalated-shell fallback is canonical English at the agent/JSON
// layers; the permission card renders the Portuguese mapping.
func TestPTBRPermissionEscalationReasonCard(t *testing.T) {
	const canonical = "This command needs to run outside the sandbox."
	const wantPT = "Este comando precisa rodar fora do ambiente isolado."

	if got := permissionDisplayReason(canonical); got != wantPT {
		t.Fatalf("permissionDisplayReason(%q) = %q, want %q", canonical, got, wantPT)
	}

	// The canonical event payload keeps the English string: only the human
	// card translates.
	event := agent.PermissionEvent{ToolName: "exec_command", Reason: canonical}
	if event.Reason != canonical {
		t.Fatalf("event Reason mutated to %q", event.Reason)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), canonical) {
		t.Fatalf("event JSON lost canonical reason: %s", raw)
	}
	if strings.Contains(string(raw), wantPT) {
		t.Fatalf("event JSON must not carry the display translation: %s", raw)
	}

	for _, width := range []int{50, 80, 120} {
		request := agent.PermissionRequest{
			ToolName:   "exec_command",
			SideEffect: "shell",
			Reason:     canonical,
			AvailableDecisions: []agent.PermissionDecisionAction{
				agent.PermissionDecisionAllow,
				agent.PermissionDecisionDeny,
			},
		}
		card, _ := renderFocusedPermissionPrompt(request, 0, false, "", width)
		got := plainRender(t, card)
		flat := strings.Join(strings.Fields(got), " ")
		if !strings.Contains(flat, wantPT) {
			t.Fatalf("width %d: permission card missing %q:\n%s", width, wantPT, got)
		}
		if strings.Contains(flat, canonical) {
			t.Fatalf("width %d: permission card leaked canonical English:\n%s", width, got)
		}
		if strings.Contains(flat, sandbox.ReasonEscalatedSandboxRequired) {
			t.Fatalf("width %d: permission card leaked internal sandbox copy:\n%s", width, got)
		}
		// The full sentence survives wrapping (flat joins the wrapped lines),
		// so the reason itself is never cut; footer chrome may still ellipsize
		// at 50 columns as in the base.
	}
}

// Item 2: /model and /profile card titles plus the model-switch not-saved note.
func TestPTBRModelAndProfileCards(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "gpt-test", ProviderName: "qg"})

	modelCard := plainRender(t, m.modelText(""))
	foundAtivo := false
	for _, line := range strings.Split(modelCard, "\n") {
		switch strings.TrimSpace(line) {
		case "Ativo":
			foundAtivo = true
		case "Active":
			t.Fatalf("/model card kept English section title:\n%s", modelCard)
		}
	}
	if !foundAtivo {
		t.Fatalf("/model card missing Ativo:\n%s", modelCard)
	}

	profileCard := plainRender(t, model{}.profileText())
	foundEstado := false
	for _, line := range strings.Split(profileCard, "\n") {
		switch strings.TrimSpace(line) {
		case "Estado":
			foundEstado = true
		case "State":
			t.Fatalf("/profile card kept English section title:\n%s", profileCard)
		}
	}
	if !foundEstado {
		t.Fatalf("/profile card missing Estado:\n%s", profileCard)
	}

	// The persisted-switch failure note is masculine singular (o modelo).
	notice, _ := model{}.modelAppliedNoticeFor(errors.New("unwritable config"))
	if !strings.Contains(notice, "não salvo (") {
		t.Fatalf("applied notice missing não salvo: %q", notice)
	}
	if strings.Contains(notice, "not saved") {
		t.Fatalf("applied notice kept English: %q", notice)
	}

	// The /model switch summary carries the same note when persistence fails.
	{
		root := t.TempDir()
		blockedParent := filepath.Join(root, "not-a-directory")
		if err := os.WriteFile(blockedParent, []byte("block config directory creation"), 0o600); err != nil {
			t.Fatal(err)
		}
		baseProfile := config.ProviderProfile{
			Name:      "qg",
			CatalogID: "qg",
			Model:     "muse-spark-1.3-contributor",
			APIKeyEnv: "QUEBRAGALHO_API_KEY",
			Provider:  string(config.ProviderKindOpenAICompatible),
		}
		sw := newModel(context.Background(), Options{
			UserConfigPath:  filepath.Join(blockedParent, "config.json"),
			ProviderName:    "qg",
			ModelName:       "muse-spark-1.3-contributor",
			Provider:        &fakeProvider{},
			ProviderProfile: baseProfile,
			NewProvider: func(config.ProviderProfile) (qgruntime.Provider, error) {
				return &fakeProvider{}, nil
			},
		})
		_, text, _, _ := sw.handleModelCommandScope("muse-spark-1.3-contributor", true)
		if !strings.Contains(text, "não salvo (") {
			t.Fatalf("switch summary missing não salvo: %q", text)
		}
		if strings.Contains(text, "not saved") {
			t.Fatalf("switch summary kept English: %q", text)
		}
	}

	// Width: the touched cards render without new cuts at 50/80.
	for _, width := range []int{50, 80} {
		for name, text := range map[string]string{"model": m.modelText(""), "profile": model{}.profileText()} {
			row := transcriptRow{kind: rowSystem, text: text}
			got := plainRender(t, m.renderRow(row, width, buildRowContext(nil)))
			if name == "model" && !strings.Contains(got, "Ativo") {
				t.Fatalf("width %d: /model card lost Ativo:\n%s", width, got)
			}
			if name == "profile" && !strings.Contains(got, "Estado") {
				t.Fatalf("width %d: /profile card lost Estado:\n%s", width, got)
			}
			for _, line := range strings.Split(got, "\n") {
				if strings.TrimSpace(line) == "Active" || strings.TrimSpace(line) == "State" {
					t.Fatalf("width %d: %s card leaked English title:\n%s", width, name, got)
				}
			}
		}
	}
}

// Item 3: missing effort labels and their canonical inputs.
func TestPTBREffortMissingLabels(t *testing.T) {
	for value, want := range map[string]string{
		"none":    "nenhum",
		"minimal": "mínimo",
		"ultra":   "ultra",
	} {
		if got := effortDisplayLabel(value); got != want {
			t.Fatalf("effortDisplayLabel(%q) = %q, want %q", value, got, want)
		}
		if got := effortDisplayLabel(strings.ToUpper(value)); got != want {
			t.Fatalf("effortDisplayLabel(%q) = %q, want %q", strings.ToUpper(value), got, want)
		}
		if value == "ultra" {
			continue
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
		"nenhum": "none",
		"NENHUM": "none",
		"mínimo": "minimal",
		"minimo": "minimal",
		"MÍNIMO": "minimal",
		"ultra":  "ultra",
		"ULTRA":  "ultra",
	} {
		if got := canonicalEffortInput(input); got != want {
			t.Fatalf("canonicalEffortInput(%q) = %q, want %q", input, got, want)
		}
	}
}

func modelWithEfforts(t *testing.T, name string, efforts []string) model {
	t.Helper()
	m := newModel(context.Background(), Options{ModelName: name, ProviderName: "test-provider"})
	m.providerProfile.CatalogID = "test-provider"
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{
		"test-provider": {{ID: name, ReasoningEfforts: efforts}},
	}
	return m
}

// Choosing the Portuguese label stores the canonical English value.
func TestPTBREffortCanonicalStoredEnglish(t *testing.T) {
	m := modelWithEfforts(t, "future-reasoner", []string{"minimal", "low", "ultra"})

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

	nextUltra, textUltra := m.handleEffortCommand("ultra")
	if nextUltra.reasoningEffort != modelregistry.ReasoningEffortUltra {
		t.Fatalf("stored effort = %q, want ultra", nextUltra.reasoningEffort)
	}
	if !strings.Contains(textUltra, "ultra") {
		t.Fatalf("card must show ultra, got %q", textUltra)
	}

	nextNone := modelWithEfforts(t, "urn-reasoner", []string{"low", "high"})
	nextNone.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{
		"test-provider": {{ID: "urn-reasoner", ReasoningEfforts: []string{"none", "low", "high"}}},
	}
	_ = nextNone
	if got := canonicalEffortInput("nenhum"); got != "none" {
		t.Fatalf("canonicalEffortInput(nenhum) = %q, want none", got)
	}
	if got := effortDisplayLabel("none"); got != "nenhum" {
		t.Fatalf("effortDisplayLabel(none) = %q, want nenhum", got)
	}
}

// Item 4: the step-explanation prompt asks for Brazilian Portuguese in simple
// language; the request itself is otherwise unchanged.
func TestPTBRPlanStepExplanationPrompt(t *testing.T) {
	step := planStep{content: "Wire the login form", status: "pending"}
	system, user := planStepExplanationPrompt(step, nil, nil)

	for _, want := range []string{"Brazilian Portuguese", "simple language", "SHORT, friendly", "a few sentences at most"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, system)
		}
	}
	for _, unwanted := range []string{"plain-English", "plain English"} {
		if strings.Contains(system, unwanted) {
			t.Fatalf("system prompt kept %q:\n%s", unwanted, system)
		}
	}
	if !strings.Contains(user, "Wire the login form") {
		t.Fatalf("user prompt lost the step text:\n%s", user)
	}
}
