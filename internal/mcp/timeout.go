package mcp

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultToolTimeout bounds one MCP tool call (any transport). A hanging
// server releases the agent turn with a clear error instead of hanging it.
const defaultToolTimeout = 60 * time.Second

// ToolTimeoutError reports a tool call that exceeded its wall-clock budget.
// The message is Portuguese on purpose: it is shown to the model and the
// user as the turn-continuation error.
type ToolTimeoutError struct {
	Server  string
	Tool    string
	Timeout time.Duration
}

func (err *ToolTimeoutError) Error() string {
	return fmt.Sprintf("ferramenta %s do servidor %s passou de %s sem responder (timeout)",
		err.Tool, err.Server, formatTimeoutDuration(err.Timeout))
}

func formatTimeoutDuration(duration time.Duration) string {
	if duration%time.Second == 0 {
		return fmt.Sprintf("%ds", duration/time.Second)
	}
	return duration.Round(time.Millisecond).String()
}

// toolTimeoutFor resolves the wall-clock budget for one tool call: the
// per-server config wins, then MCP_TOOL_TIMEOUT (milliseconds), then 60s.
func toolTimeoutFor(server Server) time.Duration {
	if server.Timeout > 0 {
		return time.Duration(server.Timeout) * time.Millisecond
	}
	if env := toolTimeoutFromEnv("MCP_TOOL_TIMEOUT"); env > 0 {
		return env
	}
	return defaultToolTimeout
}

func toolTimeoutFromEnv(key string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	millis, err := strconv.Atoi(raw)
	if err != nil || millis <= 0 {
		return 0
	}
	return time.Duration(millis) * time.Millisecond
}

// withToolTimeout bounds the call clock. The caller must cancel.
func withToolTimeout(parent context.Context, server Server) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, toolTimeoutFor(server))
}

// mapTimeoutError converts a deadline hit on the bounded call context into a
// ToolTimeoutError. A parent cancellation keeps its original error.
func mapTimeoutError(parent, bounded context.Context, server Server, tool string, err error) error {
	if err == nil {
		return nil
	}
	if bounded.Err() == context.DeadlineExceeded && parent.Err() == nil {
		return &ToolTimeoutError{Server: server.Name, Tool: tool, Timeout: toolTimeoutFor(server)}
	}
	return err
}

// connectTimeout bounds the per-server connect+list budget at startup.
// MCP_TIMEOUT (milliseconds) overrides the 8s default.
func connectTimeout() time.Duration {
	if env := toolTimeoutFromEnv("MCP_TIMEOUT"); env > 0 {
		return env
	}
	return defaultConnectTimeout
}

// initializeTimeoutValue bounds the MCP handshake. MCP_TIMEOUT
// (milliseconds) overrides the 30s default.
func initializeTimeoutValue() time.Duration {
	if env := toolTimeoutFromEnv("MCP_TIMEOUT"); env > 0 {
		return env
	}
	return initializeTimeout
}
