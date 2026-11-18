package sandbox

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildAndParseWindowsSandboxSetupArgs(t *testing.T) {
	home := t.TempDir()
	args, err := BuildWindowsSandboxSetupArgs(WindowsSandboxSetupArgsOptions{
		SandboxHome:    home,
		CommandCWD:     `C:\workspace\src`,
		WorkspaceRoots: []string{`C:\workspace`},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind: FileSystemRestricted,
				WriteRoots: []WritableRoot{
					{Root: `C:\workspace`, ProtectedMetadataNames: []string{".git"}},
				},
				DenyRead: []string{`C:\workspace\secret`},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
	})
	if err != nil {
		t.Fatalf("BuildWindowsSandboxSetupArgs: %v", err)
	}
	config, err := ParseWindowsSandboxSetupArgs(args)
	if err != nil {
		t.Fatalf("ParseWindowsSandboxSetupArgs: %v", err)
	}
	if config.SandboxHome != home || config.CommandCWD != `C:\workspace\src` || len(config.WorkspaceRoots) != 1 || config.WorkspaceRoots[0] != `C:\workspace` {
		t.Fatalf("setup config = %#v, want sandbox home, command cwd, and workspace root", config)
	}
	if config.PermissionProfile.FileSystem.Kind != FileSystemRestricted || len(config.PermissionProfile.FileSystem.DenyRead) != 1 {
		t.Fatalf("permission profile = %#v, want restricted deny-read profile", config.PermissionProfile)
	}
}

func TestWindowsSandboxSetupPathForRunner(t *testing.T) {
	got := WindowsSandboxSetupPathForRunner(filepath.Join("C:", "qg", WindowsSandboxCommandRunnerName))
	want := filepath.Join("C:", "qg", WindowsSandboxSetupName)
	if got != want {
		t.Fatalf("WindowsSandboxSetupPathForRunner = %q, want %q", got, want)
	}
	if got := WindowsSandboxSetupPathForRunner(""); got != "" {
		t.Fatalf("empty runner setup path = %q, want empty", got)
	}
}

func TestRunWindowsSandboxSetupRejectsInvalidArgs(t *testing.T) {
	var stderr bytes.Buffer
	code := RunWindowsSandboxSetup([]string{"--command-cwd", `C:\workspace`}, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want usage error", code)
	}
	if !strings.Contains(stderr.String(), WindowsSandboxSetupName) {
		t.Fatalf("stderr = %q, want setup helper name", stderr.String())
	}
}

func TestRunWindowsSandboxSetupAcceptsDenyReadProfiles(t *testing.T) {
	home := t.TempDir()
	args, err := BuildWindowsSandboxSetupArgs(WindowsSandboxSetupArgsOptions{
		SandboxHome:    home,
		CommandCWD:     `C:\workspace\src`,
		WorkspaceRoots: []string{`C:\workspace`},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: `C:\workspace`}},
				DenyRead:   []string{`C:\workspace\secret`},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
	})
	if err != nil {
		t.Fatalf("BuildWindowsSandboxSetupArgs: %v", err)
	}
	var stderr bytes.Buffer
	code := RunWindowsSandboxSetup(args, &stderr)
	if code == 2 {
		t.Fatalf("exit code = 2, want argument parsing to accept a DenyRead profile: %s", stderr.String())
	}
	if strings.Contains(stderr.String(), "DenyRead is not supported") {
		t.Fatalf("stderr = %q, DenyRead profiles are supported since Everyone returned to the restricted list", stderr.String())
	}
}

func TestWindowsSandboxSetupMarkerRefreshesWhenProfileChanges(t *testing.T) {
	config := WindowsSandboxSetupConfig{
		SandboxHome:    t.TempDir(),
		CommandCWD:     `C:\workspace`,
		WorkspaceRoots: []string{`C:\workspace`},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: `C:\workspace`}},
				DenyRead:   []string{`C:\workspace\secret-read`},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
	}
	if _, err := WriteWindowsSandboxSetupMarker(config); err != nil {
		t.Fatalf("WriteWindowsSandboxSetupMarker: %v", err)
	}
	if err := ValidateWindowsSandboxSetupMarker(config); err != nil {
		t.Fatalf("ValidateWindowsSandboxSetupMarker unchanged: %v", err)
	}

	changed := config
	changed.PermissionProfile.FileSystem.DenyRead = []string{`C:\workspace\other-secret`}
	err := ValidateWindowsSandboxSetupMarker(changed)
	if err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("ValidateWindowsSandboxSetupMarker changed error = %v, want out of date", err)
	}
}

