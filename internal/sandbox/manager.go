package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Linux backend diagnostics, exported so the doctor can name the missing piece
// from the backend message without resolving the helper a second time.
const (
	// LinuxSandboxHelperMissingMessage reports that qg-linux-sandbox was found
	// neither beside the running qg binary nor on PATH.
	LinuxSandboxHelperMissingMessage = "Linux sandbox helper is not available"
	// BubblewrapMissingMessage reports that bwrap was not found on PATH.
	BubblewrapMissingMessage = "bubblewrap is not installed"
	// BubblewrapUnusableMessagePrefix starts the Portuguese message reporting
	// that bwrap exists but a live probe failed: the binary is installed yet
	// cannot build a sandbox on this machine (e.g. Ubuntu 24.04+ where
	// AppArmor blocks unprivileged user namespaces by default). The probe's
	// own stderr is appended after this prefix, so match it with a prefix
	// comparison, never with equality.
	BubblewrapUnusableMessagePrefix = "bubblewrap (bwrap) está instalado, mas não consegue criar o ambiente isolado"
)

// bubblewrapUnusableMessage explains in Portuguese why the installed bwrap
// cannot be used here and how to fix it, keeping the probe's own diagnostic
// for support. The message is user-facing (it surfaces as the backend message,
// the downgrade reason, and the doctor warning), so it names both approved
// remediations: an AppArmor profile for bwrap or the sysctl knob.
func bubblewrapUnusableMessage(detail string) string {
	message := BubblewrapUnusableMessagePrefix + " neste sistema."
	if trimmed := strings.TrimSpace(detail); trimmed != "" {
		message += " Diagnóstico do bwrap: " + trimmed + "."
	}
	message += " No Ubuntu 24.04 ou superior, o AppArmor bloqueia user namespaces sem privilégios por padrão" +
		" (kernel.apparmor_restrict_unprivileged_userns=1): libere o bwrap com um perfil do AppArmor" +
		" que permita user namespaces ou ajuste o sysctl" +
		" (por exemplo, `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0`)." +
		" Passo a passo: https://github.com/quebragalho-dev/quebragalho-cli/blob/main/docs/INSTALL.md#linux--ubuntu-2404-ambiente-isolado." +
		" Enquanto isso, os comandos rodam sem o isolamento nativo do Linux."
	return message
}

// linuxBwrapProbe checks that bwrap can actually build a sandbox on this
// machine. Indirected through a variable so tests can stub the probe without
// touching the real binary.
var linuxBwrapProbe = runLinuxBwrapProbe

// runLinuxBwrapProbe executes bwrap once with the same user/pid/network
// namespace flags the helper always applies, so backend selection reflects
// whether bwrap really works here instead of only whether the binary exists.
// An existence check is not enough: on Ubuntu 24.04+ with the default AppArmor
// restriction (kernel.apparmor_restrict_unprivileged_userns=1),
// /usr/bin/bwrap is installed but fails with "bwrap: loopback: Failed
// RTM_NEWADDR: Operation not permitted", and every wrapped command would fail
// with it. A failing probe returns the probe's stderr so the caller can name
// the cause.
func runLinuxBwrapProbe(bwrapPath string) error {
	path := strings.TrimSpace(bwrapPath)
	if path == "" {
		return errors.New("empty bwrap path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path,
		"--unshare-user",
		"--unshare-pid",
		"--unshare-net",
		"--proc", "/proc",
		"--ro-bind", "/", "/",
		"--chdir", "/",
		"--", "true",
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return errors.New(detail)
		}
		return err
	}
	return nil
}

// errWindowsSandboxNotInitialized is returned only to a caller that explicitly
// REQUIRED the sandbox (--sandbox require) on Windows before `qg sandbox setup`
// has run. The default (auto) preference degrades instead — see BuildExecutionRequest.
var errWindowsSandboxNotInitialized = errors.New(
	"the Windows sandbox is not initialized — run `qg sandbox setup` from an elevated (Administrator) terminal, " +
		"or drop `--sandbox require` to run with workspace path-confinement and per-command approval")

// windowsSetupDowngradeReason is surfaced when Windows falls back to the
// in-process policy gate because the OS sandbox has not been set up.
const windowsSetupDowngradeReason = "Windows OS sandbox inactive — commands run with workspace path-confinement and " +
	"per-command approval only. Run `qg sandbox setup` from an elevated (Administrator) terminal for full filesystem and network isolation."

