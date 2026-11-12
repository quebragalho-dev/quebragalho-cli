package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

type slAcceptProvider struct{}

func (slAcceptProvider) StreamCompletion(context.Context, qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	ch := make(chan qgruntime.StreamEvent)
	close(ch)
	return ch, nil
}

func slAcceptWriteJSON(t *testing.T, path string, doc any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The real launch path (config discovery, Resolve, TUI options) drops a
// project statusline command, keeps the project layout, keeps the user's own
// command, and hands the TUI a warning to show (stderr before the alt screen
// is never seen).
func TestStatusLineAcceptance_LaunchDropsProjectCommandAndWarnsInTUI(t *testing.T) {
	for _, tc := range []struct {
		name        string
		userCommand string
	}{
		{name: "project command only"},
		{name: "user command kept", userCommand: "sh /home/u/.config/qg/statusline/meu.sh"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(root, "home")
			configHome := filepath.Join(root, "config")
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", configHome)
			t.Setenv("APPDATA", configHome)
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
			t.Setenv("LOCALAPPDATA", filepath.Join(root, "cache"))
			t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
			t.Setenv("QG_CRED_STORAGE", "file")
			t.Setenv("QG_PROVIDER_COMMAND", "")
			t.Setenv("QG_API_KEY", "")
			t.Setenv("ACCEPT_GATEWAY_KEY", "accept-gateway-key")

			userConfigPath, err := config.DefaultUserConfigPath()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(userConfigPath, root) {
				t.Fatalf("hermeticity: user config %q escaped %q", userConfigPath, root)
			}
			user := map[string]any{
				"activeProvider": "accept-gw",
				"providers": []map[string]any{{
					"name": "accept-gw", "providerKind": "openai-compatible",
					"baseURL": "http://127.0.0.1:9/v1", "model": "accept-model",
					"apiKeyEnv": "ACCEPT_GATEWAY_KEY",
				}},
			}
			if tc.userCommand != "" {
				user["statusline"] = map[string]any{"command": map[string]any{"command": tc.userCommand}}
			}
			slAcceptWriteJSON(t, userConfigPath, user)

			workspace := filepath.Join(home, "workspace")
			pwned := filepath.Join(root, "PWNED")
			slAcceptWriteJSON(t, filepath.Join(workspace, ".qg", "config.json"), map[string]any{
				"statusline": map[string]any{
					"lines":   []map[string]any{{"left": []string{"branch", "mode"}}},
					"command": map[string]any{"command": "touch '" + pwned + "'"},
				},
			})

			var stdout, stderr bytes.Buffer
			var launched *tui.Options
			exit := runWithDeps([]string{}, &stdout, &stderr, appDeps{
				getwd: func() (string, error) { return workspace, nil },
				newProvider: func(config.ProviderProfile) (qgruntime.Provider, error) {
					return slAcceptProvider{}, nil
				},
				registerMCPTools: func(context.Context, *tools.Registry, config.MCPConfig, mcp.RegisterOptions) (mcpToolRuntime, error) {
					return noopMCPRuntime{}, nil
				},
				runTUI: func(_ context.Context, options tui.Options) int {
					launched = &options
					return 0
				},
			})
			if launched == nil {
				t.Fatalf("TUI never launched (exit %d): stderr=%q", exit, stderr.String())
			}
			got := launched.StatusLine
			if tc.userCommand == "" && got.Command != nil {
				t.Errorf("project statusline command reached the TUI: %q", got.Command.Command)
			}
			if tc.userCommand != "" && (got.Command == nil || got.Command.Command != tc.userCommand) {
				t.Errorf("the user's own command must survive; got %+v", got.Command)
			}
			if len(got.Lines) != 1 || strings.Join(got.Lines[0].Left, ",") != "branch,mode" {
				t.Errorf("the project's layout is harmless and must apply; got %+v", got.Lines)
			}
			if !strings.Contains(strings.ToLower(launched.StatusLineWarning), "statusline") {
				t.Errorf("ignoring the project command must hand the TUI a statusline warning; got %q (stderr=%q)", launched.StatusLineWarning, stderr.String())
			}
			if _, err := os.Stat(pwned); err == nil {
				t.Errorf("project statusline command ran during launch")
			}
		})
	}
}
