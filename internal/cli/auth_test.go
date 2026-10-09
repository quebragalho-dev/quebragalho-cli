package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/oauth"
)

// withAuthStore points the provider OAuth store at a temp file for the test,
// pinning the file backend so an inherited QG_OAUTH_STORAGE=keyring can't
// ignore the temp path and hit the OS keychain.
func withAuthStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oauth-tokens.json")
	t.Setenv("QG_OAUTH_TOKENS_PATH", path)
	t.Setenv("QG_OAUTH_STORAGE", "file")
	return path
}

func TestRunAuthRejectsInvalidStorageMode(t *testing.T) {
	withAuthStore(t)
	// A mistyped value must fail fast, not silently fall back to plaintext while
	// the user believes encryption is active.
	t.Setenv("QG_OAUTH_STORAGE", "encryptd")
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "status"}, &stdout, &stderr, appDeps{}); code == exitSuccess {
		t.Fatalf("invalid QG_OAUTH_STORAGE should fail, got success; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "QG_OAUTH_STORAGE") {
		t.Fatalf("error should name the offending env var, stderr=%q", stderr.String())
	}
}

func TestRunAuthStatusEmpty(t *testing.T) {
	withAuthStore(t)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "status"}, &stdout, &stderr, appDeps{}); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No OAuth provider logins are stored.") {
		t.Fatalf("status output = %q", stdout.String())
	}
}

func TestRunAuthStatusReportsLoginWithoutSecret(t *testing.T) {
	path := withAuthStore(t)
	store, err := oauth.NewStore(oauth.StoreOptions{FilePath: path})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(oauth.ProviderKey("demo"), oauth.Token{
		AccessToken: "super-secret", RefreshToken: "super-secret-rt", Account: "me@example.com",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "status"}, &stdout, &stderr, appDeps{}); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "demo") || !strings.Contains(out, "me@example.com") {
		t.Fatalf("status should show provider + account: %q", out)
	}
	if strings.Contains(out, "super-secret") {
		t.Fatalf("status leaked token material: %q", out)
	}
}

func TestRunAuthLogoutNothing(t *testing.T) {
	withAuthStore(t)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "logout", "demo"}, &stdout, &stderr, appDeps{}); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No stored credential for demo") {
		t.Fatalf("logout output = %q", stdout.String())
	}
}

// TestRunAuthRemovedSubcommandsGuideToAPIKey locks the Etapa 4 auth contract:
// the OAuth login subcommands (login / openrouter / chatgpt) are removed, so
// every invocation reports the API-key guidance instead of parsing providers
// or starting a browser flow. The `--help` paths stay available on purpose —
// they are how a user landing here discovers what remains of `qg auth`.
func TestRunAuthRemovedSubcommandsGuideToAPIKey(t *testing.T) {
	withAuthStore(t)
	for _, args := range [][]string{
		{"auth", "login"},
		{"auth", "login", "demo"},
		{"auth", "login", "chatgpt", "--device"},
		{"auth", "openrouter", "--json"},
		{"auth", "chatgpt", "--extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runWithDeps(args, &stdout, &stderr, appDeps{}); code == exitSuccess {
			t.Errorf("%v should fail, got success", args)
		}
		if !strings.Contains(stderr.String(), "QUEBRAGALHO_API_KEY") {
			t.Errorf("%v stderr = %q, want QUEBRAGALHO_API_KEY guidance", args, stderr.String())
		}
	}
	for _, args := range [][]string{
		{"auth", "login", "--help"},
		{"auth", "openrouter", "--help"},
		{"auth", "chatgpt", "-h"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runWithDeps(args, &stdout, &stderr, appDeps{}); code != exitSuccess {
			t.Errorf("%v help should succeed, stderr=%q", args, stderr.String())
		}
		if !strings.Contains(stdout.String(), "qg auth") {
			t.Errorf("%v stdout = %q, want auth help", args, stdout.String())
		}
	}
}

func TestRunAuthRefreshNoToken(t *testing.T) {
	withAuthStore(t)
	t.Setenv("QG_OAUTH_DEMO_CLIENT_ID", "client") // so config resolves; refresh still fails (no token)
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "refresh", "demo"}, &stdout, &stderr, appDeps{}); code == exitSuccess {
		t.Fatal("refresh with no stored token should fail")
	}
}

