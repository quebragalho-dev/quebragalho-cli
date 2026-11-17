package config

import (
	"fmt"
	"strings"
)

const (
	// AutoCompactAutomatic is the unset value: the agent picks the threshold.
	AutoCompactAutomatic = 0
	// AutoCompactOff turns proactive compaction off.
	AutoCompactOff = -1
	// MinAutoCompactLimit is the smallest accepted limit. Below it the fixed
	// request overhead (system prompt and tool definitions) alone would keep
	// the conversation over the limit and every turn would try to compact.
	MinAutoCompactLimit = 20_000
	// MaxAutoCompactLimit bounds the parser; no model window is near it.
	MaxAutoCompactLimit = 100_000_000
)

// NormalizeAutoCompactLimit clamps an auto-compact limit to its valid range:
// any negative value means off, values below the minimum are raised to it and
// values above the maximum are lowered to it.
func NormalizeAutoCompactLimit(limit int) int {
	if limit < 0 {
		return AutoCompactOff
	}
	if limit == 0 {
		return AutoCompactAutomatic
	}
	if limit < MinAutoCompactLimit {
		return MinAutoCompactLimit
	}
	if limit > MaxAutoCompactLimit {
		return MaxAutoCompactLimit
	}
	return limit
}

// ParseAutoCompactLimit parses the /autocompact argument: "auto", "off", a
// plain token count ("400000"), or a suffixed shorthand ("400k", "1m").
// Matching is case-insensitive and surrounding whitespace is ignored.
func ParseAutoCompactLimit(text string) (int, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, fmt.Errorf("auto-compact limit is required: expected auto, off, or a token count like 400k")
	}
	lowered := strings.ToLower(trimmed)
	if lowered == "auto" {
		return AutoCompactAutomatic, nil
	}
	if lowered == "off" {
		return AutoCompactOff, nil
	}
	multiplier := 1
	digits := lowered
	if strings.HasSuffix(lowered, "k") {
		multiplier = 1_000
		digits = lowered[:len(lowered)-1]
	} else if strings.HasSuffix(lowered, "m") {
		multiplier = 1_000_000
		digits = lowered[:len(lowered)-1]
	}
	if digits == "" {
		return 0, fmt.Errorf("invalid auto-compact limit %q: expected auto, off, or a token count like 400k", text)
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid auto-compact limit %q: expected auto, off, or a token count like 400k", text)
		}
	}
	digitLimit := MaxAutoCompactLimit / multiplier
	value := 0
	for _, r := range digits {
		digit := int(r - '0')
		if value > (digitLimit-digit)/10 {
			return 0, fmt.Errorf("invalid auto-compact limit %q: exceeds the maximum of %d tokens", text, MaxAutoCompactLimit)
		}
		value = value*10 + digit
	}
	// The digit loop already keeps value*multiplier within the maximum.
	result := value * multiplier
	if result < MinAutoCompactLimit {
		return 0, fmt.Errorf("invalid auto-compact limit %q: must be auto, off, or at least %d tokens", text, MinAutoCompactLimit)
	}
	return result, nil
}

// FormatAutoCompactLimit renders an auto-compact limit the way
// ParseAutoCompactLimit reads it back: "auto", "off", a "m"/"k" shorthand
// for exact multiples, or plain digits otherwise.
func FormatAutoCompactLimit(limit int) string {
	if limit == 0 {
		return "auto"
	}
	if limit < 0 {
		return "off"
	}
	if limit%1_000_000 == 0 {
		return fmt.Sprintf("%dm", limit/1_000_000)
	}
	if limit%1_000 == 0 {
		return fmt.Sprintf("%dk", limit/1_000)
	}
	return fmt.Sprintf("%d", limit)
}
