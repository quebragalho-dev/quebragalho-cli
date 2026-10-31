package providercatalog

import (
	"reflect"
	"testing"
)

func TestWithoutQGCredentialsDropsBothQGKeyVars(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"QG_API_KEY=qg-sk-primary",
		"QUEBRAGALHO_API_KEY=qg-sk-catalog",
		"qg_api_key=qg-sk-lowercase", // Windows env names are case-insensitive
		"QG_API_KEY_HINT=keep",       // a different variable, only a shared prefix
		"MALFORMED",
		"HOME=/home/u",
	}
	original := append([]string(nil), env...)

	got := WithoutQGCredentials(env)
	want := []string{"PATH=/usr/bin", "QG_API_KEY_HINT=keep", "MALFORMED", "HOME=/home/u"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("WithoutQGCredentials() = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(env, original) {
		t.Fatal("WithoutQGCredentials modified its input")
	}
}

func TestQGCredentialEnvVarsReturnsACopy(t *testing.T) {
	names := QGCredentialEnvVars()
	names[0] = "MUTATED"
	if QGCredentialEnvVars()[0] != "QG_API_KEY" {
		t.Fatal("QGCredentialEnvVars exposed its backing slice")
	}
}

// The catalog's own auth var must stay in the set: if the QG descriptor ever
// renames it, the scrub list has to follow.
func TestQGCredentialEnvVarsCoverCatalogAuthVars(t *testing.T) {
	descriptor, ok := Get(QGID)
	if !ok {
		t.Fatal("QG descriptor missing")
	}
	for _, name := range descriptor.AuthEnvVars {
		if !isQGCredentialEnvVar(name) {
			t.Fatalf("catalog auth var %s is not in the QG credential env set", name)
		}
	}
}
