package sandbox

import (
	"path/filepath"
	"testing"
)

// DenyRead profiles are SUPPORTED on both restricted-token runner levels: they
// run under the fully restricted token (no WRITE_RESTRICTED, so denies apply
// to reads per #612), which launches because Everyone is back in the
// restricted list and the plan grants capability-SID read access on the
// workspace. The plan builder must therefore emit both the deny and the
// workspace read grant for such a profile.
func TestWindowsDenyReadProfileBuildsEnforcingPlan(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	secret := filepath.Join(workspace, "secret.txt")
	plan, err := BuildWindowsACLPlan(WindowsSandboxCommandConfig{
		SandboxHome:    home,
		WorkspaceRoots: []string{workspace},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: workspace}},
				DenyRead:   []string{secret},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
	})
	if err != nil {
		t.Fatalf("BuildWindowsACLPlan: %v", err)
	}
	sid, err := WindowsWorkspaceCapabilitySID(home, workspace)
	if err != nil {
		t.Fatalf("WindowsWorkspaceCapabilitySID: %v", err)
	}
	assertWindowsACLEntry(t, plan, WindowsACLDenyRead, secret, sid, true)
	assertWindowsACLEntry(t, plan, WindowsACLAllowRead, workspace, sid, false)
}
