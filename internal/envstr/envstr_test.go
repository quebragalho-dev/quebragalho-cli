package envstr

import (
	"testing"
)

func TestValue(t *testing.T) {
	t.Setenv("ENVSTR_TEST_VALUE", "ambient-value")

	tests := []struct {
		name string
		env  map[string]string
		key  string
		want string
	}{
		// Nil map falls back to the ambient environment.
		{name: "nil map reads ambient", env: nil, key: "ENVSTR_TEST_VALUE", want: "ambient-value"},
		{name: "nil map missing key", env: nil, key: "ENVSTR_TEST_VALUE_MISSING", want: ""},
		// A non-nil map is hermetic: it never consults the ambient environment,
		// and a missing or empty key yields "".
		{name: "empty map ignores ambient", env: map[string]string{}, key: "ENVSTR_TEST_VALUE", want: ""},
		{name: "missing key", env: map[string]string{"OTHER": "x"}, key: "ENVSTR_TEST_VALUE", want: ""},
		{name: "present empty stays empty", env: map[string]string{"ENVSTR_TEST_VALUE": ""}, key: "ENVSTR_TEST_VALUE", want: ""},
		// Lookup is exact and the value is returned untrimmed.
		{name: "present value", env: map[string]string{"A": "b"}, key: "A", want: "b"},
		{name: "case sensitive", env: map[string]string{"a": "lower"}, key: "A", want: ""},
		{name: "no trim", env: map[string]string{"A": "  padded  "}, key: "A", want: "  padded  "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Value(tt.env, tt.key); got != tt.want {
				t.Fatalf("Value(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   string
	}{
		// Strict left-to-right precedence.
		{name: "first wins", values: []string{"a", "b"}, want: "a"},
		{name: "empty skipped", values: []string{"", "b"}, want: "b"},
		// Blank (whitespace-only) counts as absent and is skipped.
		{name: "spaces skipped", values: []string{"   ", "b"}, want: "b"},
		{name: "tab newline skipped", values: []string{"\t\n ", "b"}, want: "b"},
		// The winner is returned trimmed.
		{name: "winner trimmed", values: []string{"  a  ", "b"}, want: "a"},
		{name: "inner spaces kept", values: []string{"  a b  "}, want: "a b"},
		// Nothing usable yields "".
		{name: "all empty", values: []string{"", "  ", ""}, want: ""},
		{name: "no args", values: nil, want: ""},
		{name: "single blank", values: []string{"   "}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FirstNonEmpty(tt.values...); got != tt.want {
				t.Fatalf("FirstNonEmpty(%q) = %q, want %q", tt.values, got, tt.want)
			}
		})
	}
}
