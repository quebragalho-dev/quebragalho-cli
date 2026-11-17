package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxConnectAttempts bounds the reconnect loop for remote servers: the first
// attempt plus up to 4 retries with exponential backoff.
const maxConnectAttempts = 5

// connectBackoffFor is the wait before attempt number (1-based): the
// first retry waits 1s, then 2s, 4s, 8s. A variable rather than a function
// so tests can shrink the schedule instead of paying real backoff seconds;
// production code never assigns it.
var connectBackoffFor = func(attempt int) time.Duration {
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
	// RetryAfter is the raw Retry-After response header, when the server sent
	// one with a 429. The run loop honors it before re-sending the call the
	// server explicitly said it did not process.
	RetryAfter string
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
		// The errno survives localization; the wording does not. Each
		// predicate covers only what this branch already claimed.
		if isRefusedDialErrno(urlErr.Err) || isResetErrno(urlErr.Err) || isClosedPipeErrno(urlErr.Err) {
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
	if isRefusedDialErrno(err) || isResetErrno(err) || isClosedPipeErrno(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, token := range []string{"connection refused", "connection reset", "broken pipe", "unexpected eof"} {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
}

// isConnectBudgetError reports whether err is qg's own handshake budget firing
// (the initialize timeout while the outer context was still live). The
// background retry treats it as a slow server and retries: when MCP_TIMEOUT
// collapses the connect and initialize budgets to the same value, either
// timer may win the race, and only the bound path was previously considered
// transient, so the retry was silently never scheduled.
func isConnectBudgetError(err error) bool {
	var budget *connectBudgetError
	return errors.As(err, &budget)
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

// retryAfterDelay parses a Retry-After header value — delay seconds or an
// HTTP date — into the wait before re-sending. ok=false means the server
// sent nothing usable, and the caller falls back to the attempt backoff.
func retryAfterDelay(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs < 0 {
			secs = 0
		}
		return time.Duration(secs) * time.Second, true
	}
	if date, err := time.Parse(time.RFC1123, raw); err == nil {
		delay := date.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

// sleepForStatusRetry waits out a 429 delay before re-sending a call the
// server said it did not process. Aborting early on cancellation keeps Esc
// responsive.
func sleepForStatusRetry(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
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

// errMCPNotDelivered marks a call failure where none of this call's request
// reached the server: the dial was refused, or the process was already dead
// before the write. Only these failures may be re-sent after a redial —
// everything after the write (an answer, a status, a cut stream, a timeout)
// goes to the model, and the next use heals the transport instead.
var errMCPNotDelivered = errors.New("MCP request was not delivered")

// notDeliveredError carries a pre-write failure, preserving the transport
// error (and its message) for errors.As/Is matching.
type notDeliveredError struct {
	err error
}

func (err *notDeliveredError) Error() string { return err.err.Error() }
func (err *notDeliveredError) Unwrap() error { return err.err }
func (err *notDeliveredError) Is(target error) bool {
	return target == errMCPNotDelivered
}

// markNotDelivered tags a failure that happened before the request reached
// the server, so the run loop may re-send it after a redial.
func markNotDelivered(err error) error {
	if err == nil {
		return nil
	}
	return &notDeliveredError{err: err}
}

// isDialPhaseError reports whether an HTTP round-trip failure happened before
// anything could have reached the server: the name did not resolve or the
// dial was refused. Resets and EOFs mid-stream stay unmarked — the request
// may already be in the server's hands.
func isDialPhaseError(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// A refused dial carries ECONNREFUSED/WSAECONNREFUSED behind any wording.
	// Resets stay out by contract: the request may already be in flight.
	if isRefusedDialErrno(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "connection refused") ||
		strings.Contains(message, "no such host")
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
	if isRefusedDialErrno(err) || isResetErrno(err) || isClosedPipeErrno(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, token := range []string{"eof", "broken pipe", "connection refused", "connection reset", "connection closed", "use of closed"} {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
}
