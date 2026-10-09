package cli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/oauth"
)

func TestSetupRequiredRecognizesOAuthLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tok.json")
	t.Setenv("QG_OAUTH_STORAGE", "file") // an inherited "keyring" would ignore the temp path and hit the OS keychain
	t.Setenv("QG_OAUTH_TOKENS_PATH", path)
	store, err := oauth.NewStore(oauth.StoreOptions{FilePath: path})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(oauth.ProviderKey("qg"), oauth.Token{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	// qg has no inline key (env-only) but IS logged in via OAuth → no setup.
	loggedIn := config.ResolvedConfig{Provider: config.ProviderProfile{Name: "qg", CatalogID: "qg", APIKeyEnv: "QUEBRAGALHO_API_KEY"}}
	if setupRequired(loggedIn) {
		t.Fatal("a provider with a stored OAuth login must not require onboarding")
	}

	// Drop the login: the same keyless profile with NO stored token still
	// requires setup.
	if _, err := store.Delete(oauth.ProviderKey("qg")); err != nil {
		t.Fatalf("delete token: %v", err)
	}
	noLogin := config.ResolvedConfig{Provider: config.ProviderProfile{Name: "qg", CatalogID: "qg", APIKeyEnv: "QUEBRAGALHO_API_KEY"}}
	if !setupRequired(noLogin) {
		t.Fatal("a keyless provider with no OAuth login must require onboarding")
	}
}