// One setup marker must validly serve BOTH network modes: an approved (allow)
// network command and an ordinary (deny) command both validate against the same
// setup. This is the fix for the "network policy changed" brick that hit every
// approved network command (curl, git push, …). The per-command mode is enforced
// at runtime by the token's SID set, not by which marker exists.
func TestWindowsSandboxSetupMarkerValidatesBothNetworkModes(t *testing.T) {
	deny := WindowsSandboxSetupConfig{
		SandboxHome:    t.TempDir(),
		CommandCWD:     `C:\workspace`,
		WorkspaceRoots: []string{`C:\workspace`},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: `C:\workspace`}},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
	}
	// Set up once (as a deny command would).
	if _, err := WriteWindowsSandboxSetupMarker(deny); err != nil {
		t.Fatalf("WriteWindowsSandboxSetupMarker: %v", err)
	}
	if err := ValidateWindowsSandboxSetupMarker(deny); err != nil {
		t.Fatalf("deny command must validate against its own setup: %v", err)
	}
	// An APPROVED (allow) command must ALSO validate against the SAME setup —
	// previously this bricked with "network policy changed".
	allow := deny
	allow.PermissionProfile.Network = NetworkPolicy{Mode: NetworkAllow}
	if err := ValidateWindowsSandboxSetupMarker(allow); err != nil {
		t.Fatalf("approved (allow) command must validate against the deny setup, got: %v", err)
	}
}

// A pre-v5 marker on disk must be rejected as out of date so the schema bump
// forces a clean re-setup (old markers had legacy DenyWrite ACEs).
func TestWindowsSandboxSetupMarkerRejectsOldSchema(t *testing.T) {
	config := WindowsSandboxSetupConfig{
		SandboxHome:    t.TempDir(),
		CommandCWD:     `C:\workspace`,
		WorkspaceRoots: []string{`C:\workspace`},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{Kind: FileSystemRestricted, WriteRoots: []WritableRoot{{Root: `C:\workspace`}}},
			Network:    NetworkPolicy{Mode: NetworkDeny},
		},
	}
	marker, err := BuildWindowsSandboxSetupMarker(config)
	if err != nil {
		t.Fatalf("BuildWindowsSandboxSetupMarker: %v", err)
	}
	marker.SchemaVersion = 4
	bytes, err := json.Marshal(marker)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(WindowsSandboxSetupMarkerPath(config.SandboxHome), bytes, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	err = ValidateWindowsSandboxSetupMarker(config)
	if err == nil || !strings.Contains(err.Error(), "out of date") {
		t.Fatalf("schema-4 marker must be out of date, got: %v", err)
	}
}

func TestWindowsSandboxSetupConfigFromCommandPreservesProfileInputs(t *testing.T) {
	command := WindowsSandboxCommandConfig{
		SandboxHome:    t.TempDir(),
		CommandCWD:     `C:\workspace\src`,
		WorkspaceRoots: []string{`C:\workspace`},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: `C:\workspace`}},
				DenyRead:   []string{`C:\workspace\secret`},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
		Env:     map[string]string{"QG_SANDBOXED": "1"},
		Command: []string{"cmd.exe", "/c", "dir"},
	}
	setup := WindowsSandboxSetupConfigFromCommand(command)
	if setup.SandboxHome != command.SandboxHome || setup.CommandCWD != command.CommandCWD || len(setup.WorkspaceRoots) != 1 || setup.WorkspaceRoots[0] != `C:\workspace` {
		t.Fatalf("setup config = %#v, want command roots", setup)
	}
	if setup.PermissionProfile.FileSystem.Kind != FileSystemRestricted || len(setup.PermissionProfile.FileSystem.DenyRead) != 1 {
		t.Fatalf("setup profile = %#v, want command permission profile", setup.PermissionProfile)
	}
}

