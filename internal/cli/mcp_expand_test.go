package cli

import (
	"bytes"
	"context"
	"os"
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

// Servers with undefined variables are dropped before registration and
// reported as skipped with the reason; healthy servers still register.
func TestRegisterDropsServersWithMissingEnv(t *testing.T) {
	os.Unsetenv("QG_TEST_EXPAND_DEFINITELY_MISSING")
	cwd := t.TempDir()
	registered := config.MCPConfig{}
	var stdout, stderr bytes.Buffer
	_ = stdout
	_ = stderr
	runtime, _, err := registerMCPToolsForWorkspace(context.Background(), t.TempDir(), tools.NewRegistry(), appDeps{
		resolveMCPConfig: func(workspaceRoot string, _ bool) (config.MCPConfig, error) {
			return config.MCPConfig{Servers: map[string]config.MCPServerConfig{
				"bad": {Type: "http", URL: "https://${QG_TEST_EXPAND_DEFINITELY_MISSING}/mcp"},
				"ok":  {Type: "http", URL: "http://127.0.0.1:1/mcp"},
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
	if _, ok := registered.Servers["bad"]; ok {
		t.Fatal("server with missing env was passed to registration")
	}
	if _, ok := registered.Servers["ok"]; !ok {
		t.Fatal("healthy server was dropped from registration")
	}
	found := false
	for _, skipped := range runtime.Skipped() {
		if skipped.Name == "bad" && strings.Contains(skipped.Err.Error(), "QG_TEST_EXPAND_DEFINITELY_MISSING") {
			found = true
		}
	}
	if !found {
		t.Fatalf("skipped = %+v, want bad with missing-var reason", runtime.Skipped())
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
