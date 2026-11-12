package mcp

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestIsTransientConnectError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"server 500", &MCPHTTPStatusError{Server: "s", Status: 500}, true},
		{"rate limited", &MCPHTTPStatusError{Server: "s", Status: 429}, true},
		{"unauthorized", &MCPHTTPStatusError{Server: "s", Status: 401}, false},
		{"not found", &MCPHTTPStatusError{Server: "s", Status: 404}, false},
		{"timeout budget", &ToolTimeoutError{Server: "s", Tool: "t", Timeout: time.Second}, false},
		{"canceled", context.Canceled, false},
		{"refused", &url.Error{Op: "dial", Err: errors.New("connection refused")}, true},
		{"dns", &url.Error{Op: "dial", Err: &net.DNSError{IsNotFound: true}}, false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTransientConnectError(context.Background(), tc.err); got != tc.want {
				t.Fatalf("isTransientConnectError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestConnectWithRetrySucceedsAfterTransient(t *testing.T) {
	attempts := 0
	factory := func(context.Context, Server) (ToolClient, error) {
		attempts++
		if attempts < 3 {
			return nil, &MCPHTTPStatusError{Server: "s", Status: 500}
		}
		return &fakeToolClient{listed: []RemoteTool{{Name: "ok"}}}, nil
	}
	client, remote, err := connectWithRetry(context.Background(), factory,
		Server{Name: "s", Type: ServerTypeHTTP}, 0)
	if err != nil {
		t.Fatalf("connectWithRetry() error = %v", err)
	}
	defer client.Close()
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if len(remote) != 1 || remote[0].Name != "ok" {
		t.Fatalf("remote = %+v, want the recovered tool list", remote)
	}
}

func TestConnectWithRetryNoRetryOnAuth(t *testing.T) {
	attempts := 0
	factory := func(context.Context, Server) (ToolClient, error) {
		attempts++
		return nil, &MCPHTTPStatusError{Server: "s", Status: 401}
	}
	_, _, err := connectWithRetry(context.Background(), factory,
		Server{Name: "s", Type: ServerTypeHTTP}, 0)
	if err == nil {
		t.Fatal("connectWithRetry() error = nil, want auth error")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no retry on auth)", attempts)
	}
}

func TestConnectWithRetryBoundedByContext(t *testing.T) {
	attempts := 0
	factory := func(context.Context, Server) (ToolClient, error) {
		attempts++
		return nil, &MCPHTTPStatusError{Server: "s", Status: 500}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := connectWithRetry(ctx, factory, Server{Name: "s", Type: ServerTypeHTTP}, 0)
	if err == nil {
		t.Fatal("connectWithRetry() error = nil, want error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("connectWithRetry() took %v, want bounded by context", elapsed)
	}
	if attempts < 1 {
		t.Fatal("connectWithRetry() made no attempt")
	}
}

type deadThenAliveClient struct {
	calls int
}

func (client *deadThenAliveClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "echo"}}, nil
}

func (client *deadThenAliveClient) CallTool(context.Context, string, map[string]any) (CallToolResult, error) {
	client.calls++
	if client.calls == 1 {
		return CallToolResult{}, errors.New("write: broken pipe")
	}
	return CallToolResult{Content: []Content{{Type: "text", Text: "alive"}}}, nil
}

func (client *deadThenAliveClient) Close() error { return nil }

// A dead stdio subprocess restarts on next use: the first failed call
// redials once and the retry succeeds.
func TestStdioRedialOnTransportFailure(t *testing.T) {
	server := Server{Name: "local", Type: ServerTypeStdio, Command: "mini"}
	dead := &deadThenAliveClient{}
	redialed := false
	tool := newRegistryTool(server, RemoteTool{Name: "echo"}, dead, RegisterOptions{})
	tool.reconnect = func(context.Context) (ToolClient, error) {
		redialed = true
		return dead, nil
	}
	result := tool.Run(context.Background(), map[string]any{})
	if result.Status != tools.StatusOK {
		t.Fatalf("Run() status = %v, want OK after redial", result.Status)
	}
	if !redialed {
		t.Fatal("Run() did not redial the dead stdio server")
	}
	if !strings.Contains(result.Output, "alive") {
		t.Fatalf("Run() output = %q, want retried result", result.Output)
	}
}
