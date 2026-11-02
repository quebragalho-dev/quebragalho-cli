//go:build darwin

package tools

import (
	"context"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

// TestBashNetworkDenialPromptsRetry runs an unclassified network command
// (a python socket connect, not curl/npm) through the real seatbelt sandbox
// with a deny-network policy and requires the structured external-network
// denial the agent loop uses to offer a permission retry.
// Policy.MonitorDenials is deliberately NOT set: the deny-network profile
// must tag the run itself.
//
// The command connects to a literal IP on purpose: a hostname forces the
// failure through DNS/getaddrinfo, which on some interpreters can die on an
// ancillary denial before ever attempting the socket, so the tagged network
// rule never fires and there is nothing to attribute. A raw connect always
// reaches the socket denial first, deterministically.
func TestBashNetworkDenialPromptsRetry(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skipf("sandbox-exec unavailable: %v", err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	backend := sandbox.SelectBackend(sandbox.BackendOptions{})
	if !backend.Available || backend.Name != sandbox.BackendMacOSSeatbelt {
		t.Skipf("host sandbox backend is not seatbelt: %s", backend.Message)
	}
	root := t.TempDir()
	engine := sandbox.NewEngine(sandbox.EngineOptions{
		WorkspaceRoot: root,
		Policy:        sandbox.DefaultPolicy(),
		Backend:       backend,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tool := NewScopedBashTool(root, nil).(interface {
		RunWithSandbox(context.Context, map[string]any, *sandbox.Engine) Result
	})
	result := tool.RunWithSandbox(ctx, map[string]any{
		"command": "python3 -c \"import socket; socket.create_connection(('93.184.216.34', 443), timeout=5)\"",
	}, engine)
	if result.Status != StatusError {
		t.Fatalf("denied network must fail, got %s: %s", result.Status, result.Output)
	}
	outcome := result.ExecutionOutcome
	if outcome == nil || outcome.Denial == nil {
		t.Fatalf("denied network must carry a structured denial, got outcome %#v", outcome)
	}
	if outcome.Denial.Capability.Kind != execution.CapabilityExternalNetwork {
		t.Fatalf("denial capability = %q, want external_network", outcome.Denial.Capability.Kind)
	}
}

// TestBashNetworkDenialPromptsRetryOverHostname covers the reported bug: a
// network denial reached through hostname resolution (the DNS path), not a
// literal IP. The sandboxed resolver answers through the allowed
// mDNSResponder channel, so the TCP connect that follows still hits the
// tagged network rule and carries the same structured denial.
func TestBashNetworkDenialPromptsRetryOverHostname(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skipf("sandbox-exec unavailable: %v", err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skipf("python3 unavailable: %v", err)
	}
	if _, err := net.DialTimeout("tcp", "example.com:80", 5*time.Second); err != nil {
		t.Skipf("no external network for the hostname denial path: %v", err)
	}
	backend := sandbox.SelectBackend(sandbox.BackendOptions{})
	if !backend.Available || backend.Name != sandbox.BackendMacOSSeatbelt {
		t.Skipf("host sandbox backend is not seatbelt: %s", backend.Message)
	}
	root := t.TempDir()
	engine := sandbox.NewEngine(sandbox.EngineOptions{
		WorkspaceRoot: root,
		Policy:        sandbox.DefaultPolicy(),
		Backend:       backend,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	tool := NewScopedBashTool(root, nil).(interface {
		RunWithSandbox(context.Context, map[string]any, *sandbox.Engine) Result
	})
	result := tool.RunWithSandbox(ctx, map[string]any{
		"command": "python3 -c \"import urllib.request; urllib.request.urlopen('http://example.com', timeout=5)\"",
	}, engine)
	if result.Status != StatusError {
		t.Fatalf("denied network must fail, got %s: %s", result.Status, result.Output)
	}
	outcome := result.ExecutionOutcome
	if outcome == nil || outcome.Denial == nil {
		t.Fatalf("denied network must carry a structured denial, got outcome %#v", outcome)
	}
	if outcome.Denial.Capability.Kind != execution.CapabilityExternalNetwork {
		t.Fatalf("denial capability = %q, want external_network", outcome.Denial.Capability.Kind)
	}
}