func TestRunAuthRejectsWrongFlags(t *testing.T) {
	withAuthStore(t)
	cases := [][]string{
		{"auth", "status", "demo", "--device"},     // device is not a status flag
		{"auth", "logout", "demo", "--scope", "x"}, // scope is not a logout flag
		{"auth", "refresh", "demo", "--json"},      // json not for refresh
		{"auth", "status", "--confirm"},            // confirm is reset-only
		{"auth", "reset", "--confirm", "--watch"},  // confirmation does not allow unrelated flags
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := runWithDeps(args, &stdout, &stderr, appDeps{}); code == exitSuccess {
			t.Errorf("args %v should be rejected, got success", args)
		}
	}
}

func TestRunAuthHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "--help"}, &stdout, &stderr, appDeps{}); code != exitSuccess {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"qg auth", "logout", "status", "refresh", "reset --confirm", "QUEBRAGALHO_API_KEY"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q:\n%s", want, stdout.String())
		}
	}
	for _, gone := range []string{"openrouter", "--device"} {
		if strings.Contains(stdout.String(), gone) {
			t.Fatalf("help still documents removed %q:\n%s", gone, stdout.String())
		}
	}
}

func TestRunAuthResetRequiresConfirmation(t *testing.T) {
	for _, args := range [][]string{{}, {"--json"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := withAuthStore(t)
			store, err := oauth.NewStore(oauth.StoreOptions{FilePath: path})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Save(oauth.ProviderKey("demo"), oauth.Token{AccessToken: "synthetic-reset-test-token"}); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			code := runAuthReset(args, &stdout, &stderr, appDeps{})
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatalf("unconfirmed reset changed the store: %v", readErr)
			}
			if code == exitSuccess || !strings.Contains(stderr.String(), "--confirm") || stdout.Len() != 0 {
				t.Fatalf("unconfirmed reset: exit=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunAuthResetJSON(t *testing.T) {
	path := withAuthStore(t)
	store, err := oauth.NewStore(oauth.StoreOptions{FilePath: path})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(oauth.ProviderKey("demo"), oauth.Token{AccessToken: "synthetic-reset-test-token"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "reset", "--confirm", "--json"}, &stdout, &stderr, appDeps{}); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	var payload struct {
		Reset bool `json:"reset"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode JSON: %v (stdout=%q)", err, stdout.String())
	}
	if !payload.Reset {
		t.Fatalf("payload = %+v, want reset=true", payload)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runWithDeps([]string{"auth", "status"}, &stdout, &stderr, appDeps{}); code != exitSuccess {
		t.Fatalf("status exit = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No OAuth provider logins are stored.") {
		t.Fatalf("expected empty store after JSON reset, got: %q", stdout.String())
	}
}

func TestRunAuthReset(t *testing.T) {
	path := withAuthStore(t)
	store, err := oauth.NewStore(oauth.StoreOptions{FilePath: path})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(oauth.ProviderKey("demo"), oauth.Token{AccessToken: "secret"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"auth", "reset", "--confirm"}, &stdout, &stderr, appDeps{}); code != exitSuccess {
		t.Fatalf("exit = %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Reset OAuth token store") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	// Verify store is now empty.
	stdout.Reset()
	stderr.Reset()
	if code := runWithDeps([]string{"auth", "status"}, &stdout, &stderr, appDeps{}); code != exitSuccess {
		t.Fatalf("status exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "No OAuth provider logins are stored.") {
		t.Fatalf("expected empty store after reset, got: %q", stdout.String())
	}
}
