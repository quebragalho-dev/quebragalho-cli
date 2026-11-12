package mcp

import (
	"os"
	"reflect"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

func TestExpandServerEnvBasic(t *testing.T) {
	t.Setenv("QG_TEST_EXPAND_TOKEN", "abc")
	raw := config.MCPServerConfig{
		Type:    "http",
		URL:     "https://example.test/${QG_TEST_EXPAND_TOKEN}/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${QG_TEST_EXPAND_TOKEN}"},
		Command: "serve --token=${QG_TEST_EXPAND_TOKEN}",
		Args:    []string{"--a=${QG_TEST_EXPAND_TOKEN}", "plain"},
		Env:     map[string]string{"K": "${QG_TEST_EXPAND_TOKEN}!"},
	}
	expanded, missing, _ := ExpandServerEnv(raw)
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
	if expanded.URL != "https://example.test/abc/mcp" {
		t.Fatalf("URL = %q", expanded.URL)
	}
	if expanded.Headers["Authorization"] != "Bearer abc" {
		t.Fatalf("header = %q", expanded.Headers["Authorization"])
	}
	if expanded.Command != "serve --token=abc" || expanded.Args[0] != "--a=abc" || expanded.Args[1] != "plain" {
		t.Fatalf("command/args = %q %q", expanded.Command, expanded.Args)
	}
	if expanded.Env["K"] != "abc!" {
		t.Fatalf("env = %q", expanded.Env["K"])
	}
}

func TestExpandServerEnvDefault(t *testing.T) {
	os.Unsetenv("QG_TEST_EXPAND_DEFINITELY_MISSING")
	raw := config.MCPServerConfig{Type: "http", URL: "https://${QG_TEST_EXPAND_DEFINITELY_MISSING:-fallback.example}/mcp"}
	expanded, missing, _ := ExpandServerEnv(raw)
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none (default applies)", missing)
	}
	if expanded.URL != "https://fallback.example/mcp" {
		t.Fatalf("URL = %q", expanded.URL)
	}
}

func TestExpandServerEnvMissingRecordedOnce(t *testing.T) {
	os.Unsetenv("QG_TEST_EXPAND_DEFINITELY_MISSING")
	raw := config.MCPServerConfig{Type: "http", URL: "https://${QG_TEST_EXPAND_DEFINITELY_MISSING}/x/${QG_TEST_EXPAND_DEFINITELY_MISSING}"}
	expanded, missing, _ := ExpandServerEnv(raw)
	if !reflect.DeepEqual(missing, []string{"QG_TEST_EXPAND_DEFINITELY_MISSING"}) {
		t.Fatalf("missing = %v", missing)
	}
	if expanded.URL != "https:///x/" {
		t.Fatalf("URL = %q, want emptied refs", expanded.URL)
	}
}

func TestExpandServerEnvLeavesBareAndMalformed(t *testing.T) {
	t.Setenv("QG_TEST_EXPAND_TOKEN", "abc")
	raw := config.MCPServerConfig{URL: "https://h/$QG_TEST_EXPAND_TOKEN/${}/${1}/${UN_CLOSED"}
	expanded, missing, _ := ExpandServerEnv(raw)
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want none", missing)
	}
	if expanded.URL != raw.URL {
		t.Fatalf("URL = %q, want literal %q", expanded.URL, raw.URL)
	}
}
