package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestSelectLinuxBackendResolvesHelper pins the single Linux helper resolution
// (beside the running binary first, then PATH) through backend selection: an
// executable adjacent helper wins even when PATH offers another, a
// non-executable adjacent entry is skipped in favor of PATH, and the missing
// pieces report their exact machine diagnostics.
func TestSelectLinuxBackendResolvesHelper(t *testing.T) {
	restore := osExecutable
	defer func() { osExecutable = restore }()
	missing := func(string) (string, error) { return "", errors.New("missing") }
	pathLookup := func(name string) (string, error) {
		if name == LinuxSandboxHelperName || name == "bwrap" {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("missing")
	}

	tests := []struct {
		name string
		// adjacent is the fixture placed beside the fake qg binary: "exec" (an
		// executable file), "plain" (a 0644 file), or "" (absent).
		adjacent string
		lookup   func(string) (string, error)
		// wantExecutable is "adjacent" for the beside-the-binary helper, a
		// literal PATH entry, or "" when unavailable.
		wantExecutable string
		wantAvailable  bool
		wantName       BackendName
		wantMessage    string
	}{
		{
			name:           "adjacent executable wins over PATH",
			adjacent:       "exec",
			lookup:         func(name string) (string, error) { return "/decoy/" + name, nil },
			wantExecutable: "adjacent",
			wantAvailable:  true,
			wantName:       BackendLinuxBwrap,
		},
		{
			name:           "helper beside binary without PATH entry is available",
			adjacent:       "exec",
			lookup:         func(name string) (string, error) { return "/usr/bin/bwrap", nil },
			wantExecutable: "adjacent",
			wantAvailable:  true,
			wantName:       BackendLinuxBwrap,
		},
		{
			name:           "helper on PATH without adjacent entry is available",
			lookup:         pathLookup,
			wantExecutable: "/usr/bin/" + LinuxSandboxHelperName,
			wantAvailable:  true,
			wantName:       BackendLinuxBwrap,
		},
		{
			name:     "non-executable adjacent entry falls back to PATH",
			adjacent: "plain",
			lookup:   pathLookup,
			// A 0644 file beside the binary must NOT be chosen: only an
			// executable regular file counts, so resolution falls through.
			wantExecutable: "/usr/bin/" + LinuxSandboxHelperName,
			wantAvailable:  true,
			wantName:       BackendLinuxBwrap,
		},
		{
			name:          "no helper is unavailable with the helper message",
			lookup:        missing,
			wantAvailable: false,
			wantName:      BackendUnavailable,
			wantMessage:   "Linux sandbox helper is not available",
		},
		{
			name:          "missing bwrap is unavailable with the bwrap message",
			adjacent:      "exec",
			lookup:        missing,
			wantAvailable: false,
			wantName:      BackendUnavailable,
			wantMessage:   "bubblewrap is not installed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			switch tc.adjacent {
			case "exec":
				writeFile(t, filepath.Join(dir, LinuxSandboxHelperName))
			case "plain":
				if err := os.WriteFile(filepath.Join(dir, LinuxSandboxHelperName), []byte("stub"), 0o644); err != nil {
					t.Fatalf("write plain helper: %v", err)
				}
			}
			osExecutable = func() (string, error) { return filepath.Join(dir, "qg"), nil }
			backend := SelectBackend(BackendOptions{
				GOOS:             "linux",
				LookupExecutable: tc.lookup,
				DetectWSL:        func() WSLInfo { return WSLInfo{} },
			})
			if backend.Name != tc.wantName || backend.Available != tc.wantAvailable {
				t.Fatalf("backend = %#v, want name %q available %v", backend, tc.wantName, tc.wantAvailable)
			}
			wantExecutable := tc.wantExecutable
			if wantExecutable == "adjacent" {
				wantExecutable = filepath.Join(dir, LinuxSandboxHelperName)
			}
			if backend.Executable != wantExecutable {
				t.Fatalf("executable = %q, want %q", backend.Executable, wantExecutable)
			}
			if tc.wantMessage != "" && backend.Message != tc.wantMessage {
				t.Fatalf("message = %q, want %q", backend.Message, tc.wantMessage)
			}
		})
	}
}
