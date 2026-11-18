package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/filelock"
	"github.com/quebragalho-dev/quebragalho-cli/internal/fsutil"
)

const WindowsSandboxSetupName = "qg-windows-sandbox-setup.exe"

const windowsSandboxSetupMarkerSchemaVersion = 6

type WindowsSandboxSetupArgsOptions struct {
	SandboxHome       string
	CommandCWD        string
	WorkspaceRoots    []string
	PermissionProfile PermissionProfile
}

type WindowsSandboxSetupConfig struct {
	SandboxHome       string
	CommandCWD        string
	WorkspaceRoots    []string
	PermissionProfile PermissionProfile
}

type WindowsSandboxSetupMarker struct {
	SchemaVersion int `json:"schemaVersion"`
	// AppliedPlans is the set of workspace ACL plans this sandbox home has
	// provisioned (by `qg sandbox setup` or by the runner's on-demand apply),
	// so one setup serves every folder instead of bricking all but one with
	// "out of date". The WFP network half below stays machine-global: one
	// elevated setup provisions it, every workspace reuses it.
	AppliedPlans []WindowsSandboxSetupAppliedPlan `json:"appliedPlans,omitempty"`
	// NetworkInfraHash fingerprints the mode-INDEPENDENT network infrastructure
	// setup provisioned (block filters scoped to the offline-marker SID), so one
	// marker validly serves both an allow command and a deny command. It replaces
	// the old per-command NetworkPolicyHash/NetworkPlanHash, which locked the
	// marker to a single mode and bricked approved network commands.
	NetworkInfraHash string `json:"networkInfraHash"`
	OfflineFilterSID string `json:"offlineFilterSid"`
	NetworkFilters   int    `json:"networkFilters"`
}

// WindowsSandboxSetupAppliedPlan fingerprints one workspace ACL plan the setup
// has provisioned. It carries no network hash: the unelevated on-demand apply
// provisions no WFP filters, so there is nothing network-side to fingerprint
// per plan.
type WindowsSandboxSetupAppliedPlan struct {
	ACLPlanHash    string `json:"aclPlanHash"`
	ACLPlanEntries int    `json:"aclPlanEntries"`
}

// windowsSandboxSetupMarkerV5 is the previous single-plan shape. A host set up
// before the upgrade migrates it into a one-element AppliedPlans set instead
// of being told to re-run setup.
type windowsSandboxSetupMarkerV5 struct {
	SchemaVersion    int    `json:"schemaVersion"`
	ACLPlanHash      string `json:"aclPlanHash"`
	ACLPlanEntries   int    `json:"aclPlanEntries"`
	NetworkInfraHash string `json:"networkInfraHash"`
	OfflineFilterSID string `json:"offlineFilterSid"`
	NetworkFilters   int    `json:"networkFilters"`
}

// windowsSandboxSetupPlanMismatchError reports a marker that is valid
// (schema and network infrastructure match) but has no record of the current
// workspace plan. The runner heals this by applying the plan on demand and
// recording it, instead of failing with "out of date".
type windowsSandboxSetupPlanMismatchError struct {
	plan WindowsSandboxSetupAppliedPlan
}

func (err *windowsSandboxSetupPlanMismatchError) Error() string {
	return "windows sandbox setup is out of date: this workspace was never provisioned — run `qg sandbox setup` from an elevated (Administrator) terminal"
}

// IsWindowsSandboxSetupPlanMismatch reports whether err is a plan-membership
// miss (healable by an on-demand apply) rather than a stale setup.
func IsWindowsSandboxSetupPlanMismatch(err error) bool {
	var mismatch *windowsSandboxSetupPlanMismatchError
	return errors.As(err, &mismatch)
}

func WindowsSandboxSetupMarkerPath(sandboxHome string) string {
	return filepath.Join(sandboxHome, "windows-setup.json")
}

