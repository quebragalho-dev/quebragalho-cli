package redaction_test

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/redaction"
	"github.com/quebragalho-dev/quebragalho-cli/internal/secrets"
)

// Audit parity: secrets.Scan (bash/plugin/spill output) and RedactString must
// agree on every shaped secret either of them catches, and neither may redact
// ordinary prose. Keep in sync with
// internal/secrets/audit_shapes_test.go,
// internal/config/redact_audit_test.go and
// internal/streamjson/redact_audit_test.go. If a list gains a format the
// other lacks, extend this corpus (and the matching list) instead of
// narrowing the test.
//
// NOTE: the openai floor deliberately differs by layer — secrets/redaction
// use 20 chars, config/streamjson use 16 so 16-19-char keys with digits keep
// being redacted — so this corpus only uses openai shapes every floor
// catches (>= 20-char bodies) and prose every floor leaves alone
// (task-list, digit-free kebab). The 16-19-char floor is pinned by the
// per-package audit tests in config and streamjson instead.
func TestAuditSecretsRedactionParity(t *testing.T) {
	mustCatch := []string{
		"glpat-abcdefghijklmnop1234",
		"AKIAIOSFODNN7EXAMPLE",
		"ASIAIOSFODNN7EXAMPLE",
		"sk-proj-abcdefghijklmnopqrstuvwx",
		"sk-abcdefghijklmnopqrstuvwxyz012345",
	}
	for _, secret := range mustCatch {
		text := "log " + secret + " end"
		if findings := secrets.Scan(text); len(findings) == 0 {
			t.Errorf("secrets.Scan missed %q", secret)
		}
		if got := redaction.RedactString(text, redaction.Options{}); strings.Contains(got, secret) {
			t.Errorf("redaction.RedactString missed %q: %q", secret, got)
		}
	}
	mustKeep := []string{
		"updated the task-list yesterday",
		"sk-learn-machine-learning-model",
	}
	for _, clean := range mustKeep {
		if findings := secrets.Scan(clean); len(findings) != 0 {
			t.Errorf("secrets.Scan over-redacted %q: %#v", clean, findings)
		}
		if got := redaction.RedactString(clean, redaction.Options{}); got != clean {
			t.Errorf("redaction.RedactString over-redacted %q: %q", clean, got)
		}
	}
}
