package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/workspacetrust"
)

// trustDeps builds a test appDeps whose getwd returns a fixed directory, so the
// trust command can be exercised without touching the real cwd.
func trustDeps(cwd string) appDeps {
	return appDeps{getwd: func() (string, error) { return cwd, nil }}
}

// TestRunTrustAddThenList proves `trust` trusts the cwd and `trust list` shows it.
func TestRunTrustAddThenList(t *testing.T) {
	setTrustConfigRoot(t)
	cwd := t.TempDir()
	deps := trustDeps(cwd)

	var out, errBuf bytes.Buffer
	if code := runTrust(nil, &out, &errBuf, deps); code != exitSuccess {
		t.Fatalf("trust returned %d, want %d; stderr=%q", code, exitSuccess, errBuf.String())
	}
	if !strings.Contains(out.String(), "Pasta de trabalho confiável: "+cwd) {
		t.Fatalf("trust stdout = %q, want it to contain %q", out.String(), "Pasta de trabalho confiável: "+cwd)
	}

	trusted, err := workspacetrust.IsTrusted(cwd)
	if err != nil {
		t.Fatalf("IsTrusted after trust: %v", err)
	}
	if !trusted {
		t.Fatalf("cwd should be trusted after `trust`")
	}

	// The store canonicalizes with filepath.EvalSymlinks, so `list` prints the
	// resolved path. On platforms where the temp dir is a symlink (macOS /var) or a
	// Windows 8.3 short name (RUNNER~1 -> runneradmin), that differs from the raw
	// cwd; compare against the same canonical form the store records.
	wantListed := cwd
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		wantListed = resolved
	}

	out.Reset()
	errBuf.Reset()
	if code := runTrust([]string{"list"}, &out, &errBuf, deps); code != exitSuccess {
		t.Fatalf("trust list returned %d, want %d; stderr=%q", code, exitSuccess, errBuf.String())
	}
	if !strings.Contains(out.String(), wantListed) {
		t.Fatalf("trust list stdout = %q, want it to contain the trusted root %q", out.String(), wantListed)
	}
}

// TestRunTrustListShowsCanonicalPath executes the canonical-path behavior that broke
// TestRunTrustAddThenList on the Windows runner (an 8.3 short name expanded by
// EvalSymlinks). It uses a symlinked cwd -- the portable, Linux-runnable analogue of
// that divergence -- and proves `trust list` prints the resolved path the store
// records, never the raw pre-resolution path.
func TestRunTrustListShowsCanonicalPath(t *testing.T) {
	setTrustConfigRoot(t)
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("EvalSymlinks(link): %v", err)
	}
	if resolved == link {
		t.Skip("symlink did not change the path on this platform")
	}

	deps := trustDeps(link)
	if code := runTrust(nil, &bytes.Buffer{}, &bytes.Buffer{}, deps); code != exitSuccess {
		t.Fatalf("trust returned %d", code)
	}

	var out, errBuf bytes.Buffer
	if code := runTrust([]string{"list"}, &out, &errBuf, deps); code != exitSuccess {
		t.Fatalf("trust list returned %d; stderr=%q", code, errBuf.String())
	}
	if !strings.Contains(out.String(), resolved) {
		t.Fatalf("trust list = %q, want the canonical path %q", out.String(), resolved)
	}
	if strings.Contains(out.String(), link) {
		t.Fatalf("trust list = %q, must not contain the raw pre-resolution path %q", out.String(), link)
	}
}

