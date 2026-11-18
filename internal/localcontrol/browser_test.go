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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
	t.Setenv("HOME", t.TempDir())
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
	pathBinName := "agent-desktop"
	if runtime.GOOS == "windows" {
		pathBinName += ".exe"
	}
	pathBin := filepath.Join(pathDir, pathBinName)
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
	pathBinName := "agent-desktop"
	if runtime.GOOS == "windows" {
		pathBinName += ".exe"
	}
	pathBin := filepath.Join(pathDir, pathBinName)
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

func stubSnapChromiumEnv(t *testing.T, goos string, snapExists bool, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv(EnvHelperManifest, "")
	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "qg"), nil }
	oldGOOS := browserGOOS
	browserGOOS = goos
	oldSnap := snapChromiumExists
	snapChromiumExists = func() bool { return snapExists }
	t.Cleanup(func() {
		executablePath = oldExecutablePath
		browserGOOS = oldGOOS
		snapChromiumExists = oldSnap
	})
}

func TestBrowserRunSetsSnapChromiumTmpDirOnLinux(t *testing.T) {
	home := canonicalPath(t, t.TempDir())
	stubSnapChromiumEnv(t, "linux", true, home)
	t.Setenv("TMPDIR", "")

	ensuredBin := filepath.Join(t.TempDir(), "agent-browser")
	if err := os.WriteFile(ensuredBin, []byte("#!/bin/sh\nprintf '%s' \"$TMPDIR\"\n"), 0o755); err != nil {
		t.Fatalf("write ensured binary: %v", err)
	}

	runner := &fakeCommandRunner{}
	browser := NewBrowser(BrowserOptions{
		Enabled: true,
		Runner:  runner,
		Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
	})

	wantTmp := filepath.Join(home, "snap", "chromium", "common", "qg-agent-browser-tmp")
	for _, cmdArgs := range [][]string{{"open", "https://example.com"}, {"snapshot", "-i"}} {
		if _, err := browser.Run(context.Background(), cmdArgs...); err != nil {
			t.Fatalf("Run(%v) returned error: %v", cmdArgs, err)
		}
		if got := envValue(runner.env, "TMPDIR"); got != wantTmp {
			t.Fatalf("Run(%v) TMPDIR = %q, want %q (env=%#v)", cmdArgs, got, wantTmp, runner.env)
		}
	}

	info, err := os.Stat(wantTmp)
	if err != nil || !info.IsDir() {
		t.Fatalf("Stat(%q): info=%v err=%v", wantTmp, info, err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %o, want 0700", info.Mode().Perm())
	}

	if runtime.GOOS != "windows" {
		t.Setenv("TMPDIR", "/tmp/outside-snap")
		execBrowser := NewBrowser(BrowserOptions{
			Enabled: true,
			Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
		})
		res, err := execBrowser.Run(context.Background(), "open")
		if err != nil {
			t.Fatalf("ExecRunner Run returned error: %v", err)
		}
		if res.Stdout != wantTmp {
			t.Fatalf("child TMPDIR = %q, want %q", res.Stdout, wantTmp)
		}
	}
}

func TestBrowserRunSnapChromiumTmpDirRespectsSnapTmpAndOverridesOutsideTmp(t *testing.T) {
	ensuredBin := filepath.Join(t.TempDir(), "agent-browser")
	if err := os.WriteFile(ensuredBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write ensured binary: %v", err)
	}

	t.Run("overrides-outside-and-traversal-tmpdir", func(t *testing.T) {
		home := canonicalPath(t, t.TempDir())
		stubSnapChromiumEnv(t, "linux", true, home)
		wantTmp := filepath.Join(home, "snap", "chromium", "common", "qg-agent-browser-tmp")

		for _, badTmp := range []string{
			"/tmp/user-1000",
			filepath.Join(home, "snap", "chromium-beta", "common"),
			filepath.Join(home, "snap", "chromium") + "/../../tmp",
		} {
			t.Setenv("TMPDIR", badTmp)
			runner := &fakeCommandRunner{}
			browser := NewBrowser(BrowserOptions{
				Enabled: true,
				Runner:  runner,
				Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
			})
			if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
				t.Fatalf("TMPDIR=%q: Run error = %v", badTmp, err)
			}
			if got := envValue(runner.env, "TMPDIR"); got != wantTmp {
				t.Fatalf("TMPDIR=%q: got overlay TMPDIR=%q, want %q", badTmp, got, wantTmp)
			}
		}
	})

	t.Run("preserves-tmpdir-already-under-snap-chromium", func(t *testing.T) {
		home := canonicalPath(t, t.TempDir())
		stubSnapChromiumEnv(t, "linux", true, home)
		customSnapTmp := filepath.Join(home, "snap", "chromium", "common", "custom-tmp")
		t.Setenv("TMPDIR", customSnapTmp)

		runner := &fakeCommandRunner{}
		browser := NewBrowser(BrowserOptions{
			Enabled: true,
			Runner:  runner,
			Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
		})
		if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
			t.Fatalf("Run error = %v", err)
		}
		if got := envValue(runner.env, "TMPDIR"); got != "" {
			t.Fatalf("overlay TMPDIR = %q, want empty so user TMPDIR %q is preserved", got, customSnapTmp)
		}
		defaultTmp := filepath.Join(home, "snap", "chromium", "common", "qg-agent-browser-tmp")
		if _, err := os.Stat(defaultTmp); !os.IsNotExist(err) {
			t.Fatalf("default snap tmp %q should not be created when user TMPDIR is already under ~/snap/chromium", defaultTmp)
		}
	})
}

