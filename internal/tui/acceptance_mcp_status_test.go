package tui

// The /mcp panel renders the session's live status from Options.MCPStatus,
// read again each time the panel opens.

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
)

func TestAcceptanceMCPPanelShowsSessionStatus(t *testing.T) {
	exa, ok := config.DefaultMCPServers()["exa"]
	if !ok {
		t.Fatal("exa default missing")
	}
	broken := mcp.ServerStatus{Name: "broken", State: mcp.ServerStateFailed, Reason: "process exited: bad credentials"}
	m := newModel(context.Background(), Options{
		MCPConfig: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"exa":    exa,
			"ok":     {Type: "stdio", Command: "ok-mcp"},
			"broken": {Type: "stdio", Command: "broken-mcp"},
		}},
		MCPStatus: func() []mcp.ServerStatus {
			return []mcp.ServerStatus{
				{Name: "ok", State: mcp.ServerStateConnected, ToolCount: 2},
				broken,
				{Name: "exa", State: mcp.ServerStateFailed, Reason: "dial tcp: lookup mcp.exa.ai: no such host"},
			}
		},
	})
	m.width = 200
	m.height = 40

	m = m.openMCPManager()
	panel := plainRender(t, m.mcpText())
	for name, want := range map[string][]string{
		"ok":     {"✔", "conectado", "2 tools"},
		"broken": {"✘", "falhou", "bad credentials"},
		"exa":    {"✘", "falhou", "no such host"},
	} {
		line := acceptancePanelServerLine(t, panel, name)
		for _, fragment := range want {
			if !strings.Contains(line, fragment) {
				t.Fatalf("server %s line %q is missing %q", name, line, fragment)
			}
		}
	}

	broken = mcp.ServerStatus{Name: "broken", State: mcp.ServerStateConnected, ToolCount: 1}
	m = m.openMCPManager()
	panel = plainRender(t, m.mcpText())
	if line := acceptancePanelServerLine(t, panel, "broken"); !strings.Contains(line, "conectado") {
		t.Fatalf("panel reopened after broken reconnected still shows %q", line)
	}
}
