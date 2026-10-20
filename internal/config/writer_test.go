package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSetActiveProviderSwitchesConfiguredProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "OpenAI",
		Providers: []ProviderProfile{
			{
				Name:         "OpenAI",
				ProviderKind: ProviderKindOpenAI,
				Model:        "gpt-4.1",
			},
			{
				Name:         "Anthropic",
				ProviderKind: ProviderKindAnthropic,
				Model:        "claude-3-5-sonnet-latest",
			},
		},
	}, 0o600)

	cfg, err := SetActiveProvider(path, "  anthropic  ")
	if err != nil {
		t.Fatalf("SetActiveProvider() error = %v", err)
	}

	if cfg.ActiveProvider != "Anthropic" {
		t.Fatalf("ActiveProvider = %q, want Anthropic", cfg.ActiveProvider)
	}

	persisted := readConfigFixture(t, path)
	if persisted.ActiveProvider != "Anthropic" {
		t.Fatalf("persisted ActiveProvider = %q, want Anthropic", persisted.ActiveProvider)
	}
}

func TestSetActiveProviderRejectsUnknownProviderWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	before := writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{Name: "openai", ProviderKind: ProviderKindOpenAI, Model: "gpt-4.1"},
			{Name: "anthropic", ProviderKind: ProviderKindAnthropic, Model: "claude-3-5-sonnet-latest"},
		},
	}, 0o600)

	_, err := SetActiveProvider(path, "google")
	if err == nil {
		t.Fatal("SetActiveProvider() error = nil, want error")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("config was rewritten for unknown provider\nbefore: %s\nafter: %s", before, after)
	}

	persisted := readConfigFixture(t, path)
	if persisted.ActiveProvider != "openai" {
		t.Fatalf("persisted ActiveProvider = %q, want openai", persisted.ActiveProvider)
	}
}

func TestSetActiveProviderRejectsEmptyProviderName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	before := writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{Name: "openai", ProviderKind: ProviderKindOpenAI, Model: "gpt-4.1"},
		},
	}, 0o600)

	_, err := SetActiveProvider(path, " \t\n ")
	if err == nil {
		t.Fatal("SetActiveProvider() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "provider name is required") {
		t.Fatalf("SetActiveProvider() error = %q, want provider name required", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("config was rewritten for empty provider name\nbefore: %s\nafter: %s", before, after)
	}
}

func TestSetActiveProviderRejectsEmptyConfigPath(t *testing.T) {
	_, err := SetActiveProvider(" \t\n ", "openai")
	if err == nil {
		t.Fatal("SetActiveProvider() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "config path is required") {
		t.Fatalf("SetActiveProvider() error = %q, want config path required", err)
	}
}

func TestSetActiveProviderRejectsMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")

	_, err := SetActiveProvider(path, "openai")
	if err == nil {
		t.Fatal("SetActiveProvider() error = nil, want error")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SetActiveProvider() error = %v, want not-exist error", err)
	}
}

func TestSetActiveProviderTightensExistingConfigFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX mode bits reliably")
	}

	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{Name: "openai", ProviderKind: ProviderKindOpenAI, Model: "gpt-4.1"},
			{Name: "anthropic", ProviderKind: ProviderKindAnthropic, Model: "claude-3-5-sonnet-latest"},
		},
	}, 0o644)

	_, err := SetActiveProvider(path, "anthropic")
	if err != nil {
		t.Fatalf("SetActiveProvider() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 0600", got)
	}
}

func TestSetProviderModelUpdatesConfiguredProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{
				Name:         "openai",
				ProviderKind: ProviderKindOpenAI,
				APIKey:       "sk-test",
				Model:        "gpt-4.1",
			},
			{
				Name:         "anthropic",
				ProviderKind: ProviderKindAnthropic,
				Model:        "claude-sonnet-4.5",
			},
		},
	}, 0o600)

	cfg, err := SetProviderModel(path, " OpenAI ", " gpt-4.1-mini ")
	if err != nil {
		t.Fatalf("SetProviderModel() error = %v", err)
	}

	if cfg.Providers[0].Model != "gpt-4.1-mini" {
		t.Fatalf("updated provider model = %q, want gpt-4.1-mini", cfg.Providers[0].Model)
	}
	if cfg.Providers[0].APIKey != "sk-test" {
		t.Fatalf("provider credential was not preserved: %#v", cfg.Providers[0])
	}
	if cfg.Providers[1].Model != "claude-sonnet-4.5" {
		t.Fatalf("unrelated provider changed: %#v", cfg.Providers[1])
	}

	persisted := readConfigFixture(t, path)
	if persisted.Providers[0].Model != "gpt-4.1-mini" {
		t.Fatalf("persisted provider model = %q, want gpt-4.1-mini", persisted.Providers[0].Model)
	}
	if persisted.ActiveProvider != "openai" {
		t.Fatalf("active provider changed to %q", persisted.ActiveProvider)
	}
}

