package cli

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

func TestPreflightRejectsSideSessions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	store := sessions.NewStore(sessions.StoreOptions{})
	if _, err := store.Create(sessions.CreateInput{SessionID: "side", SessionKind: sessions.SessionKindSide}); err != nil {
		t.Fatalf("create side session: %v", err)
	}

	err := preflightExecSession(execOptions{resumeLatest: true})
	if err == nil || !strings.Contains(err.Error(), "No QG sessions available to resume") {
		t.Fatalf("preflightExecSession error = %v, want no resumable sessions", err)
	}

	err = preflightExecSession(execOptions{resume: "side"})
	if err == nil || !strings.Contains(err.Error(), "QG session is not resumable: side") {
		t.Fatalf("explicit side-session preflight error = %v, want non-resumable rejection", err)
	}
}
