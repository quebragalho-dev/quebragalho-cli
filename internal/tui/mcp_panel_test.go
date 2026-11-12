package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// The panel states come from the startup probe: origin, status and auth need.
func TestBuildMCPViewStateMarksOriginStatusAndAuthNeed(t *testing.T) {
	cfg := config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"exa":   config.DefaultMCPServers()["exa"],
		"local": {Type: "stdio", Command: "mini", ProjectConfigured: true},
		"gh":    {Type: "http", URL: "https://gh.example/mcp", Auth: "oauth"},
	}}

	state := BuildMCPViewState(MCPStateOptions{
		Config:  cfg,
		Skipped: map[string]string{"gh": "needs login"},
	})

	byName := map[string]MCPServerView{}
	for _, server := range state.Servers {
		byName[server.Name] = server
	}
	if byName["exa"].Origin != "padrão" {
		t.Fatalf("exa origin = %q, want padrão", byName["exa"].Origin)
	}
	if byName["local"].Origin != "projeto" {
		t.Fatalf("local origin = %q, want projeto", byName["local"].Origin)
	}
	if byName["gh"].Origin != "usuário" {
		t.Fatalf("gh origin = %q, want usuário", byName["gh"].Origin)
	}
	if !byName["gh"].NeedsAuth {
		t.Fatal("gh NeedsAuth = false, want true (oauth without token)")
	}
	if byName["gh"].Status != "failed" {
		t.Fatalf("gh status = %q, want failed", byName["gh"].Status)
	}
}

// The manager meta line carries the CC-style glyph, tool count and origin.
func TestMCPManagerServerMetaShowsGlyphs(t *testing.T) {
	meta := mcpManagerServerMeta(MCPServerView{Name: "a", State: "enabled", Transport: "http", ToolCount: 3, Status: "ok", Origin: "usuário"})
	for _, want := range []string{"✔", "3 ferramentas", "usuário"} {
		if !strings.Contains(meta, want) {
			t.Fatalf("meta = %q, missing %q", meta, want)
		}
	}
	if meta := mcpManagerServerMeta(MCPServerView{Name: "b", State: "enabled", Transport: "http", Status: "failed"}); !strings.Contains(meta, "✘") {
		t.Fatalf("failed meta = %q, want ✘", meta)
	}
	if meta := mcpManagerServerMeta(MCPServerView{Name: "c", State: "disabled", Transport: "http", Status: "disabled"}); !strings.Contains(meta, "○") {
		t.Fatalf("disabled meta = %q, want ○", meta)
	}
	if meta := mcpManagerServerMeta(MCPServerView{Name: "d", State: "enabled", Transport: "http", Status: "ok", NeedsAuth: true}); !strings.Contains(meta, "⚠") {
		t.Fatalf("needs-auth meta = %q, want ⚠", meta)
	}
}

type mcpPanelStubTool struct {
	name string
}

func (tool mcpPanelStubTool) Name() string { return tool.name }
func (tool mcpPanelStubTool) Description() string {
	return "stub " + tool.name
}
func (tool mcpPanelStubTool) Parameters() tools.Schema {
	return tools.Schema{}
}
func (tool mcpPanelStubTool) Safety() tools.Safety {
	return tools.Safety{}
}
func (tool mcpPanelStubTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{}
}

// The selection detail shows the server's tools so the panel answers
// "what does this server give me" without running another command.
func TestMCPManagerSelectionDetailListsServerTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(mcpPanelStubTool{name: "mcp_docs_lookup"})
	registry.Register(mcpPanelStubTool{name: "mcp_docs_search"})

	m := model{
		mcpConfig: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"docs": {Type: "stdio", Command: "docs-mcp"},
		}},
		registry:   registry,
		mcpManager: &mcpManagerState{},
	}
	m.refreshMCPViewState()

	lines := m.mcpManagerSelectionDetail(100)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"lookup", "search"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("selection detail hides server tools, missing %q:\n%s", want, joined)
		}
	}
}
