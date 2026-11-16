package tui

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// releaseSessionLocksUnder registers a cleanup that drops every session
// presence lease held under root before the test's temp dirs are removed.
// TUI tests acquire presence leases (a session start takes one through
// TryAcquireSessionLock) that outlive the test body: nothing releases them
// on the way out. On Unix an open file can be unlinked, so the suite stays
// green; on Windows an open, locked presence.lock cannot be unlinked, so
// t.TempDir() cleanup fails the test. Register after the temp dir (or the
// store under it) exists: t.Cleanup runs last-in first-out, so this runs
// before the removal. Path-scoped
// (sessions.ReleaseSessionLocksUnderForTest), so parallel tests stay
// independent, and idempotent, so stores shared by several helpers release
// only once.
func releaseSessionLocksUnder(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		sessions.ReleaseSessionLocksUnderForTest(root)
	})
}
