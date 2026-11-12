package tui

// Item 9, Leva 1, item 4: the /mcp panel shows where each server comes from.
// Compiles against dev (e391161e) and fails there: the panel has no origin.
// The connection-state half needs the proposed mcp.ServerStatus and lives in
// acceptance_leva1_api_test.go.

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// acceptancePanelServerLine returns the header line of one server in the
// rendered panel ("<name> · ...").
func acceptancePanelServerLine(t *testing.T, panel string, name string) string {
	t.Helper()
	for _, line := range strings.Split(panel, "\n") {
		trimmed := strings.TrimLeft(line, " ›│|")
		if strings.HasPrefix(trimmed, name+" ") {
			return line
		}
	}
	t.Fatalf("panel has no line for server %q:\n%s", name, panel)
	return ""
}

func TestAcceptanceMCPPanelShowsServerOrigin(t *testing.T) {
	exa, ok := config.DefaultMCPServers()["exa"]
	if !ok {
		t.Fatal("exa default missing")
	}
	state := BuildMCPViewState(MCPStateOptions{Config: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"exa":  exa,
		"mine": {Type: "stdio", Command: "my-mcp"},
		"repo": {Type: "stdio", Command: "repo-mcp", ProjectConfigured: true},
	}}})
	panel := plainRender(t, renderMCPView(state, 200))

	for name, origin := range map[string]string{"exa": "padrão", "mine": "usuário", "repo": "projeto"} {
		if line := acceptancePanelServerLine(t, panel, name); !strings.Contains(line, origin) {
			t.Fatalf("server %s line %q does not show origin %q", name, line, origin)
		}
	}
	if !strings.Contains(panel, "qg mcp disable exa") {
		t.Fatalf("default exa cannot be turned off from /mcp:\n%s", panel)
	}
}
