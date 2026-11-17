package providercatalog

import "testing"

// Characterization: the catalog ships exactly one provider and unknown legacy
// names stay unknown. Deleting dead transports must not change any of this.
func TestLegacyUnknownProvidersStayUnknown(t *testing.T) {
	for _, id := range []string{"anthropic", "google", "bedrock", "vertex", "openai"} {
		if _, ok := Get(id); ok {
			t.Fatalf("Get(%q) = true, want false (single-provider catalog)", id)
		}
		if err := func() error { _, err := Require(id); return err }(); err == nil {
			t.Fatalf("Require(%q) = nil, want unknown-provider error", id)
		}
	}
	qg, err := Require(QGID)
	if err != nil {
		t.Fatalf("Require(qg) = %v, want qg descriptor", err)
	}
	if !RuntimeSupported(qg) {
		t.Fatal("RuntimeSupported(qg) = false, want true")
	}
	if reason := RuntimeUnsupportedReason(qg); reason != "" {
		t.Fatalf("RuntimeUnsupportedReason(qg) = %q, want empty", reason)
	}
}
