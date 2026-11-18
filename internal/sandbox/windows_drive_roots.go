package sandbox

import (
	"path/filepath"
	"strings"
)

// windowsDriveRootsFromBitmask turns a GetLogicalDrives bitmask into the
// drive-root list ("C:\", ...), lowest letter first. It is pure so the mapping
// is testable on any platform; the bitmask itself comes from the OS-specific
// windowsLogicalDriveRoots.
func windowsDriveRootsFromBitmask(mask uint32) []string {
	var out []string
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, string(rune('A'+i))+`:\`)
		}
	}
	return out
}

// policyFilesystemReadRootsForGOOS is the read-all half of the
// read-all/write-jail posture for a target platform: on Windows it spans every
// mounted drive root, not just the current drive's ("\"). The
// roots are policy only — the Windows ACL plan deliberately grants no read on
// drive roots (an inheritable allow on C:\ would stamp an ACE on every file of
// the drive), so reads outside granted roots fail closed under the fully
// restricted token instead.
func policyFilesystemReadRootsForGOOS(targetGOOS string) []string {
	if targetGOOS == "windows" {
		if drives := windowsLogicalDriveRoots(); len(drives) > 0 {
			return drives
		}
		return []string{`\`}
	}
	return []string{profileRootPath()}
}

// isFilesystemRootPath reports whether path is a filesystem root that must
// never become a command-allowed root: "/" on every platform plus Windows
// drive roots ("C:\", recognized lexically so the shared profile builder gets
// it right). Admitting a filesystem root there would drop every credential
// deny on that drive, because the credential baseline excludes denies
// overlapping a command-allowed root. The current-drive spelling ("\") stays
// covered by the profileRootPath() comparison at the call site.
func isFilesystemRootPath(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || trimmed == "." {
		return false
	}
	// A bare drive letter ("C:") is drive-relative — the current directory of
	// that drive — never a root. filepath.Dir reports it as its own parent on
	// Windows, so it needs the explicit carve-out.
	if len(trimmed) == 2 && trimmed[1] == ':' && isASCIIDriveLetter(trimmed[0]) {
		return false
	}
	clean := filepath.Clean(trimmed)
	if filepath.Dir(clean) == clean {
		return true
	}
	return len(clean) == 3 && isASCIIDriveLetter(clean[0]) && clean[1] == ':' && (clean[2] == '\\' || clean[2] == '/')
}

func isASCIIDriveLetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
