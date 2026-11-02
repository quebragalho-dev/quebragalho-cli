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

// TestBashNetworkDenialPromptsRetry runs an unclassified network command
// (python urllib, not curl/npm) through the real seatbelt sandbox with a
// deny-network policy and requires the structured external-network denial the
// agent loop uses to offer a permission retry. Policy.MonitorDenials is
// deliberately NOT set: the deny-network profile must tag the run itself.
//
// The `log stream` capture is best-effort: a command that fails in
// milliseconds can emit its denials before the stream attaches (measured
// attach is ~15-30ms). Each attempt is side-effect-free (the network call
// fails), so the test retries a bounded number of fresh runs and fails only
// when none attributes the denial.
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
	const attempts = 3
	for attempt := 1; attempt <= attempts; attempt++ {
		result := tool.RunWithSandbox(ctx, map[string]any{
			"command": "python3 -c \"import urllib.request; print(urllib.request.urlopen('https://example.com', timeout=5).status)\"",
		}, engine)
		if result.Status != StatusError {
			t.Fatalf("denied network must fail, got %s: %s", result.Status, result.Output)
		}
		outcome := result.ExecutionOutcome
		if outcome != nil && outcome.Denial != nil &&
			outcome.Denial.Capability.Kind == execution.CapabilityExternalNetwork {
			return
		}
		t.Logf("attempt %d/%d: no structured network denial yet (outcome %#v)", attempt, attempts, outcome)
	}
	t.Fatal("denied network must carry a structured external_network denial after 3 attempts")
}
