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
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var status *MCPHTTPStatusError
	if errors.As(err, &status) {
		return status.Status == 408 || status.Status == 425 || status.Status == 429 || status.Status >= 500
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
// does ONLY I/O, so it stays safe to run concurrently per server.
func connectWithRetry(ctx context.Context, factory func(context.Context, Server) (ToolClient, error), server Server) (ToolClient, []RemoteTool, error) {
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
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// redialable reports whether a failed stdio tool call is worth one redial:
// the process may have died, but a timeout or a caller cancellation must not
// restart anything.
func redialable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var timeout *ToolTimeoutError
	if errors.As(err, &timeout) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}
