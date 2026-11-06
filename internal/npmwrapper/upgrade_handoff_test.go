package npmwrapper

import (
	"encoding/json"
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

// TestWrapperUpgradeJSONFlowEmitsSingleJSON pins the --json handoff contract
// for the whole flow: the first process prints nothing to stdout (pending to
// stderr), npm output goes to stderr too, and the reopened process prints the
// single final JSON to stdout.
func TestWrapperUpgradeJSONFlowEmitsSingleJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("handoff fixtures are POSIX shell scripts; Windows is covered by E2E VMs")
	}
	prefix := t.TempDir()
	packageRoot := filepath.Join(prefix, "lib", "node_modules", "@quebragalho", "cli")
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
	binary := filepath.Join(packageRoot, "qg")
	// First run (old binary, no JUST_INSTALLED): pending handoff, nothing to
	// stdout, pending line to stderr. Second run (reopened, JUST_INSTALLED):
	// the single final JSON to stdout.
	script := "#!/bin/sh\n" +
		"echo \"$@\" >> " + shellQuote(binLog) + "\n" +
		"if [ -z \"$QG_UPDATE_JUST_INSTALLED\" ]; then\n" +
		"  printf '{\"version\":\"9.9.99\"}' > \"$QG_NPM_UPDATE_HANDOFF\"\n" +
		"  echo '[qg] a atualização para 9.9.99 termina quando o qg fechar' >&2\n" +
		"else\n" +
		"  printf '{\"applied\":true,\"installMethod\":\"npm\",\"binaryPath\":\"/tmp/qg\",\"message\":\"updated via npm to 9.9.99\",\"currentVersion\":\"9.9.99\",\"latestVersion\":\"9.9.99\",\"updateAvailable\":false}'\n" +
		"fi\n" +
		"exit 0\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	npmShim := "#!/bin/sh\n" +
		"echo 'npm noise that must not pollute stdout'\n" +
		"exit 0\n"
	shimDir := filepath.Join(prefix, "shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shimDir, "npm"), []byte(npmShim), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	stdout, stderr, exitCode := runWrapperFixtureWithEnv(t, wrapperPath, nil, "upgrade", "--json")
	if exitCode != wrapperExitSuccess {
		t.Fatalf("exit = %d, want 0; stdout=%q stderr=%q", exitCode, stdout, stderr)
	}
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		t.Fatalf("stdout must hold the single final JSON, got empty; stderr=%q", stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		t.Fatalf("stdout must be exactly one valid JSON, got %q: %v; stderr=%q", stdout, err, stderr)
	}
	if payload["applied"] != true {
		t.Fatalf("final JSON must be applied:true, got %v (stdout=%q)", payload, stdout)
	}
	if strings.Contains(trimmed, "\n") {
		// More than one JSON document or extra lines would leave newlines
		// beyond the single object; the wrapper must not emit two.
		t.Fatalf("stdout must hold a single JSON document, got %q", stdout)
	}
	if !strings.Contains(stderr, "npm noise that must not pollute stdout") {
		t.Fatalf("stderr = %q, want npm output redirected to stderr", stderr)
	}
	if strings.Contains(stdout, "npm noise") {
		t.Fatalf("stdout must not contain npm output: %q", stdout)
	}
	binRuns := readLines(t, binLog)
	if len(binRuns) != 2 {
		t.Fatalf("native binary runs = %d, want 2 (pending, then reopened JSON)", len(binRuns))
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
	packageRoot := filepath.Join(prefix, "lib", "node_modules", "@quebragalho", "cli")
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
	if !strings.Contains(stderr, "Não foi possível instalar a versão 9.9.99 via npm") {
		t.Fatalf("stderr = %q, want the npm-failure lead", stderr)
	}
	// The manual command must be copy/pasteable exactly: no trailing
	// punctuation, or the copied install 404s.
	const marker = "Para atualizar à mão, rode: "
	index := strings.Index(stderr, marker)
	if index == -1 {
		t.Fatalf("stderr = %q, want it to name the manual command", stderr)
	}
	got := strings.TrimSpace(stderr[index+len(marker):])
	want := "npm install -g --prefix \"" + canonical + "\" @quebragalho/cli@9.9.99"
	if got != want {
		t.Fatalf("manual command = %q, want exactly %q", got, want)
	}
}
