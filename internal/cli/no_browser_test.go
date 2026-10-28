package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

func launchedSetupOptions(t *testing.T, env string, args ...string) tui.SetupOptions {
	t.Helper()
	setCLIUserConfigRoot(t)
	t.Setenv("QG_NO_BROWSER", env)
	cwd := t.TempDir()
	userConfigPath := filepath.Join(t.TempDir(), "qg", "config.json")
	var launched tui.Options
	var stdout, stderr bytes.Buffer
	code := runWithDeps(args, &stdout, &stderr, appDeps{
		getwd: func() (string, error) { return cwd, nil },
		resolveConfig: func(string, config.Overrides) (config.ResolvedConfig, error) {
			return config.ResolvedConfig{MaxTurns: 12}, nil
		},
		userConfigPath: func() (string, error) { return userConfigPath, nil },
		runTUI: func(_ context.Context, options tui.Options) int {
			launched = options
			return 0
		},
	})
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	return launched.Setup
}

func TestTUILaunchNoBrowser(t *testing.T) {
	cases := []struct {
		name     string
		env      string
		args     []string
		wantOpen bool
	}{
		{name: "default opens the browser", wantOpen: true},
		{name: "flag", args: []string{"--no-browser"}},
		{name: "env", env: "1"},
		{name: "flag with unsafe launch", args: []string{"--dangerously-skip-permissions", "--no-browser"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup := launchedSetupOptions(t, tc.env, tc.args...)
			if (setup.OpenBrowser != nil) != tc.wantOpen {
				t.Fatalf("OpenBrowser set = %v, want %v", setup.OpenBrowser != nil, tc.wantOpen)
			}
			if setup.DeviceStart == nil {
				t.Fatal("device login must stay wired so the URL and code still show")
			}
		})
	}
}

func TestNoBrowserFlagRejectedOutsideInteractiveLaunch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runWithDeps([]string{"--no-browser", "doctor"}, &stdout, &stderr, appDeps{}); code == 0 {
		t.Fatal("--no-browser before a subcommand must be rejected")
	}
	if !strings.Contains(stderr.String(), "--no-browser") {
		t.Fatalf("stderr = %q, want a --no-browser error", stderr.String())
	}
}
