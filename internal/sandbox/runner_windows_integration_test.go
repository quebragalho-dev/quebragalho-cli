//go:build windows

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWindowsRestrictedTokenRealSandboxSmoke(t *testing.T) {
	if os.Getenv("QG_SANDBOX_REAL_SMOKE") != "1" {
		t.Skip("set QG_SANDBOX_REAL_SMOKE=1 to run real Windows sandbox smoke tests")
	}
	setupExe := realSmokeExecutable(t, "QG_WINDOWS_SANDBOX_SETUP_EXE", WindowsSandboxSetupName)
	runnerExe := realSmokeExecutable(t, "QG_WINDOWS_COMMAND_RUNNER_EXE", WindowsSandboxCommandRunnerName)

	root := t.TempDir()
	sandboxHome := filepath.Join(root, ".qg-sandbox")
	profile := PermissionProfile{
		FileSystem: FileSystemPolicy{
			Kind:                 FileSystemRestricted,
			ReadRoots:            []string{root},
			WriteRoots:           []WritableRoot{{Root: root, ProtectedMetadataNames: []string{".git", ".qg", ".agents"}}},
			IncludePlatformRoots: true,
			AllowTemp:            true,
		},
		Network: NetworkPolicy{Mode: NetworkDeny},
	}
	config := WindowsSandboxCommandArgsOptions{
		SandboxHome:       sandboxHome,
		CommandCWD:        root,
		WorkspaceRoots:    []string{root},
		PermissionProfile: profile,
		SandboxLevel:      WindowsSandboxLevelRestrictedToken,
	}
	runWindowsRealSmokeSetup(t, setupExe, WindowsSandboxSetupArgsOptions{
		SandboxHome:       sandboxHome,
		CommandCWD:        root,
		WorkspaceRoots:    []string{root},
		PermissionProfile: profile,
	})
	t.Cleanup(func() {
		cleanupProfile := profile
		cleanupProfile.Network = NetworkPolicy{Mode: NetworkAllow}
		runWindowsRealSmokeSetup(t, setupExe, WindowsSandboxSetupArgsOptions{
			SandboxHome:       sandboxHome,
			CommandCWD:        root,
			WorkspaceRoots:    []string{root},
			PermissionProfile: cleanupProfile,
		})
	})

	writeMarker := filepath.Join(root, "write-ok.txt")
	runWindowsRealSmokeCommand(t, runnerExe, config, []string{
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-Command",
		fmt.Sprintf("Set-Content -LiteralPath %s -Value ok", powershellSingleQuote(writeMarker)),
	}, 0)
	if bytes, err := os.ReadFile(writeMarker); err != nil || strings.TrimSpace(string(bytes)) != "ok" {
		t.Fatalf("sandboxed write marker = %q, %v; want ok", bytes, err)
	}

	// SID broadening is disabled, so the restricted-SID list never includes
	// Users/Authenticated Users. The write grant those groups hold on
	// C:\Users\Public must not be reachable through the restricted-SID check.
	// Pin that a write there fails: an independent shared-writable directory
	// outside every workspace write root.
	publicDir := os.Getenv("PUBLIC")
	if publicDir == "" {
		t.Log("PUBLIC is not set; skipping C:\\Users\\Public write-jail probe")
	} else {
		publicProbe := allocateSharedDirectoryProbe(t, publicDir, "elevated-public")
		runWindowsRealSmokeCommand(t, runnerExe, config, publicProbe.DeniedWriteCommand(), deniedWriteExitCode)
		if _, err := os.Stat(publicProbe.Path()); err == nil {
			t.Fatalf("Windows sandbox allowed a write to the shared C:\\Users\\Public directory")
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat public marker: %v", err)
		}
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen loopback for Windows network smoke: %v", err)
	}
	defer listener.Close()
	accepted := make(chan struct{}, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
			accepted <- struct{}{}
			return
		}
	}()

	networkAllowedMarker := filepath.Join(root, "network-allowed.txt")
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("parse listener address %q: %v", listener.Addr(), err)
	}
	script := fmt.Sprintf(`
$marker = %s
$client = [System.Net.Sockets.TcpClient]::new()
$connect = $client.BeginConnect('127.0.0.1', %s, $null, $null)
if ($connect.AsyncWaitHandle.WaitOne(1500, $false)) {
  try {
    $client.EndConnect($connect)
    Set-Content -LiteralPath $marker -Value allowed
    exit 42
  } catch {
    exit 0
  } finally {
    $client.Close()
  }
}
$client.Close()
exit 0
`, powershellSingleQuote(networkAllowedMarker), port)
	runWindowsRealSmokeCommand(t, runnerExe, config, []string{
		"powershell.exe",
		"-NoLogo",
		"-NoProfile",
		"-NonInteractive",
		"-ExecutionPolicy",
		"Bypass",
		"-Command",
		script,
	}, 0)
	if _, err := os.Stat(networkAllowedMarker); err == nil {
		t.Fatalf("Windows sandbox allowed loopback network connection under network deny")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat network marker: %v", err)
	}
	select {
	case <-accepted:
		t.Fatalf("Windows sandbox loopback listener accepted a denied connection")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestWindowsUnelevatedRealSandboxSmoke exercises the unelevated tier end to
// end WITHOUT running the elevated setup helper: the command runner applies
// the workspace ACLs itself, a write inside the workspace succeeds under the
// restricted token, and a write outside every granted root is denied. Unlike
// the elevated smoke above it needs no Administrator terminal.
func TestWindowsUnelevatedRealSandboxSmoke(t *testing.T) {
	if os.Getenv("QG_SANDBOX_REAL_SMOKE") != "1" {
		t.Skip("set QG_SANDBOX_REAL_SMOKE=1 to run real Windows sandbox smoke tests")
	}
	runnerExe := realSmokeExecutable(t, "QG_WINDOWS_COMMAND_RUNNER_EXE", WindowsSandboxCommandRunnerName)

	root := t.TempDir()
	outside := t.TempDir()
	privateDir := filepath.Join(root, "private")
	if err := os.MkdirAll(privateDir, 0o700); err != nil {
		t.Fatalf("MkdirAll private: %v", err)
	}
	secretFile := filepath.Join(privateDir, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("super-secret"), 0o600); err != nil {
		t.Fatalf("WriteFile secret: %v", err)
	}

	sandboxHome := filepath.Join(root, ".qg-sandbox")
	// Success path: restricted FS write-jail with no DenyRead. Non-empty DenyRead
	// is unsupported on both restricted-token tiers under the narrow SID set
	// (PR #640); the rejection probe below covers that separately.
	profile := PermissionProfile{
		FileSystem: FileSystemPolicy{
			Kind:                 FileSystemRestricted,
			ReadRoots:            []string{root},
			WriteRoots:           []WritableRoot{{Root: root, ProtectedMetadataNames: []string{".git", ".qg", ".agents"}}},
			IncludePlatformRoots: true,
			AllowTemp:            true,
		},
		Network: NetworkPolicy{Mode: NetworkDeny},
	}
	config := WindowsSandboxCommandArgsOptions{
		SandboxHome:       sandboxHome,
		CommandCWD:        root,
		WorkspaceRoots:    []string{root},
		PermissionProfile: profile,
		SandboxLevel:      WindowsSandboxLevelUnelevated,
	}

	// cmd.exe rather than powershell.exe: managed PowerShell cannot initialize
	// its crypto provider under a write-restricted token on some hosts
	// (0x8009001d, the same restricted-token limitation the runner documents for
	// Schannel), and the write-jail assertion only needs a native shell.
	insideMarker := filepath.Join(root, "unelevated-write-ok.txt")
	runWindowsRealSmokeCommand(t, runnerExe, config, []string{
		"cmd.exe", "/d", "/s", "/c", "echo ok>" + insideMarker,
	}, 0)
	if bytes, err := os.ReadFile(insideMarker); err != nil || strings.TrimSpace(string(bytes)) != "ok" {
		t.Fatalf("unelevated sandboxed write marker = %q, %v; want ok", bytes, err)
	}
	if _, err := os.Stat(WindowsUnelevatedSetupMarkerPath(sandboxHome)); err != nil {
		t.Fatalf("expected the unelevated setup marker to be recorded: %v", err)
	}

	// DenyRead is SUPPORTED on both restricted-token tiers: the profile runs
	// under the fully restricted token (no WRITE_RESTRICTED, so denies apply to
	// reads per #612), which launches because Everyone is back in the restricted
	// list and the plan grants capability-SID read access on the workspace.
	// The read itself must be denied: the command proves it ran by recording a
	// marker (locale-independent — cmd's own "Access is denied" text varies),
	// and a leaked secret in the marker fails the test just as loudly.
	denyReadConfig := config
	denyReadConfig.PermissionProfile.FileSystem.DenyRead = []string{privateDir}
	deniedMarker := filepath.Join(root, "read-denied.txt")
	runWindowsRealSmokeCommand(t, runnerExe, denyReadConfig, []string{
		"cmd.exe", "/d", "/s", "/c", "(type \"" + secretFile + "\" 2>nul || echo READ-DENIED)>\"" + deniedMarker + "\"",
	}, 0)
	if contents, err := os.ReadFile(deniedMarker); err != nil || strings.TrimSpace(string(contents)) != "READ-DENIED" {
		t.Fatalf("sandboxed read of a DenyRead path = %q, %v; want READ-DENIED", contents, err)
	}
	// The secret must remain intact on the host; the sandbox only failed to read it.
	if data, err := os.ReadFile(secretFile); err != nil || string(data) != "super-secret" {
		t.Fatalf("host secret file after denied DenyRead read: %q, %v", data, err)
	}

	outsideMarker := filepath.Join(outside, "unelevated-write-denied.txt")
	runWindowsRealSmokeCommand(t, runnerExe, config, []string{
		"cmd.exe", "/d", "/s", "/c", "echo leaked>" + outsideMarker,
	}, 1)
	if _, err := os.Stat(outsideMarker); err == nil {
		t.Fatalf("unelevated sandbox allowed a write outside every granted root")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat outside marker: %v", err)
	}

	// Verify write to C:\ProgramData is blocked
	programData := os.Getenv("ProgramData")
	if programData != "" {
		programDataProbe := allocateSharedDirectoryProbe(t, programData, "unelevated-programdata")
		runWindowsRealSmokeCommand(t, runnerExe, config, programDataProbe.DeniedWriteCommand(), deniedWriteExitCode)
		if _, err := os.Stat(programDataProbe.Path()); err == nil {
			t.Fatalf("unelevated sandbox allowed a write to ProgramData shared directory")
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat ProgramData marker: %v", err)
		}
	}
}

// TestWindowsRestrictedTokenNestedPipeCapture pins the fix in
// broadenWindowsRestrictedTokenDefaultDacl (windows_token_windows.go): a
// nested subprocess spawned FROM WITHIN the sandboxed process, with its
// output captured via a pipe, must succeed. Before the fix, the sandboxed
// process's own attempt to create a pipe for such a subprocess failed with
// ERROR_ACCESS_DENIED (Win32 error 5) — the WRITE_RESTRICTED token's extra
// access check has no restricted-SID match against a freshly created pipe's
// default security descriptor. This is exactly the pattern any tool that
// shells out internally and captures output hits (`gh` invoking `git`, for
// one; cmd.exe's own FOR /F does the identical CreatePipe+CreateProcess
// dance internally, so it reproduces the bug with no external dependency
// beyond cmd.exe itself).
func TestWindowsRestrictedTokenNestedPipeCapture(t *testing.T) {
	if os.Getenv("QG_SANDBOX_REAL_SMOKE") != "1" {
		t.Skip("set QG_SANDBOX_REAL_SMOKE=1 to run real Windows sandbox smoke tests")
	}
	runnerExe := realSmokeExecutable(t, "QG_WINDOWS_COMMAND_RUNNER_EXE", WindowsSandboxCommandRunnerName)

	root := t.TempDir()
	sandboxHome := filepath.Join(root, ".qg-sandbox")
	profile := PermissionProfile{
		FileSystem: FileSystemPolicy{
			Kind:                 FileSystemRestricted,
			ReadRoots:            []string{root},
			WriteRoots:           []WritableRoot{{Root: root, ProtectedMetadataNames: []string{".git", ".qg", ".agents"}}},
			IncludePlatformRoots: true,
			AllowTemp:            true,
		},
		Network: NetworkPolicy{Mode: NetworkDeny},
	}
	config := WindowsSandboxCommandArgsOptions{
		SandboxHome:       sandboxHome,
		CommandCWD:        root,
		WorkspaceRoots:    []string{root},
		PermissionProfile: profile,
		SandboxLevel:      WindowsSandboxLevelUnelevated,
	}

	marker := filepath.Join(root, "nested-pipe-marker.txt")
	script := filepath.Join(root, "nested-pipe.cmd")
	// FOR /F drives cmd.exe's own internal CreatePipe+CreateProcess capture
	// of the quoted command's output — the exact mechanism this fix targets.
	// The full path to whoami.exe sidesteps PATH resolution inside the
	// sandboxed process's minimal environment.
	scriptBody := "for /F %%i in ('C:\\Windows\\System32\\whoami.exe') do echo %%i> " + marker + "\r\n"
	if err := os.WriteFile(script, []byte(scriptBody), 0o644); err != nil {
		t.Fatalf("write nested-pipe script: %v", err)
	}

	runWindowsRealSmokeCommand(t, runnerExe, config, []string{
		"cmd.exe", "/d", "/s", "/c", script,
	}, 0)
	captured, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read nested-pipe marker: %v", err)
	}
	if strings.TrimSpace(string(captured)) == "" {
		t.Fatalf("nested-pipe marker is empty; FOR /F failed to capture the subprocess's output")
	}
}