func TestBrowserRunSkipsSnapChromiumTmpDirWhenNotApplicable(t *testing.T) {
	helperBin := filepath.Join(t.TempDir(), "helper-bin")
	if err := os.WriteFile(helperBin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}

	for _, tc := range []struct {
		name       string
		goos       string
		snapExists bool
		driver     string
		helperPath string
	}{
		{name: "darwin-even-if-snap-exists", goos: "darwin", snapExists: true},
		{name: "windows-even-if-snap-exists", goos: "windows", snapExists: true},
		{name: "linux-without-snap-chromium", goos: "linux", snapExists: false},
		{name: "linux-custom-driver", goos: "linux", snapExists: true, driver: "custom-browser"},
		{name: "linux-custom-helper-path", goos: "linux", snapExists: true, helperPath: helperBin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := canonicalPath(t, t.TempDir())
			stubSnapChromiumEnv(t, tc.goos, tc.snapExists, home)
			t.Setenv("TMPDIR", "")

			pathDir := t.TempDir()
			driverName := tc.driver
			if driverName == "" {
				driverName = DefaultBrowserDriver
			}
			for _, ext := range []string{"", ".exe"} {
				if err := os.WriteFile(filepath.Join(pathDir, driverName+ext), []byte("#!/bin/sh\n"), 0o755); err != nil {
					t.Fatalf("write path helper: %v", err)
				}
			}
			t.Setenv("PATH", pathDir)

			runner := &fakeCommandRunner{}
			browser := NewBrowser(BrowserOptions{
				Enabled:    true,
				Driver:     tc.driver,
				HelperPath: tc.helperPath,
				Runner:     runner,
			})
			if _, err := browser.Run(context.Background(), "snapshot"); err != nil {
				t.Fatalf("Run error = %v", err)
			}
			if got := envValue(runner.env, "TMPDIR"); got != "" {
				t.Fatalf("TMPDIR = %q, want empty", got)
			}
			if _, err := os.Stat(filepath.Join(home, "snap")); !os.IsNotExist(err) {
				t.Fatalf("snap dir should not be created for %s: %v", tc.name, err)
			}
		})
	}

	t.Run("desktop-and-terminal-unaffected", func(t *testing.T) {
		home := canonicalPath(t, t.TempDir())
		stubSnapChromiumEnv(t, "linux", true, home)
		t.Setenv("TMPDIR", "")

		runner := &fakeCommandRunner{}
		desktop := NewDesktop(DesktopOptions{
			Enabled: true,
			Runner:  runner,
			Ensure:  func(context.Context) (string, error) { return helperBin, nil },
		})
		if _, err := desktop.Run(context.Background(), "status"); err != nil {
			t.Fatalf("desktop.Run error = %v", err)
		}
		if got := envValue(runner.env, "TMPDIR"); got != "" {
			t.Fatalf("desktop TMPDIR = %q, want empty", got)
		}

		pathDir := t.TempDir()
		for _, ext := range []string{"", ".exe"} {
			if err := os.WriteFile(filepath.Join(pathDir, DefaultTerminalDriver+ext), []byte("#!/bin/sh\n"), 0o755); err != nil {
				t.Fatalf("write terminal helper: %v", err)
			}
		}
		t.Setenv("PATH", pathDir)
		terminal := NewTerminal(TerminalOptions{Enabled: true, Runner: runner})
		if _, err := terminal.Run(context.Background(), "snapshot"); err != nil {
			t.Fatalf("terminal.Run error = %v", err)
		}
		if got := envValue(runner.env, "TMPDIR"); got != "" {
			t.Fatalf("terminal TMPDIR = %q, want empty", got)
		}
		if _, err := os.Stat(filepath.Join(home, "snap")); !os.IsNotExist(err) {
			t.Fatalf("desktop/terminal must not create snap dir: %v", err)
		}
	})
}

