package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAutoCompactLimit(t *testing.T) {
	valid := map[string]int{
		"auto":      AutoCompactAutomatic,
		"AUTO":      AutoCompactAutomatic,
		"  auto  ":  AutoCompactAutomatic,
		"off":       AutoCompactOff,
		"OFF":       AutoCompactOff,
		"  Off  ":   AutoCompactOff,
		"400000":    400000,
		"20000":     20000,
		"400k":      400000,
		"400K":      400000,
		"  400K  ":  400000,
		"1m":        1000000,
		"1M":        1000000,
		"100m":      100000000,
		"100000000": 100000000,
	}
	for text, want := range valid {
		got, err := ParseAutoCompactLimit(text)
		if err != nil {
			t.Errorf("ParseAutoCompactLimit(%q) error = %v", text, err)
			continue
		}
		if got != want {
			t.Errorf("ParseAutoCompactLimit(%q) = %d, want %d", text, got, want)
		}
	}

	invalid := []string{
		"",
		"   ",
		"abc",
		"4 00k",
		"1.5m",
		"400kk",
		"-5",
		"+5k",
		"k",
		"m",
		"0",
		"0k",
		"19999",
		"19k",
		"101m",
		"100000001",
		"99999999999999999999k",
		"999999999999999999999999",
	}
	for _, text := range invalid {
		got, err := ParseAutoCompactLimit(text)
		if err == nil {
			t.Errorf("ParseAutoCompactLimit(%q) = %d, want error", text, got)
		}
	}
}

func TestFormatAutoCompactLimit(t *testing.T) {
	cases := map[int]string{
		0:       "auto",
		-1:      "off",
		-7:      "off",
		400000:  "400k",
		1000000: "1m",
		1500000: "1500k",
		123456:  "123456",
	}
	for limit, want := range cases {
		if got := FormatAutoCompactLimit(limit); got != want {
			t.Errorf("FormatAutoCompactLimit(%d) = %q, want %q", limit, got, want)
		}
	}
}

func TestAutoCompactLimitRoundTrip(t *testing.T) {
	limits := []int{20000, 25000, 400000, 1000000, 1500000, 123456, 99999999, 100000000}
	for _, want := range limits {
		formatted := FormatAutoCompactLimit(want)
		got, err := ParseAutoCompactLimit(formatted)
		if err != nil {
			t.Errorf("ParseAutoCompactLimit(FormatAutoCompactLimit(%d) = %q) error = %v", want, formatted, err)
			continue
		}
		if got != want {
			t.Errorf("ParseAutoCompactLimit(FormatAutoCompactLimit(%d) = %q) = %d", want, formatted, got)
		}
	}
}

func TestNormalizeAutoCompactLimit(t *testing.T) {
	cases := map[int]int{
		-9:        AutoCompactOff,
		-1:        AutoCompactOff,
		0:         AutoCompactAutomatic,
		1:         MinAutoCompactLimit,
		7:         MinAutoCompactLimit,
		19999:     MinAutoCompactLimit,
		20000:     20000,
		400000:    400000,
		100000000: 100000000,
		100000001: MaxAutoCompactLimit,
	}
	for limit, want := range cases {
		if got := NormalizeAutoCompactLimit(limit); got != want {
			t.Errorf("NormalizeAutoCompactLimit(%d) = %d, want %d", limit, got, want)
		}
	}
}

func TestSetAutoCompactLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qg.json")
	writeConfigFixture(t, path, FileConfig{
		ActiveProvider: "openai",
		Providers: []ProviderProfile{
			{Name: "openai", ProviderKind: ProviderKindOpenAI, Model: "gpt-4.1"},
		},
		Preferences: PreferencesConfig{Theme: "dracula"},
	}, 0o600)

	cfg, err := SetAutoCompactLimit(path, 400000)
	if err != nil {
		t.Fatalf("SetAutoCompactLimit(400000) error = %v", err)
	}
	if cfg.Preferences.AutoCompactLimit != 400000 {
		t.Fatalf("AutoCompactLimit = %d, want 400000", cfg.Preferences.AutoCompactLimit)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"autoCompactLimit": 400000`) {
		t.Fatalf("config file lacks autoCompactLimit 400000: %s", data)
	}
	persisted := readConfigFixture(t, path)
	if persisted.Preferences.Theme != "dracula" {
		t.Fatalf("unrelated preference lost: theme = %q", persisted.Preferences.Theme)
	}
	if len(persisted.Providers) != 1 || persisted.ActiveProvider != "openai" {
		t.Fatalf("unrelated config lost: %#v", persisted)
	}

	cfg, err = SetAutoCompactLimit(path, -1)
	if err != nil {
		t.Fatalf("SetAutoCompactLimit(-1) error = %v", err)
	}
	if cfg.Preferences.AutoCompactLimit != AutoCompactOff {
		t.Fatalf("AutoCompactLimit = %d, want %d", cfg.Preferences.AutoCompactLimit, AutoCompactOff)
	}
	if reread := readConfigFixture(t, path); reread.Preferences.AutoCompactLimit != AutoCompactOff {
		t.Fatalf("persisted AutoCompactLimit = %d, want %d", reread.Preferences.AutoCompactLimit, AutoCompactOff)
	}

	cfg, err = SetAutoCompactLimit(path, 0)
	if err != nil {
		t.Fatalf("SetAutoCompactLimit(0) error = %v", err)
	}
	if cfg.Preferences.AutoCompactLimit != AutoCompactAutomatic {
		t.Fatalf("AutoCompactLimit = %d, want 0", cfg.Preferences.AutoCompactLimit)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "autoCompactLimit") {
		t.Fatalf("clearing the limit should remove the key: %s", data)
	}
	if reread := readConfigFixture(t, path); reread.Preferences.Theme != "dracula" {
		t.Fatalf("clearing the limit dropped unrelated preferences: %#v", reread.Preferences)
	}

	cfg, err = SetAutoCompactLimit(path, 5)
	if err != nil {
		t.Fatalf("SetAutoCompactLimit(5) error = %v", err)
	}
	if cfg.Preferences.AutoCompactLimit != MinAutoCompactLimit {
		t.Fatalf("AutoCompactLimit = %d, want %d", cfg.Preferences.AutoCompactLimit, MinAutoCompactLimit)
	}
	if reread := readConfigFixture(t, path); reread.Preferences.AutoCompactLimit != MinAutoCompactLimit {
		t.Fatalf("persisted AutoCompactLimit = %d, want %d", reread.Preferences.AutoCompactLimit, MinAutoCompactLimit)
	}

	if _, err := SetAutoCompactLimit("", 400000); err == nil {
		t.Error("SetAutoCompactLimit with empty path should return an error")
	}
}

func TestResolveAutoCompactLimitFromUserConfig(t *testing.T) {
	cases := map[string]int{
		`{"preferences":{"autoCompactLimit":400000}}`: 400000,
		`{"preferences":{"autoCompactLimit":-9}}`:     AutoCompactOff,
		`{"preferences":{"autoCompactLimit":7}}`:      MinAutoCompactLimit,
		`{}`:                                          AutoCompactAutomatic,
	}
	for body, want := range cases {
		userPath := writeConfig(t, body)
		resolved, err := Resolve(ResolveOptions{
			UserConfigPath: userPath,
			Env:            map[string]string{},
		})
		if err != nil {
			t.Fatalf("Resolve(%s) error = %v", body, err)
		}
		if resolved.Preferences.AutoCompactLimit != want {
			t.Errorf("Resolve(%s) AutoCompactLimit = %d, want %d", body, resolved.Preferences.AutoCompactLimit, want)
		}
	}
}

func TestValidateFileAllowsAutoCompactLimit(t *testing.T) {
	path := writeConfig(t, `{
		"activeProvider": "main",
		"providers": [
			{"name": "main", "provider_kind": "openai", "model": "gpt-4.1"}
		],
		"preferences": {"autoCompactLimit": 400000}
	}`)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range unknownFieldIssues(data) {
		if strings.Contains(issue.FieldPath, "autoCompactLimit") {
			t.Fatalf("preferences.autoCompactLimit reported as unknown: %#v", unknownFieldIssues(data))
		}
	}
	var decoded FileConfig
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("config with autoCompactLimit must load: %v", err)
	}
	if decoded.Preferences.AutoCompactLimit != 400000 {
		t.Fatalf("decoded AutoCompactLimit = %d, want 400000", decoded.Preferences.AutoCompactLimit)
	}
}
