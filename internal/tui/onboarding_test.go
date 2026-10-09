package tui

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
)

func TestSetupTakeoverRendersAndCompletes(t *testing.T) {
	var saved SetupSelection
	m := newModel(context.Background(), Options{
		Setup: SetupOptions{
			Visible:    true,
			Required:   true,
			ConfigPath: "/tmp/qg/config.json",
			Providers: []SetupProviderOption{
				{ID: "qg", Name: "Quebra Galho", DefaultModel: "muse-spark-1.3-contributor", EnvVar: "QUEBRAGALHO_API_KEY", RequiresAuth: true},
			},
			Save: func(selection SetupSelection) (SetupResult, error) {
				saved = selection
				return SetupResult{
					ConfigPath: "/tmp/qg/config.json",
					Provider: config.ProviderProfile{
						Name:      selection.CatalogID,
						CatalogID: selection.CatalogID,
						Model:     selection.Model,
					},
				}, nil
			},
		},
	})
	m.width = 100
	m.height = 30

	if view := plainRender(t, m.View()); !strings.Contains(view, "Welcome to Quebra Galho") || !strings.Contains(view, "Space to set up QG") || !strings.Contains(view, "terminal agent for changing real code") {
		t.Fatalf("setup welcome view missing expected text:\n%s", view)
	}

	updated, cmd := m.Update(testKey(tea.KeySpace))
	if cmd != nil {
		t.Fatal("setup navigation should not launch a command")
	}
	m = updated.(model)
	if m.setup.stage != setupStageCredentials {
		t.Fatalf("stage = %v, want credentials", m.setup.stage)
	}

	// Paste the key and continue; fixtures inject no validator, so Enter moves on.
	updated, _ = m.Update(testPaste("sk-takeover"))
	m = updated.(model)
	updated, _ = m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if m.setup.stage != setupStageModel {
		t.Fatalf("stage = %v, want model after key entry", m.setup.stage)
	}

	updated, cmd = m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("setup completion should stay in the fullscreen chat surface")
	}

	if m.setup.visible {
		t.Fatal("setup should hide after save")
	}
	if saved.CatalogID != "qg" || saved.Model != "muse-spark-1.3-contributor" {
		t.Fatalf("saved selection = %#v, want qg + muse-spark-1.3-contributor", saved)
	}
	if m.providerName != "qg" || m.modelName != "muse-spark-1.3-contributor" {
		t.Fatalf("provider state = %q/%q, want qg/muse-spark-1.3-contributor", m.providerName, m.modelName)
	}
	if !m.transcriptEmpty() {
		t.Fatalf("setup completion should open the normal empty chat surface, transcript: %#v", m.transcript)
	}
}
func TestSetupCompletionResetsChatSurfaceInsideAltScreen(t *testing.T) {
	m := newModel(context.Background(), Options{
		DiscoverProviderModels: func(ctx context.Context, profile config.ProviderProfile) ([]providermodeldiscovery.Model, error) {
			return nil, errors.New("offline")
		},
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "openai", Name: "OpenAI", DefaultModel: "gpt-4.1", EnvVar: "OPENAI_API_KEY", RequiresAuth: true},
			},
			Save: func(selection SetupSelection) (SetupResult, error) {
				return SetupResult{
					ConfigPath: "/tmp/qg/config.json",
					Provider: config.ProviderProfile{
						Name:      selection.CatalogID,
						CatalogID: selection.CatalogID,
						Model:     selection.Model,
					},
				}, nil
			},
		},
	})
	m.width = 100
	m.height = 30
	m.setup.stage = setupStageModel
	m.headerPrinted = true
	m.flushQueue = []string{"stale setup title"}
	m.printInFlight = true

	updated, cmd := m.completeSetup()
	m = updated.(model)
	if cmd != nil {
		t.Fatal("setup completion should not exit the alt-screen")
	}
	if m.setup.visible {
		t.Fatal("setup should be hidden")
	}
	if m.headerPrinted {
		t.Fatal("chat header should be reset so the normal surface can render it")
	}
	if len(m.flushQueue) != 0 {
		t.Fatalf("stale setup flush queue should be cleared, got %#v", m.flushQueue)
	}
	if m.printInFlight {
		t.Fatal("stale setup print state should be cleared")
	}
	if !m.transcriptEmpty() {
		t.Fatalf("setup completion should keep the chat empty state, transcript: %#v", m.transcript)
	}
}

