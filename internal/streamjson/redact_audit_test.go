package streamjson

import (
	"strings"
	"testing"
)

// Audit: the stream-json redactor must use the same openai shape as the
// other lists — a word boundary plus the kebab-case digit/known-prefix
// filter — with a 16-char floor so 16-19-char keys with digits keep being
// redacted. Keep in sync with internal/secrets/audit_shapes_test.go,
// internal/config/redact_audit_test.go and
// internal/redaction/secret_parity_test.go.
func TestAuditRedactStringOpenAIParity(t *testing.T) {
	for _, clean := range []string{
		"updated the task-list yesterday",
		"sk-learn-machine-learning-model",
		// Mid-word sk- with a long digit-bearing body: without the \b
		// anchor this would match and be redacted.
		"mask-abcdef1234567890xyz",
	} {
		if got := redactString(clean); got != clean {
			t.Errorf("redactString(%q) = %q, want unchanged", clean, got)
		}
	}
	for _, secret := range []string{
		"sk-abcdef1234567890",    // 16-char body with digits
		"sk-abcdef12345678901",   // 17-char body with digits
		"sk-abcdef1234567890123", // 19-char body with digits
		// Interior-hyphen keys WITH digits and unknown prefixes are real
		// credentials, not kebab prose: the digit check must not be dropped.
		"sk-ant-api03-AbCd1234EfGh5678",
		"sk-or-v1-0123abcd4567efgh",
		"sk-proj-abcdefghijklmnopqrstuvwx",
		"sk-abcdefghijklmnopqrstuvwxyz012345",
		"leaked sk-svcacct-abcdefghijklmnopqrstuvwx today",
	} {
		if got := redactString(secret); !strings.Contains(got, "[REDACTED]") {
			t.Errorf("redactString(%q) = %q, want redaction", secret, got)
		}
	}
}
