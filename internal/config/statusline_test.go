package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Statusline config must parse from user JSON without landing in Extra, so a
// read-modify-write keeps it and /doctor does not flag it as unknown.
func TestStatusLineParsesFromUserConfig(t *testing.T) {
	raw := `{"statusline": {
		"lines": [{"left": ["mode", "effort"], "right": ["context", "branch"]}],
		"separator": " | ",
		"command": {"command": "echo hi"},
		"timeoutMs": 1500,
		"refreshMs": 3000
	}}`
	var cfg FileConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(cfg.StatusLine.Lines) != 1 {
		t.Fatalf("lines = %#v, want one line", cfg.StatusLine.Lines)
	}
	if got := cfg.StatusLine.Lines[0].Left; len(got) != 2 || got[0] != "mode" || got[1] != "effort" {
		t.Fatalf("left = %#v", got)
	}
	if cfg.StatusLine.Command == nil || cfg.StatusLine.Command.Command != "echo hi" {
		t.Fatalf("command = %#v", cfg.StatusLine.Command)
	}
	if cfg.StatusLine.TimeoutMs != 1500 || cfg.StatusLine.RefreshMs != 3000 {
		t.Fatalf("timeouts = %+v", cfg.StatusLine)
	}
	if _, ok := cfg.Extra["statusline"]; ok {
		t.Fatal("statusline landed in Extra, want a known key")
	}
}

