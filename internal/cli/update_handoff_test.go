package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/update"
)

// TestRunUpgradeHandoffPendingTextAndJSON pins the `qg upgrade` handoff
// contract: the pending result exits 0 with the pt-BR pending line, and
// --json stays honest (applied:false with a clear pending status).
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
	var payload struct {
		Applied        bool   `json:"applied"`
		HandoffPending bool   `json:"handoffPending"`
		Message        string `json:"message"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("pending JSON did not decode: %v\n%s", err, stdout.String())
	}
	if payload.Applied || !payload.HandoffPending {
		t.Fatalf("pending JSON must be applied:false handoffPending:true, got %+v", payload)
	}
	if strings.Contains(payload.Message, "atualizado") {
		t.Fatalf("pending JSON message must stay English, got %q", payload.Message)
	}
}