// TestWindowsRestrictedTokenPowerShell exercises the default Windows shell
// shape end to end under the write-restricted token. It covers PowerShell
// initialization, a native pipeline (which creates IPC objects), UTF-8 output,
// and workspace write enforcement.
func TestWindowsRestrictedTokenPowerShell(t *testing.T) {
	if os.Getenv("QG_SANDBOX_REAL_SMOKE") != "1" {
		t.Skip("set QG_SANDBOX_REAL_SMOKE=1 to run real Windows sandbox smoke tests")
	}
	runnerExe := realSmokeExecutable(t, "QG_WINDOWS_COMMAND_RUNNER_EXE", WindowsSandboxCommandRunnerName)
	powerShell := realSmokePowerShell(t)

	root := t.TempDir()
	outside := t.TempDir()
	sandboxHome := filepath.Join(root, ".qg-sandbox")
	profile := PermissionProfile{
		FileSystem: FileSystemPolicy{
			Kind:                 FileSystemRestricted,
			ReadRoots:            []string{root},
			WriteRoots:           []WritableRoot{{Root: root}},
			IncludePlatformRoots: true,
			AllowTemp:            true,
		},
		Network: NetworkPolicy{Mode: NetworkDeny},
	}
	config := WindowsSandboxCommandArgsOptions{
		SandboxHome:       sandboxHome,
		CommandCWD:        root,
		WorkspaceRoots:    []string{root},
		PermissionProfile: profile,
		SandboxLevel:      WindowsSandboxLevelUnelevated,
	}

	insideMarker := filepath.Join(root, "powershell-ok.txt")
	script := "$ErrorActionPreference='Stop'; " +
		"[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; " +
		"$values = 1,2,3 | ForEach-Object { $_ * 2 }; " +
		"[IO.File]::WriteAllText(" + powershellSingleQuote(insideMarker) + ", (($values -join ',') + ' ✓'))"
	runWindowsRealSmokeCommand(t, runnerExe, config, []string{
		powerShell, "-NoLogo", "-NoProfile", "-Command", script,
	}, 0)
	if bytes, err := os.ReadFile(insideMarker); err != nil || strings.TrimSpace(string(bytes)) != "2,4,6 ✓" {
		t.Fatalf("PowerShell sandbox marker = %q, %v; want pipeline output", bytes, err)
	}

	outsideMarker := filepath.Join(outside, "powershell-denied.txt")
	deniedScript := "$ErrorActionPreference='Stop'; " +
		"[IO.File]::WriteAllText(" + powershellSingleQuote(outsideMarker) + ", 'leaked')"
	runWindowsRealSmokeCommand(t, runnerExe, config, []string{
		powerShell, "-NoLogo", "-NoProfile", "-Command", deniedScript,
	}, 1)
	if _, err := os.Stat(outsideMarker); err == nil {
		t.Fatal("sandboxed PowerShell wrote outside every granted root")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat outside PowerShell marker: %v", err)
	}
}

