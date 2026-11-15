package tools

import "testing"

// TestFormatOnWriteEnabledUnifiesBooleanParsing locks the shared boolean
// vocabulary for QG_FORMAT_ON_WRITE: the legacy "1" still enables and
// "0"/"false" still disable, the new true words (yes/on) enable, the new
// false words (no/off) disable, and unrecognized non-empty values keep the
// legacy "set means on" behavior.
func TestFormatOnWriteEnabledUnifiesBooleanParsing(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},      // legacy: off by default
		{"1", true},      // legacy enable word
		{"true", true},   // legacy: any non-empty but 0/false enabled
		{"yes", true},    // newly spelled-out true word (was already on)
		{"on", true},     // newly spelled-out true word (was already on)
		{"banana", true}, // legacy: unrecognized values enable
		{"0", false},     // legacy disable word
		{"false", false}, // legacy disable word
		{"FALSE", false}, // legacy: case-insensitive false
		{"no", false},    // new: standard false word
		{"off", false},   // new: standard false word
		{" Off ", false}, // new: whitespace ignored
	}
	for _, tc := range cases {
		t.Setenv("QG_FORMAT_ON_WRITE", tc.value)
		if got := formatOnWriteEnabled(); got != tc.want {
			t.Errorf("QG_FORMAT_ON_WRITE=%q: formatOnWriteEnabled() = %v, want %v", tc.value, got, tc.want)
		}
	}
}
