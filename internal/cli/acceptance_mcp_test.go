package cli

// MCP acceptance tests at the CLI boundary. Real default deps are used
// wherever possible; only the working directory is injected. No test reaches a
// non-loopback address: the built-in exa default is disabled in the hermetic
// user config.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
	"github.com/quebragalho-dev/quebragalho-cli/internal/workspacetrust"
)

func acceptanceHermeticEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"xdg-config", "xdg-data", "xdg-state", "xdg-cache", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", root)
	// Windows resolves the user config and state under APPDATA/LOCALAPPDATA
	// (os.UserConfigDir, mcp/userStateDir), ignoring HOME and XDG_*, so those
	// point at the fixture root too; on other platforms they are inert.
	t.Setenv("USERPROFILE", root)
	t.Setenv("APPDATA", root)
	t.Setenv("LOCALAPPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "xdg-state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg-cache"))
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	t.Setenv("QG_CRED_STORAGE", "file")
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true}}}}`)
	return root
}

func mustUserConfigPath(t *testing.T) string {
	t.Helper()
	path, err := config.DefaultUserConfigPath()
	if err != nil {
		t.Fatalf("DefaultUserConfigPath(): %v", err)
	}
	return path
}

func writeAcceptanceJSON(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// acceptanceRepo creates a workspace with a project .qg/config.json.
func acceptanceRepo(t *testing.T, mcpServers string, trusted bool) string {
	t.Helper()
	repo := t.TempDir()
	writeAcceptanceJSON(t, filepath.Join(repo, ".qg", "config.json"), `{"mcp":{"servers":`+mcpServers+`}}`)
	if trusted {
		if err := workspacetrust.Trust(repo); err != nil {
			t.Fatalf("Trust(repo): %v", err)
		}
	}
	return repo
}

func acceptanceRunCLI(t *testing.T, cwd string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	deps := appDeps{getwd: func() (string, error) { return cwd, nil }}
	code := runWithDeps(args, &stdout, &stderr, deps)
	return stdout.String(), stderr.String(), code
}

// acceptanceStdioServerJSON is a project/user stdio server entry that runs
// this test binary as a minimal MCP server and records each start in marker.
func acceptanceStdioServerJSON(t *testing.T, marker string, extraEnv string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := `"QG_ACCEPT_CLI_HELPER":"` + marker + `"`
	if extraEnv != "" {
		env += "," + extraEnv
	}
	return `{"type":"stdio","command":"` + binary + `","args":["-test.run=^TestAcceptanceCLIStdioHelperProcess$"],"env":{` + env + `}}`
}

func acceptanceSpawned(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

// TestAcceptanceCLIStdioHelperProcess is not a test: it is a minimal stdio MCP
// server (one tool "lookup") that appends its env value ACCEPT_TOKEN to the
// marker file on start. It returns at once unless QG_ACCEPT_CLI_HELPER is set.
func TestAcceptanceCLIStdioHelperProcess(t *testing.T) {
	marker := os.Getenv("QG_ACCEPT_CLI_HELPER")
	if marker == "" {
		return
	}
	file, err := os.OpenFile(marker, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, _ = file.WriteString("token=" + os.Getenv("ACCEPT_TOKEN") + "\n")
		_ = file.Close()
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == nil {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "accept-cli", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "lookup", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}}
		}
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
		_, _ = os.Stdout.Write(append(body, '\n'))
	}
	os.Exit(0)
}

func newAcceptanceCLIHTTPServer(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		var message struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&message)
		if message.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		result := map[string]any{}
		switch message.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{"tools": map[string]any{}}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{{"name": "lookup", "inputSchema": map[string]any{"type": "object"}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// ---------------------------------------------------------------------------
// `qg mcp list` shows each server's live state, and a failure always carries
// its reason.
// ---------------------------------------------------------------------------

func TestAcceptanceMCPListShowsLiveStateAndFailureReason(t *testing.T) {
	acceptanceHermeticEnv(t)
	healthy, _ := newAcceptanceCLIHTTPServer(t, http.StatusOK)
	broken, _ := newAcceptanceCLIHTTPServer(t, http.StatusServiceUnavailable)
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true},`+
		`"healthy":{"type":"http","url":"`+healthy.URL+`"},`+
		`"broken":{"type":"http","url":"`+broken.URL+`"}}}}`)

	stdout, stderr, code := acceptanceRunCLI(t, t.TempDir(), "mcp", "list")
	output := stdout + stderr
	if code != exitSuccess {
		t.Fatalf("mcp list exit = %d: %s", code, output)
	}
	if !strings.Contains(output, "503") {
		t.Fatalf("mcp list does not show why broken failed (HTTP 503):\n%s", output)
	}
	if !containsAnyFoldCLI(output, "falhou", "failed", "✘") {
		t.Fatalf("mcp list does not mark broken as failed:\n%s", output)
	}
	if !containsAnyFoldCLI(output, "conectado", "connected", "✔") {
		t.Fatalf("mcp list does not mark healthy as connected:\n%s", output)
	}
}

