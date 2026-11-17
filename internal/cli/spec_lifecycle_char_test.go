package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// Characterization of the CLI guards (commit A): byte-identical messages that
// must not change when the store matrix lands.
func TestSpecLifecycleCharCLIRefusals(t *testing.T) {
	store, deps, draft := seedSpecDraft(t)

	// Approve the draft so it carries an implementation session.
	var first bytes.Buffer
	if exitCode := runWithDeps([]string{"spec", "approve", draft.SessionID, "--json"}, &first, &bytes.Buffer{}, deps); exitCode != exitSuccess {
		t.Fatalf("first approve exit = %d", exitCode)
	}
	updated, err := store.Get(draft.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SpecStatus != sessions.SpecStatusApproved || updated.SpecImplSessionID == "" {
		t.Fatalf("precondition: %#v", updated)
	}
	// Reject of approved+impl is refused by the CLI with the current message.
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := runWithDeps([]string{"spec", "reject", draft.SessionID}, &stdout, &stderr, deps); exitCode == exitSuccess {
		t.Fatal("reject of approved+impl must fail")
	} else {
		combined := stdout.String() + stderr.String()
		if !strings.Contains(combined, "qg spec reject cannot reject an approved spec with an implementation session") {
			t.Fatalf("reject message changed: %q", combined)
		}
	}

	// Approve of a rejected spec is refused by the CLI with the current message.
	store2, deps2, draft2 := seedSpecDraft(t)
	var rstdout bytes.Buffer
	if exitCode := runWithDeps([]string{"spec", "reject", draft2.SessionID}, &rstdout, &bytes.Buffer{}, deps2); exitCode != exitSuccess {
		t.Fatalf("reject exit = %d", exitCode)
	}
	var astdout bytes.Buffer
	var astderr bytes.Buffer
	if exitCode := runWithDeps([]string{"spec", "approve", draft2.SessionID}, &astdout, &astderr, deps2); exitCode == exitSuccess {
		t.Fatal("approve of rejected must fail")
	} else {
		combined := astdout.String() + astderr.String()
		if !strings.Contains(combined, "qg spec approve cannot approve a rejected spec") {
			t.Fatalf("approve message changed: %q", combined)
		}
	}
	_ = store2
}
