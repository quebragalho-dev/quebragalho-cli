package sessions

import "testing"

// TestCheckpointsEnabledUnifiesBooleanParsing locks the shared boolean
// vocabulary for QG_CHECKPOINTS: the legacy "off" still disables, the new
// false words (0/false/no, any case, surrounding whitespace ignored) disable
// too, and everything else — including unrecognized values — keeps the
// default-on behavior.
func TestCheckpointsEnabledUnifiesBooleanParsing(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", true},       // legacy default: on
		{"off", false},   // legacy disable word
		{"OFF", false},   // new: case-insensitive
		{" off ", false}, // new: surrounding whitespace ignored
		{"0", false},     // new: standard false word
		{"false", false}, // new: standard false word
		{"no", false},    // new: standard false word
		{"1", true},      // legacy: anything but "off" was on
		{"true", true},
		{"yes", true},
		{"on", true},
		{"banana", true}, // legacy: unrecognized values kept it on
	}
	for _, tc := range cases {
		t.Setenv("QG_CHECKPOINTS", tc.value)
		if got := CheckpointsEnabled(); got != tc.want {
			t.Errorf("QG_CHECKPOINTS=%q: CheckpointsEnabled() = %v, want %v", tc.value, got, tc.want)
		}
	}
}
