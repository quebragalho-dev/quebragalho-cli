package openai

import "testing"

// TestPromptCacheKeyDisabledUnifiesBooleanParsing locks the shared boolean
// vocabulary for QG_DISABLE_PROMPT_CACHE_KEY: the legacy "1" still omits the
// key and "0"/"false" still keep it, the new true words (yes/on) omit, the
// new false words (no/off) keep, and unrecognized non-empty values keep the
// legacy "set means omit" behavior.
func TestPromptCacheKeyDisabledUnifiesBooleanParsing(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},      // legacy: key sent by default
		{"1", true},      // legacy kill-switch word
		{"0", false},     // legacy no-op
		{"false", false}, // legacy no-op
		{"yes", true},    // newly spelled-out true word (was already omitting)
		{"on", true},     // newly spelled-out true word (was already omitting)
		{"banana", true}, // legacy: unrecognized values omit
		{"no", false},    // new: standard false word
		{"off", false},   // new: standard false word
	}
	for _, tc := range cases {
		t.Setenv("QG_DISABLE_PROMPT_CACHE_KEY", tc.value)
		if got := promptCacheKeyDisabled(); got != tc.want {
			t.Errorf("QG_DISABLE_PROMPT_CACHE_KEY=%q: promptCacheKeyDisabled() = %v, want %v", tc.value, got, tc.want)
		}
	}
}
