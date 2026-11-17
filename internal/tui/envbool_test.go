package tui

import (
	"testing"

	"github.com/charmbracelet/colorprofile"
)

// TestReducedMotionEnabledUnifiesBooleanParsing locks the shared boolean
// vocabulary for QG_REDUCED_MOTION: the legacy "1" still enables and
// "0"/"false" still disable, the new true words (yes/on) enable, the new
// false words (no/off) disable, and unrecognized non-empty values keep the
// legacy "set means on" behavior. The no-TTY forcing is unchanged.
func TestReducedMotionEnabledUnifiesBooleanParsing(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	cases := []struct {
		name string
		vars map[string]string
		want bool
	}{
		{"legacy 1 enables", map[string]string{"QG_REDUCED_MOTION": "1"}, true},
		{"legacy 0 disables", map[string]string{"QG_REDUCED_MOTION": "0"}, false},
		{"legacy false disables", map[string]string{"QG_REDUCED_MOTION": "false"}, false},
		{"new yes enables", map[string]string{"QG_REDUCED_MOTION": "yes"}, true},
		{"new on enables", map[string]string{"QG_REDUCED_MOTION": "ON"}, true},
		{"new no disables", map[string]string{"QG_REDUCED_MOTION": "no"}, false},
		{"new off disables", map[string]string{"QG_REDUCED_MOTION": " Off "}, false},
		{"legacy garbage still enables", map[string]string{"QG_REDUCED_MOTION": "banana"}, true},
		{"empty stays off", nil, false},
	}
	for _, c := range cases {
		if got := reducedMotionEnabled(env(c.vars), colorprofile.TrueColor); got != c.want {
			t.Errorf("%s: reducedMotionEnabled = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestNoStreamClearEnabledUnifiesBooleanParsing locks the shared boolean
// vocabulary for QG_NO_STREAM_CLEAR: the legacy "1" still disables the fast
// path and "0"/"false" still leave it on, the new true words (yes/on)
// disable, the new false words (no/off) leave it on, and unrecognized
// non-empty values keep the legacy "set means disable" behavior.
func TestNoStreamClearEnabledUnifiesBooleanParsing(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", false},      // legacy: fast path on by default
		{"1", true},      // legacy disable word
		{"0", false},     // legacy: explicitly off
		{"false", false}, // legacy: explicitly off
		{"yes", true},    // newly spelled-out true word (was already disabling)
		{"on", true},     // newly spelled-out true word (was already disabling)
		{"banana", true}, // legacy: unrecognized values disable
		{"no", false},    // new: standard false word
		{"off", false},   // new: standard false word
	}
	for _, tc := range cases {
		t.Setenv("QG_NO_STREAM_CLEAR", tc.value)
		if got := noStreamClearEnabled(); got != tc.want {
			t.Errorf("QG_NO_STREAM_CLEAR=%q: noStreamClearEnabled() = %v, want %v", tc.value, got, tc.want)
		}
	}
}
