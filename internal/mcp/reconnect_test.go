package mcp

import (
	"context"
	"errors"
	"fmt"
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

// TestConnectBackoffSchedule pins the production retry schedule: 1s, 2s,
// 4s, 8s, doubling per attempt. The acceptance tests below shrink the seam
// for speed, so this is the test that fails if the real values stop
// doubling.
func TestConnectBackoffSchedule(t *testing.T) {
	want := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		32 * time.Second,
	}
	for attempt, wantBackoff := range want {
		if got := connectBackoffFor(attempt + 1); got != wantBackoff {
			t.Fatalf("connectBackoffFor(%d) = %v, want %v", attempt+1, got, wantBackoff)
		}
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
		// The write itself failed: nothing reached the server, so the
		// call may be re-sent after the redial.
		return CallToolResult{}, markNotDelivered(errors.New("write: broken pipe"))
	}
	return CallToolResult{Content: []Content{{Type: "text", Text: "alive"}}}, nil
}

func (client *deadThenAliveClient) Close() error { return nil }

// A write that never left the dead stdio subprocess restarts it on next use:
// the first failed call redials once and the re-sent retry succeeds.
func TestStdioRedialOnTransportFailure(t *testing.T) {
	server := Server{Name: "local", Type: ServerTypeStdio, Command: "mini"}
	dead := &deadThenAliveClient{}
	redialed := false
	tool := newRegistryTool(server, RemoteTool{Name: "echo"}, newMCPServerConn(server, dead), RegisterOptions{})
	tool.conn.reconnect = func(context.Context) (ToolClient, error) {
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

// A failure after the request was written is never re-sent: the first call
// reports the error and latches the transport for dead, and the next use
// heals (redials) before sending.
func TestRunNeverResendsAfterWrite(t *testing.T) {
	server := Server{Name: "s", Type: ServerTypeStdio, Command: "mini"}
	first := &alwaysFailClient{err: errors.New("read: connection reset by peer")}
	conn := newMCPServerConn(server, first)
	var redials int
	conn.reconnect = func(context.Context) (ToolClient, error) {
		redials++
		return &scriptedCallClient{}, nil
	}
	tool := newRegistryTool(server, RemoteTool{Name: "echo"}, conn, RegisterOptions{})

	result := tool.Run(context.Background(), map[string]any{})
	if result.Status != tools.StatusError {
		t.Fatalf("Run() status = %v, want the post-write error", result.Status)
	}
	if first.calls != 1 {
		t.Fatalf("post-write failure re-sent the call %d times, want exactly 1", first.calls)
	}
	if redials != 0 {
		t.Fatalf("post-write failure redialed %d times, want 0: the error goes to the model", redials)
	}
	if !conn.isDead() {
		t.Fatal("post-write transport failure did not latch the conn for dead")
	}
	if result := tool.Run(context.Background(), map[string]any{}); result.Status != tools.StatusOK {
		t.Fatalf("next use after the latched failure = %v, want OK on a healed transport", result.Status)
	}
	if redials != 1 {
		t.Fatalf("healing redialed %d times, want exactly 1", redials)
	}
	if first.calls != 1 {
		t.Fatalf("the dead client served %d calls, want exactly 1: nothing is ever re-sent to it", first.calls)
	}
}

// HTTP 429 says the server did NOT process the call: it is re-sent on the
// same client after Retry-After, without a redial.
func TestRunResendsAfter429(t *testing.T) {
	server := Server{Name: "s", Type: ServerTypeHTTP, URL: "http://127.0.0.1:1/mcp"}
	client := &scriptedCallClient{script: []error{&MCPHTTPStatusError{Server: "s", Transport: ServerTypeHTTP, Status: 429, RetryAfter: "0"}}}
	conn := newMCPServerConn(server, client)
	redialed := false
	conn.reconnect = func(context.Context) (ToolClient, error) {
		redialed = true
		return &scriptedCallClient{}, nil
	}
	tool := newRegistryTool(server, RemoteTool{Name: "lookup"}, conn, RegisterOptions{})

	if result := tool.Run(context.Background(), map[string]any{}); result.Status != tools.StatusOK {
		t.Fatalf("Run() after a 429 = %v, want OK on the retry", result.Status)
	}
	if client.calls != 2 {
		t.Fatalf("a 429 reached the server %d times, want 2 (original + one retry)", client.calls)
	}
	if redialed {
		t.Fatal("a 429 redialed the alive transport")
	}
}

// An HTTP 500 answer is the server speaking: no resend, and the transport
// stays unlatched for the next use.
func TestRunDoesNotResendHTTPStatus(t *testing.T) {
	server := Server{Name: "s", Type: ServerTypeHTTP, URL: "http://127.0.0.1:1/mcp"}
	client := &scriptedCallClient{script: []error{&MCPHTTPStatusError{Server: "s", Transport: ServerTypeHTTP, Status: 500}}}
	conn := newMCPServerConn(server, client)
	tool := newRegistryTool(server, RemoteTool{Name: "lookup"}, conn, RegisterOptions{})

	if result := tool.Run(context.Background(), map[string]any{}); result.Status != tools.StatusError {
		t.Fatalf("Run() on HTTP 500 = %v, want the error without a resend", result.Status)
	}
	if client.calls != 1 {
		t.Fatalf("HTTP 500 re-sent the call %d times, want exactly 1", client.calls)
	}
	if conn.isDead() {
		t.Fatal("an answered HTTP status latched the transport dead; the next use must not redial a live server")
	}
}

type scriptedCallClient struct {
	script []error
	calls  int
}

func (client *scriptedCallClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "echo"}}, nil
}

