package config

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	// Same openai shape as the other lists except the floor: a word boundary
	// (so "task-list" never matches) and no "." in the body class. The floor
	// stays at 16 chars so 16-19-char keys with digits keep being redacted;
	// digit-free kebab-case matches (sk-learn-…) are dropped by the filter in
	// redactSecrets unless they carry a known OpenAI-issued prefix
	// (sk-proj-/sk-svcacct-/sk-admin-).
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`),
	regexp.MustCompile(`(?i)(api[_-]?key["'=:\s]+)[^"',\s)]+`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._-]+`),
}

func redactSecrets(value string, knownSecrets ...string) string {
	redacted := value
	for _, secret := range knownSecrets {
		if secret == "" {
			continue
		}
		redacted = strings.ReplaceAll(redacted, secret, "[REDACTED]")
	}
	for _, pattern := range secretPatterns {
		redacted = pattern.ReplaceAllStringFunc(redacted, func(match string) string {
			for _, prefix := range []string{"apiKey=", "api_key=", "api-key=", "Bearer "} {
				if strings.HasPrefix(strings.ToLower(match), strings.ToLower(prefix)) {
					return match[:len(prefix)] + "[REDACTED]"
				}
			}
			if strings.HasPrefix(match, "sk-") {
				// Drop digit-free kebab-case false positives (sk-learn-…),
				// mirroring secrets.Scan: known OpenAI-issued prefixes are
				// always credentials even without a digit.
				if !knownOpenAIKeyPrefix(match) && !secretMatchHasDigit(match) &&
					strings.Contains(strings.TrimPrefix(match, "sk-"), "-") {
					return match
				}
				return "[REDACTED]"
			}
			return "[REDACTED]"
		})
	}
	return redacted
}

// knownOpenAIKeyPrefix mirrors secrets.knownOpenAIKeyPrefix: known
// OpenAI-issued forms redact even with an alphabet-only body.
func knownOpenAIKeyPrefix(match string) bool {
	return strings.HasPrefix(match, "sk-proj-") ||
		strings.HasPrefix(match, "sk-svcacct-") ||
		strings.HasPrefix(match, "sk-admin-")
}

// secretMatchHasDigit mirrors secrets.containsDigit.
func secretMatchHasDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
