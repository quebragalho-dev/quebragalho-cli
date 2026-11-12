package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func mcpFilterTestConfig() config.MCPConfig {
	return config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"f": {
			Type:          "http",
			URL:           "http://127.0.0.1:1/mcp",
			EnabledTools:  []string{"keep"},
			DisabledTools: []string{"drop"},
		},
	}}
}

func TestNormalizeConfigPassesToolFilters(t *testing.T) {
	servers, err := NormalizeConfig(mcpFilterTestConfig())
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("servers = %+v", servers)
	}
	if len(servers[0].EnabledTools) != 1 || servers[0].EnabledTools[0] != "keep" {
		t.Fatalf("EnabledTools = %+v", servers[0].EnabledTools)
	}
	if len(servers[0].DisabledTools) != 1 || servers[0].DisabledTools[0] != "drop" {
		t.Fatalf("DisabledTools = %+v", servers[0].DisabledTools)
	}
}

// enabled_tools keeps only the named tools; disabled_tools removes its names
// even when also enabled (deny wins).
func TestToolFiltersApplyAtRegistration(t *testing.T) {
	factory := func(context.Context, Server) (ToolClient, error) {
		return &fakeToolClient{listed: []RemoteTool{{Name: "keep"}, {Name: "drop"}, {Name: "other"}}}, nil
	}
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry, config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"f": {Type: "http", URL: "http://127.0.0.1:1/mcp", EnabledTools: []string{"keep", "drop"}, DisabledTools: []string{"drop"}},
	}}, RegisterOptions{ClientFactory: factory})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()
	if _, ok := registry.Get("mcp_f_keep"); !ok {
		t.Fatal("mcp_f_keep was not registered")
	}
	if _, ok := registry.Get("mcp_f_drop"); ok {
		t.Fatal("mcp_f_drop was registered despite disabled_tools")
	}
	if _, ok := registry.Get("mcp_f_other"); ok {
		t.Fatal("mcp_f_other was registered despite enabled_tools")
	}
	if len(runtime.Skipped()) != 0 {
		t.Fatalf("Skipped() = %+v, want none", runtime.Skipped())
	}
}

// A filter that removes every tool skips the server with a reason instead of
// registering an empty, silently useless server.
func TestAllFilteredServerSkipsWithReason(t *testing.T) {
	factory := func(context.Context, Server) (ToolClient, error) {
		return &fakeToolClient{listed: []RemoteTool{{Name: "a"}}}, nil
	}
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry, config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"f": {Type: "http", URL: "http://127.0.0.1:1/mcp", DisabledTools: []string{"a"}},
	}}, RegisterOptions{ClientFactory: factory})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()
	if len(runtime.Skipped()) != 1 || !strings.Contains(runtime.Skipped()[0].Err.Error(), "enabled_tools") {
		t.Fatalf("Skipped() = %+v, want filter reason", runtime.Skipped())
	}
}
