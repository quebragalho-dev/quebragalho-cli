package config

import (
	"strings"
	"testing"
)

// Audit: the config redactor must use the same openai floor/filter as the
// other lists: a word boundary, a 20-char minimum, and the kebab-case
// digit/known-prefix filter. Keep in sync with
// internal/secrets/audit_shapes_test.go,
// internal/streamjson/redact_audit_test.go and
// internal/redaction/secret_parity_test.go.
func TestAuditRedactSecretsOpenAIParity(t *testing.T) {
	for _, clean := range []string{
		"updated the task-list yesterday",
		"sk-learn-machine-learning-model",
		"sk-abcdefghijklmnop",
		"token sk-abcdefghijklmnop12 end",
	} {
		if got := redactSecrets(clean); got != clean {
			t.Errorf("redactSecrets(%q) = %q, want unchanged", clean, got)
		}
	}
	for _, secret := range []string{
		"key sk-proj-abcdefghijklmnopqrstuvwx done",
		"key sk-abcdefghijklmnopqrstuvwxyz012345 done",
		"key sk-svcacct-abcdefghijklmnopqrstuvwx done",
	} {
		if got := redactSecrets(secret); !strings.Contains(got, "[REDACTED]") || strings.Contains(got, "sk-") {
			t.Errorf("redactSecrets(%q) = %q, want redaction", secret, got)
		}
	}
}
