package mcp

import (
	"strings"
	"testing"
)

func TestMergeProcessEnvDropsInheritedQGKeyButKeepsExplicitEnv(t *testing.T) {
	t.Setenv("QG_API_KEY", "qg-sk-primary")
	t.Setenv("QUEBRAGALHO_API_KEY", "qg-sk-catalog")
	t.Setenv("QG_MCP_TEST_KEEP", "kept")

	env := mergeProcessEnv(map[string]string{"SERVER_TOKEN": "configured"})

	got := map[string]string{}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		got[key] = value
	}
	if _, ok := got["QG_API_KEY"]; ok {
		t.Fatal("MCP server inherited QG_API_KEY")
	}
	if _, ok := got["QUEBRAGALHO_API_KEY"]; ok {
		t.Fatal("MCP server inherited QUEBRAGALHO_API_KEY")
	}
	if got["QG_MCP_TEST_KEEP"] != "kept" {
		t.Fatal("MCP server lost an unrelated inherited variable")
	}
	if got["SERVER_TOKEN"] != "configured" {
		t.Fatal("MCP server lost its configured env")
	}
}

// A server the user explicitly configured with the QG key (for example one that
// calls the QG API itself) still gets it: only the ambient inheritance is cut.
func TestMergeProcessEnvHonoursExplicitQGKeyInServerConfig(t *testing.T) {
	t.Setenv("QG_API_KEY", "qg-sk-ambient")
	env := mergeProcessEnv(map[string]string{"QG_API_KEY": "qg-sk-explicit"})

	var values []string
	for _, kv := range env {
		if value, ok := strings.CutPrefix(kv, "QG_API_KEY="); ok {
			values = append(values, value)
		}
	}
	if len(values) != 1 || values[0] != "qg-sk-explicit" {
		t.Fatalf("QG_API_KEY entries = %v, want only the explicitly configured one", values)
	}
}
