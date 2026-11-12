package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	internalmcp "github.com/quebragalho-dev/quebragalho-cli/internal/mcp"
)

// The /mcp manager's reconnect action calls Runtime.Reconnect on the live
// session (not a separate `qg mcp check` process), and the panel shows
// the session's live Status.
func TestMCPManagerReconnectUsesSessionRuntime(t *testing.T) {
	var reconnected []string
	status := []internalmcp.ServerStatus{
		{Name: "docs", State: internalmcp.ServerStateFailed, Reason: "dial refused"},
	}
	m := newModel(context.Background(), Options{
		MCPConfig: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"docs": {Type: "stdio", Command: "docs-mcp"},
		}},
		MCPStatus: func() []internalmcp.ServerStatus { return status },
		MCPReconnect: func(_ context.Context, name string) error {
			reconnected = append(reconnected, name)
			status[0] = internalmcp.ServerStatus{Name: "docs", State: internalmcp.ServerStateConnected, ToolCount: 1}
			return nil
		},
	})
	opened := m.openMCPManager()
	updated, cmd := opened.handleMCPManagerKey(testKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("expected reconnect to run asynchronously")
	}
	next := applyCommandResult(t, updated, cmd)
	if len(reconnected) != 1 || reconnected[0] != "docs" {
		t.Fatalf("reconnected = %v, want one session-runtime reconnect of docs", reconnected)
	}
	lines := mcpManagerServerLines(next.mcpViewState().Servers)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "✔") || !strings.Contains(joined, "conectado") {
		t.Fatalf("panel did not refresh to the reconnected state:\n%s", joined)
	}
	text := transcriptText(next.transcript)
	if !strings.Contains(text, "reconectado") {
		t.Fatalf("transcript does not confirm the reconnect:\n%s", text)
	}
}
