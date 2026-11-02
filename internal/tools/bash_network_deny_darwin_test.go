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
// The command connects to a literal IP on purpose: it reaches the tagged
// TCP socket denial (network-outbound remote:*:443) directly and
// deterministically, even on a cold log stream. The hostname variant below
// covers the slower DNS path (network-outbound /private/var/run/mDNSResponder)
// that missed the 25 ms tag grace on a cold stream and showed a raw
// DNS/EPERM error instead of the permission prompt.
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
// literal IP. The sandbox denies the resolver itself
// (network-outbound /private/var/run/mDNSResponder), so urllib fails with a
// raw gaierror instead of reaching TCP; the tagged network rule still fires
// for that denial, and the monitor must attribute it to offer the same
// structured denial. On a cold log stream the tag was lost (no warmup beyond
// the header), so the TUI showed the raw error with no prompt.
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

// TestBashNetworkDenialNodeFetch covers node, which the pre-classifier also
// leaves unclassified (like python -c): a fetch through hostname resolution
// must carry the same structured denial via the monitor, not a raw
// TypeError.
func TestBashNetworkDenialNodeFetch(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skipf("sandbox-exec unavailable: %v", err)
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skipf("node unavailable: %v", err)
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
		"command": "node -e \"fetch('http://example.com').then(r=>console.log(r.status))\"",
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

// TestBashNetworkDenialSystemPython covers /usr/bin/python3, the Xcode shim
// (xcrun) that re-execs the real interpreter: it stays inside the sandbox,
// so a hostname denial must carry the same structured denial.
func TestBashNetworkDenialSystemPython(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skipf("sandbox-exec unavailable: %v", err)
	}
	if _, err := exec.LookPath("/usr/bin/python3"); err != nil {
		t.Skipf("/usr/bin/python3 unavailable: %v", err)
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
		"command": "/usr/bin/python3 -c \"import urllib.request; urllib.request.urlopen('http://example.com', timeout=5)\"",
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

// TestBashNetworkDenialShellNc covers a shell wrapper around /usr/bin/nc to
// a literal IP: even when the shell exits zero (echo after nc), the monitor
// blocks must still surface the structured denial for the agent loop.
func TestBashNetworkDenialShellNc(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skipf("sandbox-exec unavailable: %v", err)
	}
	if _, err := exec.LookPath("/usr/bin/nc"); err != nil {
		t.Skipf("/usr/bin/nc unavailable: %v", err)
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
		"command": "sh -c '/usr/bin/nc -z -w2 93.184.215.14 80; echo nc=$?'",
	}, engine)
	outcome := result.ExecutionOutcome
	if outcome == nil || outcome.Denial == nil {
		t.Fatalf("denied network must carry a structured denial, got outcome %#v", outcome)
	}
	if outcome.Denial.Capability.Kind != execution.CapabilityExternalNetwork {
		t.Fatalf("denial capability = %q, want external_network", outcome.Denial.Capability.Kind)
	}
}
