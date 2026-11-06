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
	"runtime"
	"strings"
	"sync/atomic"
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

// updateStartupTestNow is the fake clock every startup test shares: states
// stamped with it are fresh (no network), states older than CheckInterval
// are due.
var updateStartupTestNow = time.Date(2026, time.November, 2, 12, 0, 0, 0, time.UTC)

func updateStartupTestDeps(stateFile string, env map[string]string, prompt func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error)) (updateStartupDeps, *bytes.Buffer) {
	var stderr bytes.Buffer
	now := updateStartupTestNow
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
		writeUpdateState(t, updatenotice.State{LastCheck: updateStartupTestNow, LatestVersion: "0.0.11"}),
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
		updatenotice.State{LastCheck: updateStartupTestNow, LatestVersion: "0.0.11"},
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
	deps, stderr := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{LastCheck: updateStartupTestNow, LatestVersion: "0.0.11"}), nil,
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
	deps, _ := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{LastCheck: updateStartupTestNow, LatestVersion: "0.0.11"}), nil,
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
	deps, _ := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{LastCheck: updateStartupTestNow, LatestVersion: "0.0.11"}),
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

func TestUpdateStartupSlowNetworkReturnsWithinBudget(t *testing.T) {
	// A hung release endpoint must delay opening by at most the startup
	// budget: the prompt is skipped and nothing is recorded, so the TUI
	// still refreshes in the background right after opening.
	release := make(chan struct{})
	server := newSleepingUpdateServer(t, release)
	defer server.Close()
	statePath := writeUpdateState(t, updatenotice.State{})
	deps, _ := updateStartupTestDeps(statePath,
		map[string]string{"QG_UPDATE_NPM_ENDPOINT": server.URL},
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			t.Error("a timed-out check knows no version, so no prompt may show")
			return tui.UpdateSkip, nil
		})
	deps.installMethod = update.InstallMethodNpm
	before := time.Now()
	code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps)
	elapsed := time.Since(before)
	close(release)
	if elapsed < time.Second || elapsed >= 2500*time.Millisecond {
		t.Fatalf("startup waited %v on a hung check, want about the 1.5s budget (under 2.5s)", elapsed)
	}
	if code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if got := updatenotice.Load(statePath); got != (updatenotice.State{}) {
		t.Fatalf("a failed check must record nothing, got %#v", got)
	}
}

func TestExecNeverPrompts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	state := updatenotice.State{LastCheck: updateStartupTestNow, LatestVersion: "0.0.11"}
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
	// The startup screen records the refusal without touching the network
	// (the stored check is fresh); the TUI footer refresh lands after (same
	// merge the running TUI uses) and must keep the recorded skip instead of
	// blanking it.
	release := make(chan struct{})
	server := newSleepingUpdateServer(t, release)
	defer server.Close()
	freshCheck := updateStartupTestNow.Add(-time.Minute)
	stateFile := writeUpdateState(t, updatenotice.State{LastCheck: freshCheck, LatestVersion: "0.0.11"})
	deps, _ := updateStartupTestDeps(stateFile, map[string]string{"QG_UPDATE_NPM_ENDPOINT": server.URL},
		func(_ context.Context, _, _, _ string, _ func(string) error, _ func(context.Context) error) (tui.UpdateChoice, error) {
			return tui.UpdateSkip, nil
		})
	deps.installMethod = update.InstallMethodNpm
	if code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if got := updatenotice.Load(stateFile); got.SkippedVersion != "0.0.11" {
		t.Fatalf("refusal not recorded: %#v", got)
	}
	// The startup prompt used the fresh stored check without touching the
	// network: now let the TUI-side check land through the same merge path
	// refreshUpdateNotice uses.
	close(release)
	refreshed := updatenotice.Refresh(context.Background(), updatenotice.Config{
		CurrentVersion: "0.0.10",
		InstallMethod:  update.InstallMethodNpm,
		Env:            func(string) string { return "" },
		NpmEndpoint:    server.URL,
	}, updatenotice.State{})
	if err := updatenotice.RecordCheck(stateFile, refreshed); err != nil {
		t.Fatal(err)
	}
	got := updatenotice.Load(stateFile)
	if got.SkippedVersion != "0.0.11" {
		t.Fatalf("late refresh clobbered the skip: %#v", got)
	}
	// The sleeping fake answers 0.0.99 once released: the point is the
	// refresh landed at all without touching the recorded skip.
	if got.LatestVersion != "0.0.99" {
		t.Fatalf("late refresh did not record its version: %#v", got)
	}
	if !got.LastCheck.After(freshCheck) {
		t.Fatalf("late refresh did not record its check: %#v", got)
	}
}

