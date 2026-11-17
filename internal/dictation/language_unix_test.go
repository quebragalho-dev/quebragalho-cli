//go:build !windows

package dictation

import "testing"

func TestSystemLanguagePrecedence(t *testing.T) {
	old, oldOS := localeEnv, osPreferredLanguage
	defer func() { localeEnv, osPreferredLanguage = old, oldOS }()
	osPreferredLanguage = func() string { return "" }
	set := func(all, messages, lang string) {
		localeEnv = func(key string) string {
			switch key {
			case "LC_ALL":
				return all
			case "LC_MESSAGES":
				return messages
			case "LANG":
				return lang
			}
			return ""
		}
	}
	set("en_US.UTF-8", "pt_BR.UTF-8", "fr_FR.UTF-8")
	if got := SystemLanguage(); got != "en" {
		t.Errorf("LC_ALL should win, got %q", got)
	}
	set("", "pt_BR.UTF-8", "fr_FR.UTF-8")
	if got := SystemLanguage(); got != "pt" {
		t.Errorf("LC_MESSAGES should win over LANG, got %q", got)
	}
	set("", "", "fr_FR.UTF-8")
	if got := SystemLanguage(); got != "fr" {
		t.Errorf("LANG should be the fallback, got %q", got)
	}
	set("", "", "")
	if got := SystemLanguage(); got != "" {
		t.Errorf("no locale should yield %q, got %q", "", got)
	}
}

func TestDictationLanguageFallsBackToSystem(t *testing.T) {
	old, oldOS := localeEnv, osPreferredLanguage
	defer func() { localeEnv, osPreferredLanguage = old, oldOS }()
	osPreferredLanguage = func() string { return "" }
	localeEnv = func(key string) string {
		if key == "LANG" {
			return "pt_BR.UTF-8"
		}
		return ""
	}
	if got := DictationLanguage(""); got != "pt" {
		t.Errorf("an empty stt.language should fall back to the system one, got %q", got)
	}
}

// TestSystemLanguageOSSettingBeatsLANG: the OS language wins over LANG (which
// terminals set to en_US by default) but not over an explicit LC_ALL.
func TestSystemLanguageOSSettingBeatsLANG(t *testing.T) {
	old, oldOS := localeEnv, osPreferredLanguage
	defer func() { localeEnv, osPreferredLanguage = old, oldOS }()
	osPreferredLanguage = func() string { return "pt-BR" }
	env := map[string]string{"LANG": "en_US.UTF-8"}
	localeEnv = func(key string) string { return env[key] }
	if got := SystemLanguage(); got != "pt" {
		t.Errorf("SystemLanguage = %q, want pt from the OS setting", got)
	}
	env["LC_ALL"] = "fr_FR.UTF-8"
	if got := SystemLanguage(); got != "fr" {
		t.Errorf("SystemLanguage = %q, want fr from LC_ALL", got)
	}
}
