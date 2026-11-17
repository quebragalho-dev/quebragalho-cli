package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

func runStyleCommand(m model, input string) model {
	updated, _ := m.dispatchCommand(parseCommand(input))
	got, ok := updated.(model)
	if !ok {
		panic("dispatchCommand did not return a TUI model")
	}
	return got
}

func readStylePreference(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config %s: %v", path, err)
	}
	var cfg config.FileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode config %s: %v", path, err)
	}
	return cfg.Preferences.ResponseStyle
}

func TestSavedResponseStyleAppliedAtStartup(t *testing.T) {
	cases := []struct {
		name     string
		explicit string
		saved    string
		want     string
	}{
		{name: "saved value wins over default", saved: "explanatory", want: "explanatory"},
		{name: "empty saved falls back to concise", saved: "", want: "concise"},
		{name: "invalid saved falls back to concise", saved: "eloquente", want: "concise"},
		{name: "blank saved falls back to concise", saved: "   ", want: "concise"},
		{name: "explicit selection beats saved", explicit: "balanced", saved: "review", want: "balanced"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newModel(context.Background(), Options{
				ResponseStyle:      tc.explicit,
				SavedResponseStyle: tc.saved,
			})
			if m.responseStyle != tc.want {
				t.Fatalf("responseStyle = %q, want %q", m.responseStyle, tc.want)
			}
		})
	}
}

func TestBareStyleCommandOpensPicker(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.input.SetValue("/style")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if cmd != nil {
		t.Fatal("expected /style to open the picker without starting an agent run")
	}
	if next.picker == nil || next.picker.kind != pickerStyle {
		t.Fatalf("expected style picker to open, got %#v", next.picker)
	}
	wantLabels := []string{"Equilibrado", "Conciso", "Explicativo", "Revisão"}
	wantValues := []string{"balanced", "concise", "explanatory", "review"}
	if len(next.picker.items) != len(wantLabels) {
		t.Fatalf("expected %d style options, got %d", len(wantLabels), len(next.picker.items))
	}
	for i, item := range next.picker.items {
		if item.Label != wantLabels[i] || item.Value != wantValues[i] {
			t.Errorf("option %d = (%q, %q), want (%q, %q)", i, item.Label, item.Value, wantLabels[i], wantValues[i])
		}
		if item.Detail == "" {
			t.Errorf("option %d (%s) has no Portuguese description", i, item.Value)
		}
	}
	// Default style is concise: the picker must preselect it.
	if next.picker.selected != 1 {
		t.Fatalf("picker selected = %d, want 1 (current concise style)", next.picker.selected)
	}
}

func TestStylePickerPreselectsCurrentStyle(t *testing.T) {
	m := newModel(context.Background(), Options{SavedResponseStyle: "review"})
	m = runStyleCommand(m, "/style")
	if m.picker == nil || m.picker.kind != pickerStyle {
		t.Fatalf("expected style picker to open, got %#v", m.picker)
	}
	if m.picker.selected != 3 {
		t.Fatalf("picker selected = %d, want 3 (current review style)", m.picker.selected)
	}
}

func TestStyleCommandAcceptsPortugueseSynonyms(t *testing.T) {
	cases := []struct {
		arg   string
		from  string
		want  string
		label string
	}{
		{arg: "equilibrado", from: "review", want: "balanced", label: "equilibrado"},
		{arg: "conciso", from: "review", want: "concise", label: "conciso"},
		{arg: "explicativo", from: "concise", want: "explanatory", label: "explicativo"},
		{arg: "revisão", from: "concise", want: "review", label: "revisão"},
		{arg: "revisao", from: "concise", want: "review", label: "revisão"},
		{arg: "explanatory", from: "concise", want: "explanatory", label: "explicativo"},
	}
	for _, tc := range cases {
		t.Run(tc.arg, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			m := newModel(context.Background(), Options{UserConfigPath: path, SavedResponseStyle: tc.from})
			m = runStyleCommand(m, "/style "+tc.arg)
			if m.responseStyle != tc.want {
				t.Fatalf("responseStyle = %q, want %q", m.responseStyle, tc.want)
			}
			// The stored value feeds agent.Options.ResponseStyle when a run
			// starts, so it must stay English even for Portuguese input.
			if m.responseStyle != tc.want {
				t.Fatalf("agent-bound responseStyle = %q, want %q", m.responseStyle, tc.want)
			}
			if got := readStylePreference(t, path); got != tc.want {
				t.Fatalf("persisted responseStyle = %q, want %q (English value in file)", got, tc.want)
			}
			if want := "Estilo: " + tc.label + " · salvo"; m.transientNotice.text != want {
				t.Fatalf("notice = %q, want %q", m.transientNotice.text, want)
			}
		})
	}
}

func TestStylePickerSelectionAppliesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := newModel(context.Background(), Options{UserConfigPath: path})
	m = runStyleCommand(m, "/style")
	if m.picker == nil || m.picker.kind != pickerStyle {
		t.Fatalf("expected style picker to open, got %#v", m.picker)
	}
	m.picker.selected = 2 // Explicativo.
	updated, _ := m.choosePicker()
	next := updated.(model)
	if next.responseStyle != "explanatory" {
		t.Fatalf("responseStyle = %q, want explanatory", next.responseStyle)
	}
	if next.transientNotice.text != "Estilo: explicativo · salvo" {
		t.Fatalf("notice = %q, want saved confirmation", next.transientNotice.text)
	}
	if got := readStylePreference(t, path); got != "explanatory" {
		t.Fatalf("persisted responseStyle = %q, want explanatory", got)
	}
}

func TestStyleListCardStaysPortuguese(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = runStyleCommand(m, "/style list")
	if !transcriptContains(m.transcript, "estilo ativo: conciso") {
		t.Fatalf("expected Portuguese state line, got %#v", m.transcript)
	}
	if transcriptContains(m.transcript, "active style:") || transcriptContains(m.transcript, "Unknown response style") {
		t.Fatalf("expected no English style text, got %#v", m.transcript)
	}
}

func TestStyleCommandUnknownValue(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = runStyleCommand(m, "/style prolixo")
	if m.responseStyle != "concise" {
		t.Fatalf("responseStyle = %q, want unchanged concise", m.responseStyle)
	}
	if !transcriptContains(m.transcript, "Estilo desconhecido: prolixo") {
		t.Fatalf("expected Portuguese unknown-style message, got %#v", m.transcript)
	}
}

func TestStyleCommandWithoutConfigPathIsSessionOnly(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m = runStyleCommand(m, "/style review")
	if m.responseStyle != "review" {
		t.Fatalf("responseStyle = %q, want review", m.responseStyle)
	}
	// A change surfaces the transient notice (no transcript card), so the
	// session-only scope is reported there.
	if want := "Estilo: revisão · só nesta sessão"; m.transientNotice.text != want {
		t.Fatalf("notice = %q, want %q", m.transientNotice.text, want)
	}
	// Repeating the same value renders the state card, which carries the
	// session-only note.
	m = runStyleCommand(m, "/style review")
	if !transcriptContains(m.transcript, "Vale só nesta sessão") {
		t.Fatalf("expected session-only note, got %#v", m.transcript)
	}
}
