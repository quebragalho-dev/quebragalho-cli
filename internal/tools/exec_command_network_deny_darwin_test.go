//go:build darwin

package tools

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

// TestExecCommandNetworkDenialPromptsRetry mirrors
// TestBashNetworkDenialPromptsRetry for exec_command: an unclassified network
// command (a python socket connect, not curl/npm) through the real seatbelt
// sandbox with a deny-network policy must carry the structured
// external-network denial the agent loop uses to offer a permission retry.
// Policy.MonitorDenials is deliberately NOT set: the deny-network profile
// must tag the run itself.
//
// The command connects to a literal IP on purpose: a hostname forces the
// failure through DNS/getaddrinfo, which on some interpreters can die on an
// ancillary denial before ever attempting the socket, so the tagged network
// rule never fires and there is nothing to attribute. A raw connect always
// reaches the socket denial first, deterministically.
func TestExecCommandNetworkDenialPromptsRetry(t *testing.T) {
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
	tool := NewScopedExecCommandTool(root, nil, newExecSessionManager()).(interface {
		RunWithSandbox(context.Context, map[string]any, *sandbox.Engine) Result
	})
	result := tool.RunWithSandbox(ctx, map[string]any{
		"cmd": "python3 -c \"import socket; socket.create_connection(('93.184.216.34', 443), timeout=5)\"",
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

// TestExecCommandNetworkDenialFallbackOutputSignature mirrors the bash output
// fallback wiring for exec_command: with no tag hit, a failed deny-network
// session whose output carries an unequivocal blocked-network signature must
// still offer the network retry. Deterministic and offline.
func TestExecCommandNetworkDenialFallbackOutputSignature(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skipf("sandbox-exec unavailable: %v", err)
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
	tool := NewScopedExecCommandTool(root, nil, newExecSessionManager()).(interface {
		RunWithSandbox(context.Context, map[string]any, *sandbox.Engine) Result
	})
	result := tool.RunWithSandbox(ctx, map[string]any{
		"cmd": "sh -c 'echo Could not resolve host: example.com >&2; exit 1'",
	}, engine)
	if result.Status != StatusError {
		t.Fatalf("failed command must fail, got %s: %s", result.Status, result.Output)
	}
	outcome := result.ExecutionOutcome
	if outcome == nil || outcome.Denial == nil {
		t.Fatalf("output fallback must carry a structured denial, got outcome %#v", outcome)
	}
	if outcome.Denial.Capability.Kind != execution.CapabilityExternalNetwork {
		t.Fatalf("denial capability = %q, want external_network", outcome.Denial.Capability.Kind)
	}
}

// TestExecCommandNetworkDenialFallbackExitZero pins that a successful session
// never offers the network retry, even when its output contains a signature.
func TestExecCommandNetworkDenialFallbackExitZero(t *testing.T) {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skipf("sandbox-exec unavailable: %v", err)
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
	tool := NewScopedExecCommandTool(root, nil, newExecSessionManager()).(interface {
		RunWithSandbox(context.Context, map[string]any, *sandbox.Engine) Result
	})
	result := tool.RunWithSandbox(ctx, map[string]any{
		"cmd": "sh -c 'echo Could not resolve host: example.com >&2; exit 0'",
	}, engine)
	outcome := result.ExecutionOutcome
	if outcome != nil && outcome.Denial != nil {
		t.Fatalf("exit 0 must never offer a network retry, got %#v", outcome.Denial)
	}
}
