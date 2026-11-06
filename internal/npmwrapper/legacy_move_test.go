package npmwrapper

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	legacyPackageName  = "@quebragalho-dev/cli"
	currentPackageName = "@quebragalho/cli"
	bridgeVersion      = "0.0.16"
)

// moveFixture is a global npm prefix holding qg under the legacy name, plus
// templates of both installs and a fake npm that installs and removes them,
// so the wrapper's move from the legacy name runs end to end.
type moveFixture struct {
	prefix string
	// canonicalPrefix is the prefix as the wrapper sees it (node resolves the
	// script path; /var is /private/var on macOS).
	canonicalPrefix string
	legacyRoot      string
	currentRoot     string
	npmLog          string
	runLog          string
}

// fakeBinary logs each run (which install it came from, the reopen markers it
// received, its arguments) and, run as `upgrade` outside a reopen, hands an
// update to the wrapper the way the Go side does.
func fakeBinary(label string, runLog string) string {
	return "#!/bin/sh\n" +
		"echo \"" + label + " just=$QG_UPDATE_JUST_INSTALLED tried=$QG_NPM_MOVE_TRIED $*\" >> " + shellQuote(runLog) + "\n" +
		"if [ -z \"$QG_UPDATE_JUST_INSTALLED\" ] && [ \"$1\" = upgrade ]; then\n" +
		"  printf '{\"version\":\"" + bridgeVersion + "\"}' > \"$QG_NPM_UPDATE_HANDOFF\"\n" +
		"fi\n" +
		"exit 0\n"
}