// `qg mcp list` loads the project layer even in an untrusted workspace and
// connects to servers to show their state, so it must never connect to or
// spawn an untrusted project's servers, and never print a ${VAR} expanded from
// the qg environment.
func TestAcceptanceMCPListNeverConnectsUntrustedProjectServers(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("QG_ACCEPT_SECRET", "ambient-secret-value")
	remote, hits := newAcceptanceCLIHTTPServer(t, http.StatusOK)
	marker := filepath.Join(t.TempDir(), "spawned")
	repo := acceptanceRepo(t, `{`+
		`"projhttp":{"type":"http","url":"`+remote.URL+`/mcp?k=${QG_ACCEPT_SECRET}","headers":{"X-K":"${QG_ACCEPT_SECRET}"}},`+
		`"projstdio":`+acceptanceStdioServerJSON(t, marker, `"ACCEPT_TOKEN":"${QG_ACCEPT_SECRET}"`)+`}`, false)

	for _, args := range [][]string{{"mcp", "list"}, {"mcp", "list", "--json"}} {
		stdout, stderr, _ := acceptanceRunCLI(t, repo, args...)
		if strings.Contains(stdout+stderr, "ambient-secret-value") {
			t.Fatalf("%v printed a secret expanded from the qg environment:\n%s%s", args, stdout, stderr)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if got := hits.Load(); got != 0 {
		t.Fatalf("mcp list contacted an untrusted project's server %d times", got)
	}
	if acceptanceSpawned(marker) {
		t.Fatal("mcp list spawned an untrusted project's stdio server")
	}
}

// AGENTS.md (redact success and error paths): when a user server's URL
// carries a secret through ${VAR} and the connection fails, neither `qg mcp
// check` nor `qg mcp list` may print the expanded value (net/http dial errors
// echo the URL).
func TestAcceptanceMCPExpandedSecretNeverPrintedInFailureReason(t *testing.T) {
	acceptanceHermeticEnv(t)
	t.Setenv("QG_ACCEPT_SECRET", "ambient-secret-value")
	closed := httptest.NewServer(http.NotFoundHandler())
	deadURL := closed.URL
	closed.Close()
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true},`+
		`"secretive":{"type":"http","url":"`+deadURL+`/mcp?k=${QG_ACCEPT_SECRET}"}}}}`)

	for _, args := range [][]string{{"mcp", "check", "secretive"}, {"mcp", "check", "secretive", "--json"}, {"mcp", "list"}, {"mcp", "list", "--json"}} {
		stdout, stderr, _ := acceptanceRunCLI(t, t.TempDir(), args...)
		if strings.Contains(stdout+stderr, "ambient-secret-value") {
			t.Fatalf("%v printed the expanded secret:\n%s%s", args, stdout, stderr)
		}
	}
}

// ---------------------------------------------------------------------------
// `qg mcp check` warns about a ${VAR} that is not set.
// ---------------------------------------------------------------------------

func TestAcceptanceMCPCheckWarnsOnMissingEnvVar(t *testing.T) {
	acceptanceHermeticEnv(t)
	_ = os.Unsetenv("QG_ACCEPT_ABSENT")
	marker := filepath.Join(t.TempDir(), "spawned")
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true},`+
		`"needs":`+acceptanceStdioServerJSON(t, marker, `"ACCEPT_TOKEN":"${QG_ACCEPT_ABSENT}"`)+`}}}`)

	stdout, stderr, _ := acceptanceRunCLI(t, t.TempDir(), "mcp", "check", "needs")
	if !strings.Contains(stdout+stderr, "QG_ACCEPT_ABSENT") {
		t.Fatalf("mcp check did not warn about the unset ${QG_ACCEPT_ABSENT}:\nstdout=%s\nstderr=%s", stdout, stderr)
	}
}