func TestSetupTakeoverBlocksPromptSubmission(t *testing.T) {
	m := newModel(context.Background(), Options{
		DiscoverProviderModels: func(ctx context.Context, profile config.ProviderProfile) ([]providermodeldiscovery.Model, error) {
			return nil, errors.New("offline")
		},
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "openai", Name: "OpenAI", DefaultModel: "gpt-4.1"},
			},
		},
	})
	m.input.SetValue("run tests")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("setup enter should not launch an agent run")
	}
	if m.pending {
		t.Fatal("setup enter should not start a prompt")
	}
	if m.setup.stage != setupStageWelcome {
		t.Fatalf("stage = %v, want welcome because Enter is not advertised here", m.setup.stage)
	}
}

func TestSetupRightArrowDoesNotAdvance(t *testing.T) {
	saveCalls := 0
	for _, stage := range []setupStage{
		setupStageWelcome,
		setupStageCredentials,
		setupStageModel,
	} {
		m := newModel(context.Background(), Options{
			Setup: SetupOptions{
				Visible: true,
				Providers: []SetupProviderOption{
					{ID: "openai", Name: "OpenAI", DefaultModel: "gpt-4.1", EnvVar: "OPENAI_API_KEY", RequiresAuth: true},
				},
				Save: func(selection SetupSelection) (SetupResult, error) {
					saveCalls++
					return SetupResult{}, nil
				},
			},
		})
		m.setup.stage = stage

		updated, cmd := m.Update(testKey(tea.KeyRight))
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("right arrow at stage %v should not return a command", stage)
		}
		if m.setup.stage != stage {
			t.Fatalf("right arrow advanced stage %v to %v", stage, m.setup.stage)
		}
		if !m.setup.visible {
			t.Fatalf("right arrow at stage %v should not hide setup", stage)
		}
	}
	if saveCalls != 0 {
		t.Fatalf("right arrow should not save setup, got %d save calls", saveCalls)
	}
}

func TestSetupEnterDoesNotAdvanceSpaceOnlyStages(t *testing.T) {
	saveCalls := 0
	for _, stage := range []setupStage{
		setupStageWelcome,
		setupStageCredentials,
	} {
		m := newModel(context.Background(), Options{
			Setup: SetupOptions{
				Visible: true,
				Providers: []SetupProviderOption{
					{ID: "ollama", Name: "Ollama Local", DefaultModel: "llama3.1", Local: true},
				},
				Save: func(selection SetupSelection) (SetupResult, error) {
					saveCalls++
					return SetupResult{}, nil
				},
			},
		})
		m.setup.stage = stage

		updated, cmd := m.Update(testKey(tea.KeyEnter))
		m = updated.(model)
		if cmd != nil {
			t.Fatalf("enter at stage %v should not return a command", stage)
		}
		if m.setup.stage != stage {
			t.Fatalf("enter advanced stage %v to %v", stage, m.setup.stage)
		}
		if !m.setup.visible {
			t.Fatalf("enter at stage %v should not hide setup", stage)
		}
	}
	if saveCalls != 0 {
		t.Fatalf("enter on space-only steps should not save setup, got %d save calls", saveCalls)
	}
}

func TestSetupCredentialsAcceptsPastedAPIKeyWithoutRenderingSecret(t *testing.T) {
	const secret = "sk-pasted-secret"
	var saved SetupSelection
	m := newModel(context.Background(), Options{
		DiscoverProviderModels: func(ctx context.Context, profile config.ProviderProfile) ([]providermodeldiscovery.Model, error) {
			return nil, errors.New("offline")
		},
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "ollama-cloud", Name: "Ollama Cloud", DefaultModel: "qwen3-coder:480b", EnvVar: "OLLAMA_API_KEY", RequiresAuth: true},
			},
			Save: func(selection SetupSelection) (SetupResult, error) {
				saved = selection
				return SetupResult{
					ConfigPath: "/tmp/qg/config.json",
					Provider: config.ProviderProfile{
						Name:      selection.CatalogID,
						CatalogID: selection.CatalogID,
						Model:     selection.Model,
						APIKey:    selection.APIKey,
					},
				}, nil
			},
		},
	})
	m.width = 96
	m.height = 30
	m.setup.stage = setupStageCredentials

	updated, _ := m.Update(testPaste(secret))
	m = updated.(model)
	view := plainRender(t, m.View())
	if strings.Contains(view, secret) {
		t.Fatalf("setup view leaked pasted API key:\n%s", view)
	}
	if !strings.Contains(view, strings.Repeat("*", len(secret))) {
		t.Fatalf("setup view should show masked API key, got:\n%s", view)
	}

	m = pressSetupContinueOnce(m) // credentials -> model
	updated, _ = m.Update(testKey(tea.KeyEnter))
	m = updated.(model)

	if saved.APIKey != secret {
		t.Fatalf("saved APIKey = %q, want pasted secret", saved.APIKey)
	}
	if m.providerProfile.APIKey != secret {
		t.Fatalf("providerProfile APIKey = %q, want pasted secret", m.providerProfile.APIKey)
	}
}