// TestRunTrustRemoveCurrentDir proves `trust remove` (no arg) untrusts the cwd and
// `trust list` then reports none.
func TestRunTrustRemoveCurrentDir(t *testing.T) {
	setTrustConfigRoot(t)
	cwd := t.TempDir()
	deps := trustDeps(cwd)

	if code := runTrust(nil, &bytes.Buffer{}, &bytes.Buffer{}, deps); code != exitSuccess {
		t.Fatalf("trust setup returned %d", code)
	}

	var out, errBuf bytes.Buffer
	if code := runTrust([]string{"remove"}, &out, &errBuf, deps); code != exitSuccess {
		t.Fatalf("trust remove returned %d, want %d; stderr=%q", code, exitSuccess, errBuf.String())
	}
	if !strings.Contains(out.String(), "Pasta de trabalho removida da lista de confiáveis: "+cwd) {
		t.Fatalf("trust remove stdout = %q, want it to contain %q", out.String(), "Pasta de trabalho removida da lista de confiáveis: "+cwd)
	}

	trusted, err := workspacetrust.IsTrusted(cwd)
	if err != nil {
		t.Fatalf("IsTrusted after remove: %v", err)
	}
	if trusted {
		t.Fatalf("cwd should not be trusted after `trust remove`")
	}

	out.Reset()
	errBuf.Reset()
	if code := runTrust([]string{"list"}, &out, &errBuf, deps); code != exitSuccess {
		t.Fatalf("trust list returned %d, want %d", code, exitSuccess)
	}
	if !strings.Contains(out.String(), "Nenhuma pasta de trabalho confiável.") {
		t.Fatalf("trust list stdout = %q, want %q for an empty store", out.String(), "Nenhuma pasta de trabalho confiável.")
	}
}

// TestRunTrustRemoveNamedPathNonCanonical proves `trust remove <path>` untrusts a
// specific path, and that a non-canonical argument (a trailing slash) still matches
// the stored normalized entry.
func TestRunTrustRemoveNamedPathNonCanonical(t *testing.T) {
	setTrustConfigRoot(t)
	// The cwd is a different directory than the one we trust-and-remove, so this
	// exercises the named-path branch, not the bare-cwd branch.
	cwd := t.TempDir()
	target := t.TempDir()
	deps := trustDeps(cwd)

	if err := workspacetrust.Trust(target); err != nil {
		t.Fatalf("Trust(target): %v", err)
	}
	trusted, err := workspacetrust.IsTrusted(target)
	if err != nil || !trusted {
		t.Fatalf("target should be trusted before remove (trusted=%v err=%v)", trusted, err)
	}

	// Pass the target with a trailing slash: normalization must still match the
	// stored canonical entry.
	var out, errBuf bytes.Buffer
	if code := runTrust([]string{"remove", target + "/"}, &out, &errBuf, deps); code != exitSuccess {
		t.Fatalf("trust remove <path> returned %d, want %d; stderr=%q", code, exitSuccess, errBuf.String())
	}

	trusted, err = workspacetrust.IsTrusted(target)
	if err != nil {
		t.Fatalf("IsTrusted after named remove: %v", err)
	}
	if trusted {
		t.Fatalf("target should be untrusted after `trust remove <path>/` despite the trailing slash")
	}
}

// TestRunTrustUnknownSubcommand proves an unknown subcommand returns exit code 2 and
// writes usage to stderr (not stdout).
func TestRunTrustUnknownSubcommand(t *testing.T) {
	setTrustConfigRoot(t)
	deps := trustDeps(t.TempDir())

	var out, errBuf bytes.Buffer
	if code := runTrust([]string{"bogus"}, &out, &errBuf, deps); code != exitUsage {
		t.Fatalf("unknown subcommand returned %d, want %d", code, exitUsage)
	}
	if errBuf.Len() == 0 {
		t.Fatalf("unknown subcommand should write usage to stderr, stderr was empty")
	}
	if !strings.Contains(errBuf.String(), "trust") {
		t.Fatalf("stderr usage = %q, want it to mention `trust`", errBuf.String())
	}
	if out.Len() != 0 {
		t.Fatalf("unknown subcommand should not write to stdout, got %q", out.String())
	}
}

// TestRunTrustRemoveTooManyArgs proves `trust remove a b` (more than one path) is a
// usage error: exit code 2, usage on stderr, nothing on stdout, and no store change.
func TestRunTrustRemoveTooManyArgs(t *testing.T) {
	setTrustConfigRoot(t)
	deps := trustDeps(t.TempDir())

	var out, errBuf bytes.Buffer
	if code := runTrust([]string{"remove", "a", "b"}, &out, &errBuf, deps); code != exitUsage {
		t.Fatalf("remove with two args returned %d, want %d", code, exitUsage)
	}
	if errBuf.Len() == 0 {
		t.Fatalf("remove with two args should write usage to stderr, stderr was empty")
	}
	if out.Len() != 0 {
		t.Fatalf("remove with two args should not write to stdout, got %q", out.String())
	}
}

