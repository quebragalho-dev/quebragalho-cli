package npmwrapper

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// realWrapperManifest loads the published wrapper manifest from the update
// package testdata, so the upgrade-handoff fixtures exercise the real shape.
func realWrapperManifest(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "update", "testdata", "wrapper-package.json"))
	if err != nil {
		t.Fatalf("ReadFile real wrapper manifest: %v", err)
	}
	return raw
}

// TestWrapperRunsNpmOnUpgradeHandoff pins the Windows leftover fix for the
// `qg upgrade` path: when the native binary records an upgrade handoff and
// exits, the wrapper installs that exact version into the owning prefix and
// reopens, instead of leaving a .cli-XXXX staging directory behind.
func TestWrapperRunsNpmOnUpgradeHandoff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("handoff fixtures are POSIX shell scripts; Windows is covered by E2E VMs")
	}
	prefix := t.TempDir()
	canonical, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Join(prefix, "lib", "node_modules", "@quebragalho-dev", "cli")
	binDir := filepath.Join(packageRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(repoRoot(t), "bin", "qg.js"))
	if err != nil {
		t.Fatalf("ReadFile wrapper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "package.json"), realWrapperManifest(t), 0o644); err != nil {
		t.Fatal(err)
	}
	wrapperPath := filepath.Join(binDir, "qg.js")
	if err := os.WriteFile(wrapperPath, bytes, 0o755); err != nil {
		t.Fatal(err)
	}
	binLog := filepath.Join(prefix, "qg-runs.log")
	npmLog := filepath.Join(prefix, "npm-calls.log")
	binary := filepath.Join(packageRoot, "qg")
	// Simulates `qg upgrade` under the wrapper: the old binary records the
	// target version and exits 0 with the pending line, doing no npm work
	// itself so the open binary is never replaced in place.
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + shellQuote(binLog) + "\n" +
		"if [ -z \"$QG_UPDATE_JUST_INSTALLED\" ]; then\n" +
		"  printf '{\"version\":\"9.9.99\"}' > \"$QG_NPM_UPDATE_HANDOFF\"\n" +
		"  echo '[qg] a atualização para 9.9.99 termina quando o qg fechar'\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	npmShim := "#!/bin/sh\n" +
		"echo \"$@\" >> " + shellQuote(npmLog) + "\n" +
		"exit 0\n"
	shimDir := filepath.Join(prefix, "shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shimDir, "npm"), []byte(npmShim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, exitCode := runWrapperFixtureWithEnv(t, wrapperPath, nil, "upgrade")
	if exitCode != wrapperExitSuccess {
		t.Fatalf("exit = %d, want 0; stdout=%q stderr=%q", exitCode, stdout, stderr)
	}
	if !strings.Contains(stdout, "termina quando o qg fechar") {
		t.Fatalf("stdout = %q, want the pt-BR pending line from the old binary", stdout)
	}
	npmCalls := readLines(t, npmLog)
	want := "install -g --prefix " + canonical + " @quebragalho-dev/cli@9.9.99"
	if len(npmCalls) != 1 || npmCalls[0] != want {
		t.Fatalf("npm calls = %#v, want %q", npmCalls, want)
	}
	binRuns := readLines(t, binLog)
	if len(binRuns) != 2 {
		t.Fatalf("native binary runs = %d, want 2 (upgrade handoff, then reopened)", len(binRuns))
	}
	if !strings.Contains(binRuns[0], "upgrade") || !strings.Contains(binRuns[1], "upgrade") {
		t.Fatalf("binary runs = %#v, want both to carry the upgrade command", binRuns)
	}
}

// TestWrapperUpgradeFailureNamesManualCommand pins the npm-failure guidance
// for the upgrade handoff: the wrapper reopens the current version and
// prints the copy/paste reinstall with the owning --prefix.
func TestWrapperUpgradeFailureNamesManualCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("handoff fixtures are POSIX shell scripts; Windows is covered by E2E VMs")
	}
	prefix := t.TempDir()
	canonical, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Join(prefix, "lib", "node_modules", "@quebragalho-dev", "cli")
	binDir := filepath.Join(packageRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(repoRoot(t), "bin", "qg.js"))
	if err != nil {
		t.Fatalf("ReadFile wrapper: %v", err)
	}
	if err := os.WriteFile(filepath.Join(packageRoot, "package.json"), realWrapperManifest(t), 0o644); err != nil {
		t.Fatal(err)
	}
	wrapperPath := filepath.Join(binDir, "qg.js")
	if err := os.WriteFile(wrapperPath, bytes, 0o755); err != nil {
		t.Fatal(err)
	}
	npmLog := filepath.Join(prefix, "npm-calls.log")
	binary := filepath.Join(packageRoot, "qg")
	script := "#!/bin/sh\n" +
		"if [ -z \"$QG_UPDATE_JUST_INSTALLED\" ]; then\n" +
		"  printf '{\"version\":\"9.9.99\"}' > \"$QG_NPM_UPDATE_HANDOFF\"\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	npmShim := "#!/bin/sh\n" +
		"echo \"$@\" >> " + shellQuote(npmLog) + "\n" +
		"exit 1\n"
	shimDir := filepath.Join(prefix, "shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shimDir, "npm"), []byte(npmShim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, stderr, exitCode := runWrapperFixtureWithEnv(t, wrapperPath, nil, "upgrade")
	if exitCode != wrapperExitSuccess {
		t.Fatalf("exit = %d, want 0 (reopen the current version); stderr=%q", exitCode, stderr)
	}
	for _, want := range []string{
		"Não foi possível instalar a versão 9.9.99 via npm",
		"Para atualizar à mão, rode: npm install -g --prefix \"" + canonical + "\" @quebragalho-dev/cli@9.9.99",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
		}
	}
}
