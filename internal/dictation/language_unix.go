//go:build !windows

package dictation

import "os"

// localeEnv reads a locale environment variable. A variable (not os.Getenv
// directly) so tests can inject values without depending on the machine.
var localeEnv = os.Getenv

// osPreferredLanguage reports the language chosen in the OS settings, when
// the platform has one apart from the environment (macOS); "" elsewhere.
var osPreferredLanguage = func() string { return "" }

// SystemLanguage reports the OS language as an ISO-639-1 code: an explicit
// LC_ALL or LC_MESSAGES, then the OS setting, then LANG (terminals on macOS
// often export LANG=en_US.UTF-8 whatever the system language is). "" when
// none yields a language.
func SystemLanguage() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES"} {
		if lang := LanguageFromLocale(localeEnv(key)); lang != "" {
			return lang
		}
	}
	if lang := LanguageFromLocale(osPreferredLanguage()); lang != "" {
		return lang
	}
	return LanguageFromLocale(localeEnv("LANG"))
}
