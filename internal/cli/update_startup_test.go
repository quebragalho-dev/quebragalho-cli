package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/tui"
	"github.com/quebragalho-dev/quebragalho-cli/internal/update"
	"github.com/quebragalho-dev/quebragalho-cli/internal/updatenotice"
)

// newSleepingUpdateServer blocks every request until release is closed, so the
// never-blocks test can prove the startup path returns while a check is still
// in flight.
func newSleepingUpdateServer(t *testing.T, release <-chan struct{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-release
		fmt.Fprint(writer, `{"version":"0.0.99"}`)
	}))
}

var errUpdateStartupTest = errors.New("browser unavailable in test")

type updatePromptCall struct {
	current      string
	latest       string
	changelogURL string
}

func writeUpdateState(t *testing.T, state updatenotice.State) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qg", "update.json")
	if err := updatenotice.Save(path, state); err != nil {
		t.Fatal(err)
	}
	return path
}

func updateStartupTestDeps(stateFile string, env map[string]string, prompt func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error)) (updateStartupDeps, *bytes.Buffer) {
	var stderr bytes.Buffer
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	deps := updateStartupDeps{
		currentVersion: "0.0.10",
		installMethod:  update.InstallMethodStandalone,
		stdinIsTTY:     true,
		env: func(key string) string {
			return env[key]
		},
		now:       func() time.Time { return now },
		stateFile: stateFile,
		args:      []string{"qg"},
		runPrompt: prompt,
		openURL:   func(string) error { return errUpdateStartupTest },
		applyUpdate: func(context.Context) (string, error) {
			return "", errors.New("applyUpdate must be stubbed per test")
		},
		relaunch: func(string, []string, []string) (int, error) {
			return 0, errors.New("relaunch must be stubbed per test")
		},
	}
	return deps, &stderr
}

func runUpdateStartupWithState(t *testing.T, state updatenotice.State, env map[string]string, prompt func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error)) (int, bool, string) {
	t.Helper()
	deps, stderr := updateStartupTestDeps(writeUpdateState(t, state), env, prompt)
	code, proceed := runUpdateStartup(context.Background(), stderr, deps)
	return code, proceed, stderr.String()
}

func TestUpdateStartupSkipsWhenNothingNew(t *testing.T) {
	called := false
	code, proceed, _ := runUpdateStartupWithState(t,
		updatenotice.State{LastCheck: time.Date(2026, 11, 2, 11, 59, 0, 0, time.UTC), LatestVersion: "0.0.10"},
		nil,
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			called = true
			return tui.UpdateSkip, nil
		})
	if code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if called {
		t.Fatal("no prompt may show when nothing is new")
	}
}

func TestUpdateStartupSkipsWhenDisabled(t *testing.T) {
	called := false
	code, proceed, _ := runUpdateStartupWithState(t,
		updatenotice.State{},
		map[string]string{"QG_NO_UPDATE_CHECK": "1"},
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			called = true
			return tui.UpdateSkip, nil
		})
	if code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if called {
		t.Fatal("no prompt may show when checks are disabled")
	}
}

func TestUpdateStartupSkipsWhenStdinIsNotTTY(t *testing.T) {
	// Armed state (a newer version is known) but no terminal: the prompt
	// must never show, silently proceeding to the current version.
	deps, stderr := updateStartupTestDeps(
		writeUpdateState(t, updatenotice.State{LastCheck: time.Now(), LatestVersion: "0.0.11"}),
		nil,
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			t.Error("no prompt may show without a terminal")
			return tui.UpdateSkip, nil
		})
	deps.stdinIsTTY = false
	if code, proceed := runUpdateStartup(context.Background(), stderr, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if got := strings.TrimSpace(stderr.String()); got != "" {
		t.Fatalf("stderr = %q, want silence", got)
	}
}

func TestUpdateStartupSkipsForDevVersion(t *testing.T) {
	deps, stderr := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{}), nil,
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			t.Error("no prompt may show for a dev build")
			return tui.UpdateSkip, nil
		})
	deps.currentVersion = "dev"
	if code, proceed := runUpdateStartup(context.Background(), stderr, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
}

func TestUpdateStartupSkipRecordsVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qg", "update.json")
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	// Fresh check: the prompt decision must not depend on a refresh, and no
	// background check may leak into this test's cleanup.
	if err := updatenotice.Save(path, updatenotice.State{LastCheck: now, LatestVersion: "0.0.11"}); err != nil {
		t.Fatal(err)
	}
	deps, stderr := updateStartupTestDeps(path, nil,
		func(_ context.Context, current, latest, changelogURL string, _ func(string) error, _ func(context.Context) error) (tui.UpdateChoice, error) {
			if current != "0.0.10" || latest != "0.0.11" {
				t.Fatalf("prompt got (%q, %q), want (0.0.10, 0.0.11)", current, latest)
			}
			if changelogURL != "https://github.com/quebragalho-dev/quebragalho-cli/blob/v0.0.11/CHANGELOG.md" {
				t.Fatalf("changelog link = %q", changelogURL)
			}
			return tui.UpdateSkip, nil
		})
	deps.now = func() time.Time { return now }
	if code, proceed := runUpdateStartup(context.Background(), stderr, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if got := updatenotice.Load(path); got.SkippedVersion != "0.0.11" {
		t.Fatalf("SkippedVersion = %q, want 0.0.11", got.SkippedVersion)
	}
}

