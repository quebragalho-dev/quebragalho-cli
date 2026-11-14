package streamjson

import (
	"strings"
	"testing"
)

// Audit: the stream-json redactor must use the same openai floor/filter as the
// other lists: a 20-char minimum plus the kebab-case digit/known-prefix
// filter. Keep in sync with internal/secrets/audit_shapes_test.go,
// internal/config/redact_audit_test.go and
// internal/redaction/secret_parity_test.go.
func TestAuditRedactStringOpenAIParity(t *testing.T) {
	for _, clean := range []string{
		"updated the task-list yesterday",
		"sk-learn-machine-learning-model",
		"sk-abcdefghijklmnop",
		"token sk-abcdefghijklmnop12 end",
	} {
		if got := redactString(clean); got != clean {
			t.Errorf("redactString(%q) = %q, want unchanged", clean, got)
		}
	}
	for _, secret := range []string{
		"sk-proj-abcdefghijklmnopqrstuvwx",
		"sk-abcdefghijklmnopqrstuvwxyz012345",
		"leaked sk-svcacct-abcdefghijklmnopqrstuvwx today",
	} {
		if got := redactString(secret); !strings.Contains(got, "[REDACTED]") {
			t.Errorf("redactString(%q) = %q, want redaction", secret, got)
		}
	}
}
