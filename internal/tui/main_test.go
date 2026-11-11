package tui

import (
	"os"
	"testing"
)

// TestMain forces synchronous snapshot writes for the whole TUI suite: with
// the async writer every test that runs a turn would need to drain the
// background loop before TempDir cleanup, and any miss flakes with
// "directory not empty". Sync mode keeps the runID gate and failure
// accounting identical; async behavior is covered by the writer unit tests.
func TestMain(m *testing.M) {
	syncSnapshotWrites = true
	os.Exit(m.Run())
}
