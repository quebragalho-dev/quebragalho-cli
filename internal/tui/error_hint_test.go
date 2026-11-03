package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/errhint"
)

// A hinted error row renders the raw error and the faint one-line next step
// below it; an unhinted row renders just the error.
func TestRenderErrorRowShowsHint(t *testing.T) {
	withHint := renderErrorRow(transcriptRow{
		kind: rowError,
		text: "auth error: your API key is missing or invalid",
		hint: "API key rejected — run /provider to re-check your credentials",
	}, 80)
	if !strings.Contains(withHint, "auth error:") {
		t.Fatalf("expected raw error text in output, got:\n%s", withHint)
	}
	if !strings.Contains(withHint, "/provider") {
		t.Fatalf("expected hint referencing /provider, got:\n%s", withHint)
	}

	noHint := renderErrorRow(transcriptRow{kind: rowError, text: "provider error: mystery"}, 80)
	if strings.Contains(noHint, "→") {
		t.Fatalf("unhinted error must not render a hint arrow, got:\n%s", noHint)
	}
}

func TestRenderErrorRowModelNotAllowed(t *testing.T) {
	raw := "provider request error: model not allowed for this account"
	hint := errhint.TUIHint(errors.New(raw))
	if hint == "" || strings.Contains(strings.ToLower(hint), "auth") {
		t.Fatalf("TUIHint(%q) = %q, want the generic model-unavailable hint", raw, hint)
	}
	rendered := renderErrorRow(transcriptRow{kind: rowError, text: raw, hint: hint}, 80)
	if !strings.Contains(rendered, raw) {
		t.Fatalf("expected raw error text in output, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, hint) {
		t.Fatalf("expected model hint in output, got:\n%s", rendered)
	}
}
