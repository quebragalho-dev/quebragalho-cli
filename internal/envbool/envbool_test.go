package envbool

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		raw  string
		def  bool
		want bool
	}{
		// Recognized true words (case and whitespace ignored).
		{"1", false, true},
		{"true", false, true},
		{"TRUE", false, true},
		{" True ", false, true},
		{"yes", false, true},
		{"YES", false, true},
		{" yes ", false, true},
		{"on", false, true},
		{"ON", false, true},
		{" On ", false, true},
		// Recognized false words (case and whitespace ignored).
		{"0", true, false},
		{"false", true, false},
		{"FALSE", true, false},
		{" False ", true, false},
		{"no", true, false},
		{"NO", true, false},
		{" no ", true, false},
		{"off", true, false},
		{"OFF", true, false},
		{" Off ", true, false},
		// Empty and unrecognized values yield the caller's default.
		{"", false, false},
		{"", true, true},
		{"   ", false, false},
		{"   ", true, true},
		{"banana", false, false},
		{"banana", true, true},
		{"2", false, false},
		{"2", true, true},
	}
	for _, tc := range cases {
		if got := Parse(tc.raw, tc.def); got != tc.want {
			t.Errorf("Parse(%q, %v) = %v, want %v", tc.raw, tc.def, got, tc.want)
		}
	}
}

func TestLoose(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		// Legacy "any non-empty enables" behavior is preserved.
		{"", false},
		{"   ", false},
		{"1", true},
		{"true", true},
		{"yes", true},
		{"on", true},
		{"banana", true},
		{"2", true},
		// Explicitly-false words stay off.
		{"0", false},
		{"false", false},
		{"FALSE", false},
		{" False ", false},
		{"no", false},
		{"off", false},
	}
	for _, tc := range cases {
		if got := Loose(tc.raw); got != tc.want {
			t.Errorf("Loose(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
