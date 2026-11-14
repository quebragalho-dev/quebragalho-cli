package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// A hanging Streamable-HTTP server must not hang CallTool forever: the tool
// timeout (MCP_TOOL_TIMEOUT) bounds the call and surfaces a clear error.
func TestCallToolHTTPTimeoutFromEnv(t *testing.T) {
	t.Setenv("MCP_TOOL_TIMEOUT", "120")

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		message := readHTTPRPCMessage(t, request)
		switch message.Method {
		case "initialize":
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{"protocolVersion": "2024-11-05"})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/call":
			select {
			case <-request.Context().Done():
			case <-time.After(30 * time.Second):
			}
		default:
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{})
		}
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := Connect(ctx, Server{Name: "hang", Type: ServerTypeHTTP, URL: upstream.URL})
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

	start := time.Now()
	_, err = client.CallTool(context.Background(), "slow", nil)
	if err == nil {
		t.Fatal("CallTool() error = nil, want tool timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("CallTool() took %v, want bounded by MCP_TOOL_TIMEOUT=120ms", elapsed)
	}
	if !strings.Contains(err.Error(), "passou de") {
		t.Fatalf("CallTool() error = %q, want Portuguese timeout message", err.Error())
	}
}

// The per-server timeout overrides the environment for that server.
func TestToolTimeoutForPrefersServerOverEnv(t *testing.T) {
	t.Setenv("MCP_TOOL_TIMEOUT", "60000")
	server := Server{Name: "s", Timeout: 1500}
	if got := toolTimeoutFor(server); got != 1500*time.Millisecond {
		t.Fatalf("toolTimeoutFor() = %v, want 1500ms", got)
	}
}

func TestToolTimeoutForDefaults60s(t *testing.T) {
	t.Setenv("MCP_TOOL_TIMEOUT", "")
	if got := toolTimeoutFor(Server{Name: "s"}); got != 60*time.Second {
		t.Fatalf("toolTimeoutFor() = %v, want 60s", got)
	}
}

func TestToolTimeoutErrorMessage(t *testing.T) {
	err := &ToolTimeoutError{Server: "docs", Tool: "search", Timeout: 60 * time.Second}
	if !strings.Contains(err.Error(), "ferramenta search do servidor docs passou de 60s") {
		t.Fatalf("Error() = %q, want spec message", err.Error())
	}
}

func mcpConfigForTimeoutTest() config.MCPConfig {
	return config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"s": {Type: "http", URL: "http://127.0.0.1:1/mcp", Timeout: 5000},
	}}
}

func TestNormalizeConfigPassesTimeout(t *testing.T) {
	servers, err := NormalizeConfig(mcpConfigForTimeoutTest())
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if len(servers) != 1 || servers[0].Timeout != 5000 {
		t.Fatalf("NormalizeConfig() timeout = %+v, want 5000", servers)
	}
}