// windowsUnelevatedDowngradeReason is surfaced when Windows runs the sandbox in
// the unelevated tier: the restricted-token write-jail is enforced (the command
// runner applies the workspace ACLs itself, which needs no Administrator
// rights), but the WFP network filters are Administrator-only and absent.
const windowsUnelevatedDowngradeReason = "Windows sandbox running unelevated — the filesystem write-jail is enforced, but network " +
	"isolation still relies on per-command approval. Run `qg sandbox setup` from an elevated (Administrator) terminal for OS-level network enforcement."

// windowsSandboxInitialized reports whether the per-host Windows sandbox setup
// marker exists. A missing marker is the common fresh-install state, so the
// caller degrades rather than bricking the command. Indirected through a var so
// tests can drive both the initialized and not-initialized paths.
var windowsSandboxInitialized = func() bool {
	home, err := ResolveWindowsSandboxHome(nil)
	if err != nil || home == "" {
		return false
	}
	_, statErr := os.Stat(WindowsSandboxSetupMarkerPath(home))
	return statErr == nil
}

type SandboxPreference string

const (
	SandboxPreferenceAuto    SandboxPreference = "auto"
	SandboxPreferenceRequire SandboxPreference = "require"
	SandboxPreferenceForbid  SandboxPreference = "forbid"
)

type SandboxManagerOptions struct {
	GOOS             string
	LookupExecutable func(string) (string, error)
	DetectWSL        func() WSLInfo
	Backend          Backend
}

type SandboxManager struct {
	goos    string
	backend Backend
}

type SandboxManagerRequest struct {
	WorkspaceRoot     string
	Command           CommandSpec
	Policy            Policy
	Scope             *Scope
	Profile           PermissionProfile
	Preference        SandboxPreference
	ValidateExecution bool
}

type SandboxExecutionRequest struct {
	Command                 CommandSpec         `json:"command"`
	WorkspaceRoot           string              `json:"workspaceRoot"`
	PermissionProfile       PermissionProfile   `json:"permissionProfile"`
	Backend                 Backend             `json:"backend"`
	TargetBackend           BackendName         `json:"targetBackend"`
	CommandWrapped          bool                `json:"commandWrapped"`
	SandboxEnvMarkers       []string            `json:"sandboxEnvMarkers,omitempty"`
	EnforcementLevel        EnforcementLevel    `json:"enforcementLevel"`
	DowngradeReason         string              `json:"downgradeReason,omitempty"`
	SupportLevel            BackendSupportLevel `json:"supportLevel"`
	RequiresPlatformSandbox bool                `json:"requiresPlatformSandbox"`
}

func NewSandboxManager(options SandboxManagerOptions) SandboxManager {
	backend := options.Backend
	if backend.Name != "" && backend.Platform == "" {
		backend.Platform = platformForBackendName(backend.Name)
	}
	goos := options.GOOS
	if goos == "" {
		goos = backend.Platform
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if backend.Name == "" {
		backend = selectPlatformBackend(goos, options.LookupExecutable, options.DetectWSL)
	}
	if backend.Platform == "" {
		backend.Platform = goos
	}
	backend = inferBackendCapabilities(backend)
	return SandboxManager{goos: goos, backend: backend}
}

func platformForBackendName(name BackendName) string {
	switch name {
	case BackendMacOSSeatbelt:
		return "darwin"
	case BackendLinuxBwrap, BackendLinuxLandlock, BackendWSL:
		return "linux"
	case BackendWindowsRestrictedToken, BackendWindowsElevated:
		return "windows"
	default:
		return ""
	}
}

func (manager SandboxManager) Backend() Backend {
	return manager.backend
}

// isExecutable checks whether a file is executable. On Unix, this checks the
// execute permission bits. On Windows, Go's os.FileMode does not set execute
// bits, so we check for common executable extensions instead.
func isExecutable(fi os.FileInfo) bool {
	if !fi.Mode().IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		name := strings.ToLower(fi.Name())
		return strings.HasSuffix(name, ".exe") ||
			strings.HasSuffix(name, ".com") ||
			strings.HasSuffix(name, ".bat") ||
			strings.HasSuffix(name, ".cmd")
	}
	return fi.Mode()&0o111 != 0
}

// lookupExecutable checks whether a named binary exists and is executable,
// using os.Stat instead of exec.LookPath. exec.LookPath internally uses the
// faccessat2 syscall, which is blocked by Android's seccomp filter (SIGSYS).
func lookupExecutable(name string) (string, error) {
	if !strings.Contains(name, string(os.PathSeparator)) {
		path := os.Getenv("PATH")
		for _, dir := range strings.Split(path, string(os.PathListSeparator)) {
			if dir == "" {
				continue // skip empty entries (e.g. trailing colon)
			}
			candidate := filepath.Join(dir, name)
			if fi, err := os.Stat(candidate); err == nil {
				if isExecutable(fi) {
					return candidate, nil
				}
			}
		}
		return "", errors.New("executable file not found in $PATH")
	}
	fi, err := os.Stat(name)
	if err != nil {
		return "", err
	}
	if isExecutable(fi) {
		return name, nil
	}
	return "", errors.New("executable file not found")
}

