package tools

import (
	"path/filepath"
	"strings"
	"testing"
)

// Pins the decision for a target that has no path relative to the workspace
// root (another Windows volume): refused when confined, passed through with
// its absolute slash path in an unsafe run.
func TestCrossVolumeTargetIsRefusedUnlessUnsafe(t *testing.T) {
	target := filepath.Join(t.TempDir(), "elsewhere", "file.txt")

	if _, _, err := crossVolumeTarget(target, "requested.txt", false); err == nil || !strings.Contains(err.Error(), "requested.txt must stay inside the workspace") {
		t.Fatalf("confined cross-volume target = %v, want the outside-workspace refusal", err)
	}

	absolute, relative, err := crossVolumeTarget(target, "requested.txt", true)
	if err != nil {
		t.Fatalf("unsafe cross-volume target: %v", err)
	}
	if absolute != target || relative != filepath.ToSlash(target) {
		t.Fatalf("unsafe cross-volume target = %q, %q; want %q and its slash form", absolute, relative, target)
	}
}