func (client *scriptedCallClient) CallTool(context.Context, string, map[string]any) (CallToolResult, error) {
	client.calls++
	if client.calls <= len(client.script) {
		if err := client.script[client.calls-1]; err != nil {
			return CallToolResult{}, err
		}
	}
	return CallToolResult{Content: []Content{{Type: "text", Text: "ok"}}}, nil
}

func (client *scriptedCallClient) Close() error { return nil }

// alwaysFailClient fails every call with a post-write transport error: the
// dead client never answers, however often it is (not) asked.
type alwaysFailClient struct {
	calls int
	err   error
}

func (client *alwaysFailClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "echo"}}, nil
}

func (client *alwaysFailClient) CallTool(context.Context, string, map[string]any) (CallToolResult, error) {
	client.calls++
	return CallToolResult{}, client.err
}

func (client *alwaysFailClient) Close() error { return nil }

func TestRetryAfterDelay(t *testing.T) {
	now := time.Now()
	if delay, ok := retryAfterDelay("2", now); !ok || delay != 2*time.Second {
		t.Fatalf("retryAfterDelay(2s) = %v, %v", delay, ok)
	}
	if _, ok := retryAfterDelay("", now); ok {
		t.Fatal("empty Retry-After parsed")
	}
	if _, ok := retryAfterDelay("garbage", now); ok {
		t.Fatal("garbage Retry-After parsed")
	}
	future := now.Add(30 * time.Second).UTC().Format(time.RFC1123)
	if delay, ok := retryAfterDelay(future, now); !ok || delay < 25*time.Second || delay > 35*time.Second {
		t.Fatalf("retryAfterDelay(date) = %v, %v", delay, ok)
	}
}

func TestStreamClosedIsRedialable(t *testing.T) {
	err := fmt.Errorf("MCP SSE stream closed for server r: %w", errMCPStreamClosed)
	if !redialable(context.Background(), err) {
		t.Fatal("a cleanly ended legacy stream must redial")
	}
	if isDeadTransportError(err) {
		t.Fatal("a cleanly ended legacy stream is not a dead peer by message alone; it redials through its own branch")
	}
}