func writeInstallTemplate(t *testing.T, dir string, name string, binary string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	wrapper, err := os.ReadFile(filepath.Join(repoRoot(t), "bin", "qg.js"))
	if err != nil {
		t.Fatalf("ReadFile wrapper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "qg.js"), wrapper, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"` + name + `","version":"` + bridgeVersion + `","bin":{"qg":"bin/qg.js"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// The downloaded-binary location: the fixture has no platform payload,
	// so the wrapper falls back to the binary beside it.
	if err := os.WriteFile(filepath.Join(dir, "qg"), []byte(binary), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newMoveFixture(t *testing.T) moveFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("move fixtures are POSIX shell scripts; Windows is covered by E2E VMs")
	}
	prefix := t.TempDir()
	canonical, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		t.Fatal(err)
	}
	f := moveFixture{
		prefix:          prefix,
		canonicalPrefix: canonical,
		legacyRoot:      filepath.Join(prefix, "lib", "node_modules", "@quebragalho-dev", "cli"),
		currentRoot:     filepath.Join(prefix, "lib", "node_modules", "@quebragalho", "cli"),
		npmLog:          filepath.Join(prefix, "npm-calls.log"),
		runLog:          filepath.Join(prefix, "qg-runs.log"),
	}
	legacyTemplate := filepath.Join(prefix, "templates", "legacy")
	currentTemplate := filepath.Join(prefix, "templates", "current")
	writeInstallTemplate(t, legacyTemplate, legacyPackageName, fakeBinary("legacy", f.runLog))
	writeInstallTemplate(t, currentTemplate, currentPackageName, fakeBinary("current", f.runLog))
	if err := os.MkdirAll(filepath.Dir(f.legacyRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(legacyTemplate, f.legacyRoot); err != nil {
		t.Fatal(err)
	}
	writeInstallTemplate(t, legacyTemplate, legacyPackageName, fakeBinary("legacy", f.runLog))

	// npm <verb> -g --prefix <prefix> <spec>: $1 is the verb, $5 the spec.
	// QG_FAKE_NPM_FAIL_CURRENT makes the install of the current name fail.
	npm := "#!/bin/sh\n" +
		"echo \"$*\" >> " + shellQuote(f.npmLog) + "\n" +
		"case \"$1:$5\" in\n" +
		"  uninstall:" + legacyPackageName + ")\n" +
		"    rm -rf " + shellQuote(f.legacyRoot) + "; exit 0 ;;\n" +
		"  install:" + currentPackageName + "@*)\n" +
		"    [ -n \"$QG_FAKE_NPM_FAIL_CURRENT\" ] && exit 1\n" +
		"    mkdir -p " + shellQuote(filepath.Dir(f.currentRoot)) + " && cp -R " + shellQuote(currentTemplate) + " " + shellQuote(f.currentRoot) + "; exit 0 ;;\n" +
		"  install:" + legacyPackageName + "@*)\n" +
		"    cp -R " + shellQuote(legacyTemplate) + " " + shellQuote(f.legacyRoot) + "; exit 0 ;;\n" +
		"esac\n" +
		"exit 1\n"
	shimDir := filepath.Join(prefix, "shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shimDir, "npm"), []byte(npm), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "")
	t.Setenv("QG_NPM_MOVE_TRIED", "")
	t.Setenv("QG_FAKE_NPM_FAIL_CURRENT", "")
	return f
}

func (f moveFixture) legacyWrapper() string {
	return filepath.Join(f.legacyRoot, "bin", "qg.js")
}

func (f moveFixture) npmCalls(t *testing.T) []string {
	t.Helper()
	if _, err := os.Stat(f.npmLog); os.IsNotExist(err) {
		return nil
	}
	return readLines(t, f.npmLog)
}

func (f moveFixture) uninstallLegacy() string {
	return "uninstall -g --prefix " + f.canonicalPrefix + " " + legacyPackageName
}

func (f moveFixture) install(spec string) string {
	return "install -g --prefix " + f.canonicalPrefix + " " + spec
}

func assertLines(t *testing.T, what string, got []string, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s =\n  %s\nwant\n  %s", what, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// TestWrapperLegacyUpgradeMovesToTheCurrentName is `qg upgrade` on an install
// under the legacy name: the old package goes first (both own the qg bin),
// the current one comes in at the handed-off version, and the command line
// reopens from the new install.
func TestWrapperLegacyUpgradeMovesToTheCurrentName(t *testing.T) {
	f := newMoveFixture(t)
	_, stderr, exitCode := runWrapperFixtureWithEnv(t, f.legacyWrapper(), nil, "upgrade")
	if exitCode != wrapperExitSuccess {
		t.Fatalf("exit = %d, want 0; stderr=%q", exitCode, stderr)
	}
	assertLines(t, "npm calls", f.npmCalls(t), []string{
		f.uninstallLegacy(),
		f.install(currentPackageName + "@" + bridgeVersion),
	})
	assertLines(t, "qg runs", readLines(t, f.runLog), []string{
		"legacy just= tried= upgrade",
		"current just=" + bridgeVersion + " tried= upgrade",
	})
	if _, err := os.Stat(f.legacyRoot); !os.IsNotExist(err) {
		t.Fatalf("the legacy install must be gone, stat err = %v", err)
	}
	if !strings.Contains(stderr, "trocando "+legacyPackageName+" por "+currentPackageName) {
		t.Fatalf("stderr = %q, want the move announced", stderr)
	}
}

// TestWrapperLegacyMovesRightAfterAnUpdate is the path most installs take:
// a release from before the rename updates itself to the bridge under the
// old name and reopens it, and the bridge moves to the current name before
// its own binary ever runs.
func TestWrapperLegacyMovesRightAfterAnUpdate(t *testing.T) {
	f := newMoveFixture(t)
	_, stderr, exitCode := runWrapperFixtureWithEnv(t, f.legacyWrapper(), []string{"QG_UPDATE_JUST_INSTALLED=" + bridgeVersion}, "--version")
	if exitCode != wrapperExitSuccess {
		t.Fatalf("exit = %d, want 0; stderr=%q", exitCode, stderr)
	}
	assertLines(t, "npm calls", f.npmCalls(t), []string{
		f.uninstallLegacy(),
		f.install(currentPackageName + "@" + bridgeVersion),
	})
	assertLines(t, "qg runs", readLines(t, f.runLog), []string{
		"current just=" + bridgeVersion + " tried= --version",
	})
}

// TestWrapperLegacyMoveFailurePutsTheOldNameBack: when the current name does
// not install, the old one is reinstalled at its version so qg keeps working,
// the two manual commands are printed one per line, and the reopened wrapper
// does not try the move again (no loop between wrapper and binary).
func TestWrapperLegacyMoveFailurePutsTheOldNameBack(t *testing.T) {
	f := newMoveFixture(t)
	_, stderr, exitCode := runWrapperFixtureWithEnv(t, f.legacyWrapper(), []string{"QG_FAKE_NPM_FAIL_CURRENT=1"}, "upgrade")
	if exitCode != wrapperExitSuccess {
		t.Fatalf("exit = %d, want 0 (the old install opens again); stderr=%q", exitCode, stderr)
	}
	assertLines(t, "npm calls", f.npmCalls(t), []string{
		f.uninstallLegacy(),
		f.install(currentPackageName + "@" + bridgeVersion),
		f.install(legacyPackageName + "@" + bridgeVersion),
	})
	// The reopen carries the version (so the Go side reports the move that
	// did not happen) but QG_NPM_MOVE_TRIED stays inside the wrapper.
	assertLines(t, "qg runs", readLines(t, f.runLog), []string{
		"legacy just= tried= upgrade",
		"legacy just=" + bridgeVersion + " tried= upgrade",
	})
	if _, err := os.Stat(f.legacyRoot); err != nil {
		t.Fatalf("the legacy install must be back: %v", err)
	}
	for _, want := range []string{
		"o qg continua em " + legacyPackageName,
		"\n  npm uninstall -g --prefix \"" + f.canonicalPrefix + "\" " + legacyPackageName + "\n",
		"\n  npm install -g --prefix \"" + f.canonicalPrefix + "\" " + currentPackageName + "\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want %q", stderr, want)
		}
	}
}

// TestWrapperCurrentNameNeverMoves: an install under the current name treats
// the reopen marker as a plain reopen and calls no npm at all.
func TestWrapperCurrentNameNeverMoves(t *testing.T) {
	f := newMoveFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.currentRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	writeInstallTemplate(t, f.currentRoot, currentPackageName, fakeBinary("current", f.runLog))
	_, stderr, exitCode := runWrapperFixtureWithEnv(t, filepath.Join(f.currentRoot, "bin", "qg.js"), []string{"QG_UPDATE_JUST_INSTALLED=" + bridgeVersion}, "--version")
	if exitCode != wrapperExitSuccess {
		t.Fatalf("exit = %d, want 0; stderr=%q", exitCode, stderr)
	}
	if calls := f.npmCalls(t); len(calls) != 0 {
		t.Fatalf("npm calls = %q, want none", calls)
	}
	assertLines(t, "qg runs", readLines(t, f.runLog), []string{
		"current just=" + bridgeVersion + " tried= --version",
	})
}
