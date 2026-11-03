package sandbox

import (
	"errors"
	"path/filepath"
	"testing"
)

// The Linux helper must resolve beside the running binary first (the
// installer and npm platform-package layout) and only then on PATH —
// mirroring the Windows tiers — so a qg installed outside PATH still finds
// its sandbox helper instead of degrading to unwrapped execution.
func TestResolveLinuxSandboxHelperTiers(t *testing.T) {
	restore := osExecutable
	defer func() { osExecutable = restore }()

	t.Run("adjacent helper wins over PATH", func(t *testing.T) {
		dir := t.TempDir()
		adjacent := filepath.Join(dir, LinuxSandboxHelperName)
		writeFile(t, adjacent)
		osExecutable = func() (string, error) { return filepath.Join(dir, "qg"), nil }
		got, err := ResolveLinuxSandboxHelperCommand(func(string) (string, error) {
			return "/usr/bin/" + LinuxSandboxHelperName, nil // must NOT be chosen
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.Name != adjacent {
			t.Fatalf("helper = %q, want adjacent %q", got.Name, adjacent)
		}
	})

	t.Run("PATH when no adjacent helper", func(t *testing.T) {
		dir := t.TempDir()
		osExecutable = func() (string, error) { return filepath.Join(dir, "qg"), nil }
		got, err := ResolveLinuxSandboxHelperCommand(func(name string) (string, error) {
			if name == LinuxSandboxHelperName {
				return "/usr/bin/" + LinuxSandboxHelperName, nil
			}
			return "", errors.New("missing")
		})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if got.Name != "/usr/bin/"+LinuxSandboxHelperName {
			t.Fatalf("helper = %q, want the PATH entry", got.Name)
		}
	})

	t.Run("error when neither adjacent nor PATH", func(t *testing.T) {
		dir := t.TempDir()
		osExecutable = func() (string, error) { return filepath.Join(dir, "qg"), nil }
		if _, err := ResolveLinuxSandboxHelperCommand(func(string) (string, error) {
			return "", errors.New("missing")
		}); err == nil {
			t.Fatal("expected an error when the helper is nowhere to be found")
		}
	})
}

func TestSelectLinuxBackendUsesAdjacentHelper(t *testing.T) {
	restore := osExecutable
	defer func() { osExecutable = restore }()
	missing := func(string) (string, error) { return "", errors.New("missing") }

	t.Run("helper beside binary without PATH entry is available", func(t *testing.T) {
		dir := t.TempDir()
		adjacent := filepath.Join(dir, LinuxSandboxHelperName)
		writeFile(t, adjacent)
		osExecutable = func() (string, error) { return filepath.Join(dir, "qg"), nil }
		backend := SelectBackend(BackendOptions{
			GOOS: "linux",
			LookupExecutable: func(name string) (string, error) {
				if name == "bwrap" {
					return "/usr/bin/bwrap", nil
				}
				return "", errors.New("missing")
			},
			DetectWSL: func() WSLInfo { return WSLInfo{} },
		})
		if backend.Name != BackendLinuxBwrap || !backend.Available {
			t.Fatalf("backend = %#v, want available linux-bwrap", backend)
		}
		if backend.Executable != adjacent {
			t.Fatalf("executable = %q, want adjacent helper %q", backend.Executable, adjacent)
		}
	})

	t.Run("helper on PATH without adjacent entry is available", func(t *testing.T) {
		dir := t.TempDir()
		osExecutable = func() (string, error) { return filepath.Join(dir, "qg"), nil }
		backend := SelectBackend(BackendOptions{
			GOOS: "linux",
			LookupExecutable: func(name string) (string, error) {
				if name == LinuxSandboxHelperName || name == "bwrap" {
					return "/usr/bin/" + name, nil
				}
				return "", errors.New("missing")
			},
			DetectWSL: func() WSLInfo { return WSLInfo{} },
		})
		if backend.Name != BackendLinuxBwrap || !backend.Available {
			t.Fatalf("backend = %#v, want available linux-bwrap", backend)
		}
		if backend.Executable != "/usr/bin/"+LinuxSandboxHelperName {
			t.Fatalf("executable = %q, want the PATH helper", backend.Executable)
		}
	})

	t.Run("no helper is unavailable with the helper message", func(t *testing.T) {
		dir := t.TempDir()
		osExecutable = func() (string, error) { return filepath.Join(dir, "qg"), nil }
		backend := SelectBackend(BackendOptions{
			GOOS:             "linux",
			LookupExecutable: missing,
			DetectWSL:        func() WSLInfo { return WSLInfo{} },
		})
		if backend.Name != BackendUnavailable || backend.Available {
			t.Fatalf("backend = %#v, want unavailable", backend)
		}
		if backend.Message != "Linux sandbox helper is not available" {
			t.Fatalf("message = %q, want the helper diagnostic", backend.Message)
		}
	})

	t.Run("missing bwrap is unavailable with the bwrap message", func(t *testing.T) {
		dir := t.TempDir()
		adjacent := filepath.Join(dir, LinuxSandboxHelperName)
		writeFile(t, adjacent)
		osExecutable = func() (string, error) { return filepath.Join(dir, "qg"), nil }
		backend := SelectBackend(BackendOptions{
			GOOS:             "linux",
			LookupExecutable: missing,
			DetectWSL:        func() WSLInfo { return WSLInfo{} },
		})
		if backend.Name != BackendUnavailable || backend.Available {
			t.Fatalf("backend = %#v, want unavailable", backend)
		}
		if backend.Message != "bubblewrap is not installed" {
			t.Fatalf("message = %q, want the bwrap diagnostic", backend.Message)
		}
	})
}
