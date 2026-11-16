//go:build !windows

package dictation

import "os"

// localeEnv reads a locale environment variable. A variable (not os.Getenv
// directly) so tests can inject values without depending on the machine.
var localeEnv = os.Getenv

// SystemLanguage reports the OS language as an ISO-639-1 code, from the first
// non-empty of LC_ALL, LC_MESSAGES, LANG. "" when none yields a language.
func SystemLanguage() string {
	for _, key := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if lang := LanguageFromLocale(localeEnv(key)); lang != "" {
			return lang
		}
	}
	return ""
}
