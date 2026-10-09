package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseExecArgsAPIKey(t *testing.T) {
	opts, _, err := parseExecArgs([]string{"--api-key", "sk-flag", "hello"})
	if err != nil {
		t.Fatalf("parse --api-key: %v", err)
	}
	if opts.apiKey != "sk-flag" {
		t.Fatalf("apiKey = %q, want sk-flag", opts.apiKey)
	}

	opts, _, err = parseExecArgs([]string{"--api-key=sk-inline", "hello"})
	if err != nil {
		t.Fatalf("parse --api-key=: %v", err)
	}
	if opts.apiKey != "sk-inline" {
		t.Fatalf("apiKey = %q, want sk-inline", opts.apiKey)
	}

	if _, _, err := parseExecArgs([]string{"hello", "--api-key"}); err == nil || !strings.Contains(err.Error(), "--api-key requires a value") {
		t.Fatalf("missing --api-key value error = %v, want requires-a-value", err)
	}
}

func TestWriteExecHelpMentionsAPIKey(t *testing.T) {
	var help bytes.Buffer
	if err := writeExecHelp(&help); err != nil {
		t.Fatalf("writeExecHelp() error = %v", err)
	}
	if !strings.Contains(help.String(), "--api-key") {
		t.Fatalf("exec help must document --api-key, got:\n%s", help.String())
	}
}

// The --api-key flag outranks every stored source, including an inline key in
// the project config.
func TestRunExecAPIKeyFlagBeatsConfigKey(t *testing.T) {
	clearProviderEnv(t)
	root := t.TempDir()
	configDir := filepath.Join(root, ".qg")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	writeConfig := `{
		"activeProvider": "local",
		"providers": [{
			"name": "local",
			"provider_kind": "openai-compatible",
			"base_url": "` + server.URL + `",
			"api_key": "sk-local",
			"model": "local-model"
		}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(writeConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"exec", "--cwd", root, "--api-key", "sk-flag", "hello"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit = %d, want 0: %s", exitCode, stderr.String())
	}
	if gotAuth != "Bearer sk-flag" {
		t.Fatalf("Authorization = %q, want --api-key to outrank the config key", gotAuth)
	}
}

// With no profile configured at all, --api-key must NOT synthesize a provider
// (an unnamed merge would default to the official OpenAI endpoint and ship the
// key there); the run stops on the existing no-provider guidance instead.
func TestRunExecAPIKeyFlagWithoutProfileReportsNoProvider(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"exec", "--cwd", root, "--api-key", "sk-flag", "hello"}, &stdout, &stderr)
	if exitCode == 0 {
		t.Fatalf("exit = 0, want a provider error; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if !strings.Contains(combined, "No provider configured") {
		t.Fatalf("output = %q, want the no-provider guidance", combined)
	}
	if strings.Contains(combined, "sk-flag") {
		t.Fatalf("output leaked the --api-key value: %q", combined)
	}
}

// Guard against regressions in exec wiring: the parse result must reach the
// resolved provider profile.
func TestExecAPIKeyFlagReachesResolvedProvider(t *testing.T) {
	clearProviderEnv(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	var jsonBody map[string]any
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&jsonBody)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	// A profile exists via the user config; only the KEY comes from the flag.
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	qgDir := filepath.Join(configHome, "qg")
	if err := os.MkdirAll(qgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	userConfig := `{
		"activeProvider": "local",
		"providers": [{
			"name": "local",
			"provider_kind": "openai-compatible",
			"base_url": "` + server.URL + `",
			"api_key_env": "OPENAI_API_KEY",
			"model": "local-model"
		}]
	}`
	if err := os.WriteFile(filepath.Join(qgDir, "config.json"), []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := Run([]string{"exec", "--cwd", t.TempDir(), "--api-key", "sk-flag-only", "hello"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("exit = %d, want 0: %s", exitCode, stderr.String())
	}
	if gotAuth != "Bearer sk-flag-only" {
		t.Fatalf("Authorization = %q, want flag key on the request", gotAuth)
	}
}