func TestSetupCredentialsCtrlVDoesNotRunClipboardPaste(t *testing.T) {
	m := newModel(context.Background(), Options{
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "ollama-cloud", Name: "Ollama Cloud", DefaultModel: "qwen3-coder:480b", EnvVar: "OLLAMA_API_KEY", RequiresAuth: true},
			},
		},
	})
	m.setup.stage = setupStageCredentials
	m.setup.apiKey.SetValue("existing")
	m.setup.apiKey.CursorEnd()

	updated, cmd := m.Update(testKeyCtrl('v'))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("ctrl+v should not run the setup input clipboard paste command")
	}
	if got := next.setup.apiKey.Value(); got != "existing" {
		t.Fatalf("setup API key after ctrl+v = %q, want unchanged", got)
	}
}

func TestSetupModelSearchAcceptsQ(t *testing.T) {
	m := newModel(context.Background(), Options{
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "openai", Name: "OpenAI", DefaultModel: "gpt-4.1", EnvVar: "OPENAI_API_KEY", RequiresAuth: true},
			},
		},
	})
	m.setup.stage = setupStageModel
	m.resetSetupModels()

	updated, cmd := m.Update(testKeyText("q"))
	m = updated.(model)
	if cmd != nil {
		t.Fatal("q should search on the model step, not quit setup")
	}
	if m.setup.modelQuery != "q" {
		t.Fatalf("model query = %q, want q", m.setup.modelQuery)
	}
}

func TestSetupModelFooterUsesEnter(t *testing.T) {
	m := newModel(context.Background(), Options{
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "openai", Name: "OpenAI", DefaultModel: "gpt-4.1", EnvVar: "OPENAI_API_KEY", RequiresAuth: true},
			},
		},
	})
	m.width = 96
	m.height = 24
	m.setup.stage = setupStageModel

	view := plainRender(t, m.View())
	if !strings.Contains(view, "type search") || !strings.Contains(view, "Enter continue") {
		t.Fatalf("model footer should advertise search and Enter, got:\n%s", view)
	}
	if strings.Contains(view, "Space to continue") {
		t.Fatalf("model footer should not advertise Space, got:\n%s", view)
	}
}

func TestSetupModelSearchPlaceholderPutsCursorBeforeHint(t *testing.T) {
	m := newModel(context.Background(), Options{
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "openai", Name: "OpenAI", DefaultModel: "gpt-4.1", EnvVar: "OPENAI_API_KEY", RequiresAuth: true},
			},
		},
	})
	m.setup.stage = setupStageModel

	empty := plainRender(t, m.setupModelSearchLine(60))
	if !strings.Contains(empty, "search > ▌model name...") {
		t.Fatalf("empty search line = %q, want cursor before placeholder", empty)
	}
	if strings.Contains(empty, "model name...▌") {
		t.Fatalf("empty search line should not place cursor after placeholder: %q", empty)
	}

	m.setup.modelQuery = "qwen"
	filled := plainRender(t, m.setupModelSearchLine(60))
	if !strings.Contains(filled, "search > qwen▌") {
		t.Fatalf("filled search line = %q, want cursor after query", filled)
	}
}

func TestSetupCredentialLinesCenterLikeWelcome(t *testing.T) {
	m := newModel(context.Background(), Options{
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "ollama-cloud", Name: "Ollama Cloud", DefaultModel: "qwen3-coder:480b", EnvVar: "OLLAMA_API_KEY", RequiresAuth: true},
			},
		},
	})
	m.width = 120
	m.height = 30
	m.setup.stage = setupStageCredentials

	view := m.View()
	assertSetupLineCentered(t, view, "Your Quebra Galho API key", m.width)
	assertSetupLineCentered(t, view, "Paste the key", m.width)
	assertSetupLineCentered(t, view, "paste key", m.width)
	assertSetupLineCentered(t, view, "Saved keys", m.width)
}

func TestSetupCredentialEmptyInputDoesNotHighlightPlaceholder(t *testing.T) {
	m := newModel(context.Background(), Options{
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "ollama-cloud", Name: "Ollama Cloud", DefaultModel: "qwen3-coder:480b", EnvVar: "OLLAMA_API_KEY", RequiresAuth: true},
			},
		},
	})
	m.setup.stage = setupStageCredentials

	line := m.setupAPIKeyInputLine(80)
	plain := plainRender(t, line)
	if plain != "paste your Quebra Galho API key" {
		t.Fatalf("empty API key input = %q, want placeholder only", plain)
	}
	if strings.Count(plain, "paste") != 1 {
		t.Fatalf("empty API key input should render placeholder once, got %q", plain)
	}
}