// ---------------------------------------------------------------------------
// A project server needs a one-time user approval before qg runs it, even in a
// trusted workspace, at every spawn site. The interactive start (app.go) and
// `mcp check` resolve and register MCP on their own paths, so each site is
// exercised here: workspace registration (exec/acp), the interactive start,
// `mcp check`, `mcp tools list` and `mcp list`.
// ---------------------------------------------------------------------------

// acceptanceStartInteractiveSession runs the real interactive startup up to
// the point where the TUI would take over, and returns the registry the TUI
// would receive. The provider is never contacted: runTUI is replaced.
func acceptanceStartInteractiveSession(t *testing.T, repo string) *tools.Registry {
	t.Helper()
	var registry *tools.Registry
	code := runWithDeps([]string{}, io.Discard, io.Discard, appDeps{
		getwd: func() (string, error) { return repo, nil },
		resolveConfig: func(string, config.Overrides) (config.ResolvedConfig, error) {
			return config.ResolvedConfig{Provider: config.ProviderProfile{
				Name:         "accept",
				ProviderKind: config.ProviderKindOpenAICompatible,
				BaseURL:      "http://127.0.0.1:9/v1",
				APIKey:       "sk-accept",
				Model:        "m",
			}}, nil
		},
		runTUI: func(_ context.Context, options tui.Options) int {
			registry = options.Registry
			return exitSuccess
		},
	})
	if code != exitSuccess || registry == nil {
		t.Fatalf("interactive start exit = %d, registry = %v", code, registry)
	}
	return registry
}

func acceptanceRegisterWorkspace(t *testing.T, repo string) *tools.Registry {
	t.Helper()
	registry := tools.NewRegistry()
	runtime, _, err := registerMCPToolsForWorkspace(context.Background(), repo, registry, defaultAppDeps(), mcp.AutonomyLow, repo)
	if err != nil {
		t.Fatalf("registerMCPToolsForWorkspace: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return registry
}

func TestAcceptanceMCPUnapprovedProjectServerIsNeverSpawned(t *testing.T) {
	sites := []struct {
		name string
		run  func(t *testing.T, repo string) *tools.Registry
	}{
		{"workspace registration (exec, acp)", acceptanceRegisterWorkspace},
		{"interactive start", acceptanceStartInteractiveSession},
		{"qg mcp check proj", func(t *testing.T, repo string) *tools.Registry {
			acceptanceRunCLI(t, repo, "mcp", "check", "proj")
			return nil
		}},
		{"qg mcp tools list", func(t *testing.T, repo string) *tools.Registry {
			acceptanceRunCLI(t, repo, "mcp", "tools", "list")
			return nil
		}},
		{"qg mcp list", func(t *testing.T, repo string) *tools.Registry {
			acceptanceRunCLI(t, repo, "mcp", "list")
			return nil
		}},
		{"qg mcp list --json", func(t *testing.T, repo string) *tools.Registry {
			acceptanceRunCLI(t, repo, "mcp", "list", "--json")
			return nil
		}},
	}
	for _, site := range sites {
		t.Run(site.name, func(t *testing.T) {
			acceptanceHermeticEnv(t)
			marker := filepath.Join(t.TempDir(), "spawned")
			repo := acceptanceRepo(t, `{"proj":`+acceptanceStdioServerJSON(t, marker, "")+`}`, true)

			registry := site.run(t, repo)
			if acceptanceSpawned(marker) {
				t.Fatalf("%s spawned a project MCP server the user never approved (workspace is trusted)", site.name)
			}
			if registry != nil {
				if _, ok := registry.Get("mcp_proj_lookup"); ok {
					t.Fatalf("%s put an unapproved project server's tools in the model catalog", site.name)
				}
			}
		})
	}
}

func TestAcceptanceMCPListShowsProjectServerPendingApproval(t *testing.T) {
	acceptanceHermeticEnv(t)
	marker := filepath.Join(t.TempDir(), "spawned")
	repo := acceptanceRepo(t, `{"proj":`+acceptanceStdioServerJSON(t, marker, "")+`}`, true)

	stdout, stderr, _ := acceptanceRunCLI(t, repo, "mcp", "list")
	if !containsAnyFoldCLI(stdout+stderr, "aprova", "approv") {
		t.Fatalf("mcp list does not say the project server awaits approval:\n%s%s", stdout, stderr)
	}
}

func containsAnyFoldCLI(haystack string, needles ...string) bool {
	lower := strings.ToLower(haystack)
	for _, needle := range needles {
		if strings.Contains(lower, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