// TestTrustListJSON proves `trust list --json` emits {"roots": [...]} and
// {"roots": []} for an empty store. Unknown positionals and flags are usage
// errors that write nothing to stdout.
func TestTrustListJSON(t *testing.T) {
	setTrustConfigRoot(t)
	cwd := t.TempDir()
	deps := trustDeps(cwd)

	var out, errBuf bytes.Buffer
	if code := runTrust([]string{"list", "--json"}, &out, &errBuf, deps); code != exitSuccess {
		t.Fatalf("trust list --json returned %d, want %d; stderr=%q", code, exitSuccess, errBuf.String())
	}
	var empty map[string]any
	if err := json.Unmarshal(out.Bytes(), &empty); err != nil {
		t.Fatalf("empty trust list --json is not valid JSON %q: %v", out.String(), err)
	}
	emptyRoots, ok := empty["roots"].([]any)
	if !ok || len(emptyRoots) != 0 || len(empty) != 1 {
		t.Fatalf("empty trust list --json = %q, want {\"roots\": []}", out.String())
	}

	if err := workspacetrust.Trust(cwd); err != nil {
		t.Fatalf("Trust(cwd): %v", err)
	}
	wantListed := cwd
	if resolved, err := filepath.EvalSymlinks(cwd); err == nil {
		wantListed = resolved
	}

	out.Reset()
	errBuf.Reset()
	if code := runTrust([]string{"list", "--json"}, &out, &errBuf, deps); code != exitSuccess {
		t.Fatalf("trust list --json returned %d, want %d; stderr=%q", code, exitSuccess, errBuf.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("trust list --json is not valid JSON %q: %v", out.String(), err)
	}
	rawRoots, ok := payload["roots"].([]any)
	if !ok || len(rawRoots) != 1 || rawRoots[0] != wantListed || len(payload) != 1 {
		t.Fatalf("trust list --json = %q, want {\"roots\": [%q]}", out.String(), wantListed)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"list", "extra"}, want: "argumento inesperado em trust list"},
		{args: []string{"list", "-x"}, want: "flag desconhecida em trust list"},
	} {
		out.Reset()
		errBuf.Reset()
		if code := runTrust(tc.args, &out, &errBuf, deps); code != exitUsage {
			t.Fatalf("trust %v returned %d, want %d", tc.args, code, exitUsage)
		}
		if out.Len() != 0 {
			t.Fatalf("trust %v must not print to stdout, got %q", tc.args, out.String())
		}
		if !strings.Contains(errBuf.String(), tc.want) {
			t.Fatalf("trust %v stderr = %q, want %q", tc.args, errBuf.String(), tc.want)
		}
	}
}