func BuildWindowsSandboxSetupArgs(options WindowsSandboxSetupArgsOptions) ([]string, error) {
	commandCWD := strings.TrimSpace(options.CommandCWD)
	if commandCWD == "" {
		return nil, errors.New("windows sandbox setup requires command cwd")
	}
	sandboxHome := strings.TrimSpace(options.SandboxHome)
	if sandboxHome == "" {
		var err error
		sandboxHome, err = ResolveWindowsSandboxHome(nil)
		if err != nil {
			return nil, err
		}
	}
	workspaceRoots := trimNonEmptyStrings(options.WorkspaceRoots)
	if len(workspaceRoots) == 0 {
		workspaceRoots = []string{commandCWD}
	}
	profileJSON, err := json.Marshal(options.PermissionProfile)
	if err != nil {
		return nil, fmt.Errorf("marshal windows sandbox setup permission profile: %w", err)
	}
	args := []string{
		"--sandbox-home", sandboxHome,
		"--command-cwd", commandCWD,
		"--permission-profile", string(profileJSON),
	}
	for _, root := range workspaceRoots {
		args = append(args, "--workspace-root", root)
	}
	return args, nil
}

func ParseWindowsSandboxSetupArgs(args []string) (WindowsSandboxSetupConfig, error) {
	var config WindowsSandboxSetupConfig
	var profileJSON string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--command-cwd":
			value, next, err := nextWindowsSandboxFlagValue(args, index)
			if err != nil {
				return WindowsSandboxSetupConfig{}, err
			}
			config.CommandCWD = strings.TrimSpace(value)
			index = next
		case "--sandbox-home":
			value, next, err := nextWindowsSandboxFlagValue(args, index)
			if err != nil {
				return WindowsSandboxSetupConfig{}, err
			}
			config.SandboxHome = strings.TrimSpace(value)
			index = next
		case "--workspace-root":
			value, next, err := nextWindowsSandboxFlagValue(args, index)
			if err != nil {
				return WindowsSandboxSetupConfig{}, err
			}
			if root := strings.TrimSpace(value); root != "" {
				config.WorkspaceRoots = append(config.WorkspaceRoots, root)
			}
			index = next
		case "--permission-profile":
			value, next, err := nextWindowsSandboxFlagValue(args, index)
			if err != nil {
				return WindowsSandboxSetupConfig{}, err
			}
			profileJSON = strings.TrimSpace(value)
			index = next
		default:
			return WindowsSandboxSetupConfig{}, fmt.Errorf("unknown windows sandbox setup flag %q", arg)
		}
	}
	if config.CommandCWD == "" {
		return WindowsSandboxSetupConfig{}, errors.New("missing --command-cwd")
	}
	if config.SandboxHome == "" {
		return WindowsSandboxSetupConfig{}, errors.New("missing --sandbox-home")
	}
	if len(config.WorkspaceRoots) == 0 {
		config.WorkspaceRoots = []string{config.CommandCWD}
	}
	if profileJSON == "" {
		return WindowsSandboxSetupConfig{}, errors.New("missing --permission-profile")
	}
	if err := json.Unmarshal([]byte(profileJSON), &config.PermissionProfile); err != nil {
		return WindowsSandboxSetupConfig{}, fmt.Errorf("invalid --permission-profile: %w", err)
	}
	return config, nil
}

func RunWindowsSandboxSetup(args []string, stderr io.Writer) int {
	config, err := ParseWindowsSandboxSetupArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, WindowsSandboxSetupName+": "+err.Error())
		return 2
	}
	return runWindowsSandboxSetup(config, stderr)
}

func (config WindowsSandboxSetupConfig) commandConfig() WindowsSandboxCommandConfig {
	return WindowsSandboxCommandConfig{
		SandboxHome:       config.SandboxHome,
		CommandCWD:        config.CommandCWD,
		WorkspaceRoots:    cloneStrings(config.WorkspaceRoots),
		PermissionProfile: config.PermissionProfile,
		SandboxLevel:      WindowsSandboxLevelRestrictedToken,
	}
}

func WindowsSandboxSetupConfigFromCommand(config WindowsSandboxCommandConfig) WindowsSandboxSetupConfig {
	return WindowsSandboxSetupConfig{
		SandboxHome:       config.SandboxHome,
		CommandCWD:        config.CommandCWD,
		WorkspaceRoots:    cloneStrings(config.WorkspaceRoots),
		PermissionProfile: config.PermissionProfile,
	}
}

