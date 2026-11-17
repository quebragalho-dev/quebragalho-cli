package localcontrol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeCommandRunner struct {
	path     string
	args     []string
	env      []string
	stdin    string
	hasStdin bool
}

func (runner *fakeCommandRunner) Run(_ context.Context, path string, args []string, env []string, _ time.Duration) (CommandResult, error) {
	runner.path = path
	runner.args = append([]string(nil), args...)
	runner.env = append([]string(nil), env...)
	runner.stdin = ""
	runner.hasStdin = false
	return CommandResult{
		Path:     path,
		Args:     append([]string(nil), args...),
		Stdout:   "ok\n",
		ExitCode: 0,
	}, nil
}

func (runner *fakeCommandRunner) RunWithStdin(_ context.Context, path string, args []string, env []string, stdin string, _ time.Duration) (CommandResult, error) {
	runner.path = path
	runner.args = append([]string(nil), args...)
	runner.env = append([]string(nil), env...)
	runner.stdin = stdin
	runner.hasStdin = true
	return CommandResult{
		Path:     path,
		Args:     append([]string(nil), args...),
		Stdout:   "ok\n",
		ExitCode: 0,
	}, nil
}

func TestBrowserRunUsesConfiguredHelperPath(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "agent-browser")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	runner := &fakeCommandRunner{}
	browser := NewBrowser(BrowserOptions{
		Enabled:    true,
		HelperPath: helper,
		Runner:     runner,
	})

	result, err := browser.Run(context.Background(), "open", "https://example.com")
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Output() != "ok" {
		t.Fatalf("Output = %q, want ok", result.Output())
	}
	if runner.path != helper {
		t.Fatalf("runner path = %q, want %q", runner.path, helper)
	}
	if want := []string{"open", "https://example.com"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("runner args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserRunDisabledFailsBeforeDiscovery(t *testing.T) {
	browser := NewBrowser(BrowserOptions{Enabled: false, HelperPath: "/does/not/exist"})
	_, err := browser.Run(context.Background(), "snapshot")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Run error = %v, want disabled", err)
	}
}

func TestBrowserRunMissingHelperPathIsActionable(t *testing.T) {
	browser := NewBrowser(BrowserOptions{Enabled: true, HelperPath: filepath.Join(t.TempDir(), "missing")})
	_, err := browser.Run(context.Background(), "snapshot")
	if err == nil || !strings.Contains(err.Error(), "helper not found") {
		t.Fatalf("Run error = %v, want missing helper", err)
	}
}

func TestBrowserRunUsesHelperManifest(t *testing.T) {
	dir := t.TempDir()
	binDir := filepath.Join(dir, "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	helper := filepath.Join(binDir, "agent-browser")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	t.Setenv(EnvHelperManifest, `{"version":1,"helpers":{"agent-browser":{"command":`+quoteJSON(helper)+`,"pathPrepend":[`+quoteJSON(binDir)+`],"env":{"QG_HELPER_TEST":"1"}}}}`)

	runner := &fakeCommandRunner{}
	browser := NewBrowser(BrowserOptions{
		Enabled: true,
		Runner:  runner,
	})

	if _, err := browser.Run(context.Background(), "snapshot"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.path != helper {
		t.Fatalf("runner path = %q, want manifest helper %q", runner.path, helper)
	}
	if !envContains(runner.env, "QG_HELPER_TEST=1") {
		t.Fatalf("env = %#v, want QG_HELPER_TEST", runner.env)
	}
	pathValue := envValue(runner.env, "PATH")
	if !strings.HasPrefix(pathValue, binDir+string(os.PathListSeparator)) && pathValue != binDir {
		t.Fatalf("PATH overlay = %q, want prefix %q", pathValue, binDir)
	}
}

func TestBrowserRunUsesManifestPrefixArgs(t *testing.T) {
	runner := &fakeCommandRunner{}
	t.Setenv(EnvHelperManifest, `{"version":1,"helpers":{"agent-browser":{"command":"cmd.exe","prefixArgs":["/d","/s","/c","C:\\qg\\agent-browser.cmd"]}}}`)
	browser := NewBrowser(BrowserOptions{
		Enabled: true,
		Runner:  runner,
	})

	if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.path != "cmd.exe" {
		t.Fatalf("runner path = %q, want cmd.exe", runner.path)
	}
	want := []string{"/d", "/s", "/c", `C:\qg\agent-browser.cmd`, "open", "https://example.com"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("runner args = %#v, want %#v", runner.args, want)
	}
}

func TestMergeEnvReplacesPathCaseInsensitivelyOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows env keys are case-insensitive")
	}
	env := mergeEnv([]string{`Path=C:\Windows`, "QG=1"}, []string{`PATH=C:\QG`})
	pathCount := 0
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok || !strings.EqualFold(key, "PATH") {
			continue
		}
		pathCount++
		if value != `C:\QG` {
			t.Fatalf("PATH value = %q, want C:\\QG", value)
		}
	}
	if pathCount != 1 {
		t.Fatalf("PATH entries = %d in %#v, want 1", pathCount, env)
	}
}

