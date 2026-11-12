package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

type mcpApproveFakeClient struct {
	calls int
}

func (mcpApproveFakeClient) ListTools(context.Context) ([]mcp.RemoteTool, error) {
	return []mcp.RemoteTool{{Name: "lookup"}}, nil
}

func (client *mcpApproveFakeClient) CallTool(context.Context, string, map[string]any) (mcp.CallToolResult, error) {
	client.calls++
	return mcp.CallToolResult{}, nil
}

func (mcpApproveFakeClient) Close() error { return nil }

func approveTestDeps(cwd string, servers map[string]config.MCPServerConfig, spawned *bool) appDeps {
	return appDeps{
		getwd: func() (string, error) { return cwd, nil },
		resolveMCPConfig: func(workspaceRoot string, _ bool) (config.MCPConfig, error) {
			return config.MCPConfig{Servers: servers}, nil
		},
		newMCPStore: func() (*mcp.PermissionStore, error) { return nil, nil },
		// Real RegisterTools with a fake transport: the factory only fires
		// when a server is actually connected, so `spawned` proves the gate
		// let the server through. No subprocess ever starts.
		registerMCPTools: func(_ context.Context, registry *tools.Registry, cfg config.MCPConfig, options mcp.RegisterOptions) (mcpToolRuntime, error) {
			options.ClientFactory = func(context.Context, mcp.Server) (mcp.ToolClient, error) {
				*spawned = true
				return &mcpApproveFakeClient{}, nil
			}
			return mcp.RegisterTools(context.Background(), registry, cfg, options)
		},
	}
}

// `mcp approve` records the approval; a second registration then runs the
// project server instead of skipping it.
func TestRunMCPApproveGrantsProjectServer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cwd := t.TempDir()
	servers := map[string]config.MCPServerConfig{
		"proj": {Type: "stdio", Command: "mini", ProjectConfigured: true},
	}

	var stdout, stderr bytes.Buffer
	if exitCode := runWithDeps([]string{"mcp", "approve", "--yes", "proj"}, &stdout, &stderr, approveTestDeps(cwd, servers, new(bool))); exitCode != exitSuccess {
		t.Fatalf("exitCode = %d stderr=%s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "aprovado") {
		t.Fatalf("stdout = %q, want approval confirmation", stdout.String())
	}

	var spawned bool
	runtime, _, err := registerMCPToolsForWorkspace(context.Background(), cwd, tools.NewRegistry(), approveTestDeps(cwd, servers, &spawned), mcp.AutonomyLow, cwd)
	if err != nil {
		t.Fatalf("registerMCPToolsForWorkspace() error = %v", err)
	}
	defer func() { _ = runtime.Close() }()
	if !spawned {
		t.Fatal("approved project server was not connected")
	}
	if len(runtime.Skipped()) != 0 {
		t.Fatalf("Skipped() = %+v, want none after approval", runtime.Skipped())
	}
}

// Without approval, the project server never connects: it is skipped with
// the approve command as the reason.
func TestRegisterSkipsUnapprovedProjectServer(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cwd := t.TempDir()
	var spawned bool
	runtime, _, err := registerMCPToolsForWorkspace(context.Background(), cwd, tools.NewRegistry(), approveTestDeps(cwd, map[string]config.MCPServerConfig{
		"proj": {Type: "stdio", Command: "mini", ProjectConfigured: true},
		"mine": {Type: "http", URL: "http://127.0.0.1:1/mcp"},
	}, &spawned), mcp.AutonomyLow, cwd)
	if err != nil {
		t.Fatalf("registerMCPToolsForWorkspace() error = %v", err)
	}
	defer func() { _ = runtime.Close() }()
	found := false
	for _, skipped := range runtime.Skipped() {
		if skipped.Name == "proj" {
			found = true
			if !strings.Contains(skipped.Err.Error(), "qg mcp approve proj") {
				t.Fatalf("reason = %q, want the approve command", skipped.Err.Error())
			}
		}
	}
	if !found {
		t.Fatalf("Skipped() = %+v, want proj pending approval", runtime.Skipped())
	}
}

// Without --yes and without an interactive terminal, approval is refused:
// nobody confirmed the exact target being approved.
func TestRunMCPApproveRequiresConfirmation(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cwd := t.TempDir()
	var spawned bool
	deps := approveTestDeps(cwd, map[string]config.MCPServerConfig{
		"proj": {Type: "stdio", Command: "mini", ProjectConfigured: true},
	}, &spawned)
	var stdout, stderr bytes.Buffer
	if exitCode := runWithDeps([]string{"mcp", "approve", "proj"}, &stdout, &stderr, deps); exitCode != exitCrash {
		t.Fatalf("exitCode = %d, want refusal without TTY or --yes", exitCode)
	}
	if !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("stderr = %q, want the --yes hint", stderr.String())
	}
	_ = spawned
}