func BuildWindowsSandboxSetupMarker(config WindowsSandboxSetupConfig) (WindowsSandboxSetupMarker, error) {
	applied, _, err := buildWindowsSandboxSetupAppliedPlan(config.commandConfig())
	if err != nil {
		return WindowsSandboxSetupMarker{}, err
	}
	// Fingerprint the mode-INDEPENDENT network infrastructure (block filters
	// scoped to the offline-marker SID), NOT the per-command network mode, so the
	// marker validates for both allow and deny commands against this one setup.
	infraPlan, err := BuildWindowsNetworkInfraPlan(config.commandConfig())
	if err != nil {
		return WindowsSandboxSetupMarker{}, err
	}
	infraHash, err := WindowsNetworkInfraHash(infraPlan)
	if err != nil {
		return WindowsSandboxSetupMarker{}, err
	}
	offlineSID := ""
	if len(infraPlan.IdentitySIDs) > 0 {
		offlineSID = infraPlan.IdentitySIDs[0]
	}
	return WindowsSandboxSetupMarker{
		SchemaVersion:    windowsSandboxSetupMarkerSchemaVersion,
		AppliedPlans:     []WindowsSandboxSetupAppliedPlan{applied},
		NetworkInfraHash: infraHash,
		OfflineFilterSID: offlineSID,
		NetworkFilters:   len(infraPlan.Filters),
	}, nil
}

// buildWindowsSandboxSetupAppliedPlan derives the ACL plan for the setup's
// permission profile plus its marker fingerprint. The same BuildWindowsACLPlan
// output later feeds the apply step, so the fingerprint and the applied grants
// can never drift apart.
func buildWindowsSandboxSetupAppliedPlan(config WindowsSandboxCommandConfig) (WindowsSandboxSetupAppliedPlan, WindowsACLPlan, error) {
	plan, err := BuildWindowsACLPlan(config)
	if err != nil {
		return WindowsSandboxSetupAppliedPlan{}, WindowsACLPlan{}, err
	}
	hash, err := WindowsACLPlanHash(plan)
	if err != nil {
		return WindowsSandboxSetupAppliedPlan{}, WindowsACLPlan{}, err
	}
	return WindowsSandboxSetupAppliedPlan{
		ACLPlanHash:    hash,
		ACLPlanEntries: len(plan.Entries),
	}, plan, nil
}

// lockWindowsSandboxSetupMarkerFile serializes the marker read-modify-write
// sequence across processes with the stable "<file>.lockfile" sibling,
// mirroring lockWindowsUnelevatedSetupMarkerFile: without it two runners
// recording at once both load the same snapshot and the last save silently
// drops the other's plan. The lock never covers the ACL apply itself, only
// the marker bookkeeping. Timeouts fail closed and name the lock path.
func lockWindowsSandboxSetupMarkerFile(path string) (func(), error) {
	lockPath := path + ".lockfile"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(grantLockTimeout)
	for {
		locked, err := filelock.TryLockExclusive(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if locked {
			return func() {
				_ = filelock.Unlock(file)
				_ = file.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("timed out waiting for windows sandbox setup marker lock at %s", lockPath)
		}
		time.Sleep(grantLockRetry)
	}
}

// recordWindowsSandboxSetupAppliedPlan appends the plan to the marker,
// dropping nothing: one marker serves every workspace the sandbox home has
// provisioned. It writes atomically (temp file + rename) so a concurrent
// command never reads a torn marker, and serializes the read-modify-write
// across processes so two workspaces recording at once cannot lose each
// other's plan.
func recordWindowsSandboxSetupAppliedPlan(sandboxHome string, applied WindowsSandboxSetupAppliedPlan) error {
	sandboxHome = strings.TrimSpace(sandboxHome)
	if sandboxHome == "" {
		return errors.New("windows sandbox home is required")
	}
	unlock, err := lockWindowsSandboxSetupMarkerFile(WindowsSandboxSetupMarkerPath(sandboxHome))
	if err != nil {
		return err
	}
	defer unlock()
	marker, err := loadWindowsSandboxSetupMarker(sandboxHome)
	if err != nil {
		return err
	}
	if marker.containsAppliedPlan(applied) {
		return nil
	}
	marker.SchemaVersion = windowsSandboxSetupMarkerSchemaVersion
	marker.AppliedPlans = append(marker.AppliedPlans, applied)
	path := WindowsSandboxSetupMarkerPath(sandboxHome)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create windows sandbox setup marker dir: %w", err)
	}
	bytes, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal windows sandbox setup marker: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".windows-setup-*.tmp")
	if err != nil {
		return fmt.Errorf("create windows sandbox setup marker temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(bytes); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write windows sandbox setup marker temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close windows sandbox setup marker temp file: %w", err)
	}
	// Concurrent runners may be validating mid-replace; on Windows that rename
	// then fails with access denied, so retry with the long budget.
	if err := fsutil.RenameWithLongRetry(tmpPath, path, nil); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace windows sandbox setup marker: %w", err)
	}
	return nil
}

