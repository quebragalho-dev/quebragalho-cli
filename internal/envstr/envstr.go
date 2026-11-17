// Package envstr holds the two string helpers that used to be copied across
// a dozen internal packages: map-backed env lookup with ambient fallback,
// and first-non-blank selection.
//
// Both helpers are leaf code (stdlib only) so any internal package can use
// them without creating an import cycle.
package envstr

import (
	"os"
	"strings"
)

// Value returns env[key] when env is non-nil, or os.Getenv(key) when env is
// nil. A non-nil map is hermetic: a missing or empty key yields "" without
// consulting the ambient environment. Lookup is exact (case-sensitive) and
// the value is returned untrimmed.
func Value(env map[string]string, key string) string {
	if env != nil {
		return env[key]
	}
	return os.Getenv(key)
}

// FirstNonEmpty returns the first value whose TrimSpace form is non-empty,
// trimmed; it returns "" when every value is empty or blank. Precedence is
// strictly left to right.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
