package mcp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func startupTestServer(url string) config.MCPConfig {
	return config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"s": {Type: "http", URL: url},
	}}
}

func TestStartupBackgroundRetryAdoptsRecoveredServer(t *testing.T) {
	var calls atomic.Int32
	factory := func(context.Context, Server) (ToolClient, error) {
		if calls.Add(1) <= 2 {
			return nil, &MCPHTTPStatusError{Server: "s", Status: 503}
		}
		return &fakeToolClient{listed: []RemoteTool{{Name: "lookup"}}}, nil
	}
	registry := tools.NewRegistry()
	start := time.Now()
	runtime, err := RegisterTools(context.Background(), registry,
		startupTestServer("http://127.0.0.1:9/mcp"), RegisterOptions{ClientFactory: factory})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()
	if took := time.Since(start); took >= 2*time.Second {
		t.Fatalf("RegisterTools() took %v, startup must not wait out backoff", took)
	}
	if _, ok := registry.Get("mcp_s_lookup"); ok {
		t.Fatal("failed server must not publish tools at startup")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := registry.Get("mcp_s_lookup"); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background retry never adopted the recovered server")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, skipped := range runtime.Skipped() {
		if skipped.Name == "s" {
			t.Fatalf("adopted server must drop its skip, still present: %v", skipped.Err)
		}
	}
}

// A stdio server that fails at startup must not gain a background respawn:
// the background loop is HTTP/SSE only, like connectWithRetry.
func TestStartupNeverRetriesStdioInBackground(t *testing.T) {
	var calls atomic.Int32
	factory := func(context.Context, Server) (ToolClient, error) {
		calls.Add(1)
		return nil, errors.New("connection refused")
	}
	registry := tools.NewRegistry()
	runtime, err := RegisterTools(context.Background(), registry,
		config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"s": {Type: "stdio", Command: "qg-offline-helper"},
		}}, RegisterOptions{ClientFactory: factory})
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()
	time.Sleep(1500 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("connect attempts = %d, want exactly 1 (no background retry for stdio)", got)
	}
	if _, ok := acceptanceSkippedReason(runtime, "s"); !ok {
		t.Fatal("failed stdio server must stay skipped")
	}
}