func TestBrowserRunUsesAdjacentPackagedHelper(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "qg")
	if err := os.WriteFile(native, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write native: %v", err)
	}
	helpersDir := filepath.Join(root, "helpers")
	if err := os.MkdirAll(helpersDir, 0o755); err != nil {
		t.Fatalf("mkdir helpers: %v", err)
	}
	helper := filepath.Join(helpersDir, "agent-browser")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return native, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	runner := &fakeCommandRunner{}
	browser := NewBrowser(BrowserOptions{
		Enabled: true,
		Runner:  runner,
	})
	if _, err := browser.Run(context.Background(), "snapshot"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.path != helper {
		t.Fatalf("runner path = %q, want adjacent helper %q", runner.path, helper)
	}
}

func TestBrowserRunUsesAdjacentPackagedNodeBinHelper(t *testing.T) {
	root := t.TempDir()
	native := filepath.Join(root, "qg")
	if err := os.WriteFile(native, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write native: %v", err)
	}
	binDir := filepath.Join(root, "helpers", "node_modules", ".bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir helper bin: %v", err)
	}
	helper := filepath.Join(binDir, "agent-browser")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return native, nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	runner := &fakeCommandRunner{}
	browser := NewBrowser(BrowserOptions{
		Enabled: true,
		Runner:  runner,
	})
	if _, err := browser.Run(context.Background(), "snapshot"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.path != helper {
		t.Fatalf("runner path = %q, want packaged node bin helper %q", runner.path, helper)
	}
}

func quoteJSON(value string) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func envContains(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

func envValue(env []string, key string) string {
	for _, item := range env {
		if got, value, ok := strings.Cut(item, "="); ok && got == key {
			return value
		}
	}
	return ""
}

func TestDesktopDefaultDriverAndMissingHint(t *testing.T) {
	if DefaultDesktopDriver != "agent-desktop" {
		t.Fatalf("DefaultDesktopDriver = %q, want agent-desktop", DefaultDesktopDriver)
	}
	t.Setenv("PATH", "")
	t.Setenv(EnvHelperManifest, "")
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "qg"), nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	desktop := NewDesktop(DesktopOptions{Enabled: true})
	_, err := desktop.Run(context.Background(), "status")
	if err == nil {
		t.Fatal("expected missing helper error")
	}
	if !strings.Contains(err.Error(), "agent-desktop") || !strings.Contains(err.Error(), "localControl.desktop.helperPath") {
		t.Fatalf("missing hint = %q, want agent-desktop and helperPath hint", err.Error())
	}
}

