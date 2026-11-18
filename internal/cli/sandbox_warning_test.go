package cli

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

// Disabled configurations share one explicit-false pointer: sandbox.enabled
// is only ever read, never written, by the warning path.
func sandboxDisabledPtr() *bool {
	disabled := false
	return &disabled
}

func TestSandboxUnavailableSessionWarning(t *testing.T) {
	unavailable := sandbox.Backend{Name: sandbox.BackendUnavailable, Platform: "linux", Fallback: true, Message: "broken"}
	available := sandbox.Backend{Name: sandbox.BackendLinuxBwrap, Available: true, Platform: "linux", Executable: "/usr/bin/qg-linux-sandbox"}

	tests := []struct {
		name    string
		enabled bool
		backend sandbox.Backend
		want    bool
	}{
		{name: "enabled and unavailable warns", enabled: true, backend: unavailable, want: true},
		{name: "enabled and available stays silent", enabled: true, backend: available, want: false},
		{name: "disabled and unavailable stays silent", enabled: false, backend: unavailable, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sandboxUnavailableSessionWarning(tc.enabled, tc.backend)
			if tc.want && got == "" {
				t.Fatal("expected the session warning, got empty")
			}
			if !tc.want && got != "" {
				t.Fatalf("expected silence, got %q", got)
			}
			if tc.want {
				for _, want := range []string{"Ambiente isolado", "isolamento nativo", "Rode `qg doctor`"} {
					if !strings.Contains(got, want) {
						t.Fatalf("warning = %q, want it to contain %q", got, want)
					}
				}
				for _, unwanted := range []string{"docs/INSTALL.md", "Ubuntu", "AppArmor", "sysctl"} {
					if strings.Contains(got, unwanted) {
						t.Fatalf("warning = %q, must stay platform-neutral (no %q)", got, unwanted)
					}
				}
			}
		})
	}
}

func TestSandboxUnavailableSessionWarningSkipsAlreadySandboxed(t *testing.T) {
	t.Setenv(sandbox.EnvSandboxed, "1")
	t.Setenv(sandbox.EnvSandboxBackend, string(sandbox.BackendLinuxBwrap))
	unavailable := sandbox.Backend{Name: sandbox.BackendUnavailable, Platform: "linux"}
	if got := sandboxUnavailableSessionWarning(true, unavailable); got != "" {
		t.Fatalf("already isolated process must stay silent, got %q", got)
	}
}

// runExecWithStubSandboxBackend drives the full exec entry point with a fake
// tool-calling provider and a stubbed backend, returning captured
// stdout/stderr and the exit code.
func runExecWithStubSandboxBackend(t *testing.T, args []string, backend sandbox.Backend, resolve func(string, config.Overrides) (config.ResolvedConfig, error)) (string, string, int) {
	t.Helper()
	if resolve == nil {
		resolve = func(string, config.Overrides) (config.ResolvedConfig, error) {
			return execResolvedConfig(), nil
		}
	}
	var stdout, stderr bytes.Buffer
	code := runWithDeps(args, &stdout, &stderr, appDeps{
		getwd:         func() (string, error) { return t.TempDir(), nil },
		resolveConfig: resolve,
		newProvider: func(config.ProviderProfile) (qgruntime.Provider, error) {
			return toolThenTextProvider{toolName: "glob"}, nil
		},
		selectSandboxBackend: func(sandbox.BackendOptions) sandbox.Backend { return backend },
	})
	return stdout.String(), stderr.String(), code
}

func unavailableStubBackend() sandbox.Backend {
	return sandbox.Backend{Name: sandbox.BackendUnavailable, Platform: "linux", Fallback: true, Message: "bubblewrap cannot create sandboxes"}
}

func availableStubBackend() sandbox.Backend {
	return sandbox.Backend{Name: sandbox.BackendLinuxBwrap, Available: true, Platform: "linux", Executable: "/usr/bin/qg-linux-sandbox"}
}

// availableSandboxBackendStub fakes a working native backend so tests that
// assert exact stderr stay hermetic on machines where the host sandbox is
// unavailable (e.g. Ubuntu 24.04 AppArmor default): without it, the honest
// session warning would pollute their stderr. Tests for the warning itself
// live in this file and stub the backend explicitly instead.
func availableSandboxBackendStub(sandbox.BackendOptions) sandbox.Backend {
	return availableStubBackend()
}

// TestExecWarnsOnceOnStderrWhenSandboxUnavailable pins the exec half of the
// session warning: exactly one stderr line with the unavailable backend, and a
// clean stdout (the stream-json contract) in both text and stream-json modes.
func TestExecWarnsOnceOnStderrWhenSandboxUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec fake-provider harness assumes a POSIX process environment")
	}
	for _, args := range [][]string{
		{"exec", "--skip-permissions-unsafe", "--max-turns", "3", "go"},
		{"exec", "-o", "stream-json", "--skip-permissions-unsafe", "--max-turns", "3", "go"},
	} {
		stdout, stderr, code := runExecWithStubSandboxBackend(t, args, unavailableStubBackend(), nil)
		if code != exitSuccess {
			t.Fatalf("args %v: exit = %d, stderr = %q", args, code, stderr)
		}
		if got := strings.Count(stderr, "Ambiente isolado"); got != 1 {
			t.Fatalf("args %v: stderr mentions the warning %d times, want exactly 1: %q", args, got, stderr)
		}
		if strings.Contains(stdout, "Ambiente isolado") {
			t.Fatalf("args %v: warning leaked to stdout: %q", args, stdout)
		}
	}
}

