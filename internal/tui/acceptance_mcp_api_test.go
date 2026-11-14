package tui

// The /mcp panel shows each server's live state and offers the matching
// action.

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
)

func TestAcceptanceMCPPanelShowsLiveStatesAndActions(t *testing.T) {
	exa, ok := config.DefaultMCPServers()["exa"]
	if !ok {
		t.Fatal("exa default missing")
	}
	cfg := config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"exa":    exa,
		"ok":     {Type: "http", URL: "https://ok.example.test/mcp"},
		"login":  {Type: "http", URL: "https://login.example.test/mcp", Auth: "oauth"},
		"broken": {Type: "http", URL: "https://broken.example.test/mcp"},
		"off":    {Type: "http", URL: "https://off.example.test/mcp", Disabled: true},
	}}
	state := BuildMCPViewState(MCPStateOptions{
		Config: cfg,
		Status: []mcp.ServerStatus{
			{Name: "ok", State: mcp.ServerStateConnected, ToolCount: 3},
			{Name: "login", State: mcp.ServerStateNeedsAuth},
			{Name: "broken", State: mcp.ServerStateFailed, Reason: "HTTP 503 Service Unavailable"},
			{Name: "off", State: mcp.ServerStateDisabled},
			{Name: "exa", State: mcp.ServerStateFailed, Reason: "dial tcp: lookup mcp.exa.ai: no such host"},
		},
	})
	panel := plainRender(t, renderMCPView(state, 200))

	for name, want := range map[string][]string{
		"ok":     {"✔", "conectado", "3 tools"},
		"login":  {"⚠", "precisa autenticar"},
		"broken": {"✘", "falhou", "HTTP 503"},
		"off":    {"○", "desativado"},
		// An unconfigured default that fails stays out of the transcript but
		// shows its reason here.
		"exa": {"✘", "falhou", "no such host", "padrão"},
	} {
		line := acceptancePanelServerLine(t, panel, name)
		for _, fragment := range want {
			if !strings.Contains(line, fragment) {
				t.Fatalf("server %s line %q is missing %q", name, line, fragment)
			}
		}
	}
	for _, action := range []string{"reconectar", "qg mcp oauth login login", "qg mcp enable off", "qg mcp disable ok"} {
		if !strings.Contains(panel, action) {
			t.Fatalf("panel is missing action %q:\n%s", action, panel)
		}
	}
}