func TestHelperEnsureRunsBeforePATHWhenHelperPathUnset(t *testing.T) {
	t.Setenv(EnvHelperManifest, "")
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "qg"), nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	pathDir := t.TempDir()
	pathBin := filepath.Join(pathDir, "agent-desktop")
	if err := os.WriteFile(pathBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write path binary: %v", err)
	}
	t.Setenv("PATH", pathDir)

	ensuredBin := filepath.Join(t.TempDir(), "agent-desktop-ensured")
	if err := os.WriteFile(ensuredBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write ensured binary: %v", err)
	}

	called := false
	runner := &fakeCommandRunner{}
	desktop := NewDesktop(DesktopOptions{
		Enabled: true,
		Runner:  runner,
		Ensure: func(context.Context) (string, error) {
			called = true
			return ensuredBin, nil
		},
	})
	if _, err := desktop.Run(context.Background(), "status"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !called {
		t.Fatal("Ensure was not called")
	}
	if runner.path != ensuredBin {
		t.Fatalf("runner path = %q, want ensured %q before PATH %q", runner.path, ensuredBin, pathBin)
	}
}

func TestHelperEnsureSkippedWhenHelperPathConfigured(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "custom-agent-desktop")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	called := false
	runner := &fakeCommandRunner{}
	desktop := NewDesktop(DesktopOptions{
		Enabled:    true,
		HelperPath: helper,
		Runner:     runner,
		Ensure: func(context.Context) (string, error) {
			called = true
			return "", errors.New("must not be called")
		},
	})
	if _, err := desktop.Run(context.Background(), "status"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if called {
		t.Fatal("Ensure should not run when HelperPath is configured")
	}
	if runner.path != helper {
		t.Fatalf("runner path = %q, want %q", runner.path, helper)
	}
}

func TestHelperEnsureFallsBackToPATHWhenEmptyAndPropagatesError(t *testing.T) {
	t.Setenv(EnvHelperManifest, "")
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "qg"), nil }
	t.Cleanup(func() { executablePath = oldExecutablePath })

	pathDir := t.TempDir()
	pathBin := filepath.Join(pathDir, "agent-desktop")
	if err := os.WriteFile(pathBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write path binary: %v", err)
	}
	t.Setenv("PATH", pathDir)

	runner := &fakeCommandRunner{}
	desktop := NewDesktop(DesktopOptions{
		Enabled: true,
		Runner:  runner,
		Ensure:  func(context.Context) (string, error) { return "", nil },
	})
	if _, err := desktop.Run(context.Background(), "status"); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.path != pathBin {
		t.Fatalf("runner path = %q, want PATH fallback %q", runner.path, pathBin)
	}

	failing := NewDesktop(DesktopOptions{
		Enabled: true,
		Runner:  runner,
		Ensure:  func(context.Context) (string, error) { return "", errors.New("ensure boom") },
	})
	_, err := failing.Run(context.Background(), "status")
	if err == nil || !strings.Contains(err.Error(), "ensure boom") {
		t.Fatalf("Run error = %v, want ensure boom", err)
	}
}

func TestDesktopRunWithStdinPassesInput(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "agent-desktop")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	runner := &fakeCommandRunner{}
	desktop := NewDesktop(DesktopOptions{
		Enabled:    true,
		HelperPath: helper,
		Runner:     runner,
	})
	if _, err := desktop.RunWithStdin(context.Background(), "secret text", "type", "--stdin", "@s1:e1"); err != nil {
		t.Fatalf("RunWithStdin returned error: %v", err)
	}
	if !runner.hasStdin || runner.stdin != "secret text" {
		t.Fatalf("stdin = (%q, %v), want (%q, true)", runner.stdin, runner.hasStdin, "secret text")
	}
	if want := []string{"type", "--stdin", "@s1:e1"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestExecRunnerRunWithStdinPassesInputToChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture uses /bin/sh")
	}
	script := filepath.Join(t.TempDir(), "echo-stdin.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncat\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	res, err := (ExecRunner{}).RunWithStdin(context.Background(), script, nil, nil, "hello\nstdin", 5*time.Second)
	if err != nil {
		t.Fatalf("RunWithStdin returned error: %v", err)
	}
	if res.Stdout != "hello\nstdin" {
		t.Fatalf("Stdout = %q, want %q", res.Stdout, "hello\nstdin")
	}
}