func TestSetupProgressRendersAboveFooter(t *testing.T) {
	m := newModel(context.Background(), Options{
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "openai", Name: "OpenAI", DefaultModel: "gpt-4.1", EnvVar: "OPENAI_API_KEY", RequiresAuth: true},
			},
		},
	})
	m.width = 96
	m.height = 24
	m.setup.stage = setupStageModel

	lines := strings.Split(plainRender(t, m.View()), "\n")
	stepIndex := -1
	footerIndex := -1
	for index, line := range lines {
		if strings.Contains(line, "3/3") {
			stepIndex = index
		}
		if strings.Contains(line, "Enter continue") {
			footerIndex = index
		}
		if strings.Contains(line, "Choose a model") && strings.Contains(line, "3/3") {
			t.Fatalf("progress should not render in setup body: %q", line)
		}
	}
	if stepIndex < 0 || footerIndex < 0 {
		t.Fatalf("missing setup progress/footer, step=%d footer=%d view:\n%s", stepIndex, footerIndex, strings.Join(lines, "\n"))
	}
	if stepIndex != footerIndex-1 {
		t.Fatalf("progress should render immediately above footer, step line %d footer line %d", stepIndex, footerIndex)
	}
}

func pressSetupContinue(m model) model {
	return pressSetupContinueOnce(m)
}

func pressSetupContinueOnce(m model) model {
	var updated tea.Model
	var cmd tea.Cmd
	if m.setupCredentialInputActive() || m.setup.stage == setupStageModel {
		updated, cmd = m.Update(testKey(tea.KeyEnter))
	} else {
		updated, cmd = m.Update(testKey(tea.KeySpace))
	}
	m = updated.(model)
	if cmd != nil {
		updated, _ = m.Update(cmd())
		m = updated.(model)
	}
	return m
}

func displayColumnForVisibleLine(t *testing.T, view any, marker string) int {
	t.Helper()
	rendered := plainRender(t, view)
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, marker) {
			return displayColumn(line, marker)
		}
	}
	t.Fatalf("marker %q missing from view:\n%s", marker, rendered)
	return -1
}

func displayColumn(line string, marker string) int {
	index := strings.Index(line, marker)
	if index < 0 {
		return -1
	}
	return lipgloss.Width(line[:index])
}

func assertSetupLineCentered(t *testing.T, view any, marker string, width int) {
	t.Helper()
	line := visibleLineForMarker(t, view, marker)
	trimmed := strings.TrimSpace(line)
	start := lipgloss.Width(line[:strings.Index(line, strings.TrimLeft(line, " "))])
	midpoint := start + lipgloss.Width(trimmed)/2
	want := width / 2
	if delta := absInt(midpoint - want); delta > 2 {
		t.Fatalf("line %q midpoint = %d, want near %d (delta %d)", trimmed, midpoint, want, delta)
	}
}

func visibleLineForMarker(t *testing.T, view any, marker string) string {
	t.Helper()
	rendered := plainRender(t, view)
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("marker %q missing from view:\n%s", marker, rendered)
	return ""
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

// Completing setup switches the live provider, so it must export QG_PROVIDER
// exactly like the /model, /provider, and wizard switch paths — a stale value
// from an earlier switch would otherwise win over config in every spawned
// child (applyEnv) and pin specialists/swarm members to the OLD provider's
// credentials.
func TestCompleteSetupExportsActiveProviderEnv(t *testing.T) {
	t.Setenv(config.ActiveProviderEnv, "stale-previous-provider")
	m := newModel(context.Background(), Options{
		Setup: SetupOptions{
			Visible: true,
			Providers: []SetupProviderOption{
				{ID: "openai", Name: "OpenAI", DefaultModel: "gpt-4.1", EnvVar: "OPENAI_API_KEY", RequiresAuth: true},
			},
			Save: func(selection SetupSelection) (SetupResult, error) {
				return SetupResult{
					Provider: config.ProviderProfile{
						Name:      selection.CatalogID,
						CatalogID: selection.CatalogID,
						Model:     selection.Model,
					},
				}, nil
			},
		},
	})
	m.width = 100
	m.height = 30
	m.setup.stage = setupStageModel

	updated, _ := m.completeSetup()
	next := updated.(model)

	if next.providerName == "" {
		t.Fatal("setup completion should have set a provider name")
	}
	if got := os.Getenv(config.ActiveProviderEnv); got != next.providerName {
		t.Fatalf("%s = %q after setup save, want %q (children would spawn on the stale provider)", config.ActiveProviderEnv, got, next.providerName)
	}
}
