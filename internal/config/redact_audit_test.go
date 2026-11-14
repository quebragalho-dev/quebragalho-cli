package config

import (
	"strings"
	"testing"
)

// Audit: the config redactor must use the same openai shape as the other
// lists — a word boundary plus the kebab-case digit/known-prefix filter —
// with a 16-char floor so 16-19-char keys with digits keep being redacted.
// Keep in sync with internal/secrets/audit_shapes_test.go,
// internal/streamjson/redact_audit_test.go and
// internal/redaction/secret_parity_test.go.
func TestAuditRedactSecretsOpenAIParity(t *testing.T) {
	for _, clean := range []string{
		"updated the task-list yesterday",
		"sk-learn-machine-learning-model",
	} {
		if got := redactSecrets(clean); got != clean {
			t.Errorf("redactSecrets(%q) = %q, want unchanged", clean, got)
		}
	}
	for _, secret := range []string{
		"key sk-abcdef1234567890 done",    // 16-char body with digits
		"key sk-abcdef12345678901 done",   // 17-char body with digits
		"key sk-abcdef1234567890123 done", // 19-char body with digits
		"key sk-abcdefghijklmnop done",    // 16-char body, legacy digit-free
		"key sk-proj-abcdefghijklmnopqrstuvwx done",
		"key sk-abcdefghijklmnopqrstuvwxyz012345 done",
		"key sk-svcacct-abcdefghijklmnopqrstuvwx done",
	} {
		if got := redactSecrets(secret); !strings.Contains(got, "[REDACTED]") || strings.Contains(got, "sk-") {
			t.Errorf("redactSecrets(%q) = %q, want redaction", secret, got)
		}
	}
}
