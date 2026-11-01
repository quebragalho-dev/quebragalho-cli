package updatenotice

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/update"
)

func testEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func TestLoadCorruptStateIsEmpty(t *testing.T) {
	dir := t.TempDir()
	missing := Load(filepath.Join(dir, "nope.json"))
	if missing != (State{}) {
		t.Fatalf("Load missing file = %#v, want empty State", missing)
	}
	corrupt := filepath.Join(dir, "update.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load(corrupt); got != (State{}) {
		t.Fatalf("Load corrupt file = %#v, want empty State", got)
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qg", "update.json")
	want := State{LastCheck: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), LatestVersion: "0.0.11", SkippedVersion: "0.0.10"}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	if got := Load(path); got != want {
		t.Fatalf("Load after Save = %#v, want %#v", got, want)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, "qg", "*.tmp*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
	// Overwriting an existing state must also leave a single intact file.
	want.LatestVersion = "0.0.12"
	if err := Save(path, want); err != nil {
		t.Fatalf("second Save returned error: %v", err)
	}
	if got := Load(path); got != want {
		t.Fatalf("Load after second Save = %#v, want %#v", got, want)
	}
}

func TestEnabled(t *testing.T) {
	for _, test := range []struct {
		name   string
		config Config
		want   bool
	}{
		{name: "dev version never checks", config: Config{CurrentVersion: "dev"}, want: false},
		{name: "homebrew never checks", config: Config{CurrentVersion: "0.0.10", InstallMethod: update.InstallMethodHomebrew}, want: false},
		{name: "kill switch 1", config: Config{CurrentVersion: "0.0.10", Env: testEnv(map[string]string{"QG_NO_UPDATE_CHECK": "1"})}, want: false},
		{name: "kill switch true", config: Config{CurrentVersion: "0.0.10", Env: testEnv(map[string]string{"QG_NO_UPDATE_CHECK": "true"})}, want: false},
		{name: "default on", config: Config{CurrentVersion: "0.0.10"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Enabled(test.config); got != test.want {
				t.Fatalf("Enabled = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDueAfterOneHour(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	config := Config{Now: func() time.Time { return now }}
	for _, test := range []struct {
		name  string
		state State
		want  bool
	}{
		{name: "59 minutes ago is not due", state: State{LastCheck: now.Add(-59 * time.Minute)}, want: false},
		{name: "60 minutes ago is due", state: State{LastCheck: now.Add(-60 * time.Minute)}, want: true},
		{name: "never checked is due", state: State{}, want: true},
		{name: "last check in the future is due", state: State{LastCheck: now.Add(time.Hour)}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Due(config, test.state); got != test.want {
				t.Fatalf("Due = %v, want %v", got, test.want)
			}
		})
	}
}

func TestPromptVersion(t *testing.T) {
	for _, test := range []struct {
		name            string
		current, latest string
		skipped         string
		want            string
	}{
		{name: "newer prompts", current: "0.0.10", latest: "0.0.11", want: "0.0.11"},
		{name: "equal stays silent", current: "0.0.11", latest: "0.0.11", want: ""},
		{name: "older stays silent", current: "0.0.12", latest: "0.0.11", want: ""},
		{name: "skipped stays silent", current: "0.0.10", latest: "0.0.11", skipped: "0.0.11", want: ""},
		{name: "newer than skipped prompts", current: "0.0.10", latest: "0.0.12", skipped: "0.0.11", want: "0.0.12"},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := Config{CurrentVersion: test.current}
			state := State{LatestVersion: test.latest, SkippedVersion: test.skipped}
			if got := PromptVersion(config, state); got != test.want {
				t.Fatalf("PromptVersion = %q, want %q", got, test.want)
			}
		})
	}
}

func npmReleaseServer(version string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"version":%q}`, version)
	}))
}

func githubReleaseServer(t *testing.T, version string) *httptest.Server {
	t.Helper()
	platform, arch := releaseAssetNames(t)
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"tag_name":"v%s","assets":[{"name":%q},{"name":%q}]}`,
			version, platform, arch)
	}))
}

func releaseAssetNames(t *testing.T) (string, string) {
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
	name := fmt.Sprintf("qg-v%s-%s-%s.%s", "0.0.11", platform, arch, extension)
	return name, name + ".sha256"
}

