package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// maxConnectAttempts bounds the reconnect loop for remote servers: the first
// attempt plus up to 4 retries with exponential backoff.
const maxConnectAttempts = 5

// connectBackoffFor returns the wait before attempt number (1-based): the
// first retry waits 1s, then 2s, 4s, 8s.
func connectBackoffFor(attempt int) time.Duration {
	return time.Duration(1<<uint(attempt-1)) * time.Second
}

// MCPHTTPStatusError carries the HTTP status of a failed MCP-over-HTTP call
// so the reconnect loop can tell transient failures (retry) from permanent
// ones (fail fast). Its message matches the historical plain error.
type MCPHTTPStatusError struct {
	Server    string
	Transport ServerType
	Status    int
	Detail    string
}

func (err *MCPHTTPStatusError) Error() string {
	if strings.TrimSpace(err.Detail) == "" {
		return fmt.Sprintf("MCP %s server %s returned HTTP %d", err.Transport, err.Server, err.Status)
	}
	return fmt.Sprintf("MCP %s server %s returned HTTP %d: %s", err.Transport, err.Server, err.Status, err.Detail)
}

// isTransientConnectError reports whether a failed connect is worth retrying.
// Context exhaustion and the tool-call budget are never transient: retrying
// them only burns the startup budget. Authentication and client errors fail
// fast because another attempt would fail the same way.
func isTransientConnectError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	var timeout *ToolTimeoutError
	if errors.As(err, &timeout) {
		return false
	}
	var budget *connectBudgetError
	if errors.As(err, &budget) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status *MCPHTTPStatusError
	if errors.As(err, &status) {
		return status.Status == 408 || status.Status == 425 || status.Status == 429 || status.Status >= 500
	}
	var rpcErr *MCPRequestError
	if errors.As(err, &rpcErr) {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			return false
		}
		if urlErr.Timeout() {
			return true
		}
		message := strings.ToLower(urlErr.Err.Error())
		for _, token := range []string{"connection refused", "connection reset", "broken pipe", "unexpected eof", "eof"} {
			if strings.Contains(message, token) {
				return true
			}
		}
		return false
	}
	message := strings.ToLower(err.Error())
	for _, token := range []string{"connection refused", "connection reset", "broken pipe", "unexpected eof"} {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
}

// connectWithRetry connects to one remote server and lists its tools, retrying
// transient failures with exponential backoff (1s, 2s, 4s, 8s). The context
// still bounds the total: a cancelled or expired context stops the loop. It
// does ONLY I/O, so it stays safe to run concurrently per server. When
// timeout > 0, no new attempt starts past the budget; the last connect error
// is returned — never a generic timeout — so the skipped reason names the
// server's answer (a 503, a refused dial).
func connectWithRetry(ctx context.Context, factory func(context.Context, Server) (ToolClient, error), server Server, timeout time.Duration) (ToolClient, []RemoteTool, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	var err error
	for attempt := 1; ; attempt++ {
		var client ToolClient
		var remote []RemoteTool
		client, remote, err = connectAndList(ctx, factory, server)
		if err == nil {
			return client, remote, nil
		}
		if server.Type != ServerTypeHTTP && server.Type != ServerTypeSSE {
			return nil, nil, err
		}
		if attempt >= maxConnectAttempts || !isTransientConnectError(ctx, err) {
			return nil, nil, err
		}
		backoff := connectBackoffFor(attempt)
		if !deadline.IsZero() && time.Now().Add(backoff).After(deadline) {
			return nil, nil, err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			// Report the last connect failure, not the budget: the caller
			// (and the user reading the skipped reason) needs the server's
			// answer (a 503, a refused dial), not our own timeout.
			return nil, nil, err
		case <-timer.C:
		}
	}
}

// sleepForReconnect waits out the backoff before reconnect attempt number
// (1-based: 1s, 2s, 4s, 8s...), aborting early when the caller goes away.
func sleepForReconnect(ctx context.Context, attempt int) bool {
	timer := time.NewTimer(connectBackoffFor(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// MCPRequestError is a JSON-RPC error answer from the server (e.g.
// -32602 invalid params). The transport is alive — the server answered — so
// it must never trigger a reconnect or a connect retry. Its message matches
// the historical plain error text.
type MCPRequestError struct {
	Method  string
	Code    int
	Message string
}

func (err *MCPRequestError) Error() string {
	return fmt.Sprintf("MCP %s failed: %s", err.Method, err.Message)
}

// redialable reports whether a failed tool call means the transport died and
// is worth a reconnect: the process may have exited, the pipe may be broken,
// or the remote end may have dropped. A timeout, a caller cancellation, or a
// JSON-RPC error answer (the server is alive and said no) must never restart
// anything.
func redialable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var timeout *ToolTimeoutError
	if errors.As(err, &timeout) {
		return false
	}
	var budget *connectBudgetError
	if errors.As(err, &budget) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, errMCPStreamClosed) {
		return true
	}
	return isDeadTransportError(err)
}

// isDeadTransportError reports whether err proves the peer is gone: EOF or a
// broken/closed pipe for stdio, and refused/reset connections or 408/425/429
// and 5xx for remote servers. Anything the server answered — including a
// JSON-RPC error — is not dead.
func isDeadTransportError(err error) bool {
	var rpcErr *MCPRequestError
	if errors.As(err, &rpcErr) {
		return false
	}
	if errors.Is(err, errMCPClientClosed) {
		return true
	}
	var status *MCPHTTPStatusError
	if errors.As(err, &status) {
		return status.Status == 408 || status.Status == 425 || status.Status == 429 || status.Status >= 500
	}
	message := strings.ToLower(err.Error())
	for _, token := range []string{"eof", "broken pipe", "connection refused", "connection reset", "connection closed", "use of closed"} {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
}