func TestUpdateStartupQuitExitsZero(t *testing.T) {
	code, proceed, _ := runUpdateStartupWithState(t,
		updatenotice.State{LastCheck: time.Now(), LatestVersion: "0.0.11"},
		nil,
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			return tui.UpdateQuit, nil
		})
	if code != 0 || proceed {
		t.Fatalf("got (%d, %v), want (0, false)", code, proceed)
	}
}

func TestUpdateStartupInstallFailureProceeds(t *testing.T) {
	ranInstall := false
	deps, stderr := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{LastCheck: time.Now(), LatestVersion: "0.0.11"}), nil,
		func(_ context.Context, _, _, _ string, _ func(string) error, install func(context.Context) error) (tui.UpdateChoice, error) {
			ranInstall = true
			if err := install(context.Background()); err == nil {
				t.Error("expected the install to fail")
			}
			return tui.UpdateInstallFailed, nil
		})
	deps.applyUpdate = func(context.Context) (string, error) {
		return "", errors.New("network down")
	}
	calledRelaunch := false
	deps.relaunch = func(string, []string, []string) (int, error) {
		calledRelaunch = true
		return 0, nil
	}
	path := deps.stateFile
	code, proceed := runUpdateStartup(context.Background(), stderr, deps)
	if code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if !ranInstall {
		t.Fatal("the prompt must run the install")
	}
	if calledRelaunch {
		t.Fatal("a failed install must not relaunch")
	}
	if got := strings.TrimSpace(stderr.String()); got != "" {
		t.Fatalf("stderr = %q, want nothing (the prompt card already showed the failure)", got)
	}
	if got := updatenotice.Load(path); got.SkippedVersion != "" {
		t.Fatalf("a failed install must not record a skip, got %#v", got)
	}
}

func TestUpdateStartupInstallStandaloneRelaunches(t *testing.T) {
	var gotBinary string
	var gotArgs []string
	var gotEnv []string
	deps, _ := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{LastCheck: time.Now(), LatestVersion: "0.0.11"}), nil,
		func(ctx context.Context, _, _, _ string, _ func(string) error, install func(context.Context) error) (tui.UpdateChoice, error) {
			if err := install(ctx); err != nil {
				t.Fatalf("install failed: %v", err)
			}
			return tui.UpdateInstall, nil
		})
	deps.args = []string{"qg", "--flag"}
	deps.applyUpdate = func(context.Context) (string, error) { return "/tmp/qg-new", nil }
	deps.relaunch = func(newBinary string, args []string, env []string) (int, error) {
		gotBinary, gotArgs, gotEnv = newBinary, args, env
		return 0, nil
	}
	code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps)
	if code != 0 || proceed {
		t.Fatalf("got (%d, %v), want (0, false)", code, proceed)
	}
	if gotBinary != "/tmp/qg-new" {
		t.Fatalf("relaunch binary = %q, want the installed one", gotBinary)
	}
	if len(gotArgs) != 2 || gotArgs[0] != "/tmp/qg-new" || gotArgs[1] != "--flag" {
		t.Fatalf("relaunch args = %#v, want the new binary plus the original args", gotArgs)
	}
	found := false
	for _, item := range gotEnv {
		if item == "QG_UPDATE_JUST_INSTALLED=0.0.11" {
			found = true
		}
	}
	if !found {
		t.Fatal("relaunch env must carry QG_UPDATE_JUST_INSTALLED=0.0.11")
	}
}

func TestUpdateStartupNpmHandoffWritesFile(t *testing.T) {
	dir := t.TempDir()
	handoff := filepath.Join(dir, "handoff.json")
	deps, _ := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{LastCheck: time.Now(), LatestVersion: "0.0.11"}),
		map[string]string{"QG_NPM_UPDATE_HANDOFF": handoff},
		func(ctx context.Context, _, _, _ string, _ func(string) error, install func(context.Context) error) (tui.UpdateChoice, error) {
			if err := install(ctx); err != nil {
				t.Fatalf("handoff install failed: %v", err)
			}
			return tui.UpdateInstall, nil
		})
	deps.installMethod = update.InstallMethodNpm
	deps.applyUpdate = func(context.Context) (string, error) {
		t.Error("npm installs go through the wrapper handoff, never Apply")
		return "", nil
	}
	code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps)
	if code != 0 || proceed {
		t.Fatalf("got (%d, %v), want (0, false)", code, proceed)
	}
	raw, err := os.ReadFile(handoff)
	if err != nil {
		t.Fatalf("handoff file missing: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(raw, &payload); err != nil || payload["version"] != "0.0.11" {
		t.Fatalf("handoff payload = %q, want {\"version\":\"0.0.11\"}", strings.TrimSpace(string(raw)))
	}
}

