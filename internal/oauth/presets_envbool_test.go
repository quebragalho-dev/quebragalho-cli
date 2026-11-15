package oauth

import "testing"

// TestPresetsAllowedUnifiesBooleanParsing locks the shared boolean vocabulary
// for QG_OAUTH_ALLOW_PRESETS: the legacy "1" still opts in, the other unified
// true words (true/yes/on, any case, surrounding whitespace ignored) opt in,
// and false words, empty, and unrecognized values keep presets off.
func TestPresetsAllowedUnifiesBooleanParsing(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"1", true},     // legacy opt-in word
		{"true", true},  // legacy opt-in word
		{"yes", true},   // legacy opt-in word
		{"on", true},    // legacy opt-in word
		{"YES", true},   // unified: case-insensitive
		{" on ", true},  // unified: whitespace ignored
		{"", false},     // legacy: presets off by default
		{"0", false},    // unified false word
		{"false", false},
		{"no", false},
		{"off", false},
		{"banana", false}, // unrecognized values keep presets off
	}
	for _, tc := range cases {
		if got := presetsAllowed(map[string]string{"QG_OAUTH_ALLOW_PRESETS": tc.value}); got != tc.want {
			t.Errorf("QG_OAUTH_ALLOW_PRESETS=%q: presetsAllowed = %v, want %v", tc.value, got, tc.want)
		}
	}
}