// TestExecStaysSilentWithWorkingBackendOrDisabledSandbox covers the two silent
// paths through the same entry point: a working backend, and an explicitly
// disabled sandbox with the backend unavailable.
func TestExecStaysSilentWithWorkingBackendOrDisabledSandbox(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec fake-provider harness assumes a POSIX process environment")
	}
	t.Run("backend ok", func(t *testing.T) {
		_, stderr, code := runExecWithStubSandboxBackend(t,
			[]string{"exec", "--skip-permissions-unsafe", "--max-turns", "3", "go"},
			availableStubBackend(), nil)
		if code != exitSuccess {
			t.Fatalf("exit = %d, stderr = %q", code, stderr)
		}
		if strings.Contains(stderr, "Ambiente isolado") {
			t.Fatalf("working backend must stay silent, stderr = %q", stderr)
		}
	})
	t.Run("sandbox disabled", func(t *testing.T) {
		disabled := execResolvedConfig()
		disabled.Sandbox.Enabled = sandboxDisabledPtr()
		_, stderr, code := runExecWithStubSandboxBackend(t,
			[]string{"exec", "--skip-permissions-unsafe", "--max-turns", "3", "go"},
			unavailableStubBackend(),
			func(string, config.Overrides) (config.ResolvedConfig, error) { return disabled, nil })
		if code != exitSuccess {
			t.Fatalf("exit = %d, stderr = %q", code, stderr)
		}
		if strings.Contains(stderr, "Ambiente isolado") {
			t.Fatalf("disabled sandbox must stay silent, stderr = %q", stderr)
		}
	})
}

// The Windows setup warning fires when the helper is installed but `qg
// sandbox setup` never ran: commands still run, but without OS-level network
// enforcement, and the operator should hear it once per session.
func TestSandboxSetupPendingWarning(t *testing.T) {
	windowsBackend := sandbox.Backend{Name: sandbox.BackendWindowsRestrictedToken, Available: true, Platform: "windows"}
	pendingHome := t.TempDir()
	t.Setenv("QG_WINDOWS_SANDBOX_HOME", pendingHome)

	if got := sandboxSetupPendingWarning(true, windowsBackend, "windows"); got == "" {
		t.Fatal("expected the setup-pending warning with no marker on disk")
	} else {
		for _, want := range []string{"Sandbox do Windows", "qg sandbox setup", "elevado"} {
			if !strings.Contains(got, want) {
				t.Fatalf("warning = %q, want it to contain %q", got, want)
			}
		}
	}

	t.Run("silent paths", func(t *testing.T) {
		linuxBackend := sandbox.Backend{Name: sandbox.BackendLinuxBwrap, Available: true, Platform: "linux"}
		unavailable := sandbox.Backend{Name: sandbox.BackendUnavailable, Platform: "windows"}
		for _, tc := range []struct {
			name    string
			enabled bool
			backend sandbox.Backend
			goos    string
		}{
			{"disabled stays silent", false, windowsBackend, "windows"},
			{"off windows stays silent", true, windowsBackend, "linux"},
			{"other backend stays silent", true, linuxBackend, "windows"},
			{"unavailable backend stays silent", true, unavailable, "windows"},
		} {
			if got := sandboxSetupPendingWarning(tc.enabled, tc.backend, tc.goos); got != "" {
				t.Fatalf("%s: got %q, want silence", tc.name, got)
			}
		}
	})

	t.Run("already sandboxed stays silent", func(t *testing.T) {
		t.Setenv(sandbox.EnvSandboxed, "1")
		t.Setenv(sandbox.EnvSandboxBackend, string(sandbox.BackendWindowsRestrictedToken))
		if got := sandboxSetupPendingWarning(true, windowsBackend, "windows"); got != "" {
			t.Fatalf("already isolated process must stay silent, got %q", got)
		}
	})

	t.Run("initialized stays silent", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("QG_WINDOWS_SANDBOX_HOME", home)
		workspace := t.TempDir()
		if _, err := sandbox.WriteWindowsSandboxSetupMarker(sandbox.WindowsSandboxSetupConfig{
			SandboxHome:    home,
			CommandCWD:     workspace,
			WorkspaceRoots: []string{workspace},
			PermissionProfile: sandbox.PermissionProfile{
				FileSystem: sandbox.FileSystemPolicy{Kind: sandbox.FileSystemRestricted},
				Network:    sandbox.NetworkPolicy{Mode: sandbox.NetworkDeny},
			},
		}); err != nil {
			t.Fatalf("WriteWindowsSandboxSetupMarker: %v", err)
		}
		if got := sandboxSetupPendingWarning(true, windowsBackend, "windows"); got != "" {
			t.Fatalf("initialized host must stay silent, got %q", got)
		}
	})
}
