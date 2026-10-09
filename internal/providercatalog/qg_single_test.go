package providercatalog

import "testing"

func TestQGIsSingleProvider(t *testing.T) {
	descriptors := All()
	if len(descriptors) != 1 {
		t.Fatalf("All() returned %d descriptors, want 1 (qg)", len(descriptors))
	}
	d := descriptors[0]
	if d.ID != "qg" {
		t.Fatalf("All()[0].ID = %q, want qg", d.ID)
	}
	if d.DefaultBaseURL != "https://api.quebragalho.dev/v1" {
		t.Fatalf("DefaultBaseURL = %q, want https://api.quebragalho.dev/v1", d.DefaultBaseURL)
	}
	if d.DefaultModel != "muse-spark-1.3-contributor" {
		t.Fatalf("DefaultModel = %q, want muse-spark-1.3-contributor", d.DefaultModel)
	}
	if len(d.AuthEnvVars) != 1 || d.AuthEnvVars[0] != "QUEBRAGALHO_API_KEY" {
		t.Fatalf("AuthEnvVars = %#v, want [QUEBRAGALHO_API_KEY]", d.AuthEnvVars)
	}
	if d.Transport != TransportOpenAICompatible {
		t.Fatalf("Transport = %q, want %q", d.Transport, TransportOpenAICompatible)
	}
	if !d.RequiresAuth {
		t.Fatal("qg RequiresAuth = false, want true")
	}
	if _, ok := Get("openai"); ok {
		t.Fatal("Get(openai) = true, want false after single-provider cut")
	}
	if _, ok := Get("qg"); !ok {
		t.Fatal("Get(qg) = false, want true")
	}
}
