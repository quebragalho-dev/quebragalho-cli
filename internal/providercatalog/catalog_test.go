package providercatalog

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestAllHasSingleQGID(t *testing.T) {
	descriptors := All()
	if len(descriptors) != 1 || descriptors[0].ID != "qg" {
		t.Fatalf("All() IDs = %#v, want [qg]", descriptorIDs(descriptors))
	}
	if !reflect.DeepEqual(IDs(), []string{"qg"}) {
		t.Fatalf("IDs() = %#v, want [qg]", IDs())
	}
}

func TestQGDescriptorDefaults(t *testing.T) {
	descriptor, err := Require("qg")
	if err != nil {
		t.Fatalf("Require(qg) error = %v", err)
	}
	if descriptor.Name == "" {
		t.Fatal("qg should expose a display name")
	}
	if descriptor.DefaultBaseURL != "https://api.quebragalho.dev/v1" {
		t.Fatalf("DefaultBaseURL = %q, want https://api.quebragalho.dev/v1", descriptor.DefaultBaseURL)
	}
	if descriptor.DefaultModel != "muse-spark-1.3-contributor" {
		t.Fatalf("DefaultModel = %q, want muse-spark-1.3-contributor", descriptor.DefaultModel)
	}
	if descriptor.Transport != TransportOpenAICompatible {
		t.Fatalf("Transport = %q, want %q", descriptor.Transport, TransportOpenAICompatible)
	}
	if !reflect.DeepEqual(descriptor.AuthEnvVars, []string{"QUEBRAGALHO_API_KEY"}) {
		t.Fatalf("AuthEnvVars = %#v, want [QUEBRAGALHO_API_KEY]", descriptor.AuthEnvVars)
	}
	if !descriptor.RequiresAuth {
		t.Fatal("qg RequiresAuth = false, want true")
	}
	if !descriptor.Recommended {
		t.Fatal("qg should be recommended")
	}
	if !ValidTransport(descriptor.Transport) {
		t.Fatalf("qg has unknown transport %q", descriptor.Transport)
	}
	for _, format := range descriptor.SupportedAPIFormats {
		if !ValidAPIFormat(format) {
			t.Fatalf("qg has unknown API format %q", format)
		}
	}
}

func TestQGRejectsLegacyProviders(t *testing.T) {
	for _, id := range []string{"openai", "anthropic", "google", "gitlawb-opengateway", "aimlapi", "chatgpt", "ollama"} {
		if _, ok := Get(id); ok {
			t.Fatalf("Get(%q) = true, want false after single-provider cut", id)
		}
		if _, err := Require(id); !errors.Is(err, ErrUnknownProvider) {
			t.Fatalf("Require(%q) error = %v, want ErrUnknownProvider", id, err)
		}
	}
}

func TestLookupNormalizesQGIDAndAlias(t *testing.T) {
	for input, want := range map[string]string{
		" qg ":         "qg",
		"QG":           "qg",
		"quebragalho":  "qg",
		"Quebra Galho": "qg",
	} {
		descriptor, ok := Get(input)
		if !ok {
			t.Fatalf("Get(%q) returned false", input)
		}
		if descriptor.ID != want {
			t.Fatalf("Get(%q).ID = %q, want %q", input, descriptor.ID, want)
		}
	}
	if _, ok := Get("unknown-provider"); ok {
		t.Fatal("Get should reject unknown provider IDs")
	}
	if _, err := Require("  Not_A_Provider  "); err == nil {
		t.Fatal("Require should reject unknown provider IDs")
	} else {
		if !errors.Is(err, ErrUnknownProvider) {
			t.Fatalf("Require error = %v, want ErrUnknownProvider", err)
		}
		if !strings.Contains(err.Error(), `unknown provider "not-a-provider"`) {
			t.Fatalf("Require error = %q, want normalized provider ID", err.Error())
		}
	}
}

func TestListByTransportSingleProvider(t *testing.T) {
	got := ListByTransport(TransportOpenAICompatible)
	if len(got) != 1 || got[0].ID != "qg" {
		t.Fatalf("ListByTransport(openai-compatible) IDs = %#v, want [qg]", descriptorIDs(got))
	}
	if descriptors := ListByTransport("missing"); len(descriptors) != 0 {
		t.Fatalf("ListByTransport(missing) returned %#v, want empty", descriptors)
	}
}

func TestReturnedDescriptorsAreCopies(t *testing.T) {
	descriptors := All()
	descriptors[0].ID = "changed"
	descriptors[0].AuthEnvVars[0] = "BROKEN"
	descriptors[0].SupportedAPIFormats[0] = "broken"

	descriptor, ok := Get("qg")
	if !ok {
		t.Fatal("Get(qg) returned false")
	}
	if descriptor.ID != "qg" {
		t.Fatalf("catalog entry mutated through All(): %#v", descriptor)
	}
	if descriptor.AuthEnvVars[0] != "QUEBRAGALHO_API_KEY" {
		t.Fatalf("descriptor auth env vars are shared, got %q", descriptor.AuthEnvVars[0])
	}
	if descriptor.SupportedAPIFormats[0] != APIFormatOpenAIChatCompletions {
		t.Fatalf("descriptor API formats are shared, got %q", descriptor.SupportedAPIFormats[0])
	}

	descriptor.AuthEnvVars[0] = "BROKEN-AGAIN"
	next, ok := Get("qg")
	if !ok {
		t.Fatal("Get(qg) returned false on second lookup")
	}
	if next.AuthEnvVars[0] != "QUEBRAGALHO_API_KEY" {
		t.Fatalf("descriptor slices are shared, got %q", next.AuthEnvVars[0])
	}
}

func TestRemoteProvidersDeclareAuthOrExplicitPublicAccess(t *testing.T) {
	for _, descriptor := range All() {
		if descriptor.Local {
			continue
		}
		if descriptor.RequiresAuth && (len(descriptor.AuthEnvVars) > 0 || descriptor.UsesAmbientAuth) {
			continue
		}
		if descriptor.Public && !descriptor.RequiresAuth {
			continue
		}
		t.Fatalf("%s is remote but declares neither credential env vars, ambient auth, nor public access", descriptor.ID)
	}
}

func descriptorIDs(descriptors []Descriptor) []string {
	ids := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		ids = append(ids, descriptor.ID)
	}
	return ids
}
