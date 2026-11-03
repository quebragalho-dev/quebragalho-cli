package update

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestApplyNpmHandoffWritesRequestWithoutRunningNpm pins the Windows leftover
// fix: under the npm wrapper (QG_NPM_UPDATE_HANDOFF set) an npm update must
// record the target version and return pending, never running npm
// in-process while the binary is open.
func TestApplyNpmHandoffWritesRequestWithoutRunningNpm(t *testing.T) {
	prefix, _, binary := realNpmLayout(t, "0.1.0", "0.1.0-darwin-arm64")
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
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.2.0", "linux", "amd64"), nil
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("runNpm ran %d times, want 0: the wrapper owns the install", calls)
	}
	raw, err := os.ReadFile(handoff)
	if err != nil {
		t.Fatalf("handoff file missing: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(raw, &payload); err != nil || payload["version"] != "0.2.0" {
		t.Fatalf("handoff payload = %q, want {\"version\":\"0.2.0\"}", strings.TrimSpace(string(raw)))
	}
	if result.Applied {
		t.Fatalf("pending result must not claim Applied, got %#v", result)
	}
	if !result.HandoffPending {
		t.Fatalf("pending result must set HandoffPending, got %#v", result)
	}
	if result.InstallMethod != InstallMethodNpm {
		t.Fatalf("InstallMethod = %q, want npm", result.InstallMethod)
	}
	if result.LatestVersion != "0.2.0" {
		t.Fatalf("LatestVersion = %q, want 0.2.0", result.LatestVersion)
	}
	if strings.Contains(result.Message, "updated") {
		t.Fatalf("pending Message = %q, must not claim an update happened", result.Message)
	}
	human := FormatApply(result)
	if !strings.Contains(human, "termina quando o qg fechar") {
		t.Fatalf("human output = %q, want the pt-BR pending line", human)
	}
	if strings.Contains(human, "atualizado") {
		t.Fatalf("pending human output must not claim success: %q", human)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Applied        bool   `json:"applied"`
		HandoffPending bool   `json:"handoffPending"`
		Message        string `json:"message"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Applied || !decoded.HandoffPending {
		t.Fatalf("JSON must stay honest (applied:false, handoffPending:true), got %+v", decoded)
	}
	if strings.Contains(decoded.Message, "atualizado") {
		t.Fatalf("JSON message must stay English, got %q", decoded.Message)
	}
	_ = prefix
}

// TestApplyNpmHandoffSkipsWhenUpToDate ensures no handoff file is written
// when there is nothing to install.
func TestApplyNpmHandoffSkipsWhenUpToDate(t *testing.T) {
	_, _, binary := realNpmLayout(t, "0.1.0", "0.1.0-darwin-arm64")
	stubCurrentExecutable(t, binary)
	handoff := filepath.Join(t.TempDir(), "handoff.json")
	t.Setenv("QG_NPM_UPDATE_HANDOFF", handoff)
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "")

	result, err := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.1.0", "linux", "amd64"), nil
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if _, statErr := os.Stat(handoff); !os.IsNotExist(statErr) {
		t.Fatal("no handoff may be written when already up to date")
	}
	if result.Applied || result.HandoffPending {
		t.Fatalf("up-to-date result must be plain noop, got %#v", result)
	}
}

// TestApplyJustInstalledMismatchFailsWithoutRetryingNpm pins the reopened
// upgrade path: when the wrapper's install did not take, Apply must error
// with the manual command instead of running npm again in-process.
func TestApplyJustInstalledMismatchFailsWithoutRetryingNpm(t *testing.T) {
	prefix, _, binary := realNpmLayout(t, "0.1.0", "0.1.0-darwin-arm64")
	canonicalPrefix, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		t.Fatal(err)
	}
	prefix = canonicalPrefix
	stubCurrentExecutable(t, binary)
	calls := 0
	original := runNpm
	runNpm = func(context.Context, string, []string) error {
		calls++
		return nil
	}
	t.Cleanup(func() { runNpm = original })

	t.Setenv("QG_UPDATE_JUST_INSTALLED", "0.2.0")
	t.Setenv("QG_NPM_UPDATE_HANDOFF", "")

	_, applyErr := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			t.Fatal("a failed handoff must not reach the network check")
			return Release{}, nil
		},
	})
	if applyErr == nil {
		t.Fatal("expected an error when the wrapper install did not take")
	}
	if calls != 0 {
		t.Fatalf("runNpm ran %d times, want 0: no in-process retry after a failed handoff", calls)
	}
	for _, want := range []string{
		"não ficou ativa",
		"0.2.0",
		"0.1.0",
		`--prefix "` + prefix + `"`,
		"Para atualizar à mão, rode: npm install -g",
	} {
		if !strings.Contains(applyErr.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", applyErr, want)
		}
	}
	if got := strings.TrimSpace(os.Getenv("QG_UPDATE_JUST_INSTALLED")); got != "" {
		t.Fatalf("QG_UPDATE_JUST_INSTALLED leaked: %q", got)
	}
}

// TestApplyJustInstalledMatchProceedsToCheck ensures a successful wrapper
// install does not block the normal already-up-to-date path.
func TestApplyJustInstalledMatchProceedsToCheck(t *testing.T) {
	_, _, binary := realNpmLayout(t, "0.1.0", "0.1.0-darwin-arm64")
	stubCurrentExecutable(t, binary)
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "0.1.0")
	t.Setenv("QG_NPM_UPDATE_HANDOFF", "")

	checked := false
	result, err := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			checked = true
			return releaseForTarget(t, "v0.1.0", "linux", "amd64"), nil
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !checked {
		t.Fatal("a matching just-installed install must still check")
	}
	if result.Applied || result.HandoffPending {
		t.Fatalf("matching install with no newer release must be noop, got %#v", result)
	}
	if got := strings.TrimSpace(os.Getenv("QG_UPDATE_JUST_INSTALLED")); got != "" {
		t.Fatalf("QG_UPDATE_JUST_INSTALLED leaked: %q", got)
	}
}

// TestApplyStandaloneIgnoresHandoff keeps the same handoff path on every OS:
// a standalone binary never writes the npm handoff, even when the variable
// is set.
func TestApplyStandaloneIgnoresHandoff(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "qg")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubCurrentExecutable(t, binary)
	handoff := filepath.Join(t.TempDir(), "handoff.json")
	t.Setenv("QG_NPM_UPDATE_HANDOFF", handoff)
	t.Setenv("QG_UPDATE_JUST_INSTALLED", "")

	_, err := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.2.0", "linux", "amd64"), nil
		},
	})
	// The standalone path needs real archive bytes and fails to download
	// from the fake release host; the point is the handoff stays untouched.
	if err == nil {
		t.Fatal("expected the standalone download to fail against the fake host")
	}
	if _, statErr := os.Stat(handoff); !os.IsNotExist(statErr) {
		t.Fatal("a standalone install must never write the npm handoff")
	}
}
