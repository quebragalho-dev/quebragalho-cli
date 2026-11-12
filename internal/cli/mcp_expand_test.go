package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// `mcp check` warns which variable is missing instead of failing on an
// emptied URL.
func TestRunMCPCheckWarnsMissingEnv(t *testing.T) {
	os.Unsetenv("QG_TEST_EXPAND_DEFINITELY_MISSING")
	cwd := t.TempDir()
	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"mcp", "check", "srv"}, &stdout, &stderr, appDeps{
		getwd: func() (string, error) { return cwd, nil },
		resolveMCPConfig: func(workspaceRoot string, _ bool) (config.MCPConfig, error) {
			return config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"srv": {Type: "http", URL: "https://${QG_TEST_EXPAND_DEFINITELY_MISSING}/mcp"},
			}}, nil
		},
	})
	if exitCode != exitCrash {
		t.Fatalf("exitCode = %d, want crash with missing-var report", exitCode)
	}
	if !strings.Contains(stderr.String(), "QG_TEST_EXPAND_DEFINITELY_MISSING") {
		t.Fatalf("stderr does not name the missing variable:\n%s", stderr.String())
	}
}

// Servers render ${VAR}/${VAR:-default} from the process env before
// registration; sibling config values never leak into the environment.
// (Explicit commands like `qg mcp check` warn about the missing names.)
func TestRegisterExpandsMissingEnvToEmpty(t *testing.T) {
	os.Unsetenv("QG_TEST_EXPAND_DEFINITELY_MISSING")
	cwd := t.TempDir()
	registered := config.MCPConfig{}
	runtime, _, err := registerMCPToolsForWorkspace(context.Background(), t.TempDir(), tools.NewRegistry(), appDeps{
		userConfigPath: func() (string, error) {
			return filepath.Join(t.TempDir(), "qg", "config.json"), nil
		},
		resolveMCPConfig: func(workspaceRoot string, _ bool) (config.MCPConfig, error) {
			return config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"r": {Type: "http", URL: "http://127.0.0.1:1/mcp", Headers: map[string]string{"X-K": "${QG_TEST_EXPAND_DEFINITELY_MISSING:-dflt}"}},
			}}, nil
		},
		newMCPStore: func() (*mcp.PermissionStore, error) { return nil, nil },
		registerMCPTools: func(ctx context.Context, registry *tools.Registry, cfg config.MCPConfig, options mcp.RegisterOptions) (mcpToolRuntime, error) {
			registered = cfg
			return noopMCPRuntime{}, nil
		},
	}, mcp.AutonomyLow, cwd)
	if err != nil {
		t.Fatalf("registerMCPToolsForWorkspace() error = %v", err)
	}
	if _, ok := registered.Servers["r"]; !ok {
		t.Fatal("server with missing env (and default) was dropped from registration")
	}
	if len(runtime.Skipped()) != 0 {
		t.Fatalf("Skipped() = %+v, want none", runtime.Skipped())
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