func (marker WindowsSandboxSetupMarker) containsAppliedPlan(applied WindowsSandboxSetupAppliedPlan) bool {
	if strings.TrimSpace(applied.ACLPlanHash) == "" {
		return false
	}
	for _, plan := range marker.AppliedPlans {
		if plan.ACLPlanHash == applied.ACLPlanHash && plan.ACLPlanEntries == applied.ACLPlanEntries {
			return true
		}
	}
	return false
}

// loadWindowsSandboxSetupMarker reads the marker; a missing file is the common
// first-run state and yields an empty marker (not an error) so the caller can
// distinguish "never set up" from "set up for another workspace". A v5
// single-plan marker migrates into a one-element plan set; anything older or
// corrupt fails validation instead of bricking the command.
func loadWindowsSandboxSetupMarker(sandboxHome string) (WindowsSandboxSetupMarker, error) {
	sandboxHome = strings.TrimSpace(sandboxHome)
	if sandboxHome == "" {
		return WindowsSandboxSetupMarker{}, errors.New("windows sandbox home is required")
	}
	path := WindowsSandboxSetupMarkerPath(sandboxHome)
	// The setup helper replaces this file via rename; a concurrent runner
	// reading mid-replace hits a Windows sharing violation without a brief
	// retry (same race as the unelevated marker).
	bytes, err := fsutil.ReadFileWithRetry(path, nil)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WindowsSandboxSetupMarker{SchemaVersion: windowsSandboxSetupMarkerSchemaVersion}, nil
		}
		return WindowsSandboxSetupMarker{}, fmt.Errorf("read windows sandbox setup marker: %w", err)
	}
	var version struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(bytes, &version); err != nil {
		return WindowsSandboxSetupMarker{}, fmt.Errorf("parse windows sandbox setup marker: %w", err)
	}
	if version.SchemaVersion == 5 {
		var legacy windowsSandboxSetupMarkerV5
		if err := json.Unmarshal(bytes, &legacy); err != nil {
			return WindowsSandboxSetupMarker{}, fmt.Errorf("parse windows sandbox setup marker: %w", err)
		}
		return WindowsSandboxSetupMarker{
			SchemaVersion: windowsSandboxSetupMarkerSchemaVersion,
			AppliedPlans: []WindowsSandboxSetupAppliedPlan{{
				ACLPlanHash:    legacy.ACLPlanHash,
				ACLPlanEntries: legacy.ACLPlanEntries,
			}},
			NetworkInfraHash: legacy.NetworkInfraHash,
			OfflineFilterSID: legacy.OfflineFilterSID,
			NetworkFilters:   legacy.NetworkFilters,
		}, nil
	}
	var marker WindowsSandboxSetupMarker
	if err := json.Unmarshal(bytes, &marker); err != nil {
		return WindowsSandboxSetupMarker{}, fmt.Errorf("parse windows sandbox setup marker: %w", err)
	}
	return marker, nil
}

