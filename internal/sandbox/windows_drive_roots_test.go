package sandbox

import (
	"testing"
)

// EVERY DRIVE ROOT IS A READ ROOT ON WINDOWS, NOT JUST THE CURRENT DRIVE.
//
// The sandbox posture is read-all/write-jail: reads are unrestricted under
// WRITE_RESTRICTED and granted per-root under the fully restricted token. A
// profile rooted at "\" covers only the current drive, hiding every other
// mounted drive from the policy. The mapper below turns the GetLogicalDrives
// bitmask into the drive-root list; the empty mask maps to nothing (the caller
// falls back to the current-drive root).
func TestWindowsDriveRootsFromBitmask(t *testing.T) {
	if roots := windowsDriveRootsFromBitmask(0); len(roots) != 0 {
		t.Fatalf("empty mask = %q, want no drives", roots)
	}
	roots := windowsDriveRootsFromBitmask(0b1011)
	want := []string{`A:\`, `B:\`, `D:\`}
	if len(roots) != len(want) {
		t.Fatalf("mask 0b1011 = %q, want %q", roots, want)
	}
	for i := range want {
		if roots[i] != want[i] {
			t.Fatalf("mask 0b1011 = %q, want %q", roots, want)
		}
	}
}

// FILESYSTEM ROOTS MUST NEVER BECOME COMMAND-ALLOWED ROOTS.
//
// The credential baseline drops every deny overlapping a command-allowed root,
// so admitting a filesystem root there ("/", "\", "C:\") would silently drop
// every credential deny on that drive. The profile-root exclusion already
// covers the current drive's spelling; this pins the drive-root shapes too.
func TestIsFilesystemRootPath(t *testing.T) {
	for _, root := range []string{"/", `C:\`, `D:\`} {
		if !isFilesystemRootPath(root) {
			t.Errorf("isFilesystemRootPath(%q) = false, want true", root)
		}
	}
	// UNC roots are platform-shaped (a root on Windows, a filename elsewhere);
	// they are covered by the Windows-only test.
	for _, notRoot := range []string{"", ".", "/workspace", `C:\workspace`, `C:`} {
		if isFilesystemRootPath(notRoot) {
			t.Errorf("isFilesystemRootPath(%q) = true, want false", notRoot)
		}
	}
}