func failingServer(t *testing.T, name string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Errorf("%s must not be called", name)
		http.Error(writer, "must not be called", http.StatusInternalServerError)
	}))
}

func TestRefreshUsesNpmForNpmInstalls(t *testing.T) {
	npm := npmReleaseServer("0.0.11")
	defer npm.Close()
	github := failingServer(t, "GitHub")
	defer github.Close()

	initial := State{LastCheck: time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)}
	config := Config{
		CurrentVersion: "0.0.10",
		InstallMethod:  update.InstallMethodNpm,
		NpmEndpoint:    npm.URL,
		GitHubEndpoint: github.URL,
		Now:            func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) },
	}
	got := Refresh(t.Context(), config, initial)
	if got.LatestVersion != "0.0.11" {
		t.Fatalf("LatestVersion = %q, want 0.0.11 from npm", got.LatestVersion)
	}
	if !got.LastCheck.Equal(config.Now()) {
		t.Fatalf("LastCheck = %v, want the refresh time", got.LastCheck)
	}
}

func TestRefreshUsesGitHubForStandalone(t *testing.T) {
	npm := failingServer(t, "npm registry")
	defer npm.Close()
	github := githubReleaseServer(t, "0.0.11")
	defer github.Close()

	initial := State{LastCheck: time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC)}
	config := Config{
		CurrentVersion: "0.0.10",
		InstallMethod:  update.InstallMethodStandalone,
		NpmEndpoint:    npm.URL,
		GitHubEndpoint: github.URL,
		Now:            func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) },
	}
	got := Refresh(t.Context(), config, initial)
	if got.LatestVersion != "0.0.11" {
		t.Fatalf("LatestVersion = %q, want 0.0.11 from GitHub", got.LatestVersion)
	}
	if !got.LastCheck.Equal(config.Now()) {
		t.Fatalf("LastCheck = %v, want the refresh time", got.LastCheck)
	}
}

func TestRefreshNetworkErrorKeepsLatest(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "registry down", http.StatusInternalServerError)
	}))
	defer broken.Close()

	initial := State{LastCheck: time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC), LatestVersion: "0.0.9"}
	config := Config{
		CurrentVersion: "0.0.10",
		InstallMethod:  update.InstallMethodNpm,
		NpmEndpoint:    broken.URL,
		Now:            func() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) },
	}
	got := Refresh(t.Context(), config, initial)
	if got.LatestVersion != "0.0.9" {
		t.Fatalf("LatestVersion = %q, want to keep 0.0.9 after the error", got.LatestVersion)
	}
	if !got.LastCheck.Equal(config.Now()) {
		t.Fatalf("LastCheck = %v, want the refresh time even on error", got.LastCheck)
	}
}

func TestStatePathEndsInQGUpdateJSON(t *testing.T) {
	// The per-OS roots are pinned by TestStateDirRootCoversAllPlatforms. Here
	// we only prove StatePath appends qg/update.json on the current OS.
	path, err := StatePath(testEnv(map[string]string{
		"XDG_STATE_HOME": "/tmp/xdg-state",
		"LOCALAPPDATA":   `C:\Users\tester\AppData\Local`,
		"HOME":           "/tmp/home-fallback",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join("qg", "update.json")) {
		t.Fatalf("StatePath = %q, want it to end in qg/update.json", path)
	}
}

func TestStateDirRootCoversAllPlatforms(t *testing.T) {
	for _, test := range []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{name: "linux xdg", goos: "linux", env: map[string]string{"XDG_STATE_HOME": "/tmp/xdg"}, want: "/tmp/xdg"},
		{name: "linux home fallback", goos: "linux", env: map[string]string{"HOME": "/tmp/h"}, want: "/tmp/h/.local/state"},
		{name: "darwin xdg", goos: "darwin", env: map[string]string{"XDG_STATE_HOME": "/tmp/xdg"}, want: "/tmp/xdg"},
		{name: "windows localappdata", goos: "windows", env: map[string]string{"LOCALAPPDATA": `C:\A`}, want: filepath.Join(`C:\A`)},
		{name: "windows userprofile", goos: "windows", env: map[string]string{"USERPROFILE": `C:\U\tester`}, want: filepath.Join(`C:\U\tester`, "AppData", "Local")},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := stateDirRoot(test.goos, testEnv(test.env))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("stateDirRoot = %q, want %q", got, test.want)
			}
		})
	}
}
