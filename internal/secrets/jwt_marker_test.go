package secrets

import (
	"testing"
)

// TestScanDetectsNonHblJWTAndJWE pins JWT shapes whose header is not the
// common {"alg"} ("eyJhbGciOi...") form, plus a full five-segment compact
// JWE. The scanner probes the "eyJ" marker before running the pattern; every
// token below carries "eyJ" but none carries a rarer marker, so narrowing the
// probe (or dropping a segment form) leaves one of these undetected and
// fails the table.
func TestScanDetectsNonHblJWTAndJWE(t *testing.T) {
	cases := []string{
		// Three-segment JWT with a {"typ"} header: the strict form matches
		// (both segments start with eyJ).
		"eyJ0eXAiOiJKV1QifQ.eyJzdWIiOiIxIn0.c2lnbmF0dXJlMTIz",
		// Loose form: payload is opaque, only the header starts with eyJ.
		"eyJ0eXAiOiJKV1QifQ.cGF5bG9hZDEy.c2lnbmF0dXJlMTIz",
		// Full compact JWE: header, encrypted key, IV, ciphertext, tag.
		"eyJ0eXAiOiJKV1QifQ.ZW5jcnlwdGVka2V5.aXZ2ZWN0b3IxMg.Y2lwaGVydGV4dDEy.dGFnMTIzNDU2Nzg",
	}
	for _, token := range cases {
		redacted, findings := Redact("auth=" + token)
		if len(findings) == 0 {
			t.Fatalf("expected jwt finding for %q, got none", token)
			continue
		}
		if findings[0].Type != "jwt" {
			t.Fatalf("expected jwt type for %q, got %#v", token, findings)
		}
		if redacted != "auth=[REDACTED:jwt]" {
			t.Fatalf("jwt incompletely redacted for %q: got %q", token, redacted)
		}
	}
}
