package secrets

import (
	"strings"
	"testing"
)

// Audit: the scanner must catch every shaped secret the redaction pass
// catches (GitLab tokens, temporary AWS keys) and must not redact ordinary
// prose. Keep in sync with internal/redaction/secret_parity_test.go,
// internal/config/redact_audit_test.go and
// internal/streamjson/redact_audit_test.go.
func TestAuditScannerCatchesGitLabAndTemporaryAWSKeys(t *testing.T) {
	cases := map[string]string{
		"gitlab_token":      "glpat-abcdefghijklmnop1234",
		"aws_access_key_id": "ASIAIOSFODNN7EXAMPLE",
	}
	for wantType, secret := range cases {
		findings := Scan("log line before " + secret + " and after")
		found := false
		for _, f := range findings {
			if f.Match == secret && f.Type == wantType {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: expected a finding for %q, got %#v", wantType, secret, findings)
		}
		if redacted, _ := Redact("token " + secret); strings.Contains(redacted, secret) {
			t.Errorf("%s: Redact left %q visible: %q", wantType, secret, redacted)
		}
	}
	// The permanent key shape keeps working under the widened pattern.
	if findings := Scan("log AKIAIOSFODNN7EXAMPLE end"); len(findings) == 0 {
		t.Errorf("expected a finding for AKIAIOSFODNN7EXAMPLE, got %#v", findings)
	}
}

func TestAuditScannerLeavesOrdinaryProseAlone(t *testing.T) {
	for _, clean := range []string{
		"updated the task-list yesterday",
		"sk-learn-machine-learning-model",
		"sk-abcdefghijklmnop",
	} {
		if redacted, findings := Redact(clean); len(findings) != 0 || redacted != clean {
			t.Errorf("over-redacted %q -> %q (findings=%#v)", clean, redacted, findings)
		}
	}
}