func TestUpdateStartupJustInstalledDoesNotLeak(t *testing.T) {
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "0.0.9")
	deps, _ := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{LastCheck: updateStartupTestNow}), nil,
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

// npmUpdateServer answers the npm .../latest document with version, counting
// requests when hits is non-nil.
func npmUpdateServer(t *testing.T, version string, hits *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"version":%q}`, version)
	}))
}

// githubUpdateServer answers the GitHub latest-release API with version and
// the release assets for the test platform, which standalone checks verify.
func githubUpdateServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	platform := map[string]string{"linux": "linux", "darwin": "macos", "windows": "windows"}[runtime.GOOS]
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	if platform == "" || arch == "" {
		t.Skipf("no release archive for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	extension := "tar.gz"
	if platform == "windows" {
		extension = "zip"
	}
	name := fmt.Sprintf("qg-v%s-%s-%s.%s", version, platform, arch, extension)
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"tag_name":"v%s","assets":[{"name":%q},{"name":%q}]}`,
			version, name, name+".sha256")
	}))
}

func TestUpdateStartupStaleCheckPromptsOnFirstOpening(t *testing.T) {
	// No stored check yet and the registry answers fast: the screen shows in
	// this opening with the fresh version, and the check is recorded so the
	// footer does not check again right after opening.
	var hits atomic.Int64
	server := npmUpdateServer(t, "0.0.99", &hits)
	defer server.Close()
	statePath := writeUpdateState(t, updatenotice.State{})
	var gotLatest string
	deps, _ := updateStartupTestDeps(statePath,
		map[string]string{"QG_UPDATE_NPM_ENDPOINT": server.URL},
		func(_ context.Context, current, latest, changelogURL string, _ func(string) error, _ func(context.Context) error) (tui.UpdateChoice, error) {
			gotLatest = latest
			if current != "0.0.10" {
				t.Errorf("prompt current = %q, want 0.0.10", current)
			}
			if changelogURL != "https://github.com/quebragalho-dev/quebragalho-cli/blob/v0.0.99/CHANGELOG.md" {
				t.Errorf("changelog link = %q", changelogURL)
			}
			return tui.UpdateSkip, nil
		})
	deps.installMethod = update.InstallMethodNpm
	if code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("network checks = %d, want exactly 1", got)
	}
	if gotLatest != "0.0.99" {
		t.Fatalf("prompt latest = %q, want 0.0.99 from this opening's check", gotLatest)
	}
	stored := updatenotice.Load(statePath)
	if !stored.LastCheck.Equal(updateStartupTestNow) {
		t.Fatalf("LastCheck = %v, want the check time", stored.LastCheck)
	}
	if stored.LatestVersion != "0.0.99" {
		t.Fatalf("LatestVersion = %q, want 0.0.99", stored.LatestVersion)
	}
	if stored.SkippedVersion != "0.0.99" {
		t.Fatalf("SkippedVersion = %q, want the answered 0.0.99", stored.SkippedVersion)
	}
	dueConfig := updatenotice.Config{Now: func() time.Time { return updateStartupTestNow }}
	if updatenotice.Due(dueConfig, stored) {
		t.Fatal("the recorded check must not be due right after opening")
	}
}

func TestUpdateStartupGitHubCheckPromptsOnFirstOpening(t *testing.T) {
	// Standalone installs read GitHub releases: a stale check refreshes from
	// QG_UPDATE_RELEASE_URL and prompts in this opening.
	server := githubUpdateServer(t, "0.0.99")
	defer server.Close()
	statePath := writeUpdateState(t, updatenotice.State{})
	var gotLatest string
	deps, _ := updateStartupTestDeps(statePath,
		map[string]string{"QG_UPDATE_RELEASE_URL": server.URL},
		func(_ context.Context, _, latest, _ string, _ func(string) error, _ func(context.Context) error) (tui.UpdateChoice, error) {
			gotLatest = latest
			return tui.UpdateSkip, nil
		})
	if code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if gotLatest != "0.0.99" {
		t.Fatalf("prompt latest = %q, want 0.0.99 from GitHub", gotLatest)
	}
	if got := updatenotice.Load(statePath); got.LatestVersion != "0.0.99" || !got.LastCheck.Equal(updateStartupTestNow) {
		t.Fatalf("successful GitHub check not recorded: %#v", got)
	}
}