func TestSetProviderModelRejectsUnknownProviderWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	before := writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{Name: "openai", ProviderKind: ProviderKindOpenAI, Model: "gpt-4.1"},
		},
	}, 0o600)

	_, err := SetProviderModel(path, "anthropic", "claude-sonnet-4.5")
	if err == nil {
		t.Fatal("SetProviderModel() error = nil, want error")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("config was rewritten for unknown provider\nbefore: %s\nafter: %s", before, after)
	}
}

func TestUpsertProviderTightensExistingConfigFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX mode bits reliably")
	}

	path := filepath.Join(t.TempDir(), "qg.json")
	if err := os.WriteFile(path, []byte(`{"providers":[]}`), 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
	}

	_, err := UpsertProvider(path, ProviderProfile{
		Name:         "openai",
		ProviderKind: ProviderKindOpenAI,
		APIKey:       "sk-test",
		Model:        "gpt-4.1",
	}, true)
	if err != nil {
		t.Fatalf("UpsertProvider() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 0600", got)
	}
}

func TestSetFavoriteModelsPersistsUserPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{Name: "openai", ProviderKind: ProviderKindOpenAI, Model: "gpt-4.1"},
		},
	}, 0o600)

	cfg, err := SetFavoriteModels(path, []string{" qwen3-coder:480b ", "", "rnj-1:8b", "qwen3-coder:480b"})
	if err != nil {
		t.Fatalf("SetFavoriteModels() error = %v", err)
	}

	want := []string{"qwen3-coder:480b", "rnj-1:8b"}
	if !reflect.DeepEqual(cfg.Preferences.FavoriteModels, want) {
		t.Fatalf("FavoriteModels = %#v, want %#v", cfg.Preferences.FavoriteModels, want)
	}
	persisted := readConfigFixture(t, path)
	if !reflect.DeepEqual(persisted.Preferences.FavoriteModels, want) {
		t.Fatalf("persisted FavoriteModels = %#v, want %#v", persisted.Preferences.FavoriteModels, want)
	}
	if persisted.ActiveProvider != "openai" || len(persisted.Providers) != 1 {
		t.Fatalf("provider config was not preserved: %#v", persisted)
	}
}

func TestSetRecentModelsPersistsOrderDedupesAndCaps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{Name: "openai", ProviderKind: ProviderKindOpenAI, Model: "gpt-4.1"},
		},
	}, 0o600)

	cfg, err := SetRecentModels(path, []RecentModelEntry{
		{Provider: " openrouter ", Model: " google/gemini-2.5-pro "},
		{Provider: "openrouter", Model: "minimax/minimax-m2.1"},
		{Provider: "openrouter", Model: "google/gemini-2.5-pro"}, // duplicate pair, older: dropped
		{Provider: "", Model: ""},                                // blank model: dropped
		{Provider: "openrouter", Model: "a"},
		{Provider: "openrouter", Model: "b"},
		{Provider: "openrouter", Model: "c"},
		{Provider: "openrouter", Model: "d"}, // beyond MaxRecentModels (5): dropped
	})
	if err != nil {
		t.Fatalf("SetRecentModels() error = %v", err)
	}

	want := []RecentModelEntry{
		{Provider: "openrouter", Model: "google/gemini-2.5-pro"},
		{Provider: "openrouter", Model: "minimax/minimax-m2.1"},
		{Provider: "openrouter", Model: "a"},
		{Provider: "openrouter", Model: "b"},
		{Provider: "openrouter", Model: "c"},
	}
	if !reflect.DeepEqual(cfg.Preferences.RecentModels, want) {
		t.Fatalf("RecentModels = %#v, want %#v", cfg.Preferences.RecentModels, want)
	}
	persisted := readConfigFixture(t, path)
	if !reflect.DeepEqual(persisted.Preferences.RecentModels, want) {
		t.Fatalf("persisted RecentModels = %#v, want %#v", persisted.Preferences.RecentModels, want)
	}
	if persisted.ActiveProvider != "openai" || len(persisted.Providers) != 1 {
		t.Fatalf("provider config was not preserved: %#v", persisted)
	}
}