// An empty statusline means "render like today": no custom layout, no command.
func TestStatusLineEmptyIsNotCustom(t *testing.T) {
	var cfg FileConfig
	if err := json.Unmarshal([]byte(`{}`), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.StatusLine.IsCustom() {
		t.Fatalf("empty statusline reports custom: %+v", cfg.StatusLine)
	}
	if cfg.StatusLine.EffectiveTimeoutMs() != DefaultStatusLineTimeoutMs {
		t.Fatalf("timeout = %d, want %d", cfg.StatusLine.EffectiveTimeoutMs(), DefaultStatusLineTimeoutMs)
	}
	if cfg.StatusLine.EffectiveRefreshMs() != DefaultStatusLineRefreshMs {
		t.Fatalf("refresh = %d, want %d", cfg.StatusLine.EffectiveRefreshMs(), DefaultStatusLineRefreshMs)
	}
}

// mergeConfig applies the user's full statusline; mergeProjectConfig applies
// only the layout and drops the command (fail-closed: a cloned repo must not
// get code execution). Resolve reports the dropped project command.
func TestStatusLineMergeProjectDropsCommand(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")
	projectPath := filepath.Join(dir, "project.json")
	if err := os.WriteFile(userPath, []byte(`{"activeProvider": "p", "providers": [{"name": "p", "provider_kind": "openai-compatible", "model": "m", "baseURL": "https://a.example.com/v1"}]}`), 0600); err != nil {
		t.Fatalf("write user: %v", err)
	}
	if err := os.WriteFile(projectPath, []byte(`{"statusline": {
		"lines": [{"left": ["mode"], "right": ["cwd"]}],
		"command": {"command": "evil"}
	}}`), 0600); err != nil {
		t.Fatalf("write project: %v", err)
	}
	resolved, err := Resolve(ResolveOptions{UserConfigPath: userPath, ProjectConfigPath: projectPath})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !resolved.ProjectStatusLineCommandIgnored {
		t.Fatal("project command was not reported as ignored")
	}
	if resolved.StatusLine.Command != nil {
		t.Fatalf("project command leaked into resolved config: %#v", resolved.StatusLine.Command)
	}
	if len(resolved.StatusLine.Lines) != 1 || len(resolved.StatusLine.Lines[0].Left) != 1 {
		t.Fatalf("project layout was not applied: %+v", resolved.StatusLine)
	}
	if !resolved.StatusLine.IsCustom() {
		t.Fatal("project layout should count as custom")
	}
}

// Valores cosmeticos invalidos nunca impedem o QG de abrir: sao ajustados
// com aviso, em pt-BR. So a forma (nao o conteudo) e tocada.
func TestStatusLineAdjustsInvalidShapes(t *testing.T) {
	cfg := StatusLineConfig{
		Lines:     []StatusLineLine{{}, {}, {}, {}},
		TimeoutMs: -5,
		RefreshMs: 500,
	}
	adjusted, warnings := cfg.Adjusted()
	if len(adjusted.Lines) != 3 {
		t.Fatalf("lines = %d, want clamped to 3", len(adjusted.Lines))
	}
	if adjusted.TimeoutMs != 0 {
		t.Fatalf("timeoutMs = %d, want default (0)", adjusted.TimeoutMs)
	}
	if adjusted.RefreshMs != 1000 {
		t.Fatalf("refreshMs = %d, want the 1000 floor", adjusted.RefreshMs)
	}
	if len(warnings) != 3 {
		t.Fatalf("warnings = %#v, want one per adjustment", warnings)
	}
	for _, warning := range warnings {
		if !strings.HasPrefix(warning, "statusline: ") {
			t.Fatalf("warning %q must name the block in pt-BR", warning)
		}
	}
	valid := StatusLineConfig{TimeoutMs: 1500, RefreshMs: 3000}
	if _, warnings := valid.Adjusted(); len(warnings) != 0 {
		t.Fatalf("valid config warns: %#v", warnings)
	}
}

// O ajuste acontece no Resolve: refresh abaixo de 1s vira 1000 e o QG abre.
func TestStatusLineResolveClampsRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	seed := `{"activeProvider": "p", "providers": [{"name": "p", "provider_kind": "openai-compatible", "model": "m", "baseURL": "https://a.example.com/v1"}], "statusline": {"lines": [{"left": ["mode"]}], "refreshMs": 100}}`
	if err := os.WriteFile(path, []byte(seed), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	resolved, err := Resolve(ResolveOptions{UserConfigPath: path, Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Resolve must not fail on cosmetic values: %v", err)
	}
	if resolved.StatusLine.EffectiveRefreshMs() != 1000 {
		t.Fatalf("refresh = %d, want clamped 1000", resolved.StatusLine.EffectiveRefreshMs())
	}
	if len(resolved.StatusLineWarnings) == 0 {
		t.Fatal("clamp without a warning")
	}
}

// SetStatusLine persists and round-trips; the zero value clears the key so
// /statusline reset removes it instead of leaving an empty object behind.
func TestSetStatusLineRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	seed := `{"activeProvider": "p", "providers": [{"name": "p", "provider_kind": "openai-compatible", "model": "m", "baseURL": "https://a.example.com/v1"}]}`
	if err := os.WriteFile(path, []byte(seed), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	want := StatusLineConfig{
		Lines:     []StatusLineLine{{Left: []string{"mode"}, Right: []string{"context"}}},
		Separator: " | ",
		Command:   &StatusLineCommand{Command: "echo hi"},
		TimeoutMs: 1500,
		RefreshMs: 3000,
	}
	if _, err := SetStatusLine(path, want); err != nil {
		t.Fatalf("SetStatusLine: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(raw), `"statusline"`) {
		t.Fatalf("config.json lacks statusline:\n%s", raw)
	}
	resolved, err := Resolve(ResolveOptions{UserConfigPath: path})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.StatusLine.Command == nil || resolved.StatusLine.Command.Command != "echo hi" {
		t.Fatalf("resolved = %+v", resolved.StatusLine)
	}
	if _, err := SetStatusLine(path, StatusLineConfig{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	resolved, err = Resolve(ResolveOptions{UserConfigPath: path})
	if err != nil {
		t.Fatalf("Resolve after clear: %v", err)
	}
	if resolved.StatusLine.IsCustom() {
		t.Fatalf("cleared statusline still custom: %+v", resolved.StatusLine)
	}
}
