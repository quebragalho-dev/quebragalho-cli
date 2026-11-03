package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/update"
)

// TestRunUpgradeHandoffPendingTextAndJSON pins the `qg upgrade` handoff
// contract: without --json the pending line goes to stdout; with --json
// stdout stays empty for the reopened process's single final JSON while the
// pending line goes to stderr.
func TestRunUpgradeHandoffPendingTextAndJSON(t *testing.T) {
	pending := update.ApplyResult{
		Result: update.Result{
			CurrentVersion:  "0.1.0",
			LatestVersion:   "0.2.0",
			UpdateAvailable: true,
		},
		Applied:        false,
		HandoffPending: true,
		InstallMethod:  update.InstallMethodNpm,
		BinaryPath:     "/tmp/qg",
		Message:        "handed off npm update to wrapper for 0.2.0",
	}
	deps := appDeps{
		applyUpdate: func(context.Context, update.Options) (update.ApplyResult, error) {
			return pending, nil
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := runWithDeps([]string{"upgrade"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "termina quando o qg fechar") {
		t.Fatalf("text output = %q, want the pt-BR pending line", stdout.String())
	}
	if strings.Contains(stdout.String(), "atualizado") {
		t.Fatalf("pending text must not claim success: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runWithDeps([]string{"upgrade", "--json"}, &stdout, &stderr, deps); code != exitSuccess {
		t.Fatalf("json exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Fatalf("pending --json must leave stdout empty for the final JSON, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "termina quando o qg fechar") {
		t.Fatalf("pending --json must print the pt-BR line to stderr, got %q", stderr.String())
	}
}
