package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// otherVolume returns the root of a volume other than the one root lives on,
// or skips: on a single-volume machine nothing is outside the workspace's drive.
func otherVolume(t *testing.T, root string) string {
	t.Helper()
	own := strings.ToUpper(filepath.VolumeName(root))
	for letter := 'A'; letter <= 'Z'; letter++ {
		volume := string(letter) + ":"
		if volume == own {
			continue
		}
		if info, err := os.Stat(volume + `\`); err == nil && info.IsDir() {
			return volume + `\`
		}
	}
	t.Skip("single-volume machine: no path outside the workspace's drive to test with")
	return ""
}

// Pins the three resolvers on a real second volume, where filepath.Rel fails:
// a confined run refuses the path, an unsafe run reaches it.
func TestPathOnAnotherVolumeFollowsTheUnsafeRule(t *testing.T) {
	root := t.TempDir()
	other := otherVolume(t, root)
	missing := filepath.Join(other, "qg-cross-volume-test-does-not-exist", "file.txt")
	const refusal = "must stay inside the workspace"

	if _, _, err := resolveWorkspacePath(root, other, false); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("confined read on %s = %v, want the outside-workspace refusal", other, err)
	}
	if _, _, err := resolveWorkspaceTargetPath(root, missing, false); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("confined write target on %s = %v, want the outside-workspace refusal", other, err)
	}
	if err := recheckWorkspaceWriteTarget(root, missing, false); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("confined write recheck on %s = %v, want the outside-workspace refusal", other, err)
	}

	absolute, relative, err := resolveWorkspacePath(root, other, true)
	if err != nil {
		t.Fatalf("unsafe read on %s: %v", other, err)
	}
	if !strings.EqualFold(filepath.VolumeName(absolute), filepath.VolumeName(other)) || relative != filepath.ToSlash(absolute) {
		t.Fatalf("unsafe read on %s = %q, %q; want a path on that volume and its slash form", other, absolute, relative)
	}
	absolute, relative, err = resolveWorkspaceTargetPath(root, missing, true)
	if err != nil {
		t.Fatalf("unsafe write target on %s: %v", other, err)
	}
	if !strings.EqualFold(absolute, missing) || relative != filepath.ToSlash(absolute) {
		t.Fatalf("unsafe write target = %q, %q; want %q and its slash form", absolute, relative, missing)
	}
	if err := recheckWorkspaceWriteTarget(root, missing, true); err != nil {
		t.Fatalf("unsafe write recheck on %s: %v", other, err)
	}
}
