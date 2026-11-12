package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// The client announces the newest version it implements.
func TestNegotiateProtocolVersion(t *testing.T) {
	if got := latestMCPProtocolVersion(); got != "2025-06-18" {
		t.Fatalf("latestMCPProtocolVersion() = %q", got)
	}
	used, warning := negotiateMCPProtocolVersion("2025-03-26")
	if used != "2025-03-26" || warning != "" {
		t.Fatalf("negotiate(2025-03-26) = %q, %q; want version without warning", used, warning)
	}
	used, warning = negotiateMCPProtocolVersion("2099-01-01")
	if used != "2025-06-18" {
		t.Fatalf("negotiate(2099-01-01) used = %q, want latest", used)
	}
	if !strings.Contains(warning, "2099-01-01") {
		t.Fatalf("negotiate(2099-01-01) warning = %q, want the demanded version", warning)
	}
}

// A server speaking a newer revision still connects, and the mismatch is
// recorded so `mcp check` can warn instead of failing the tools.
func TestNewerProtocolVersionWarnsButConnects(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		message := readHTTPRPCMessage(t, request)
		switch message.Method {
		case "initialize":
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{"protocolVersion": "2099-01-01"})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{"tools": []RemoteTool{{Name: "future"}}})
		default:
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{})
		}
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(ctx, registry,
		config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"future": {Type: "http", URL: upstream.URL},
		}},
		RegisterOptions{AdvertiseInAuto: true})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()
	if len(runtime.Warnings()) != 1 {
		t.Fatalf("Warnings() = %v, want one protocol warning", runtime.Warnings())
	}
	if !strings.Contains(runtime.Warnings()[0], "2099-01-01") {
		t.Fatalf("Warnings() = %v, want the demanded version", runtime.Warnings())
	}
	if _, ok := registry.Get("mcp_future_future"); !ok {
		t.Fatal("future server tools were not registered")
	}
}
