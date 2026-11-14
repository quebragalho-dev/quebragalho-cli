package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubLinuxBwrapProbe replaces the live bwrap usability probe for the test,
// restoring it afterwards. Tests that stub the executable lookup with a
// non-existent /usr/bin/bwrap must stub the probe too: selection now runs the
// real binary, and a stubbed path would fail the probe.
func stubLinuxBwrapProbe(t *testing.T, err error) {
	t.Helper()
	previous := linuxBwrapProbe
	linuxBwrapProbe = func(string) error { return err }
	t.Cleanup(func() { linuxBwrapProbe = previous })
}

// writeFakeBwrap writes an executable stub standing in for the bwrap binary:
// a "working" stub execs its arguments (so the probe's `true` succeeds),
// while a "broken" stub replays the Ubuntu 24.04 AppArmor failure on stderr.
func writeFakeBwrap(t *testing.T, broken bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bwrap")
	var body string
	if broken {
		body = "#!/bin/sh\necho 'bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted' >&2\nexit 1\n"
	} else {
		// Mimic bwrap's option separator: drop every flag up to `--`, then
		// exec the command, so the probe's `true` decides the exit status.
		body = "#!/bin/sh\nwhile [ $# -gt 0 ]; do if [ \"$1\" = \"--\" ]; then shift; break; fi; shift; done\nexec \"$@\"\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake bwrap: %v", err)
	}
	return path
}

func linuxProbeLookup(helperPath, bwrapPath string) func(string) (string, error) {
	return func(name string) (string, error) {
		switch name {
		case LinuxSandboxHelperName:
			return helperPath, nil
		case "bwrap":
			return bwrapPath, nil
		}
		return "", errors.New("missing")
	}
}

// TestLinuxBwrapProbeRejectsBrokenBubblewrap pins the regression: a bwrap
// binary that exists but cannot build namespaces (Ubuntu 24.04 AppArmor
// default) must NOT select the native backend. Selection must stay honest —
// unavailable with a Portuguese remediation — instead of handing every
// wrapped command a backend that fails them all.
func TestLinuxBwrapProbeRejectsBrokenBubblewrap(t *testing.T) {
	helper := filepath.Join(t.TempDir(), LinuxSandboxHelperName)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake helper: %v", err)
	}
	restoreExe := osExecutable
	osExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "qg"), nil }
	defer func() { osExecutable = restoreExe }()

	backend := SelectBackend(BackendOptions{
		GOOS:             "linux",
		LookupExecutable: linuxProbeLookup(helper, writeFakeBwrap(t, true)),
		DetectWSL:        func() WSLInfo { return WSLInfo{} },
	})
	if backend.Name != BackendUnavailable || backend.Available {
		t.Fatalf("backend = %#v, want unavailable when the bwrap probe fails", backend)
	}
	for _, want := range []string{
		BubblewrapUnusableMessagePrefix,
		"AppArmor",
		"apparmor_restrict_unprivileged_userns",
		"sysctl",
		"loopback",
	} {
		if !strings.Contains(backend.Message, want) {
			t.Fatalf("backend message = %q, want it to contain %q", backend.Message, want)
		}
	}
}

// TestLinuxBwrapProbeAcceptsWorkingBubblewrap guards the other side: a bwrap
// that passes the probe keeps selecting the native backend, so the probe does
// not brick healthy machines.
func TestLinuxBwrapProbeAcceptsWorkingBubblewrap(t *testing.T) {
	helper := filepath.Join(t.TempDir(), LinuxSandboxHelperName)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake helper: %v", err)
	}
	restoreExe := osExecutable
	osExecutable = func() (string, error) { return filepath.Join(t.TempDir(), "qg"), nil }
	defer func() { osExecutable = restoreExe }()

	backend := SelectBackend(BackendOptions{
		GOOS:             "linux",
		LookupExecutable: linuxProbeLookup(helper, writeFakeBwrap(t, false)),
		DetectWSL:        func() WSLInfo { return WSLInfo{} },
	})
	if backend.Name != BackendLinuxBwrap || !backend.Available {
		t.Fatalf("backend = %#v, want available native backend when the bwrap probe passes", backend)
	}
}

// TestRunLinuxBwrapProbe PropagatesStderr proves the probe surfaces bwrap's
// own diagnostic (the loopback line operators will search for) instead of a
// bare exit status.
func TestRunLinuxBwrapProbePropagatesStderr(t *testing.T) {
	err := runLinuxBwrapProbe(writeFakeBwrap(t, true))
	if err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("probe error = %v, want the bwrap loopback diagnostic", err)
	}
	if err := runLinuxBwrapProbe(writeFakeBwrap(t, false)); err != nil {
		t.Fatalf("probe with working bwrap: %v", err)
	}
}