func realSmokePowerShell(t *testing.T) string {
	t.Helper()
	for _, candidate := range []string{"pwsh.exe", "pwsh", "powershell.exe", "powershell"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Skip("PowerShell is unavailable")
	return ""
}

func realSmokeExecutable(t *testing.T, envKey string, fallbackName string) string {
	t.Helper()
	if path := os.Getenv(envKey); path != "" {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s=%q is not usable: %v", envKey, path, err)
		}
		return path
	}
	path, err := exec.LookPath(fallbackName)
	if err != nil {
		t.Skipf("%s is not available and %s is unset", fallbackName, envKey)
	}
	return path
}

func runWindowsRealSmokeSetup(t *testing.T, setupExe string, options WindowsSandboxSetupArgsOptions) {
	t.Helper()
	args, err := BuildWindowsSandboxSetupArgs(options)
	if err != nil {
		t.Fatalf("BuildWindowsSandboxSetupArgs: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, setupExe, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Windows sandbox setup failed: %v\n%s", err, output)
	}
}

func runWindowsRealSmokeCommand(t *testing.T, runnerExe string, base WindowsSandboxCommandArgsOptions, command []string, wantCode int) {
	t.Helper()
	base.Command = command
	args, err := BuildWindowsSandboxCommandArgs(base)
	if err != nil {
		t.Fatalf("BuildWindowsSandboxCommandArgs: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, runnerExe, args...)
	output, err := cmd.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == wantCode {
		return
	}
	if err != nil {
		// Always show the runner's own output and exit code: a bare
		// "exit status 1" hides whether the command ran and failed or the
		// runner refused to launch it at all.
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("Windows sandbox command failed: exit code %d\ncommand: %q\noutput:\n%s", exitErr.ExitCode(), command, output)
		}
		t.Fatalf("Windows sandbox command failed: %v\ncommand: %q\noutput:\n%s", err, command, output)
	}
	if wantCode != 0 {
		t.Fatalf("Windows sandbox command exit code = 0, want %d\ncommand: %q\noutput:\n%s", wantCode, command, output)
	}
}

// runWindowsRealSmokeCommandExpectError runs the command runner and requires a
// non-zero exit whose combined output contains each want substring (used for
// explicit unsupported-mode rejections rather than sandboxed command failures).
func runWindowsRealSmokeCommandExpectError(t *testing.T, runnerExe string, base WindowsSandboxCommandArgsOptions, command []string, wantSubstr ...string) {
	t.Helper()
	base.Command = command
	args, err := BuildWindowsSandboxCommandArgs(base)
	if err != nil {
		t.Fatalf("BuildWindowsSandboxCommandArgs: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, runnerExe, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Windows sandbox command timed out: %v\n%s", ctx.Err(), output)
	}
	if err == nil {
		t.Fatalf("Windows sandbox command exit code = 0, want error containing %v\n%s", wantSubstr, output)
	}
	text := string(output)
	for _, want := range wantSubstr {
		if !strings.Contains(text, want) {
			t.Fatalf("Windows sandbox command error missing %q: %v\n%s", want, err, output)
		}
	}
}

// The write jail must hold on a PLAN-COVERED path whose DACL grants Everyone
// write access.
//
// Everyone is back in the restricted-SID list in both token modes because real
// programs need it (managed PowerShell/.NET fail to initialize without it, and
// the fully restricted token cannot open cmd.exe without it). That reopens the
// #865 hole for ARBITRARY Everyone-writable paths outside plan coverage — share
// roots opened with Everyone:F and loose installer ACLs — which now fall back
// to the ordinary user's own permissions. That residual is stated plainly
// rather than claimed away.
//
// What the compensation keeps closed is every SENSITIVE path the plan manages:
// the ACL plan stamps capability-SID deny ACEs on workspace .qg/.agents, .git
// hooks/config, operator DenyWrite/DenyRead and credential stores, and a
// restricted-SID deny fails the write check in both modes, with Everyone
// present or not. This test proves exactly that: a protected metadata
// directory that ALSO grants Everyone write must stay unwritable to the
// sandbox, even though the restricted-SID check passes for free on it.
func TestWindowsRestrictedTokenDeniesWritesToEveryoneWritablePaths(t *testing.T) {
	if os.Getenv("QG_SANDBOX_REAL_SMOKE") != "1" {
		t.Skip("set QG_SANDBOX_REAL_SMOKE=1 to run real Windows sandbox smoke tests")
	}
	runnerExe := realSmokeExecutable(t, "QG_WINDOWS_COMMAND_RUNNER_EXE", WindowsSandboxCommandRunnerName)

	root := t.TempDir()
	outside := t.TempDir()

	sandboxHome := filepath.Join(root, ".qg-sandbox")
	// No DenyRead on purpose: that is what makes the runner choose the
	// WRITE_RESTRICTED token, which is the default posture.
	profile := PermissionProfile{
		FileSystem: FileSystemPolicy{
			Kind:                 FileSystemRestricted,
			ReadRoots:            []string{root},
			WriteRoots:           []WritableRoot{{Root: root, ProtectedMetadataNames: []string{".git", ".qg", ".agents"}}},
			IncludePlatformRoots: true,
			AllowTemp:            true,
		},
		Network: NetworkPolicy{Mode: NetworkDeny},
	}
	config := WindowsSandboxCommandArgsOptions{
		SandboxHome:       sandboxHome,
		CommandCWD:        root,
		WorkspaceRoots:    []string{root},
		PermissionProfile: profile,
		SandboxLevel:      WindowsSandboxLevelUnelevated,
	}

	// Granted root first. A jail that denies everything would satisfy the
	// assertions below while being useless, and this separates the two outcomes.
	// It also triggers the unelevated on-demand apply, which materializes .qg
	// and stamps the capability deny on it.
	insideMarker := filepath.Join(root, "write-ok.txt")
	runWindowsRealSmokeCommand(t, runnerExe, config, []string{
		"cmd.exe", "/d", "/s", "/c", "echo ok>" + insideMarker,
	}, 0)
	if contents, err := os.ReadFile(insideMarker); err != nil || strings.TrimSpace(string(contents)) != "ok" {
		t.Fatalf("sandboxed write to a granted root = %q, %v; want ok", contents, err)
	}

	// Control: an ordinary directory outside every granted root.
	controlMarker := filepath.Join(outside, "control-denied.txt")
	runWindowsRealSmokeCommand(t, runnerExe, config, deniedWriteCommand(controlMarker), deniedWriteExitCode)
	if _, err := os.Stat(controlMarker); err == nil {
		t.Fatal("precondition: the sandbox allowed a write to an ordinary path outside every granted root, so this test cannot measure the Everyone case")
	}

	// The compensation case: open the protected metadata directory itself to
	// Everyone write through the production ACL applier, the way a hostile or
	// careless DACL would. The capability-SID deny the plan stamped on it must
	// win over the Everyone grant even though the token carries Everyone.
	metadataDir := filepath.Join(root, ".qg")
	snapshot, applied, err := applyWindowsACLPathGroup(windowsACLPathGroup{
		Path: metadataDir,
		Entries: []WindowsACLEntry{{
			Action:     WindowsACLAllowWrite,
			Path:       metadataDir,
			Capability: "S-1-1-0",
		}},
	})
	if err != nil {
		t.Fatalf("grant Everyone write on the metadata dir: %v", err)
	}
	if !applied {
		t.Fatal("precondition: the Everyone grant did not apply, so there is no bypass to test for")
	}
	t.Cleanup(func() { _ = rollbackWindowsACLSnapshots([]windowsACLSnapshot{snapshot}) })

	everyoneMarker := filepath.Join(metadataDir, "everyone-denied.txt")
	runWindowsRealSmokeCommand(t, runnerExe, config, deniedWriteCommand(everyoneMarker), deniedWriteExitCode)
	if _, err := os.Stat(everyoneMarker); err == nil {
		t.Error("the sandbox wrote to a plan-covered metadata dir despite its capability deny: the Everyone grant must not override the sensitive-path compensation")
	} else if !os.IsNotExist(err) {
		t.Errorf("stat the Everyone-writable marker: %v", err)
	}
}

// deniedWriteExitCode is chosen so a denial cannot be confused with the runner
// failing to start the command at all.
//
// The obvious spelling of these assertions is `echo leaked>path` expecting exit
// 1, but the runner itself exits 1 for its own errors — a failed marker
// validation, a bad argument, a token it could not build. A test written that
// way passes both when the sandbox denies the write and when nothing ever ran,
// and the second case proves nothing while looking identical to success. On a
// test whose whole job is catching a confinement regression, that is a failure
// mode to design out rather than hope about.
//
// 77 is arbitrary beyond being outside the range the runner produces for itself.
const deniedWriteExitCode = 77

// deniedWriteCommand attempts a write and reports deniedWriteExitCode when the
// redirect is refused, so the exit code also proves cmd.exe actually ran.
func deniedWriteCommand(marker string) []string {
	return []string{"cmd.exe", "/d", "/c", "echo leaked>" + cmdQuote(marker) + " || exit " + strconv.Itoa(deniedWriteExitCode)}
}

func cmdQuote(path string) string {
	return `"` + strings.ReplaceAll(path, `"`, `\"`) + `"`
}

type sharedDirectoryProbe struct {
	path   string
	marker string
}

func allocateSharedDirectoryProbe(t testing.TB, dir, prefix string) *sharedDirectoryProbe {
	t.Helper()
	if dir == "" {
		t.Skip("shared directory path is not set")
	}
	var probePath string
	for attempt := 0; attempt < 50; attempt++ {
		candidate := filepath.Join(dir, fmt.Sprintf("qg-smoke-%s-%d-%d-%d.txt", prefix, os.Getpid(), time.Now().UnixNano(), attempt))
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			probePath = candidate
			break
		}
	}
	if probePath == "" {
		t.Fatalf("allocate shared directory probe in %s: failed to find unused filename", dir)
	}
	marker := fmt.Sprintf("leaked-%s-%d-%d", prefix, os.Getpid(), time.Now().UnixNano())
	p := &sharedDirectoryProbe{path: probePath, marker: marker}
	t.Cleanup(func() {
		p.cleanup(t)
	})
	return p
}

