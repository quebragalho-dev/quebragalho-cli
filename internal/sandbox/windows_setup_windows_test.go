//go:build windows

package sandbox

import (
	"os"
	"path/filepath"
	"testing"
)

// A SETUP DONE ELSEWHERE MUST NOT BRICK THIS FOLDER: THE RUNNER HEALS IT.
//
// The marker holds another workspace's plan only. ensureWindowsSandboxSetup
// applies this workspace's plan on demand (user-owned paths need no
// elevation), records it, and from then on the command validates.
func TestWindowsEnsureSandboxSetupHealsAnotherWorkspace(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	setupA := WindowsSandboxSetupConfig{
		SandboxHome:    home,
		CommandCWD:     workspaceA,
		WorkspaceRoots: []string{workspaceA},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{Kind: FileSystemRestricted, WriteRoots: []WritableRoot{{Root: workspaceA}}},
			Network:    NetworkPolicy{Mode: NetworkDeny},
		},
	}
	if _, err := WriteWindowsSandboxSetupMarker(setupA); err != nil {
		t.Fatalf("WriteWindowsSandboxSetupMarker A: %v", err)
	}
	commandB := WindowsSandboxCommandConfig{
		SandboxHome:    home,
		CommandCWD:     workspaceB,
		WorkspaceRoots: []string{workspaceB},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: workspaceB, ProtectedMetadataNames: []string{".qg"}}},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
		SandboxLevel: WindowsSandboxLevelRestrictedToken,
	}
	setupB := WindowsSandboxSetupConfigFromCommand(commandB)
	if err := ValidateWindowsSandboxSetupMarker(setupB); !IsWindowsSandboxSetupPlanMismatch(err) {
		t.Fatalf("ValidateWindowsSandboxSetupMarker B = %v, want a healable plan-mismatch", err)
	}
	if err := ensureWindowsSandboxSetup(commandB); err != nil {
		t.Fatalf("ensureWindowsSandboxSetup B: %v", err)
	}
	if err := ValidateWindowsSandboxSetupMarker(setupB); err != nil {
		t.Fatalf("ValidateWindowsSandboxSetupMarker B after ensure: %v", err)
	}
	// The on-demand apply is a real apply: .qg was materialized and denied.
	qgDir := filepath.Join(workspaceB, ".qg")
	if info, err := os.Stat(qgDir); err != nil || !info.IsDir() {
		t.Fatalf("on-demand apply did not materialize %s: %v", qgDir, err)
	}
}

// A CLEANED-UP RUNTIME TREE MUST COME BACK GRANTED, NOT JUST RECREATED.
//
// Deleting the QG runtime dirs (30-day cleanup) and letting them be recreated
// without the capability ACE leaves TEMP unwritable with a valid marker. The
// ensure step recreates whatever is missing and re-grants the write-root
// capability allow ACE; a second run is a no-op.
func TestWindowsEnsureSandboxRuntimeDirsRecreatesGrants(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	root := filepath.Join(home, "runtime")
	runtimeState := SandboxRuntime{
		Root:  root,
		Cache: filepath.Join(root, "cache"),
		Data:  filepath.Join(root, "data"),
		Temp:  filepath.Join(root, "tmp"),
	}
	config := WindowsSandboxCommandConfig{
		SandboxHome:    home,
		WorkspaceRoots: []string{workspace},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: workspace}, {Root: root}},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
	}
	config.PermissionProfile.Runtime = &runtimeState

	// Whole tree missing (post-cleanup state).
	if err := ensureWindowsSandboxRuntimeDirs(config); err != nil {
		t.Fatalf("ensureWindowsSandboxRuntimeDirs (missing tree): %v", err)
	}
	sid, err := windowsCapabilitySIDForWriteRoot(config, root)
	if err != nil {
		t.Fatalf("windowsCapabilitySIDForWriteRoot: %v", err)
	}
	for _, dir := range []string{root, runtimeState.Cache, runtimeState.Data, runtimeState.Temp} {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Fatalf("runtime dir %s was not recreated: %v", dir, err)
		}
		if granted, err := windowsDACLGrantsWriteSID(dir, sid); err != nil || !granted {
			t.Fatalf("runtime dir %s granted = %v, %v; want the capability allow ACE", dir, granted, err)
		}
	}

	// Root deleted, children revived without a grant: the recreate must inherit
	// (or re-stamp) the grant, and the rerun must stay quiet.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("RemoveAll runtime root: %v", err)
	}
	if err := os.MkdirAll(runtimeState.Temp, 0o700); err != nil {
		t.Fatalf("revive Temp without grants: %v", err)
	}
	if err := ensureWindowsSandboxRuntimeDirs(config); err != nil {
		t.Fatalf("ensureWindowsSandboxRuntimeDirs (stale tree): %v", err)
	}
	for _, dir := range []string{root, runtimeState.Temp} {
		if granted, err := windowsDACLGrantsWriteSID(dir, sid); err != nil || !granted {
			t.Fatalf("runtime dir %s granted = %v, %v after stale-tree ensure", dir, granted, err)
		}
	}
	if err := ensureWindowsSandboxRuntimeDirs(config); err != nil {
		t.Fatalf("ensureWindowsSandboxRuntimeDirs (idempotent rerun): %v", err)
	}
}
