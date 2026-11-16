//go:build !windows

package dictation

import "testing"

func TestSystemLanguagePrecedence(t *testing.T) {
	old := localeEnv
	defer func() { localeEnv = old }()
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
	old := localeEnv
	defer func() { localeEnv = old }()
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
