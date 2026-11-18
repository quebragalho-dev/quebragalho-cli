//go:build windows

package sandbox

import (
	"testing"
)

// THE POLICY SEES EVERY DRIVE, NOT JUST THE CURRENT ONE.
func TestPolicyFilesystemReadRootsCoversAllDrives(t *testing.T) {
	drives := windowsLogicalDriveRoots()
	if len(drives) == 0 {
		t.Skip("no visible drives to enumerate")
	}
	roots := policyFilesystemReadRootsForGOOS("windows")
	if len(roots) != len(drives) {
		t.Fatalf("policyFilesystemReadRoots() = %q, want the %d visible drive roots %q", roots, len(drives), drives)
	}
	seen := map[string]bool{}
	for _, root := range roots {
		seen[root] = true
		if !isFilesystemRootPath(root) {
			t.Errorf("policy read root %q is not a filesystem root", root)
		}
	}
	for _, drive := range drives {
		if !seen[drive] {
			t.Errorf("visible drive %q missing from the policy read roots %q", drive, roots)
		}
	}
}