func stubWindowsBrowserFS(t *testing.T, goos string, existingFiles []string, dirEntries map[string][]string) {
	t.Helper()
	const (
		fakeUserProfile  = `C:\Users\TestUser`
		fakeLocalAppData = `C:\Users\TestUser\AppData\Local`
	)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", fakeUserProfile)
	t.Setenv("LOCALAPPDATA", fakeLocalAppData)
	t.Setenv("ProgramFiles", "")
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("ProgramW6432", "")
	t.Setenv("AGENT_BROWSER_EXECUTABLE_PATH", "")
	t.Setenv(EnvHelperManifest, "")

	fileSet := make(map[string]bool, len(existingFiles))
	for _, f := range existingFiles {
		fileSet[f] = true
	}

	oldExecutablePath := executablePath
	executablePath = func() (string, error) { return filepath.Join(t.TempDir(), "qg"), nil }
	oldGOOS := browserGOOS
	browserGOOS = goos
	oldFileExists := browserFileExists
	browserFileExists = func(path string) bool { return fileSet[path] }
	oldReadDir := browserReadDirNames
	browserReadDirNames = func(dir string) ([]string, error) {
		if entries, ok := dirEntries[dir]; ok {
			return entries, nil
		}
		return nil, os.ErrNotExist
	}
	oldSnap := snapChromiumExists
	snapChromiumExists = func() bool { return false }
	t.Cleanup(func() {
		executablePath = oldExecutablePath
		browserGOOS = oldGOOS
		browserFileExists = oldFileExists
		browserReadDirNames = oldReadDir
		snapChromiumExists = oldSnap
	})
}

