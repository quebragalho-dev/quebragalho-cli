package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
)

// autoCompactCommandModel builds a hermetic session model: an unknown model
// name resolves through the 200k fallback window, and the config path (when
// set) is a scratch file the command may persist to.
func autoCompactCommandModel(t *testing.T, withConfig bool) (model, string) {
	t.Helper()
	path := ""
	if withConfig {
		path = filepath.Join(t.TempDir(), "config.json")
	}
	catalog, err := modelregistry.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return model{userConfigPath: path, modelName: "autocompact-test-unknown", modelCatalog: catalog}, path
}

func runAutoCompactCommand(m model, input string) model {
	updated, _ := m.dispatchCommand(parseCommand(input))
	got, ok := updated.(model)
	if !ok {
		panic("dispatchCommand did not return a TUI model")
	}
	return got
}

func readAutoCompactFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Preferences map[string]any `json:"preferences"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Preferences == nil {
		return map[string]any{}
	}
	return raw.Preferences
}

func TestAutoCompactCommandAppliesLimit(t *testing.T) {
	m, path := autoCompactCommandModel(t, true)
	got := runAutoCompactCommand(m, "/autocompact 400k")
	if got.autoCompactLimit != 400000 {
		t.Fatalf("autoCompactLimit = %d, want 400000", got.autoCompactLimit)
	}
	if prefs := readAutoCompactFile(t, path); prefs["autoCompactLimit"] != 400000.0 {
		t.Fatalf("persisted autoCompactLimit = %v, want 400000", prefs["autoCompactLimit"])
	}
	want := "Auto-compact: 400k tokens"
	if !strings.Contains(got.transientNotice.text, want) {
		t.Fatalf("notice = %q, want it to contain %q", got.transientNotice.text, want)
	}
}

func TestAutoCompactCommandOffAndAuto(t *testing.T) {
	m, path := autoCompactCommandModel(t, true)

	got := runAutoCompactCommand(m, "/autocompact off")
	if got.autoCompactLimit != config.AutoCompactOff {
		t.Fatalf("autoCompactLimit = %d, want %d", got.autoCompactLimit, config.AutoCompactOff)
	}
	if prefs := readAutoCompactFile(t, path); prefs["autoCompactLimit"] != -1.0 {
		t.Fatalf("persisted autoCompactLimit = %v, want -1", prefs["autoCompactLimit"])
	}

	got = runAutoCompactCommand(got, "/autocompact auto")
	if got.autoCompactLimit != 0 {
		t.Fatalf("autoCompactLimit = %d, want 0", got.autoCompactLimit)
	}
	if prefs := readAutoCompactFile(t, path); prefs["autoCompactLimit"] != nil {
		t.Fatalf("auto should clear the key; preferences = %v", prefs)
	}
}

func TestAutoCompactCommandRejectsBadArguments(t *testing.T) {
	for _, in := range []string{"/autocompact banana", "/autocompact 5k", "/autocompact 1.5m"} {
		m, path := autoCompactCommandModel(t, true)
		m.autoCompactLimit = 400000
		got := runAutoCompactCommand(m, in)
		if got.autoCompactLimit != 400000 {
			t.Fatalf("%s changed the limit to %d", in, got.autoCompactLimit)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s wrote the config file", in)
		}
		last := lastAutoCompactTranscriptText(got)
		if !strings.Contains(last, "Uso: /autocompact <n|off|auto>") {
			t.Fatalf("%s should append usage, transcript ends with %q", in, last)
		}
	}
}

func TestAutoCompactCommandStatus(t *testing.T) {
	m, _ := autoCompactCommandModel(t, false)
	m.autoCompactLimit = 400000
	got := runAutoCompactCommand(m, "/autocompact status")
	if got.autoCompactLimit != 400000 {
		t.Fatalf("status changed the limit to %d", got.autoCompactLimit)
	}
	last := lastAutoCompactTranscriptText(got)
	if !strings.Contains(last, "Auto-compact\nLimite: 400k tokens") || !strings.Contains(last, "Mude com /autocompact") {
		t.Fatalf("status text wrong: %q", last)
	}
}

func TestAutoCompactSummaries(t *testing.T) {
	catalog, err := modelregistry.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	spark := model{modelName: "muse-spark-1.3-contributor", modelCatalog: catalog}
	unknown := model{modelName: "autocompact-test-unknown", modelCatalog: catalog}

	spark.autoCompactLimit = 0
	if got := spark.autoCompactSummary(); got != "automático (compacta em 700k tokens neste modelo)" {
		t.Fatalf("automatic on a 1M window: %q", got)
	}
	unknown.autoCompactLimit = 0
	if got := unknown.autoCompactSummary(); got != "automático (compacta em 140k tokens neste modelo)" {
		t.Fatalf("automatic on a 200k window: %q", got)
	}
	spark.autoCompactLimit = 400000
	if got := spark.autoCompactSummary(); got != "400k tokens" {
		t.Fatalf("400k on a 1M window: %q", got)
	}
	unknown.autoCompactLimit = 400000
	if got := unknown.autoCompactSummary(); got != "400k tokens (neste modelo compacta em 140k, o que cabe na janela de 200k)" {
		t.Fatalf("400k on a 200k window: %q", got)
	}
	unknown.autoCompactLimit = config.AutoCompactOff
	if got := unknown.autoCompactSummary(); got != "desligado (só compacta se o provedor recusar por excesso de contexto)" {
		t.Fatalf("off: %q", got)
	}
}

func TestAutoCompactCommandWhilePending(t *testing.T) {
	m, _ := autoCompactCommandModel(t, false)
	m.pending = true
	got := runAutoCompactCommand(m, "/autocompact 400k")
	if got.autoCompactLimit != 400000 {
		t.Fatalf("autoCompactLimit = %d, want 400000", got.autoCompactLimit)
	}
	if !strings.Contains(got.transientNotice.text, "Vale a partir da próxima mensagem.") {
		t.Fatalf("pending notice should carry the next-message suffix, got %q", got.transientNotice.text)
	}
}

func TestCompactStatusShowsAutoCompactSetting(t *testing.T) {
	m, _ := autoCompactCommandModel(t, true)
	m.autoCompactLimit = 400000
	_, text, _ := m.handleCompactCommand("status")
	if strings.Contains(text, "80%") {
		t.Fatalf("/compact status still mentions 80%%:\n%s", text)
	}
	if !strings.Contains(text, "400k tokens") || !strings.Contains(text, "(/autocompact)") {
		t.Fatalf("/compact status should show the real setting:\n%s", text)
	}
}

func TestAutoCompactCommandWithoutConfigPath(t *testing.T) {
	m, _ := autoCompactCommandModel(t, false)
	got := runAutoCompactCommand(m, "/autocompact 400k")
	if got.autoCompactLimit != 400000 {
		t.Fatalf("autoCompactLimit = %d, want 400000", got.autoCompactLimit)
	}
	if !strings.Contains(got.transientNotice.text, "Auto-compact: 400k tokens") {
		t.Fatalf("session-only apply should still confirm, got %q", got.transientNotice.text)
	}
}

// lastAutoCompactTranscriptText returns the text of the last transcript row
// for tests that assert on an appended system message.
func lastAutoCompactTranscriptText(m model) string {
	if len(m.transcript) == 0 {
		return ""
	}
	return m.transcript[len(m.transcript)-1].text
}
