package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNpmPackageNameMatchesPackageJSON holds the Go constant to the name the
// release publishes: package.json is the one place the name lives, and a
// drift would make every npm install read as standalone.
func TestNpmPackageNameMatchesPackageJSON(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	if pkg.Name != npmPackageName {
		t.Fatalf("package.json name = %q, npmPackageName = %q", pkg.Name, npmPackageName)
	}
	if npmLegacyPackageName == npmPackageName {
		t.Fatal("the legacy name must differ from the current one")
	}
}

// legacyManifest is a published manifest from testdata renamed to the legacy
// package name, aliases included, as the bridge release assembles it.
func legacyManifest(t *testing.T, name string, version string) []byte {
	t.Helper()
	var manifest map[string]any
	if err := json.Unmarshal(realManifest(t, name, version), &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["name"] = npmLegacyPackageName
	if aliases, ok := manifest["optionalDependencies"].(map[string]any); ok {
		renamed := map[string]any{}
		for alias, target := range aliases {
			renamed[strings.Replace(alias, npmPackageName, npmLegacyPackageName, 1)] = strings.Replace(target.(string), npmPackageName, npmLegacyPackageName, 1)
		}
		manifest["optionalDependencies"] = renamed
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// legacyNpmLayout is realNpmLayout under the legacy name: the wrapper root at
// <prefix>/lib/node_modules/@quebragalho-dev/cli and the platform payload
// below it. It returns the prefix, the wrapper root and the platform binary.
func legacyNpmLayout(t *testing.T, version string, platformVersion string) (string, string, string) {
	t.Helper()
	prefix := t.TempDir()
	root := filepath.Join(prefix, "lib", "node_modules", "@quebragalho-dev", "cli")
	platformDir := filepath.Join(root, "node_modules", "@quebragalho-dev", "cli-darwin-arm64")
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), legacyManifest(t, "wrapper-package.json", version), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platformDir, "package.json"), legacyManifest(t, "platform-darwin-arm64-package.json", platformVersion), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(platformDir, "qg")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return prefix, root, binary
}

// TestLegacyNpmInstallIsRecognized pins the bridge's footing: a binary
// installed under the legacy name still reads as npm-managed, with its root
// and prefix, and is told apart from an install under the current name.
func TestLegacyNpmInstallIsRecognized(t *testing.T) {
	prefix, root, binary := legacyNpmLayout(t, "0.0.16", "0.0.16-darwin-arm64")
	if method := DetectInstallMethod(binary); method != InstallMethodNpm {
		t.Fatalf("DetectInstallMethod = %q, want npm", method)
	}
	if got, ok := findNpmPackageRoot(binary); !ok || got != root {
		t.Fatalf("findNpmPackageRoot = %q, %v; want %q, true", got, ok, root)
	}
	if got, ok := npmInstallPrefix(binary); !ok || got != prefix {
		t.Fatalf("npmInstallPrefix = %q, %v; want %q, true", got, ok, prefix)
	}
	if !isLegacyNpmInstall(binary) {
		t.Fatal("isLegacyNpmInstall = false for an install under the legacy name")
	}

	_, _, current := realNpmLayout(t, "0.0.16", "0.0.16-darwin-arm64")
	if isLegacyNpmInstall(current) {
		t.Fatal("isLegacyNpmInstall = true for an install under the current name")
	}
}

// TestNpmWrapperRootNameNeedsMatchingPathAndManifest keeps the walk honest: a
// directory only counts as a wrapper root when its path is the install
// location of the very name its manifest carries.
func TestNpmWrapperRootNameNeedsMatchingPathAndManifest(t *testing.T) {
	base := t.TempDir()
	write := func(dir string, name string) string {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"`+name+`","version":"0.0.16"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	cases := []struct {
		name     string
		dir      string
		wantName string
		wantOk   bool
	}{
		{"current name", write(filepath.Join(base, "a", "node_modules", "@quebragalho", "cli"), npmPackageName), npmPackageName, true},
		{"legacy name", write(filepath.Join(base, "b", "node_modules", "@quebragalho-dev", "cli"), npmLegacyPackageName), npmLegacyPackageName, true},
		{"manifest of the other name", write(filepath.Join(base, "c", "node_modules", "@quebragalho", "cli"), npmLegacyPackageName), "", false},
		{"another scope", write(filepath.Join(base, "d", "node_modules", "@someone", "cli"), "@someone/cli"), "", false},
		{"not under node_modules", write(filepath.Join(base, "e", "vendor", "@quebragalho", "cli"), npmPackageName), "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := npmWrapperRootName(tc.dir)
			if ok != tc.wantOk || got != tc.wantName {
				t.Fatalf("npmWrapperRootName = %q, %v; want %q, %v", got, ok, tc.wantName, tc.wantOk)
			}
		})
	}
}

// TestApplyLegacyNpmHandsOffTheMoveWhenUpToDate is the bridge's main path for
// `qg upgrade`: the old name gets no more versions, so the move is handed to
// the wrapper even when the check finds nothing newer, and npm never runs
// in-process (it would remove the directory holding this binary).
func TestApplyLegacyNpmHandsOffTheMoveWhenUpToDate(t *testing.T) {
	_, _, binary := legacyNpmLayout(t, "0.0.16", "0.0.16-darwin-arm64")
	stubCurrentExecutable(t, binary)
	calls := 0
	original := runNpm
	runNpm = func(context.Context, string, []string) error {
		calls++
		return nil
	}
	t.Cleanup(func() { runNpm = original })
	handoff := filepath.Join(t.TempDir(), "handoff.json")
	t.Setenv("QG_NPM_UPDATE_HANDOFF", handoff)
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "")

	result, err := Apply(context.Background(), Options{
		CurrentVersion: "0.0.16",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.0.16", "linux", "amd64"), nil
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("runNpm ran %d times, want 0: the wrapper owns the move", calls)
	}
	raw, err := os.ReadFile(handoff)
	if err != nil {
		t.Fatalf("handoff file missing: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(raw, &payload); err != nil || payload["version"] != "0.0.16" {
		t.Fatalf("handoff payload = %q, want {\"version\":\"0.0.16\"}", strings.TrimSpace(string(raw)))
	}
	if !result.HandoffPending || !result.NpmMove || result.Applied {
		t.Fatalf("result = %#v, want a pending move, not applied", result)
	}
	human := FormatApply(result)
	if !strings.Contains(human, "a troca para "+npmPackageName) || !strings.Contains(human, "termina quando o qg fechar") {
		t.Fatalf("human output = %q, want the pending move line", human)
	}
}

// TestApplyLegacyNpmTargetsANewerRelease moves straight to the latest release
// when one is out, instead of moving first and updating after.
func TestApplyLegacyNpmTargetsANewerRelease(t *testing.T) {
	_, _, binary := legacyNpmLayout(t, "0.0.16", "0.0.16-darwin-arm64")
	stubCurrentExecutable(t, binary)
	handoff := filepath.Join(t.TempDir(), "handoff.json")
	t.Setenv("QG_NPM_UPDATE_HANDOFF", handoff)
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "")

	result, err := Apply(context.Background(), Options{
		CurrentVersion: "0.0.16",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.0.18", "linux", "amd64"), nil
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	raw, err := os.ReadFile(handoff)
	if err != nil {
		t.Fatalf("handoff file missing: %v", err)
	}
	if !strings.Contains(string(raw), `"0.0.18"`) || result.LatestVersion != "0.0.18" {
		t.Fatalf("handoff = %q, LatestVersion = %q; want 0.0.18", strings.TrimSpace(string(raw)), result.LatestVersion)
	}
}

// TestApplyLegacyNpmMovesOffline: without release metadata the move still
// goes out, at the running version.
func TestApplyLegacyNpmMovesOffline(t *testing.T) {
	_, _, binary := legacyNpmLayout(t, "0.0.16", "0.0.16-darwin-arm64")
	stubCurrentExecutable(t, binary)
	handoff := filepath.Join(t.TempDir(), "handoff.json")
	t.Setenv("QG_NPM_UPDATE_HANDOFF", handoff)
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "")

	result, err := Apply(context.Background(), Options{
		CurrentVersion: "0.0.16",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(context.Context, string) (Release, error) {
			return Release{}, errors.New("offline")
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.NpmMove || result.LatestVersion != "0.0.16" {
		t.Fatalf("result = %#v, want a move at 0.0.16", result)
	}
	if _, err := os.Stat(handoff); err != nil {
		t.Fatalf("handoff file missing: %v", err)
	}
}

// TestApplyLegacyNpmRefusesOutsideTheWrapper: run directly, the binary has no
// wrapper to finish the move, so it refuses with both commands, each with the
// owning prefix, and runs nothing.
func TestApplyLegacyNpmRefusesOutsideTheWrapper(t *testing.T) {
	prefix, _, binary := legacyNpmLayout(t, "0.0.16", "0.0.16-darwin-arm64")
	stubCurrentExecutable(t, binary)
	calls := 0
	original := runNpm
	runNpm = func(context.Context, string, []string) error {
		calls++
		return nil
	}
	t.Cleanup(func() { runNpm = original })
	t.Setenv("QG_NPM_UPDATE_HANDOFF", "")
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "")

	_, err := Apply(context.Background(), Options{
		CurrentVersion: "0.0.16",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.0.17", "linux", "amd64"), nil
		},
	})
	if err == nil {
		t.Fatal("Apply outside the wrapper must refuse the move")
	}
	if calls != 0 {
		t.Fatalf("runNpm ran %d times, want 0", calls)
	}
	// The prefix comes from the resolved executable path (/var is
	// /private/var on macOS), so compare against the canonical spelling.
	if canonical, err := filepath.EvalSymlinks(prefix); err == nil {
		prefix = canonical
	}
	for _, want := range []string{
		`npm uninstall -g --prefix "` + prefix + `" ` + npmLegacyPackageName,
		`npm install -g --prefix "` + prefix + `" ` + npmPackageName,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %q", err, want)
		}
	}
}

// TestApplyLegacyJustInstalledIsNoSuccess: reopened still under the legacy
// name after an update means the move did not happen, which `qg upgrade`
// must not report as done.
func TestApplyLegacyJustInstalledIsNoSuccess(t *testing.T) {
	_, _, binary := legacyNpmLayout(t, "0.0.16", "0.0.16-darwin-arm64")
	stubCurrentExecutable(t, binary)
	t.Setenv("QG_NPM_UPDATE_HANDOFF", "")
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "0.0.16")

	_, err := Apply(context.Background(), Options{
		CurrentVersion: "0.0.16",
		Fetch: func(context.Context, string) (Release, error) {
			t.Fatal("no release check after the wrapper's reopen")
			return Release{}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "não aconteceu") {
		t.Fatalf("err = %v, want the move that did not happen", err)
	}
}

// TestFormatGuidesALegacyInstallToMove: `qg update` tells a legacy install how
// to move whether or not an update is out, and never offers the plain npm
// reinstall, which fails on it (both names own the qg bin).
func TestFormatGuidesALegacyInstallToMove(t *testing.T) {
	for _, available := range []bool{false, true} {
		result := Result{
			CurrentVersion:  "0.0.16",
			LatestVersion:   "0.0.17",
			ReleaseURL:      "https://example.test/release",
			UpdateAvailable: available,
			installMethod:   InstallMethodNpm,
			npmPrefix:       "/opt/npm",
			npmLegacy:       true,
		}
		text := Format(result)
		for _, want := range []string{
			"`qg upgrade` troca para " + npmPackageName,
			`npm uninstall -g --prefix "/opt/npm" ` + npmLegacyPackageName,
			`npm install -g --prefix "/opt/npm" ` + npmPackageName + "`",
		} {
			if !strings.Contains(text, want) {
				t.Fatalf("UpdateAvailable=%v: Format = %q, want %q", available, text, want)
			}
		}
		if strings.Contains(text, npmPackageName+"@0.0.17") {
			t.Fatalf("UpdateAvailable=%v: Format = %q offers the plain reinstall", available, text)
		}
	}

	current := Format(Result{CurrentVersion: "0.0.16", LatestVersion: "0.0.16", installMethod: InstallMethodNpm})
	if strings.Contains(current, npmLegacyPackageName) {
		t.Fatalf("an install under the current name must not hear about the move: %q", current)
	}
}
