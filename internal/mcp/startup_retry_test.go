package mcp

import (
	"context"
	"errors"
	"net/url"
	"strings"
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

func skippedReasonOf(t *testing.T, runtime *Runtime) (string, bool) {
	t.Helper()
	for _, skipped := range runtime.Skipped() {
		if skipped.Name == "s" {
			if skipped.Err == nil {
				return "", true
			}
			return skipped.Err.Error(), true
		}
	}
	return "", false
}

func TestStartupMakesOneAttemptOnRefusedDial(t *testing.T) {
	var calls atomic.Int32
	factory := func(context.Context, Server) (ToolClient, error) {
		calls.Add(1)
		return nil, &url.Error{Op: "dial", Err: errors.New("connection refused")}
	}
	registry := tools.NewRegistry()
	start := time.Now()
	runtime, err := RegisterTools(context.Background(), registry,
		startupTestServer("http://127.0.0.1:9/mcp"), RegisterOptions{ClientFactory: factory})
	took := time.Since(start)
	if err != nil {
		t.Fatalf("RegisterTools() error = %v", err)
	}
	defer runtime.Close()
	if calls.Load() != 1 {
		t.Fatalf("startup attempts = %d, want exactly 1", calls.Load())
	}
	if took >= 2*time.Second {
		t.Fatalf("RegisterTools() took %v, want an immediate skip", took)
	}
	reason, ok := skippedReasonOf(t, runtime)
	if !ok {
		t.Fatal("refused server must be skipped with its error")
	}
	if !strings.Contains(reason, "connection refused") {
		t.Fatalf("skipped reason = %q, want the refused dial", reason)
	}
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
	time.Sleep(2500 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("connect attempts = %d, want exactly 1 (no background retry for stdio)", got)
	}
	if _, ok := skippedReasonOf(t, runtime); !ok {
		t.Fatal("failed stdio server must stay skipped")
	}
}