func TestWindowsACLPlanHashIsStableAcrossEntryOrder(t *testing.T) {
	left, err := WindowsACLPlanHash(WindowsACLPlan{Entries: []WindowsACLEntry{
		{Action: WindowsACLDenyRead, Path: `C:\workspace\secret`, Capability: "S-1-5-21-3", Materialize: true},
		{Action: WindowsACLAllowWrite, Path: `C:\workspace`, Capability: "S-1-5-21-1"},
		{Action: WindowsACLDenyWrite, Path: `C:\workspace\.git`, Capability: "S-1-5-21-1"},
	}})
	if err != nil {
		t.Fatalf("WindowsACLPlanHash left: %v", err)
	}
	right, err := WindowsACLPlanHash(WindowsACLPlan{Entries: []WindowsACLEntry{
		{Action: WindowsACLAllowWrite, Path: `c:/workspace`, Capability: "s-1-5-21-1"},
		{Action: WindowsACLDenyWrite, Path: `c:/workspace/.git`, Capability: "S-1-5-21-1"},
		{Action: WindowsACLDenyRead, Path: `c:/workspace/secret`, Capability: "S-1-5-21-3", Materialize: true},
	}})
	if err != nil {
		t.Fatalf("WindowsACLPlanHash right: %v", err)
	}
	if left != right {
		t.Fatalf("ACL plan hashes differ: %q vs %q", left, right)
	}
}

// ONE SETUP MUST SERVE EVERY WORKSPACE: THE MARKER RECORDS APPLIED PLANS, NOT
// ONE PLAN.
//
// Setup in folder A fingerprints A's workspace plan. A command in folder B
// builds a different plan; instead of bricking with "out of date", the runner
// applies B's plan on demand (user-owned paths need no elevation) and records
// it, so the next B command validates without re-applying.
func TestWindowsSandboxSetupMarkerServesMultipleWorkspaces(t *testing.T) {
	home := t.TempDir()
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	configA := WindowsSandboxSetupConfig{
		SandboxHome:    home,
		CommandCWD:     workspaceA,
		WorkspaceRoots: []string{workspaceA},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{Kind: FileSystemRestricted, WriteRoots: []WritableRoot{{Root: workspaceA}}},
			Network:    NetworkPolicy{Mode: NetworkDeny},
		},
	}
	if _, err := WriteWindowsSandboxSetupMarker(configA); err != nil {
		t.Fatalf("WriteWindowsSandboxSetupMarker A: %v", err)
	}
	if err := ValidateWindowsSandboxSetupMarker(configA); err != nil {
		t.Fatalf("ValidateWindowsSandboxSetupMarker A: %v", err)
	}

	configB := configA
	configB.CommandCWD = workspaceB
	configB.WorkspaceRoots = []string{workspaceB}
	configB.PermissionProfile.FileSystem.WriteRoots = []WritableRoot{{Root: workspaceB}}
	err := ValidateWindowsSandboxSetupMarker(configB)
	if err == nil {
		t.Fatal("ValidateWindowsSandboxSetupMarker B = nil, want a plan-mismatch: B was never set up")
	}
	if !IsWindowsSandboxSetupPlanMismatch(err) {
		t.Fatalf("ValidateWindowsSandboxSetupMarker B = %v, want a plan-mismatch (not a stale-setup error)", err)
	}

	// Simulating the runner's on-demand apply: recording B's plan must make B
	// validate while keeping A valid.
	applied, _, err := buildWindowsSandboxSetupAppliedPlan(configB.commandConfig())
	if err != nil {
		t.Fatalf("buildWindowsSandboxSetupAppliedPlan B: %v", err)
	}
	if err := recordWindowsSandboxSetupAppliedPlan(home, applied); err != nil {
		t.Fatalf("recordWindowsSandboxSetupAppliedPlan B: %v", err)
	}
	if err := ValidateWindowsSandboxSetupMarker(configB); err != nil {
		t.Fatalf("ValidateWindowsSandboxSetupMarker B after recording: %v", err)
	}
	if err := ValidateWindowsSandboxSetupMarker(configA); err != nil {
		t.Fatalf("ValidateWindowsSandboxSetupMarker A after recording B: %v", err)
	}
}

