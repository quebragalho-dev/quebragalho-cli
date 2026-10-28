package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

func TestKeyHelpDocumentsPrecedence(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"key", "--help"}, &stdout, &stderr, appDeps{}); code != exitSuccess {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	out := stdout.String()
	inline, env, saved := strings.Index(out, "inline"), strings.Index(out, "QG_API_KEY"), strings.LastIndex(out, "saved key")
	if inline < 0 || env < 0 || saved < 0 || !(inline < env && env < saved) {
		t.Fatalf("key help must order inline > env > saved key (idx %d, %d, %d):\n%s", inline, env, saved, out)
	}
}

// The runtime key is inline > environment > saved key: each source wins only
// once the one above it is gone.
func TestQGKeyPrecedenceInlineThenEnvThenSaved(t *testing.T) {
	withAuthStore(t)
	t.Setenv("QG_CRED_STORAGE", "encrypted-file")
	configPath := filepath.Join(t.TempDir(), "qg", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("qg", "sk-vault"); err != nil {
		t.Fatal(err)
	}
	userConfigPath := func() (string, error) { return configPath, nil }

	runtimeKey := func(profileJSON string, env map[string]string) string {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(`{"providers":[`+profileJSON+`]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		resolved, err := config.Resolve(config.ResolveOptions{UserConfigPath: configPath, Env: env})
		if err != nil {
			t.Fatal(err)
		}
		return applyStoredProviderKeyAt(resolved.Provider, userConfigPath).APIKey
	}

	withInline := `{"name":"qg","catalogId":"qg","apiKey":"sk-inline","apiKeyStored":true}`
	stored := `{"name":"qg","catalogId":"qg","apiKeyStored":true}`
	steps := []struct {
		name    string
		profile string
		env     map[string]string
		want    string
	}{
		{"inline beats env and vault", withInline, map[string]string{"QG_API_KEY": "sk-env"}, "sk-inline"},
		{"env beats vault once inline is gone", stored, map[string]string{"QG_API_KEY": "sk-env"}, "sk-env"},
		{"vault when nothing else is set", stored, map[string]string{}, "sk-vault"},
	}
	for _, step := range steps {
		if got := runtimeKey(step.profile, step.env); got != step.want {
			t.Errorf("%s: key = %q, want %q", step.name, got, step.want)
		}
	}
}