// TestTrustRemoveJSONFlagPosition proves `trust remove --json` accepts the flag
// both before and after the path, echoes the normalized path (the same entry
// `trust list` shows), and reports whether the path was actually listed
// (removed false for a never-trusted path, and "." resolves to the cwd).
func TestTrustRemoveJSONFlagPosition(t *testing.T) {
	setTrustConfigRoot(t)
	cwd := t.TempDir()
	deps := trustDeps(cwd)

	normalize := func(path string) string {
		abs, err := filepath.Abs(path)
		if err != nil {
			t.Fatalf("Abs(%q): %v", path, err)
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			return resolved
		}
		return abs
	}

	before := t.TempDir()
	after := t.TempDir()
	untrusted := t.TempDir()
	for _, dir := range []string{before, after, cwd} {
		if err := workspacetrust.Trust(dir); err != nil {
			t.Fatalf("Trust(%q): %v", dir, err)
		}
	}

	for _, tc := range []struct {
		name        string
		args        []string
		target      string
		wantRemoved bool
	}{
		{name: "flag before path", args: []string{"remove", "--json", before}, target: normalize(before), wantRemoved: true},
		{name: "flag after path", args: []string{"remove", after, "--json"}, target: normalize(after), wantRemoved: true},
		{name: "never trusted", args: []string{"remove", "--json", untrusted}, target: normalize(untrusted), wantRemoved: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errBuf bytes.Buffer
			if code := runTrust(tc.args, &out, &errBuf, deps); code != exitSuccess {
				t.Fatalf("trust %v returned %d, want %d; stderr=%q", tc.args, code, exitSuccess, errBuf.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
				t.Fatalf("trust %v is not valid JSON %q: %v", tc.args, out.String(), err)
			}
			if len(payload) != 2 || payload["path"] != tc.target || payload["removed"] != tc.wantRemoved {
				t.Fatalf("trust %v JSON = %q, want path %q removed %v", tc.args, out.String(), tc.target, tc.wantRemoved)
			}
			if trusted, err := workspacetrust.IsTrusted(tc.target); err != nil || trusted {
				t.Fatalf("target %q should be untrusted after trust %v (trusted=%v err=%v)", tc.target, tc.args, trusted, err)
			}
		})
	}

	// "." resolves against the process cwd like any other relative path; the
	// payload still echoes the normalized form.
	t.Run("dot echoes normalized", func(t *testing.T) {
		want := normalize(".")
		var out, errBuf bytes.Buffer
		if code := runTrust([]string{"remove", ".", "--json"}, &out, &errBuf, deps); code != exitSuccess {
			t.Fatalf("trust remove . --json returned %d, want %d; stderr=%q", code, exitSuccess, errBuf.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
			t.Fatalf("trust remove . --json is not valid JSON %q: %v", out.String(), err)
		}
		if len(payload) != 2 || payload["path"] != want || payload["removed"] != false {
			t.Fatalf("trust remove . --json = %q, want path %q removed false", out.String(), want)
		}
	})

	for _, tc := range []struct {
		args []string
		want string
	}{
		{args: []string{"remove", "-x"}, want: "flag desconhecida em trust remove"},
	} {
		var out, errBuf bytes.Buffer
		if code := runTrust(append(tc.args, "--json"), &out, &errBuf, deps); code != exitUsage {
			t.Fatalf("trust %v --json returned %d, want %d", tc.args, code, exitUsage)
		}
		if out.Len() != 0 {
			t.Fatalf("trust %v --json must not print to stdout, got %q", tc.args, out.String())
		}
		if !strings.Contains(errBuf.String(), tc.want) {
			t.Fatalf("trust %v --json stderr = %q, want %q", tc.args, errBuf.String(), tc.want)
		}
	}

	var out, errBuf bytes.Buffer
	if code := runTrust([]string{"remove", "--help", "--json"}, &out, &errBuf, deps); code != exitSuccess {
		t.Fatalf("trust remove --help returned %d, want %d; stderr=%q", code, exitSuccess, errBuf.String())
	}
	if !strings.Contains(out.String(), "Uso:") {
		t.Fatalf("trust remove --help must print usage, got %q", out.String())
	}
}

// TestRunTrustHelp proves the -h / --help / help subcommands print usage to stdout,
// nothing to stderr, and return success, matching the other CLI help entrypoints
// (unknown subcommands, by contrast, write to stderr with the usage exit code).
func TestRunTrustHelp(t *testing.T) {
	setTrustConfigRoot(t)
	deps := trustDeps(t.TempDir())

	for _, flag := range []string{"-h", "--help", "help"} {
		var out, errBuf bytes.Buffer
		if code := runTrust([]string{flag}, &out, &errBuf, deps); code != exitSuccess {
			t.Fatalf("trust %s returned %d, want %d", flag, code, exitSuccess)
		}
		if !strings.Contains(out.String(), "Uso:") {
			t.Fatalf("trust %s stdout = %q, want it to contain usage text", flag, out.String())
		}
		if errBuf.Len() != 0 {
			t.Fatalf("trust %s should not write to stderr, got %q", flag, errBuf.String())
		}
	}
}