// A V5 SINGLE-PLAN MARKER MIGRATES INSTEAD OF BRICKING.
//
// v5 stored one plan hash; v6 stores a plan set. A host set up before the
// upgrade must not be told to re-run setup: the old plan seeds the set.
func TestWindowsSandboxSetupMarkerMigratesV5SinglePlan(t *testing.T) {
	config := WindowsSandboxSetupConfig{
		SandboxHome:    t.TempDir(),
		CommandCWD:     t.TempDir(),
		WorkspaceRoots: []string{`C:\workspace`},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: `C:\workspace`}},
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
	}
	plan, err := BuildWindowsACLPlan(config.commandConfig())
	if err != nil {
		t.Fatalf("BuildWindowsACLPlan: %v", err)
	}
	hash, err := WindowsACLPlanHash(plan)
	if err != nil {
		t.Fatalf("WindowsACLPlanHash: %v", err)
	}
	infra, err := BuildWindowsNetworkInfraPlan(config.commandConfig())
	if err != nil {
		t.Fatalf("BuildWindowsNetworkInfraPlan: %v", err)
	}
	infraHash, err := WindowsNetworkInfraHash(infra)
	if err != nil {
		t.Fatalf("WindowsNetworkInfraHash: %v", err)
	}
	legacy := map[string]any{
		"schemaVersion":    5,
		"aclPlanHash":      hash,
		"aclPlanEntries":   len(plan.Entries),
		"networkInfraHash": infraHash,
		"offlineFilterSid": infra.IdentitySIDs[0],
		"networkFilters":   len(infra.Filters),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy marker: %v", err)
	}
	if err := os.WriteFile(WindowsSandboxSetupMarkerPath(config.SandboxHome), raw, 0o600); err != nil {
		t.Fatalf("write legacy marker: %v", err)
	}
	if err := ValidateWindowsSandboxSetupMarker(config); err != nil {
		t.Fatalf("ValidateWindowsSandboxSetupMarker migrated v5 = %v, want nil (no forced re-setup)", err)
	}
}

// THE RUNNER MUST KNOW WHICH RUNTIME DIRS TO HEAL.
//
// The QG runtime tree (TEMP/TMP point inside it) is recreated by
// prepareSandboxRuntime, but a 30-day cleanup between setup and the next
// command deletes the capability grant with the directory. The ensure step
// re-grants exactly these dirs under the write-root capability SID; nothing
// else on the host is in scope.
func TestWindowsSandboxRuntimeDirGrantsCoverTheRuntimeTree(t *testing.T) {
	home := t.TempDir()
	workspace := t.TempDir()
	runtimeState := SandboxRuntime{
		Root:  filepath.Join(home, "runtime"),
		Cache: filepath.Join(home, "runtime", "cache"),
		Data:  filepath.Join(home, "runtime", "data"),
		Temp:  filepath.Join(home, "runtime", "tmp"),
	}
	config := WindowsSandboxCommandConfig{
		SandboxHome:    home,
		WorkspaceRoots: []string{workspace},
		PermissionProfile: PermissionProfile{
			FileSystem: FileSystemPolicy{
				Kind:       FileSystemRestricted,
				WriteRoots: []WritableRoot{{Root: workspace}, {Root: runtimeState.Root}},
				AllowTemp:  true,
			},
			Network: NetworkPolicy{Mode: NetworkDeny},
		},
	}
	config.PermissionProfile.Runtime = &runtimeState
	grants, err := windowsSandboxRuntimeDirGrants(config)
	if err != nil {
		t.Fatalf("windowsSandboxRuntimeDirGrants: %v", err)
	}
	if len(grants) != 4 {
		t.Fatalf("runtime dir grants = %#v, want the 4 runtime tree dirs", grants)
	}
	want := map[string]bool{
		filepath.Clean(runtimeState.Root):  false,
		filepath.Clean(runtimeState.Cache): false,
		filepath.Clean(runtimeState.Data):  false,
		filepath.Clean(runtimeState.Temp):  false,
	}
	for _, grant := range grants {
		key := filepath.Clean(grant.Path)
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected runtime dir grant for %q", grant.Path)
		}
		want[key] = true
		if grant.Capability == "" {
			t.Fatalf("runtime dir grant for %q has no capability SID", grant.Path)
		}
	}
	// Without a runtime state there is nothing to heal.
	plain := config
	plain.PermissionProfile.Runtime = nil
	grants, err = windowsSandboxRuntimeDirGrants(plain)
	if err != nil {
		t.Fatalf("windowsSandboxRuntimeDirGrants without runtime: %v", err)
	}
	if len(grants) != 0 {
		t.Fatalf("runtime dir grants without runtime = %#v, want none", grants)
	}
}