func TestUpdateStartupJustInstalledMismatchDoesNotPrompt(t *testing.T) {
	called := false
	code, proceed, stderr := runUpdateStartupWithState(t,
		updatenotice.State{LatestVersion: "0.0.11"},
		map[string]string{"QG_UPDATE_JUST_INSTALLED": "0.0.9"},
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			called = true
			return tui.UpdateSkip, nil
		})
	if code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if called {
		t.Fatal("a mismatched just-installed install must not prompt")
	}
	if got := strings.TrimSpace(stderr); !strings.Contains(got, "a atualização para 0.0.9 não ficou ativa") {
		t.Fatalf("stderr = %q, want the inactive-update line", got)
	}
}

func TestUpdateStartupNeverBlocksOnNetwork(t *testing.T) {
	release := make(chan struct{})
	server := newSleepingUpdateServer(t, release)
	defer server.Close()
	statePath := writeUpdateState(t, updatenotice.State{})
	deps, _ := updateStartupTestDeps(statePath,
		map[string]string{"QG_UPDATE_NPM_ENDPOINT": server.URL},
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			t.Error("no update is known, so no prompt may show")
			return tui.UpdateSkip, nil
		})
	deps.installMethod = update.InstallMethodNpm
	before := time.Now()
	code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps)
	if elapsed := time.Since(before); elapsed >= 200*time.Millisecond {
		t.Fatalf("startup blocked %v on the background check, want < 200ms", elapsed)
	}
	if code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	// Drain the background refresh before cleanup: it is still blocked on the
	// sleeping server, and TempDir removal races its atomic save otherwise.
	// Releasing the server lets the check finish; the saved LastCheck proves
	// the write landed.
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := updatenotice.Load(statePath); !got.LastCheck.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background refresh never recorded its check")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecNeverPrompts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	state := updatenotice.State{LastCheck: time.Now(), LatestVersion: "0.0.11"}
	stateFile := writeUpdateState(t, state)

	exitCode, _, stderr := runExecWithEcho(t, []string{"exec", "-p", "say hi"})
	if exitCode != 0 {
		t.Fatalf("exec exit = %d, want 0; stderr = %q", exitCode, stderr)
	}
	for _, marker := range []string{"Nova versão", "Atualização", "Instalando a versão", "Changelog", "reinicie para instalar"} {
		if strings.Contains(stderr, marker) {
			t.Fatalf("qg exec must never show update UI, stderr contains %q: %q", marker, stderr)
		}
	}

	// Prove the staged state really would prompt an interactive run: the same
	// state file through runUpdateStartup must reach the prompt.
	called := false
	deps, _ := updateStartupTestDeps(stateFile, nil,
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			called = true
			return tui.UpdateSkip, nil
		})
	deps.stdinIsTTY = true
	if code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps); code != 0 || !proceed {
		t.Fatalf("interactive startup with the staged state returned (%d, %v), want (0, true)", code, proceed)
	}
	if !called {
		t.Fatal("the staged newer version must trigger the interactive prompt")
	}
}

func TestUpdateStartupSkipSurvivesLateRefresh(t *testing.T) {
	// A background refresh that finishes after the user pressed "skip" must
	// merge into the stored state, not blank the recorded skip with the
	// pre-check copy it started from.
	release := make(chan struct{})
	server := newSleepingUpdateServer(t, release)
	defer server.Close()
	stateFile := writeUpdateState(t, updatenotice.State{LastCheck: time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC), LatestVersion: "0.0.11"})
	deps, _ := updateStartupTestDeps(stateFile, map[string]string{"QG_UPDATE_NPM_ENDPOINT": server.URL},
		func(_ context.Context, _, _, _ string, _ func(string) error, _ func(context.Context) error) (tui.UpdateChoice, error) {
			return tui.UpdateSkip, nil
		})
	deps.installMethod = update.InstallMethodNpm
	code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps)
	if code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	// The prompt answered before the release: now let the refresh land and
	// prove the skip survived it.
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := updatenotice.Load(stateFile); !got.LastCheck.IsZero() && got.LastCheck.After(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)) {
			if got.SkippedVersion != "0.0.11" {
				t.Fatalf("late refresh clobbered the skip: %#v", got)
			}
			// The sleeping fake answers 0.0.99 once released: the point is the
			// refresh landed at all without touching the recorded skip.
			if got.LatestVersion != "0.0.99" {
				t.Fatalf("late refresh did not record its version: %#v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background refresh never recorded its check")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUpdateStartupJustInstalledDoesNotLeak(t *testing.T) {
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "0.0.9")
	deps, _ := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{LastCheck: time.Now()}), nil,
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			return tui.UpdateSkip, nil
		})
	// Real process env (not the test map) so the test proves the unset hits
	// the same variable the production lookup reads.
	deps.env = nil
	deps.currentVersion = "0.0.9"
	deps.stdinIsTTY = true
	if code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if got := os.Getenv("QG_UPDATE_JUST_INSTALLED"); got != "" {
		t.Fatalf("QG_UPDATE_JUST_INSTALLED leaked into the environment: %q", got)
	}
}
