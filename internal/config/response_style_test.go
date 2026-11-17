package config

import (
	"path/filepath"
	"testing"
)

func TestSetResponseStylePersistsUserPreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{Name: "openai", ProviderKind: ProviderKindOpenAI, Model: "gpt-4.1"},
		},
	}, 0o600)

	cfg, err := SetResponseStyle(path, "explanatory")
	if err != nil {
		t.Fatalf("SetResponseStyle() error = %v", err)
	}
	if cfg.Preferences.ResponseStyle != "explanatory" {
		t.Fatalf("ResponseStyle = %q, want explanatory", cfg.Preferences.ResponseStyle)
	}
	persisted := readConfigFixture(t, path)
	if persisted.Preferences.ResponseStyle != "explanatory" {
		t.Fatalf("persisted ResponseStyle = %q, want explanatory", persisted.Preferences.ResponseStyle)
	}
	if persisted.ActiveProvider != "openai" || len(persisted.Providers) != 1 {
		t.Fatalf("provider config was not preserved by SetResponseStyle: %#v", persisted)
	}

	// A blank value clears the stored preference.
	if cfg, err = SetResponseStyle(path, ""); err != nil {
		t.Fatalf("SetResponseStyle(\"\") error = %v", err)
	}
	if cfg.Preferences.ResponseStyle != "" {
		t.Fatalf("SetResponseStyle(\"\") should clear the style, got %q", cfg.Preferences.ResponseStyle)
	}
}
