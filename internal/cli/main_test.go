package cli

import (
	"os"
	"testing"
)

// TestMain pins the credential backend to a plaintext file for every test in
// this package. Without this, any test that resolves the credential store
// without pinning QG_CRED_STORAGE itself falls back to keyring-first on
// darwin, which execs the real `security` binary and pops a Keychain dialog
// on the developer's screen. Per-test t.Setenv calls still override this for
// the tests that pin encrypted-file or bogus storage themselves.
func TestMain(m *testing.M) {
	if err := os.Setenv("QG_CRED_STORAGE", "file"); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}