func TestUpdateStartupFailedCheckRecordsNothing(t *testing.T) {
	// Port 1 refuses connections: the check fails fast, opening carries on
	// with the stored result, and nothing is recorded.
	statePath := writeUpdateState(t, updatenotice.State{})
	deps, _ := updateStartupTestDeps(statePath,
		map[string]string{"QG_UPDATE_NPM_ENDPOINT": "http://127.0.0.1:1/"},
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			t.Error("a failed check knows no version, so no prompt may show")
			return tui.UpdateSkip, nil
		})
	deps.installMethod = update.InstallMethodNpm
	if code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if got := updatenotice.Load(statePath); got != (updatenotice.State{}) {
		t.Fatalf("a failed check must record nothing, got %#v", got)
	}
}

func TestUpdateStartupFreshCheckMakesNoRequest(t *testing.T) {
	var hits atomic.Int64
	server := npmUpdateServer(t, "0.0.99", &hits)
	defer server.Close()
	statePath := writeUpdateState(t, updatenotice.State{LastCheck: updateStartupTestNow})
	deps, _ := updateStartupTestDeps(statePath,
		map[string]string{"QG_UPDATE_NPM_ENDPOINT": server.URL},
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			t.Error("a fresh stored check must not prompt")
			return tui.UpdateSkip, nil
		})
	deps.installMethod = update.InstallMethodNpm
	if code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("a fresh check made %d requests, want none", got)
	}
}

func TestUpdateStartupGatedPathsMakeNoRequest(t *testing.T) {
	for _, test := range []struct {
		name string
		tty  bool
		env  map[string]string
	}{
		{name: "kill switch", tty: true, env: map[string]string{"QG_NO_UPDATE_CHECK": "1"}},
		{name: "no tty", tty: false, env: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var hits atomic.Int64
			server := npmUpdateServer(t, "0.0.99", &hits)
			defer server.Close()
			env := map[string]string{"QG_UPDATE_NPM_ENDPOINT": server.URL}
			for key, value := range test.env {
				env[key] = value
			}
			deps, _ := updateStartupTestDeps(writeUpdateState(t, updatenotice.State{}), env,
				func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
					t.Error("a gated check must never prompt")
					return tui.UpdateSkip, nil
				})
			deps.stdinIsTTY = test.tty
			deps.installMethod = update.InstallMethodNpm
			if code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps); code != 0 || !proceed {
				t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
			}
			if got := hits.Load(); got != 0 {
				t.Fatalf("a gated check made %d requests, want none", got)
			}
		})
	}
}

func TestUpdateStartupSkippedVersionStaysSilentWithNetwork(t *testing.T) {
	// The registry answers with the declined version: the check succeeds and
	// is recorded, but no screen shows.
	var hits atomic.Int64
	server := npmUpdateServer(t, "0.0.99", &hits)
	defer server.Close()
	oldCheck := updateStartupTestNow.Add(-time.Hour)
	statePath := writeUpdateState(t, updatenotice.State{LastCheck: oldCheck, LatestVersion: "0.0.99", SkippedVersion: "0.0.99"})
	deps, _ := updateStartupTestDeps(statePath,
		map[string]string{"QG_UPDATE_NPM_ENDPOINT": server.URL},
		func(context.Context, string, string, string, func(string) error, func(context.Context) error) (tui.UpdateChoice, error) {
			t.Error("a skipped version must never prompt, even with a fresh network answer")
			return tui.UpdateSkip, nil
		})
	deps.installMethod = update.InstallMethodNpm
	if code, proceed := runUpdateStartup(context.Background(), &bytes.Buffer{}, deps); code != 0 || !proceed {
		t.Fatalf("got (%d, %v), want (0, true)", code, proceed)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("network checks = %d, want exactly 1", got)
	}
	stored := updatenotice.Load(statePath)
	if stored.SkippedVersion != "0.0.99" {
		t.Fatalf("SkippedVersion = %q, want to keep 0.0.99", stored.SkippedVersion)
	}
	if !stored.LastCheck.Equal(updateStartupTestNow) {
		t.Fatalf("LastCheck = %v, want the successful check recorded", stored.LastCheck)
	}
}