func TestBrowserRunWindowsEdgeFallback(t *testing.T) {
	const (
		edgeX86         = `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`
		edgeX64         = `C:\Program Files\Microsoft\Edge\Application\msedge.exe`
		chromeProgFiles = `C:\Program Files\Google\Chrome\Application\chrome.exe`
		chromeProgX86   = `C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`
		chromeLocal     = `C:\Users\TestUser\AppData\Local\Google\Chrome\Application\chrome.exe`
		braveLocal      = `C:\Users\TestUser\AppData\Local\BraveSoftware\Brave-Browser\Application\brave.exe`
		cftBrowsersDir  = `C:\Users\TestUser\.agent-browser\browsers`
		cftChromeExe    = `C:\Users\TestUser\.agent-browser\browsers\chromium-1492\chrome-win64\chrome.exe`
	)

	ensuredBin := filepath.Join(t.TempDir(), "agent-browser.exe")
	if err := os.WriteFile(ensuredBin, []byte("MZ"), 0o755); err != nil {
		t.Fatalf("write ensured binary: %v", err)
	}

	t.Run("no-chrome-with-edge-x86-sets-executable-path", func(t *testing.T) {
		stubWindowsBrowserFS(t, "windows", []string{edgeX86, edgeX64}, map[string][]string{
			cftBrowsersDir: {"incomplete-dir"},
		})
		runner := &fakeCommandRunner{}
		browser := NewBrowser(BrowserOptions{
			Enabled: true,
			Runner:  runner,
			Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
		})
		if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
			t.Fatalf("Run error = %v", err)
		}
		if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != edgeX86 {
			t.Fatalf("AGENT_BROWSER_EXECUTABLE_PATH = %q, want %q (env=%#v)", got, edgeX86, runner.env)
		}
	})

	t.Run("no-chrome-with-edge-programfiles-sets-executable-path", func(t *testing.T) {
		stubWindowsBrowserFS(t, "windows", []string{edgeX64}, nil)
		runner := &fakeCommandRunner{}
		browser := NewBrowser(BrowserOptions{
			Enabled: true,
			Runner:  runner,
			Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
		})
		if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
			t.Fatalf("Run error = %v", err)
		}
		if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != edgeX64 {
			t.Fatalf("AGENT_BROWSER_EXECUTABLE_PATH = %q, want %q", got, edgeX64)
		}
	})

	t.Run("installed-chrome-or-brave-skips-edge", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			path string
		}{
			{"program-files-chrome", chromeProgFiles},
			{"program-files-x86-chrome", chromeProgX86},
			{"localappdata-chrome", chromeLocal},
			{"localappdata-brave", braveLocal},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stubWindowsBrowserFS(t, "windows", []string{edgeX86, tc.path}, nil)
				runner := &fakeCommandRunner{}
				browser := NewBrowser(BrowserOptions{
					Enabled: true,
					Runner:  runner,
					Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
				})
				if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
					t.Fatalf("Run error = %v", err)
				}
				if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != "" {
					t.Fatalf("%s: AGENT_BROWSER_EXECUTABLE_PATH = %q, want empty", tc.name, got)
				}
			})
		}
	})

	t.Run("chrome-for-testing-skips-edge", func(t *testing.T) {
		stubWindowsBrowserFS(t, "windows", []string{edgeX86, cftChromeExe}, map[string][]string{
			cftBrowsersDir: {"chromium-1492"},
		})
		runner := &fakeCommandRunner{}
		browser := NewBrowser(BrowserOptions{
			Enabled: true,
			Runner:  runner,
			Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
		})
		if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
			t.Fatalf("Run error = %v", err)
		}
		if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != "" {
			t.Fatalf("AGENT_BROWSER_EXECUTABLE_PATH = %q, want empty when Chrome for Testing exists", got)
		}
	})

	t.Run("without-edge-does-not-set", func(t *testing.T) {
		stubWindowsBrowserFS(t, "windows", nil, nil)
		runner := &fakeCommandRunner{}
		browser := NewBrowser(BrowserOptions{
			Enabled: true,
			Runner:  runner,
			Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
		})
		if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
			t.Fatalf("Run error = %v", err)
		}
		if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != "" {
			t.Fatalf("AGENT_BROWSER_EXECUTABLE_PATH = %q, want empty", got)
		}
	})

	t.Run("prefers-programfiles-env-vars-for-edge-over-fixed-paths", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			env  map[string]string
			want string
		}{
			{
				name: "ProgramFiles(x86)",
				env: map[string]string{
					"ProgramFiles":      `D:\Program Files`,
					"ProgramFiles(x86)": `D:\Program Files (x86)`,
					"ProgramW6432":      `D:\Program Files`,
				},
				want: `D:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			},
			{
				name: "ProgramFiles",
				env: map[string]string{
					"ProgramFiles":      `D:\Arquivos de Programas`,
					"ProgramFiles(x86)": `D:\Arquivos de Programas (x86)`,
				},
				want: `D:\Arquivos de Programas\Microsoft\Edge\Application\msedge.exe`,
			},
			{
				name: "ProgramW6432-under-wow64",
				env: map[string]string{
					"ProgramFiles":      `D:\Program Files (x86)`,
					"ProgramFiles(x86)": `D:\Program Files (x86)`,
					"ProgramW6432":      `D:\Program Files`,
				},
				want: `D:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				// Include edgeX86 on C:\ too: env-derived roots must win, keeping
				// hardcoded C:\Program Files* strictly as last resort.
				stubWindowsBrowserFS(t, "windows", []string{edgeX86, edgeX64, tc.want}, nil)
				for k, v := range tc.env {
					t.Setenv(k, v)
				}
				runner := &fakeCommandRunner{}
				browser := NewBrowser(BrowserOptions{
					Enabled: true,
					Runner:  runner,
					Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
				})
				if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
					t.Fatalf("Run error = %v", err)
				}
				if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != tc.want {
					t.Fatalf("AGENT_BROWSER_EXECUTABLE_PATH = %q, want %q", got, tc.want)
				}
			})
		}
	})

	t.Run("detects-chrome-under-programfiles-env-vars-and-skips-edge", func(t *testing.T) {
		customEdge := `D:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`
		for _, tc := range []struct {
			name       string
			envKey     string
			envDir     string
			chromePath string
		}{
			{
				name:       "ProgramFiles",
				envKey:     "ProgramFiles",
				envDir:     `D:\Program Files`,
				chromePath: `D:\Program Files\Google\Chrome\Application\chrome.exe`,
			},
			{
				name:       "ProgramFiles(x86)",
				envKey:     "ProgramFiles(x86)",
				envDir:     `E:\Program Files (x86)`,
				chromePath: `E:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			},
			{
				name:       "ProgramW6432",
				envKey:     "ProgramW6432",
				envDir:     `F:\ProgramW6432`,
				chromePath: `F:\ProgramW6432\Google\Chrome\Application\chrome.exe`,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stubWindowsBrowserFS(t, "windows", []string{customEdge, edgeX86, tc.chromePath}, nil)
				t.Setenv("ProgramFiles(x86)", `D:\Program Files (x86)`)
				t.Setenv(tc.envKey, tc.envDir)
				runner := &fakeCommandRunner{}
				browser := NewBrowser(BrowserOptions{
					Enabled: true,
					Runner:  runner,
					Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
				})
				if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
					t.Fatalf("Run error = %v", err)
				}
				if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != "" {
					t.Fatalf("%s: AGENT_BROWSER_EXECUTABLE_PATH = %q, want empty when Chrome exists at %s", tc.name, got, tc.chromePath)
				}
			})
		}
	})

	t.Run("uses-fixed-c-program-files-only-as-last-resort", func(t *testing.T) {
		stubWindowsBrowserFS(t, "windows", []string{edgeX86}, nil)
		t.Setenv("ProgramFiles", `D:\Program Files`)
		t.Setenv("ProgramFiles(x86)", `D:\Program Files (x86)`)
		t.Setenv("ProgramW6432", `D:\Program Files`)

		runner := &fakeCommandRunner{}
		browser := NewBrowser(BrowserOptions{
			Enabled: true,
			Runner:  runner,
			Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
		})
		if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
			t.Fatalf("Run error = %v", err)
		}
		if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != edgeX86 {
			t.Fatalf("AGENT_BROWSER_EXECUTABLE_PATH = %q, want last-resort %q", got, edgeX86)
		}
	})

	t.Run("respects-user-defined-executable-path", func(t *testing.T) {
		stubWindowsBrowserFS(t, "windows", []string{edgeX86}, nil)
		t.Setenv("AGENT_BROWSER_EXECUTABLE_PATH", `C:\Custom\browser.exe`)

		runner := &fakeCommandRunner{}
		browser := NewBrowser(BrowserOptions{
			Enabled: true,
			Runner:  runner,
			Ensure:  func(context.Context) (string, error) { return ensuredBin, nil },
		})
		if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
			t.Fatalf("Run error = %v", err)
		}
		if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != "" {
			t.Fatalf("overlay AGENT_BROWSER_EXECUTABLE_PATH = %q, want empty so user env is preserved", got)
		}
	})

	t.Run("outside-windows-or-custom-driver-or-helper-path-skips-edge", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			goos       string
			driver     string
			helperPath string
		}{
			{name: "linux", goos: "linux"},
			{name: "darwin", goos: "darwin"},
			{name: "windows-custom-driver", goos: "windows", driver: "custom-driver"},
			{name: "windows-custom-helper-path", goos: "windows", helperPath: ensuredBin},
		} {
			t.Run(tc.name, func(t *testing.T) {
				stubWindowsBrowserFS(t, tc.goos, []string{edgeX86}, nil)
				pathDir := t.TempDir()
				driverName := tc.driver
				if driverName == "" {
					driverName = DefaultBrowserDriver
				}
				for _, ext := range []string{"", ".exe"} {
					if err := os.WriteFile(filepath.Join(pathDir, driverName+ext), []byte("#!/bin/sh\n"), 0o755); err != nil {
						t.Fatalf("write path helper: %v", err)
					}
				}
				t.Setenv("PATH", pathDir)

				runner := &fakeCommandRunner{}
				browser := NewBrowser(BrowserOptions{
					Enabled:    true,
					Driver:     tc.driver,
					HelperPath: tc.helperPath,
					Runner:     runner,
				})
				if _, err := browser.Run(context.Background(), "open", "https://example.com"); err != nil {
					t.Fatalf("Run error = %v", err)
				}
				if got := envValue(runner.env, "AGENT_BROWSER_EXECUTABLE_PATH"); got != "" {
					t.Fatalf("%s: AGENT_BROWSER_EXECUTABLE_PATH = %q, want empty", tc.name, got)
				}
			})
		}
	})
}