// Two providers offering the same model id must both survive normalization —
// recent history de-duplicates by provider+model pair, not model id alone.
func TestSetRecentModelsDedupesByProviderAndModelPair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")

	cfg, err := SetRecentModels(path, []RecentModelEntry{
		{Provider: "provider-a", Model: "shared-model"},
		{Provider: "provider-b", Model: "shared-model"},
	})
	if err != nil {
		t.Fatalf("SetRecentModels() error = %v", err)
	}
	want := []RecentModelEntry{
		{Provider: "provider-a", Model: "shared-model"},
		{Provider: "provider-b", Model: "shared-model"},
	}
	if !reflect.DeepEqual(cfg.Preferences.RecentModels, want) {
		t.Fatalf("RecentModels = %#v, want both providers preserved: %#v", cfg.Preferences.RecentModels, want)
	}
}

func TestSetThemePersistsUserPreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{Name: "openai", ProviderKind: ProviderKindOpenAI, Model: "gpt-4.1"},
		},
	}, 0o600)

	cfg, err := SetTheme(path, "  dracula  ")
	if err != nil {
		t.Fatalf("SetTheme() error = %v", err)
	}
	if cfg.Preferences.Theme != "dracula" {
		t.Fatalf("Theme = %q, want dracula (trimmed)", cfg.Preferences.Theme)
	}
	persisted := readConfigFixture(t, path)
	if persisted.Preferences.Theme != "dracula" {
		t.Fatalf("persisted Theme = %q, want dracula", persisted.Preferences.Theme)
	}
	if persisted.ActiveProvider != "openai" || len(persisted.Providers) != 1 {
		t.Fatalf("provider config was not preserved by SetTheme: %#v", persisted)
	}

	// A blank value clears the stored preference.
	if cfg, err = SetTheme(path, ""); err != nil {
		t.Fatalf("SetTheme(\"\") error = %v", err)
	}
	if cfg.Preferences.Theme != "" {
		t.Fatalf("SetTheme(\"\") should clear the theme, got %q", cfg.Preferences.Theme)
	}
}

func TestSetThemePreservesUnknownTopLevelFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	if err := os.WriteFile(path, []byte(`{
  "preferences": {"theme": "default"},
  "futureSetting": {"a": 1}
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := SetTheme(path, "dracula"); err != nil {
		t.Fatalf("SetTheme() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	var futureSetting struct {
		A int `json:"a"`
	}
	if err := json.Unmarshal(persisted["futureSetting"], &futureSetting); err != nil {
		t.Fatalf("unknown field was not preserved: %v\nconfig: %s", err, data)
	}
	if futureSetting.A != 1 {
		t.Fatalf("futureSetting = %s, want {\"a\":1}", persisted["futureSetting"])
	}
}

func TestRecapsPreferenceRoundTrips(t *testing.T) {
	// Default (unset) is ON.
	if !(PreferencesConfig{}).RecapsEnabled() {
		t.Error("unset recaps should default to ON")
	}

	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{ActiveProvider: "openai"}, 0o600)

	// Persist OFF, then read it back.
	cfg, err := SetRecapsEnabled(path, false)
	if err != nil {
		t.Fatalf("SetRecapsEnabled(false) error = %v", err)
	}
	if cfg.Preferences.RecapsEnabled() {
		t.Error("after SetRecapsEnabled(false), RecapsEnabled() should be false")
	}
	persisted := readConfigFixture(t, path)
	if persisted.Preferences.Recaps == nil || *persisted.Preferences.Recaps {
		t.Errorf("persisted recaps should be explicit false, got %v", persisted.Preferences.Recaps)
	}
	if persisted.ActiveProvider != "openai" {
		t.Errorf("unrelated config must be preserved, got %q", persisted.ActiveProvider)
	}

	// Flip back ON — the write must succeed and persist an explicit true.
	cfg, err = SetRecapsEnabled(path, true)
	if err != nil {
		t.Fatalf("SetRecapsEnabled(true) error = %v", err)
	}
	if !cfg.Preferences.RecapsEnabled() {
		t.Error("after SetRecapsEnabled(true), RecapsEnabled() should be true")
	}
	if reread := readConfigFixture(t, path); reread.Preferences.Recaps == nil || !*reread.Preferences.Recaps {
		t.Errorf("re-enable should persist an explicit true, got %v", reread.Preferences.Recaps)
	}
}

func TestSetFavoriteModelsCreatesMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg", "config.json")

	cfg, err := SetFavoriteModels(path, []string{"glm-5.1"})
	if err != nil {
		t.Fatalf("SetFavoriteModels() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Preferences.FavoriteModels, []string{"glm-5.1"}) {
		t.Fatalf("FavoriteModels = %#v, want glm-5.1", cfg.Preferences.FavoriteModels)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected config file to be created: %v", err)
	}
}

func writeConfigFixture(t *testing.T, path string, cfg FileConfig, mode fs.FileMode) []byte {
	t.Helper()

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return data
}

func readConfigFixture(t *testing.T, path string) FileConfig {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg FileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg
}

func TestMarkProviderAPIKeyStoredClearsInlineAndEnvKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openrouter",
		Providers: []ProviderProfile{{
			Name:      "openrouter",
			CatalogID: "openrouter",
			APIKey:    "sk-inline",
			APIKeyEnv: "OPENROUTER_API_KEY",
			Model:     "openai/gpt-4.1",
		}},
	}, 0o600)

	if err := MarkProviderAPIKeyStored(path, "openrouter"); err != nil {
		t.Fatalf("MarkProviderAPIKeyStored: %v", err)
	}

	cfg := readConfigFixture(t, path)
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %#v", cfg.Providers)
	}
	profile := cfg.Providers[0]
	if !profile.APIKeyStored || profile.APIKey != "" || profile.APIKeyEnv != "" {
		t.Fatalf("provider credential fields = %#v", profile)
	}
	if profile.Model != "openai/gpt-4.1" || cfg.ActiveProvider != "openrouter" {
		t.Fatalf("unrelated config changed: %#v", cfg)
	}
}

func TestRemoveProviderDeletesAndHandsOffActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "beta",
		Providers: []ProviderProfile{
			{Name: "alpha", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://a.example.com/v1", Model: "m1"},
			{Name: "beta", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://b.example.com/v1", Model: "m2"},
		},
	}, 0o600)

	cfg, err := RemoveProvider(path, " BETA ")
	if err != nil {
		t.Fatalf("RemoveProvider() error = %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != "alpha" {
		t.Fatalf("expected only alpha to remain, got %+v", cfg.Providers)
	}
	if cfg.ActiveProvider != "alpha" {
		t.Fatalf("active must hand off to the first remaining provider, got %q", cfg.ActiveProvider)
	}

	persisted := readConfigFixture(t, path)
	if len(persisted.Providers) != 1 || persisted.ActiveProvider != "alpha" {
		t.Fatalf("persisted config wrong: %+v", persisted)
	}

	// Removing the last provider clears the active pointer entirely.
	cfg, err = RemoveProvider(path, "alpha")
	if err != nil {
		t.Fatalf("RemoveProvider(last) error = %v", err)
	}
	if len(cfg.Providers) != 0 || cfg.ActiveProvider != "" {
		t.Fatalf("expected empty providers and no active, got %+v", cfg)
	}
}

func TestRemoveProviderKeepsActiveWhenOtherRemoved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "alpha",
		Providers: []ProviderProfile{
			{Name: "alpha", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://a.example.com/v1", Model: "m1"},
			{Name: "beta", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://b.example.com/v1", Model: "m2"},
		},
	}, 0o600)

	cfg, err := RemoveProvider(path, "beta")
	if err != nil {
		t.Fatalf("RemoveProvider() error = %v", err)
	}
	if cfg.ActiveProvider != "alpha" {
		t.Fatalf("active provider must be untouched, got %q", cfg.ActiveProvider)
	}
}

func TestRemoveProviderRejectsUnknownWithoutRewriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	before := writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "alpha",
		Providers:      []ProviderProfile{{Name: "alpha", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://a.example.com/v1", Model: "m1"}},
	}, 0o600)

	if _, err := RemoveProvider(path, "ghost"); err == nil {
		t.Fatal("RemoveProvider() error = nil, want not-found error")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("config was rewritten for unknown provider")
	}
}

func TestRenameProviderFollowsActiveAndMigratesStoredKey(t *testing.T) {
	dir := t.TempDir()
	// Force the file credential backend so the test never touches the real OS
	// keyring regardless of platform.
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	path := filepath.Join(dir, "config.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "oldname",
		Providers: []ProviderProfile{
			{Name: "oldname", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://a.example.com/v1", APIKeyStored: true, Model: "m1"},
			{Name: "other", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://b.example.com/v1", Model: "m2"},
		},
	}, 0o600)
	store, err := ProviderKeyStoreAt(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Set("oldname", "sk-secret"); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	cfg, err := RenameProvider(path, "oldname", "newname")
	if err != nil {
		t.Fatalf("RenameProvider() error = %v", err)
	}
	if cfg.ActiveProvider != "newname" {
		t.Fatalf("active must follow the rename, got %q", cfg.ActiveProvider)
	}
	if cfg.Providers[0].Name != "newname" || !cfg.Providers[0].APIKeyStored {
		t.Fatalf("renamed profile wrong: %+v", cfg.Providers[0])
	}
	if cfg.Providers[1].Name != "other" {
		t.Fatalf("unrelated profile changed: %+v", cfg.Providers[1])
	}

	key, ok, err := store.Get("newname")
	if err != nil || !ok || key != "sk-secret" {
		t.Fatalf("stored key must migrate to the new name, got key=%q ok=%v err=%v", key, ok, err)
	}
	if _, ok, _ := store.Get("oldname"); ok {
		t.Fatalf("old credential entry must be deleted after migration")
	}
}

func TestRenameProviderRejectsCollisionAndUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	before := writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "alpha",
		Providers: []ProviderProfile{
			{Name: "alpha", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://a.example.com/v1", Model: "m1"},
			{Name: "beta", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://b.example.com/v1", Model: "m2"},
		},
	}, 0o600)

	if _, err := RenameProvider(path, "alpha", "BETA"); err == nil {
		t.Fatal("rename onto an existing name must fail")
	}
	if _, err := RenameProvider(path, "ghost", "gamma"); err == nil {
		t.Fatal("renaming an unknown provider must fail")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("config was rewritten by a rejected rename")
	}
}

func TestUpsertProviderPreservesStoredKeyMarkerOnExistingProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	// An env-keyed profile with NO stored-key marker — the shape a provider has
	// before its key is captured into the credential store.
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "groq",
		Providers: []ProviderProfile{
			{Name: "groq", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://api.groq.com/openai/v1", APIKeyEnv: "GROQ_API_KEY", Model: "m1"},
		},
	}, 0o600)

	// The manager/setup edit paths persist a SecureProviderProfile-shaped
	// profile: key already in the store, marker set, inline key cleared.
	cfg, err := UpsertProvider(path, ProviderProfile{Name: "groq", APIKeyStored: true}, false)
	if err != nil {
		t.Fatalf("UpsertProvider() error = %v", err)
	}
	if !cfg.Providers[0].APIKeyStored {
		t.Fatalf("APIKeyStored marker must survive the merge, got %+v", cfg.Providers[0])
	}
	if cfg.Providers[0].APIKeyEnv != "GROQ_API_KEY" || cfg.Providers[0].BaseURL == "" {
		t.Fatalf("other fields must be preserved: %+v", cfg.Providers[0])
	}
	persisted := readConfigFixture(t, path)
	if !persisted.Providers[0].APIKeyStored {
		t.Fatalf("marker not persisted to disk: %+v", persisted.Providers[0])
	}
}

func TestSetProviderDescriptionSetsAndClears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "alpha",
		Providers: []ProviderProfile{
			{Name: "alpha", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://a.example.com/v1", Model: "m1", Description: "old text"},
		},
	}, 0o600)

	cfg, err := SetProviderDescription(path, " ALPHA ", "new text")
	if err != nil {
		t.Fatalf("SetProviderDescription() error = %v", err)
	}
	if cfg.Providers[0].Description != "new text" {
		t.Fatalf("description not set: %+v", cfg.Providers[0])
	}

	// Clearing must persist too — the reason this setter exists (UpsertProvider's
	// merge treats an empty description as "leave unchanged").
	cfg, err = SetProviderDescription(path, "alpha", "  ")
	if err != nil {
		t.Fatalf("SetProviderDescription(clear) error = %v", err)
	}
	if cfg.Providers[0].Description != "" {
		t.Fatalf("description not cleared: %+v", cfg.Providers[0])
	}
	persisted := readConfigFixture(t, path)
	if persisted.Providers[0].Description != "" {
		t.Fatalf("cleared description not persisted: %+v", persisted.Providers[0])
	}

	if _, err := SetProviderDescription(path, "ghost", "x"); err == nil {
		t.Fatal("unknown provider must error")
	}
}

func TestRenameProviderRollsBackKeyMigrationWhenConfigWriteFails(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("uses chflags uchg to force the config write to fail; macOS only")
	}
	dir := t.TempDir()
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	path := filepath.Join(dir, "config.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "oldname",
		Providers: []ProviderProfile{
			{Name: "oldname", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://a.example.com/v1", APIKeyStored: true, Model: "m1"},
		},
	}, 0o600)
	store, err := ProviderKeyStoreAt(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Set("oldname", "sk-secret"); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	// Immutable flag: temp-file creation and store writes in the directory keep
	// working, but the final rename over config.json fails — the exact window
	// where a migrated key would otherwise strand under the new name.
	if out, err := exec.Command("chflags", "uchg", path).CombinedOutput(); err != nil {
		t.Skipf("chflags uchg unavailable: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("chflags", "nouchg", path).Run() })

	if _, err := RenameProvider(path, "oldname", "newname"); err == nil {
		t.Fatal("expected the config write to fail under the immutable flag")
	}

	key, ok, err := store.Get("oldname")
	if err != nil || !ok || key != "sk-secret" {
		t.Fatalf("key must be rolled back to the old name, got key=%q ok=%v err=%v", key, ok, err)
	}
	if _, ok, _ := store.Get("newname"); ok {
		t.Fatalf("rolled-back migration must not leave a key under the new name")
	}
}

// TestRenameProviderCaseOnlyKeepsStoredKey: the credential store normalizes
// names case-insensitively, so a case-only rename targets ONE store entry —
// migrating would Set and then Delete the same key, losing it (PR #560 review).
func TestRenameProviderCaseOnlyKeepsStoredKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	path := filepath.Join(dir, "config.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "groq",
		Providers: []ProviderProfile{
			{Name: "groq", ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://api.groq.com/openai/v1", APIKeyStored: true, Model: "m1"},
		},
	}, 0o600)
	store, err := ProviderKeyStoreAt(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Set("groq", "sk-secret"); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	cfg, err := RenameProvider(path, "groq", "Groq")
	if err != nil {
		t.Fatalf("RenameProvider() error = %v", err)
	}
	if cfg.Providers[0].Name != "Groq" || cfg.ActiveProvider != "Groq" {
		t.Fatalf("case-only rename must still apply to config: %+v", cfg)
	}
	// The store is case-insensitive: the key must remain retrievable under the
	// new casing (same entry), not deleted by a same-entry "migration".
	if key, ok, err := store.Get("Groq"); err != nil || !ok || key != "sk-secret" {
		t.Fatalf("stored key lost on case-only rename: key=%q ok=%v err=%v", key, ok, err)
	}
}

// A stale inline key from an earlier plaintext-fallback login must not survive
// a later login that stores the new key in the credential store: the secured
// profile carries APIKeyStored with an empty APIKey, and mergeProfile's
// "only overwrite when non-empty" rule would otherwise keep the OLD inline
// secret on disk — where it wins over env and store at resolve time.
func TestUpsertProviderClearsStaleInlineKeyWhenKeyStored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "qg",
		Providers: []ProviderProfile{{
			Name:   "qg",
			Model:  "deepseek-v4.1-flash",
			APIKey: "sk-inline-obsoleta",
		}},
	}, 0o600)

	// Shape produced by SecureProviderProfile after the new key lands in the
	// store: marker set, inline cleared.
	if _, err := UpsertProvider(path, ProviderProfile{Name: "qg", APIKeyStored: true}, true); err != nil {
		t.Fatalf("UpsertProvider() error = %v", err)
	}

	persisted := readConfigFixture(t, path)
	if len(persisted.Providers) != 1 {
		t.Fatalf("providers = %#v", persisted.Providers)
	}
	profile := persisted.Providers[0]
	if profile.APIKey != "" {
		t.Fatalf("stale inline key survived a stored-key save: APIKey = %q", profile.APIKey)
	}
	if !profile.APIKeyStored {
		t.Fatalf("APIKeyStored marker must be set, got %+v", profile)
	}
	if profile.Model != "deepseek-v4.1-flash" {
		t.Fatalf("unrelated field changed: %+v", profile)
	}
	// End-to-end of the reported bug: resolving the persisted profile against
	// a store holding the NEW key must yield the new key, not the stale inline
	// one (ApplyStoredAPIKey only fills when inline is empty).
	resolved := ApplyStoredAPIKey(profile, fakeKeyGetter{keys: map[string]string{"qg": "sk-nova-do-store"}})
	if resolved.APIKey != "sk-nova-do-store" {
		t.Fatalf("resolved APIKey = %q, want the stored new key", resolved.APIKey)
	}
}
