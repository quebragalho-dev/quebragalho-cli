package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

type rpcErrorFakeClient struct {
	calls   int
	redials int
}

func (client *rpcErrorFakeClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "lookup"}}, nil
}

func (client *rpcErrorFakeClient) CallTool(context.Context, string, map[string]any) (CallToolResult, error) {
	client.calls++
	return CallToolResult{}, &MCPRequestError{Method: "tools/call", Code: -32602, Message: "invalid params: missing field query"}
}

func (client *rpcErrorFakeClient) Close() error { return nil }

// A deterministic JSON-RPC error answer (invalid params) proves the transport
// is alive: it must never trigger a reconnect, let alone 5 attempts with
// backoff that pin the turn for half a minute.
func TestRPCErrorNeverReconnects(t *testing.T) {
	dead := &rpcErrorFakeClient{}
	tool := newRegistryTool(Server{Name: "s", Type: ServerTypeStdio}, RemoteTool{Name: "lookup"}, newMCPServerConn(Server{Name: "s"}, dead), RegisterOptions{})
	tool.conn.reconnect = func(context.Context) (ToolClient, error) {
		dead.redials++
		return dead, nil
	}
	start := time.Now()
	result := tool.Run(context.Background(), map[string]any{"query": "hi"})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run() took %v on a JSON-RPC error, want an immediate answer", elapsed)
	}
	if result.Status != tools.StatusError {
		t.Fatalf("Run() status = %v, want the tool error surfaced", result.Status)
	}
	if dead.redials != 0 {
		t.Fatalf("redials = %d, want 0: a JSON-RPC error is not a dead transport", dead.redials)
	}
	if !strings.Contains(result.Output, "invalid params") {
		t.Fatalf("Run() output = %q, want the server's error message", result.Output)
	}
}

func TestMCPRequestErrorIsNotTransient(t *testing.T) {
	err := &MCPRequestError{Method: "tools/call", Code: -32602, Message: "nope"}
	if isTransientConnectError(context.Background(), err) {
		t.Fatal("JSON-RPC error must not be retried as a transient connect failure")
	}
	if redialable(context.Background(), err) {
		t.Fatal("JSON-RPC error must not redial")
	}
	var target *MCPRequestError
	if !errors.As(err, &target) || target.Code != -32602 {
		t.Fatalf("errors.As(%v) did not find the RPC code", err)
	}
}
