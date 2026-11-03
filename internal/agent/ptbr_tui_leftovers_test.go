package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// Item 1 guard: the escalated-shell fallback stays canonical English at the
// agent layer (model, JSON and decision log). Only the TUI card translates it,
// via permissionDisplayReason.
func TestPTBRUserFacingEscalatedFallbackStaysCanonical(t *testing.T) {
	const canonical = "This command needs to run outside the sandbox."

	got := userFacingPermissionReason("exec_command", map[string]any{
		"command":             "ps aux",
		"sandbox_permissions": "require_escalated",
	}, "unsandboxed shell command requires approval", "unsandboxed shell command requires approval")
	if got != canonical {
		t.Fatalf("userFacingPermissionReason = %q, want canonical %q", got, canonical)
	}

	event := PermissionEvent{ToolName: "exec_command", Reason: got, DecisionReason: "unsandboxed shell command requires approval"}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), canonical) {
		t.Fatalf("permission JSON lost canonical reason: %s", raw)
	}
	for _, wantPT := range []string{"fora do ambiente isolado", "Este comando precisa rodar"} {
		if strings.Contains(string(raw), wantPT) {
			t.Fatalf("permission JSON must not carry the display translation: %s", raw)
		}
	}
}
