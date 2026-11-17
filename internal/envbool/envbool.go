// Package envbool parses QG_* boolean environment values with one shared
// vocabulary so every toggle spells true and false the same way.
//
// Recognized true values: 1, true, yes, on. Recognized false values: 0,
// false, no, off. Matching ignores case and surrounding whitespace. Anything
// else (including empty) is not a boolean and resolves to the caller's
// default, so each call site keeps its own on/off default while sharing the
// spelling.
package envbool

import "strings"

// Parse interprets raw as a boolean. Empty or unrecognized values yield def.
func Parse(raw string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

// Loose interprets raw for legacy "any non-empty value enables" toggles
// (QG_FORMAT_ON_WRITE, QG_REDUCED_MOTION, QG_NO_STREAM_CLEAR,
// QG_DISABLE_PROMPT_CACHE_KEY): empty is false, explicitly-false words are
// false, and anything else — recognized-true or unrecognized — is true, so a
// value that enabled the toggle before keeps enabling it.
func Loose(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	return Parse(raw, true)
}
