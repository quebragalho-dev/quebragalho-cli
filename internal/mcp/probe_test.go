package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// ProbeServers must report a live server with its tool count.
func TestProbeServersReportsLiveServer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		message := readHTTPRPCMessage(t, request)
		switch message.Method {
		case "initialize":
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{"protocolVersion": "2024-11-05"})
		case "notifications/initialized":
			writer.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{"tools": []RemoteTool{{Name: "echo"}}})
		default:
			writeHTTPRPCResponse(t, writer, message.ID, map[string]any{})
		}
	}))
	defer upstream.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	results := ProbeServers(ctx, []Server{{Name: "up", Type: ServerTypeHTTP, URL: upstream.URL}}, nil)
	result, ok := results["up"]
	if !ok {
		t.Fatal("ProbeServers() has no entry for up")
	}
	if result.Err != nil {
		t.Fatalf("ProbeServers() err = %v, want nil", result.Err)
	}
	if result.ToolCount != 1 {
		t.Fatalf("ProbeServers() tools = %d, want 1", result.ToolCount)
	}
}

// ProbeServers must report an unreachable server with the reason, not hang.
func TestProbeServersReportsFailureReason(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	results := ProbeServers(ctx, []Server{{Name: "down", Type: ServerTypeHTTP, URL: "http://127.0.0.1:1/mcp"}}, nil)
	result, ok := results["down"]
	if !ok {
		t.Fatal("ProbeServers() has no entry for down")
	}
	if result.Err == nil {
		t.Fatal("ProbeServers() err = nil, want failure reason")
	}
	if elapsed := time.Since(start); elapsed > 9*time.Second {
		t.Fatalf("ProbeServers() took %v, want bounded probe", elapsed)
	}
}
