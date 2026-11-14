package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// Expanded ${VAR} values must never reach user-visible text: skipped
// reasons, status reasons, model output, or CLI displays. The server
// remembers every non-empty value it substituted so each surface can trade
// it back for the ${VAR} reference.
func TestRedactExpandedSecrets(t *testing.T) {
	server := Server{Name: "s", Secrets: map[string]string{"QG_TOKEN": "ambient-secret-value"}}
	got := server.Redact("Post https://h/sk-path/ambient-secret-value/mcp?q=ambient-secret-value: refused")
	if strings.Contains(got, "ambient-secret-value") {
		t.Fatalf("Redact() leaked the value: %q", got)
	}
	if strings.Count(got, "${QG_TOKEN}") != 2 {
		t.Fatalf("Redact() = %q, want both occurrences traded for ${QG_TOKEN}", got)
	}
}

func TestRedactPrefersLongestValue(t *testing.T) {
	server := Server{Name: "s", Secrets: map[string]string{"A": "ambient-abc", "B": "ambient-abcdef"}}
	got := server.Redact("x ambient-abcdef y")
	if strings.Contains(got, "abcdef") || !strings.Contains(got, "${B}") {
		t.Fatalf("Redact() = %q, want the longest value traded first", got)
	}
}

func TestRedactIgnoresShortValues(t *testing.T) {
	server := Server{Name: "s", Secrets: map[string]string{"MODE": "ok"}}
	if got := server.Redact("ok"); got != "ok" {
		t.Fatalf("Redact() = %q, short values must never rewrite text", got)
	}
}

type secretEchoClient struct{ secret string }

func (client *secretEchoClient) ListTools(context.Context) ([]RemoteTool, error) {
	return []RemoteTool{{Name: "lookup"}}, nil
}

func (client *secretEchoClient) Close() error { return nil }

func (client *secretEchoClient) CallTool(context.Context, string, map[string]any) (CallToolResult, error) {
	return CallToolResult{}, &MCPHTTPStatusError{Server: "s", Transport: ServerTypeHTTP, Status: 500, Detail: "token=" + client.secret}
}

// A transport error echoing the expanded secret is redacted before the model
// sees it, keeping only the ${VAR} reference.
func TestRunOutputRedactsExpandedSecret(t *testing.T) {
	const secret = "ambient-secret-value"
	server := Server{Name: "s", Type: ServerTypeStdio, Secrets: map[string]string{"QG_TOKEN": secret}}
	conn := newMCPServerConn(server, &secretEchoClient{secret: secret})
	redials := 0
	conn.reconnect = func(context.Context) (ToolClient, error) {
		redials++
		return &secretEchoClient{secret: secret}, nil
	}
	tool := newRegistryTool(server, RemoteTool{Name: "lookup"}, conn, RegisterOptions{})
	result := tool.Run(context.Background(), map[string]any{})
	if result.Status != tools.StatusError {
		t.Fatalf("Run() status = %v, want the transport error surfaced", result.Status)
	}
	if strings.Contains(result.Output, secret) {
		t.Fatalf("Run() output leaked the secret: %q", result.Output)
	}
	if !strings.Contains(result.Output, "${QG_TOKEN}") {
		t.Fatalf("Run() output = %q, want the ${VAR} reference kept", result.Output)
	}
}

func TestSanitizeKeepsVarReferenceReadable(t *testing.T) {
	text := `Post "http://127.0.0.1:1/${QG_ACCEPT_KEY}/mcp": dial tcp 127.0.0.1:1: connect: connection refused`
	got := sanitizeMCPErrorText(text)
	if strings.Contains(got, "%7B") || strings.Contains(got, "%7D") {
		t.Fatalf("sanitizeMCPErrorText() = %q, shows the ${VAR} reference percent-encoded", got)
	}
	if !strings.Contains(got, "${QG_ACCEPT_KEY}") {
		t.Fatalf("sanitizeMCPErrorText() = %q, want the readable ${VAR} reference", got)
	}
}