func WriteWindowsSandboxSetupMarker(config WindowsSandboxSetupConfig) (WindowsSandboxSetupMarker, error) {
	marker, err := BuildWindowsSandboxSetupMarker(config)
	if err != nil {
		return WindowsSandboxSetupMarker{}, err
	}
	path := WindowsSandboxSetupMarkerPath(config.SandboxHome)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return WindowsSandboxSetupMarker{}, fmt.Errorf("create windows sandbox setup marker dir: %w", err)
	}
	bytes, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return WindowsSandboxSetupMarker{}, fmt.Errorf("marshal windows sandbox setup marker: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".windows-setup-*.tmp")
	if err != nil {
		return WindowsSandboxSetupMarker{}, fmt.Errorf("create windows sandbox setup marker temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(bytes); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return WindowsSandboxSetupMarker{}, fmt.Errorf("write windows sandbox setup marker temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return WindowsSandboxSetupMarker{}, fmt.Errorf("close windows sandbox setup marker temp file: %w", err)
	}
	// Concurrent runners may be validating mid-replace; on Windows that rename
	// then fails with access denied, so retry with the long budget.
	if err := fsutil.RenameWithLongRetry(tmpPath, path, nil); err != nil {
		_ = os.Remove(tmpPath)
		return WindowsSandboxSetupMarker{}, fmt.Errorf("replace windows sandbox setup marker: %w", err)
	}
	return marker, nil
}

func ValidateWindowsSandboxSetupMarker(config WindowsSandboxSetupConfig) error {
	expected, err := BuildWindowsSandboxSetupMarker(config)
	if err != nil {
		return err
	}
	actual, err := loadWindowsSandboxSetupMarker(config.SandboxHome)
	if err != nil {
		return err
	}
	if len(actual.AppliedPlans) == 0 && actual.NetworkInfraHash == "" {
		return fmt.Errorf("windows sandbox is not initialized for this workspace — run `qg sandbox setup` from an elevated (Administrator) terminal (missing %s)", filepath.Base(WindowsSandboxSetupMarkerPath(config.SandboxHome)))
	}
	if actual.SchemaVersion != expected.SchemaVersion {
		return fmt.Errorf("windows sandbox setup is out of date: schema %d, want %d", actual.SchemaVersion, expected.SchemaVersion)
	}
	if len(expected.AppliedPlans) != 1 {
		return fmt.Errorf("windows sandbox setup is out of date: cannot fingerprint the current workspace plan")
	}
	if !actual.containsAppliedPlan(expected.AppliedPlans[0]) {
		return &windowsSandboxSetupPlanMismatchError{plan: expected.AppliedPlans[0]}
	}
	// Mode-agnostic: validate the provisioned infrastructure, never the
	// per-command network mode — so an approved (allow) network command and an
	// ordinary (deny) command both validate against this one setup.
	if actual.NetworkInfraHash != expected.NetworkInfraHash {
		return errors.New("windows sandbox setup is out of date: network infrastructure changed")
	}
	if actual.OfflineFilterSID != expected.OfflineFilterSID {
		return errors.New("windows sandbox setup is out of date: offline network identity changed")
	}
	if actual.NetworkFilters != expected.NetworkFilters {
		return errors.New("windows sandbox setup is out of date: network enforcement plan changed")
	}
	return nil
}

func WindowsACLPlanHash(plan WindowsACLPlan) (string, error) {
	entries := canonicalWindowsACLEntries(plan.Entries)
	bytes, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("marshal windows ACL plan hash input: %w", err)
	}
	sum := sha256.Sum256(bytes)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalWindowsACLEntries(entries []WindowsACLEntry) []WindowsACLEntry {
	out := make([]WindowsACLEntry, 0, len(entries))
	for _, entry := range dedupeWindowsACLEntries(entries) {
		entry.Path = windowsCapabilityPathKey(entry.Path)
		entry.Capability = strings.ToLower(strings.TrimSpace(entry.Capability))
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		left := out[i]
		right := out[j]
		if left.Action != right.Action {
			return left.Action < right.Action
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}
		if left.Capability != right.Capability {
			return left.Capability < right.Capability
		}
		return !left.Materialize && right.Materialize
	})
	return out
}
