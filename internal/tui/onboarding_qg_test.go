package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// qgSetupOptions builds a first-run setup state the way the launch path will:
// QG-only catalog entry, gateway validator injected.
func qgSetupOptions(save func(SetupSelection) (SetupResult, error), validate func(context.Context, string) error) SetupOptions {
	return SetupOptions{
		Visible:    true,
		Required:   true,
		ConfigPath: "/tmp/qg/config.json",
		Providers: []SetupProviderOption{
			{ID: "qg", Name: "Quebra Galho", DefaultModel: "muse-spark-1.3-contributor", EnvVar: "QUEBRAGALHO_API_KEY", RequiresAuth: true},
		},
		ValidateAPIKey: validate,
		Save:           save,
	}
}

// The wizard is exactly three QG screens: welcome, API key, model.
func TestSetupStagesAreThreeQGOnlyScreens(t *testing.T) {
	m := newModel(context.Background(), Options{Setup: qgSetupOptions(nil, nil)})
	got := m.setupStages()
	want := []setupStage{setupStageWelcome, setupStageCredentials, setupStageModel}
	if len(got) != len(want) {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("stages = %v, want %v", got, want)
		}
	}
}

// The credential screen demands a key and validates it against the gateway
// before the model screen is reachable.
func TestSetupCredentialsRequiresAndValidatesKey(t *testing.T) {
	checked := ""
	m := newModel(context.Background(), Options{Setup: qgSetupOptions(nil, func(_ context.Context, key string) error {
		checked = key
		return nil
	})})
	m.width, m.height = 100, 30
	if m.setup.stage != setupStageWelcome {
		t.Fatalf("stage = %v, want welcome", m.setup.stage)
	}

	m.setup.stage = setupStageCredentials
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if m.setup.stage != setupStageCredentials {
		t.Fatalf("empty key advanced to %v", m.setup.stage)
	}
	if m.setup.err == "" {
		t.Fatal("empty key should report an inline error")
	}
	if checked != "" {
		t.Fatalf("validator called with empty key: %q", checked)
	}

	m.setup.apiKey.SetValue("sk-good")
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmd == nil || !m.setup.keyCheck {
		t.Fatalf("valid key should dispatch validation (cmd=%v keyCheck=%v)", cmd != nil, m.setup.keyCheck)
	}
	if m.setup.stage != setupStageCredentials {
		t.Fatalf("stage = %v, want to stay on credentials while validating", m.setup.stage)
	}
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if m.setup.stage != setupStageModel {
		t.Fatalf("stage after successful validation = %v, want model", m.setup.stage)
	}
	if m.setup.keyCheck {
		t.Fatal("keyCheck must clear after the result lands")
	}
	if checked != "sk-good" {
		t.Fatalf("validator received %q, want sk-good", checked)
	}
}

// A rejected key keeps the credential screen, surfaces the gateway error, and
// never echoes the key itself.
func TestSetupCredentialsValidationFailureKeepsStage(t *testing.T) {
	m := newModel(context.Background(), Options{Setup: qgSetupOptions(nil, func(context.Context, string) error {
		return errors.New("the gateway rejected the API key (HTTP 401)")
	})})
	m.setup.stage = setupStageCredentials
	m.setup.apiKey.SetValue("sk-wrong")
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected a validation command")
	}
	updated, _ = m.Update(cmd())
	m = updated.(model)
	if m.setup.stage != setupStageCredentials {
		t.Fatalf("stage = %v, want credentials after rejection", m.setup.stage)
	}
	if !strings.Contains(m.setup.err, "rejected") {
		t.Fatalf("err = %q, want rejection message", m.setup.err)
	}
	if strings.Contains(m.setup.err, "sk-wrong") {
		t.Fatalf("err leaked the API key: %q", m.setup.err)
	}
	if !m.setup.visible {
		t.Fatal("setup must stay visible after a rejected key")
	}
}

// The model screen is the offline 19-model QG catalog with the default model
// preselected, so Enter alone completes a correct setup.
func TestSetupModelListIsQG19WithMuseSparkPreselected(t *testing.T) {
	m := newModel(context.Background(), Options{Setup: qgSetupOptions(nil, nil)})
	m.setup.stage = setupStageModel
	m.resetSetupModels()
	models := m.setupFilteredModels()
	if len(models) != 19 {
		t.Fatalf("model count = %d, want 19 QG models", len(models))
	}
	if current := m.setupCurrentModel(); current.ID != "muse-spark-1.3-contributor" {
		t.Fatalf("preselected model = %q, want muse-spark-1.3-contributor", current.ID)
	}
	m.width, m.height = 100, 30
	view := plainRender(t, m.View())
	if !strings.Contains(view, "muse-spark-1.3-contributor") {
		t.Fatalf("model screen must show muse-spark:\n%s", view)
	}
}

// The welcome screen names Quebra Galho and announces the browser login that
// follows it.
func TestSetupWelcomeNamesQuebraGalhoAndLogin(t *testing.T) {
	m := newModel(context.Background(), Options{Setup: qgSetupOptions(nil, nil)})
	m.width, m.height = 100, 30
	view := plainRender(t, m.View())
	if !strings.Contains(view, "Welcome to Quebra Galho") {
		t.Fatalf("welcome title missing:\n%s", view)
	}
	if !strings.Contains(view, "sign in with your browser") {
		t.Fatalf("welcome must announce the browser login:\n%s", view)
	}
}
