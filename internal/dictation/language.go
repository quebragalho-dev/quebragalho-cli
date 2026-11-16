package dictation

import (
	"slices"
	"strings"
)

// LanguageRank orders a model for a user who dictates in lang: 0 when the
// model is made for that language, 1 when it is multilingual and covers lang
// (or lang is unknown, or the model claims every language), 2 when it is made
// for another language or is multilingual without covering lang. The
// /stt-model picker sorts by this rank so the user's language comes first.
func LanguageRank(v ModelVariant, lang string) int {
	if lang == "" {
		return 1
	}
	if v.Language != "" {
		if v.Language == lang {
			return 0
		}
		return 2
	}
	if v.Languages == nil || slices.Contains(v.Languages, lang) {
		return 1
	}
	return 2
}

// LanguageFromLocale extracts the ISO-639-1 language from a locale name:
// "pt_BR.UTF-8" → "pt", "en_US" → "en", "pt-BR" → "pt". Anything that is not
// a two-letter language ("C", "POSIX", "", "x") → "".
func LanguageFromLocale(locale string) string {
	s := strings.TrimSpace(locale)
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i] // "pt_BR.UTF-8" → "pt_BR"
	}
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[:i] // "pt_PT@euro" → "pt_PT"
	}
	if i := strings.IndexAny(s, "_-"); i >= 0 {
		s = s[:i] // "pt_BR"/"pt-BR" → "pt"; "zh-Hans-CN" → "zh"
	}
	s = strings.ToLower(s)
	if len(s) != 2 {
		return ""
	}
	if s[0] < 'a' || s[0] > 'z' || s[1] < 'a' || s[1] > 'z' {
		return ""
	}
	return s
}

// DictationLanguage resolves the language dictation should listen in: the
// configured stt.language when set, else the system language.
func DictationLanguage(configured string) string {
	if c := strings.TrimSpace(configured); c != "" {
		return c
	}
	return SystemLanguage()
}