func selectPlatformBackend(goos string, lookup func(string) (string, error), detect func() WSLInfo) Backend {
	if lookup == nil {
		lookup = lookupExecutable
	}
	if detect == nil {
		detect = detectWSL
	}
	switch goos {
	case "linux":
		if helper, err := ResolveLinuxSandboxHelperCommand(lookup); err == nil && helper.Name != "" {
			bwrapPath, bwrapErr := lookup("bwrap")
			if bwrapErr != nil {
				return unavailableBackend(goos, BubblewrapMissingMessage)
			}
			// Probe the real binary: existing does not mean working. A bwrap that
			// cannot build namespaces (Ubuntu 24.04+ AppArmor default) must
			// select the honest unavailable backend with a remediation
			// message, never the native backend that would fail every
			// wrapped command.
			if probeErr := linuxBwrapProbe(bwrapPath); probeErr != nil {
				return unavailableBackend(goos, bubblewrapUnusableMessage(probeErr.Error()))
			}
			return nativeBackend(goos, BackendLinuxBwrap, helper.Name, "Linux sandbox helper available")
		}
		if info := detect(); info.IsWSL {
			return wslBackend(goos, info)
		}
		return unavailableBackend(goos, LinuxSandboxHelperMissingMessage)
	case "darwin":
		if path, err := lookup("sandbox-exec"); err == nil && path != "" {
			return nativeBackend(goos, BackendMacOSSeatbelt, path, "macOS Seatbelt backend available")
		}
		return unavailableBackend(goos, "sandbox-exec is not available")
	case "windows":
		runner := findWindowsSandboxCommandRunner(lookup)
		setup := findWindowsSandboxSetupHelper(lookup)
		if runner.Available() && setup.Available() {
			backend := nativeBackend(goos, BackendWindowsRestrictedToken, runner.Name, "Windows sandbox command runner and setup helper available")
			backend.ExecutableArgsPrefix = runner.ArgsPrefix
			return backend
		}
		if runner.Available() {
			return unavailableBackend(goos, "Windows sandbox setup helper is not available")
		}
		return unavailableBackend(goos, "Windows sandbox command runner is not available")
	default:
		return unavailableBackend(goos, "no platform sandbox adapter is available for "+goos)
	}
}