func (p *sharedDirectoryProbe) Path() string {
	return p.path
}

func (p *sharedDirectoryProbe) Marker() string {
	return p.marker
}

func (p *sharedDirectoryProbe) DeniedWriteCommand() []string {
	return []string{"cmd.exe", "/d", "/c", "echo " + p.marker + ">" + cmdQuote(p.path) + " || exit " + strconv.Itoa(deniedWriteExitCode)}
}

func (p *sharedDirectoryProbe) cleanup(t testing.TB) {
	t.Helper()
	data, err := os.ReadFile(p.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cleanup probe read %s: %v", p.path, err)
		}
		return
	}
	if strings.TrimSpace(string(data)) == p.marker {
		if err := os.Remove(p.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("cleanup probe remove %s: %v", p.path, err)
		}
	}
}

func powershellSingleQuote(value string) string {
	out := "'"
	for _, r := range value {
		if r == '\'' {
			out += "''"
		} else {
			out += string(r)
		}
	}
	return out + "'"
}

func TestSharedDirectoryProbeLifecycle(t *testing.T) {
	dir := t.TempDir()

	// 1. Two simultaneous allocations produce distinct non-colliding paths and markers.
	probe1 := allocateSharedDirectoryProbe(t, dir, "p1")
	probe2 := allocateSharedDirectoryProbe(t, dir, "p2")
	if probe1.Path() == probe2.Path() {
		t.Fatalf("expected distinct probe paths, got %q and %q", probe1.Path(), probe2.Path())
	}
	if probe1.Marker() == probe2.Marker() {
		t.Fatalf("expected distinct probe markers, got %q and %q", probe1.Marker(), probe2.Marker())
	}

	// 2. Pre-existing unrelated file is never selected or deleted.
	unrelatedFile := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(unrelatedFile, []byte("preserve me"), 0o600); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}
	probe3 := allocateSharedDirectoryProbe(t, dir, "p3")
	probe3.cleanup(t)
	if data, err := os.ReadFile(unrelatedFile); err != nil || string(data) != "preserve me" {
		t.Fatalf("unrelated file was modified or deleted: data=%q, err=%v", data, err)
	}

	// 3. Interrupted / no-create path: cleanup on absent file succeeds quietly.
	probe4 := allocateSharedDirectoryProbe(t, dir, "p4")
	probe4.cleanup(t)

	// 4. Unexpected write created during test with matching marker is cleaned up.
	probe5 := allocateSharedDirectoryProbe(t, dir, "p5")
	if err := os.WriteFile(probe5.Path(), []byte(probe5.Marker()), 0o600); err != nil {
		t.Fatalf("write probe5 file: %v", err)
	}
	probe5.cleanup(t)
	if _, err := os.Lstat(probe5.Path()); !os.IsNotExist(err) {
		t.Fatalf("expected probe5 to be cleaned up after creation, stat err=%v", err)
	}

	// 5. File at probe path with foreign/unmatched content is NOT removed.
	probe6 := allocateSharedDirectoryProbe(t, dir, "p6")
	if err := os.WriteFile(probe6.Path(), []byte("unrelated-foreign-content"), 0o600); err != nil {
		t.Fatalf("write probe6 file: %v", err)
	}
	probe6.cleanup(t)
	if _, err := os.Lstat(probe6.Path()); err != nil {
		t.Fatalf("expected probe6 with foreign content to be preserved, stat err=%v", err)
	}
	_ = os.Remove(probe6.Path())
}
