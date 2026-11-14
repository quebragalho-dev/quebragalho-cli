package cli

// The /mcp panel works on the running session: the app hands the TUI
// tui.Options.MCPStatus, read each time the panel renders, covering both the
// servers registered at startup and the optional defaults that finish later,
// and tui.Options.MCPReconnect, which reconnects a server on the live runtime.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
)

func acceptancePanelStatus(t *testing.T, options tui.Options) map[string]mcp.ServerStatus {
	t.Helper()
	if options.MCPStatus == nil {
		t.Fatal("the app does not hand the TUI the session's MCP status")
	}
	byName := map[string]mcp.ServerStatus{}
	for _, status := range options.MCPStatus() {
		byName[status.Name] = status
	}
	return byName
}

func acceptancePanelReconnect(t *testing.T, ctx context.Context, options tui.Options, server string) {
	t.Helper()
	if options.MCPReconnect == nil {
		t.Fatal("the app does not hand the TUI a reconnect for the running session")
	}
	if err := options.MCPReconnect(ctx, server); err != nil {
		t.Fatalf("reconnect %s: %v", server, err)
	}
}

// /mcp "reconectar" on a server that failed at startup brings its tools into
// the running session.
func TestAcceptanceMCPReconnectFromPanelRestoresSessionTools(t *testing.T) {
	acceptanceHermeticEnv(t)
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true},"flaky":`+
		acceptanceSessionServerJSON(t, "fail-first", t.TempDir())+`}}}`)
	repo := acceptanceRepo(t, `{}`, true)

	acceptanceInteractive(t, repo, appDeps{}, func(ctx context.Context, options tui.Options) {
		if _, ok := options.Registry.Get("mcp_flaky_lookup"); ok {
			t.Fatal("precondition: flaky should have failed at startup")
		}
		acceptancePanelReconnect(t, ctx, options, "flaky")
		if _, ok := options.Registry.Get("mcp_flaky_lookup"); !ok {
			t.Fatal("/mcp reconnect left the session without flaky's tools")
		}
		if result := acceptanceCallTool(ctx, options.Registry, "mcp_flaky_lookup"); result.Status != tools.StatusOK {
			t.Fatalf("flaky after reconnect = %s %q, want ok", result.Status, result.Output)
		}
	})
}

func TestAcceptanceMCPSessionStatusReachesPanel(t *testing.T) {
	acceptanceHermeticEnv(t)
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{"mcp":{"servers":{"exa":{"disabled":true},`+
		`"ok":`+acceptanceSessionServerJSON(t, "basic", t.TempDir())+`,`+
		`"flaky":`+acceptanceSessionServerJSON(t, "fail-first", t.TempDir())+`}}}`)
	repo := acceptanceRepo(t, `{}`, true)

	acceptanceInteractive(t, repo, appDeps{}, func(ctx context.Context, options tui.Options) {
		statuses := acceptancePanelStatus(t, options)
		if ok := statuses["ok"]; ok.State != mcp.ServerStateConnected || ok.ToolCount != 1 {
			t.Fatalf("panel status for ok = %#v, want connected with 1 tool", ok)
		}
		if flaky := statuses["flaky"]; flaky.State != mcp.ServerStateFailed || strings.TrimSpace(flaky.Reason) == "" {
			t.Fatalf("panel status for flaky = %#v, want failed with a reason", flaky)
		}
		acceptancePanelReconnect(t, ctx, options, "flaky")
		if flaky := acceptancePanelStatus(t, options)["flaky"]; flaky.State != mcp.ServerStateConnected || flaky.ToolCount != 1 {
			t.Fatalf("panel status for flaky after reconnect = %#v, want connected with 1 tool", flaky)
		}
	})
}

// An unconfigured default that fails stays out of the startup output, but its
// reason reaches /mcp. The default's transport is replaced by one that fails
// the way an offline machine does, so the test never leaves loopback.
func TestAcceptanceMCPDefaultServerFailureReasonReachesPanel(t *testing.T) {
	acceptanceHermeticEnv(t)
	writeAcceptanceJSON(t, mustUserConfigPath(t), `{}`)
	repo := acceptanceRepo(t, `{}`, true)
	register := defaultAppDeps().registerMCPTools
	deps := appDeps{registerMCPTools: func(ctx context.Context, registry *tools.Registry, cfg config.MCPConfig, options mcp.RegisterOptions) (mcpToolRuntime, error) {
		if _, ok := cfg.Servers["exa"]; ok {
			options.ClientFactory = func(context.Context, mcp.Server) (mcp.ToolClient, error) {
				return nil, errors.New(`Post "https://mcp.exa.ai/mcp": dial tcp: lookup mcp.exa.ai: no such host`)
			}
		}
		return register(ctx, registry, cfg, options)
	}}

	stderr := acceptanceInteractive(t, repo, deps, func(_ context.Context, options tui.Options) {
		deadline := time.Now().Add(15 * time.Second)
		for {
			exa := acceptancePanelStatus(t, options)["exa"]
			if exa.State == mcp.ServerStateFailed && strings.Contains(exa.Reason, "no such host") {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("panel status for the failing exa default = %#v, want failed with its reason", exa)
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	if strings.Contains(stderr, "exa") {
		t.Fatalf("the unconfigured default's failure reached the startup output:\n%s", stderr)
	}
}