func (manager SandboxManager) BuildExecutionRequest(request SandboxManagerRequest) (SandboxExecutionRequest, error) {
	policy := request.Policy
	if policy.Mode == "" {
		policy = DefaultPolicy()
	}
	preference := request.Preference
	if preference == "" {
		preference = SandboxPreferenceAuto
	}
	profile := request.Profile
	if permissionProfileUnset(profile) {
		profile = PermissionProfileFromPolicy(request.WorkspaceRoot, policy, request.Scope)
	}
	requiresPlatformSandbox := profile.RequiresPlatformSandbox() && preference != SandboxPreferenceForbid
	backend := manager.backend
	enforcementLevel := backend.EnforcementLevel(policy)
	if preference == SandboxPreferenceForbid || policy.Mode == ModeDisabled || !requiresPlatformSandbox {
		enforcementLevel = EnforcementDisabled
	}
	if request.ValidateExecution && preference == SandboxPreferenceRequire && backend.SupportLevel() != BackendSupportNative {
		return SandboxExecutionRequest{}, nativeSandboxUnavailableError(backend)
	}
	if request.ValidateExecution && preference != SandboxPreferenceForbid && backend.SupportLevel() != BackendSupportNative && policyHasExplicitDeny(policy) {
		return SandboxExecutionRequest{}, errors.New("native sandbox unavailable: configured deny_read or deny_write rules cannot be enforced")
	}
	// Windows: the FULL OS sandbox needs a one-time elevated `qg sandbox setup`
	// (it applies WFP network filters + workspace ACLs and writes a marker).
	// Without it, a restricted-filesystem profile can still run in the UNELEVATED
	// tier: the command runner applies the workspace ACL plan itself (DACL edits
	// on user-owned roots need no Administrator rights) and wraps the command in
	// the write-restricted token, so the filesystem write-jail holds. Only the
	// WFP network filters are Administrator-only, so network stays with the
	// in-process approval gate at that tier. A profile that needs the sandbox
	// solely for network isolation gains nothing from the unelevated tier and
	// DEGRADES to the policy gate as before. A strict caller (--sandbox require)
	// still gets a clear error pointing at setup.
	windowsNeedsSetup := false
	if manager.goos == "windows" && requiresPlatformSandbox && enforcementLevel == EnforcementNative && !windowsSandboxInitialized() {
		if preference == SandboxPreferenceRequire {
			return SandboxExecutionRequest{}, errWindowsSandboxNotInitialized
		}
		windowsNeedsSetup = true
		if profile.FileSystem.Kind == FileSystemRestricted {
			enforcementLevel = EnforcementUnelevated
		} else {
			enforcementLevel = EnforcementDegraded
		}
	}
	targetBackend := manager.targetBackend(preference, policy, requiresPlatformSandbox)
	downgradeReason := ""
	if requiresPlatformSandbox && enforcementLevel == EnforcementDegraded {
		downgradeReason = backend.DowngradeReason(policy)
		if windowsNeedsSetup {
			downgradeReason = windowsSetupDowngradeReason
		}
	}
	if enforcementLevel == EnforcementUnelevated {
		downgradeReason = windowsUnelevatedDowngradeReason
	}
	wrapped := backend.CommandWrapping && backend.Available &&
		(enforcementLevel == EnforcementNative || enforcementLevel == EnforcementUnelevated)
	markers := backend.SandboxEnvMarkers(policy)
	if !wrapped && backend.Name != BackendWSL {
		markers = nil
	}
	return SandboxExecutionRequest{
		Command:                 request.Command,
		WorkspaceRoot:           request.WorkspaceRoot,
		PermissionProfile:       profile,
		Backend:                 backend,
		TargetBackend:           targetBackend,
		CommandWrapped:          wrapped,
		SandboxEnvMarkers:       markers,
		EnforcementLevel:        enforcementLevel,
		DowngradeReason:         downgradeReason,
		SupportLevel:            backend.SupportLevel(),
		RequiresPlatformSandbox: requiresPlatformSandbox,
	}, nil
}

func policyHasExplicitDeny(policy Policy) bool {
	return len(normalizeProfilePaths(policy.DenyRead)) > 0 || len(normalizeProfilePaths(policy.DenyWrite)) > 0
}

func (manager SandboxManager) BuildCommandPlan(request SandboxManagerRequest) (CommandPlan, error) {
	execRequest, err := manager.BuildExecutionRequest(request)
	if err != nil {
		return CommandPlan{}, err
	}
	policy := request.Policy
	if policy.Mode == "" {
		policy = DefaultPolicy()
	}
	return buildPlatformCommandPlan(execRequest, policy)
}

func (manager SandboxManager) targetBackend(preference SandboxPreference, policy Policy, requiresPlatformSandbox bool) BackendName {
	if preference == SandboxPreferenceForbid || policy.Mode == ModeDisabled || !requiresPlatformSandbox {
		return BackendNone
	}
	if manager.backend.Name == BackendWSL {
		return BackendLinuxBwrap
	}
	return manager.backend.TargetBackend()
}

func (request SandboxExecutionRequest) BackendPlan(policy Policy) BackendPlan {
	return BackendPlan{
		Backend:                 request.Backend,
		TargetBackend:           request.TargetBackend,
		WorkspaceRoot:           request.WorkspaceRoot,
		Policy:                  policy,
		PermissionProfile:       request.PermissionProfile,
		CommandWrapped:          request.CommandWrapped,
		SandboxEnvMarkers:       request.SandboxEnvMarkers,
		EnforcementLevel:        request.EnforcementLevel,
		DowngradeReason:         request.DowngradeReason,
		SupportLevel:            request.SupportLevel,
		RequiresPlatformSandbox: request.RequiresPlatformSandbox,
		Capabilities:            request.Backend.Capabilities(policy),
		Restrictions:            request.Backend.restrictions(policy),
		Warnings:                request.Backend.Warnings(),
	}
}

func permissionProfileUnset(profile PermissionProfile) bool {
	return profile.FileSystem.Kind == "" && profile.Network.Mode == ""
}

func inferBackendCapabilities(backend Backend) Backend {
	if backend.Available && backend.Executable != "" {
		switch backend.Name {
		case BackendLinuxBwrap, BackendMacOSSeatbelt, BackendWindowsRestrictedToken:
			backend.CommandWrapping = true
			backend.NativeIsolation = true
		}
	}
	return backend
}
