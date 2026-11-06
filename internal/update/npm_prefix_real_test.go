package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// realManifest loads a published manifest from testdata. Only the version
// field may be overridden, so the shape under test stays the registry's.
func realManifest(t *testing.T, name string, version string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if version == "" {
		return raw
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["version"] = version
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// realNpmLayout builds the published install shape: a wrapper root carrying
// the real wrapper manifest (bin entry, broad os/cpu, no marker) and a
// platform payload below it carrying the real platform manifest (same name,
// X-os-arch version, single os/cpu, no bin). It returns the prefix, the
// wrapper root, and the platform binary.
func realNpmLayout(t *testing.T, version string, platformVersion string) (string, string, string) {
	t.Helper()
	prefix := t.TempDir()
	root := filepath.Join(prefix, "lib", "node_modules", "@quebragalho", "cli")
	platformDir := filepath.Join(root, "node_modules", "@quebragalho", "cli-darwin-arm64")
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), realManifest(t, "wrapper-package.json", version), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(platformDir, "package.json"), realManifest(t, "platform-darwin-arm64-package.json", platformVersion), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(platformDir, "qg")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return prefix, root, binary
}

// TestFindNpmPackageRootSkipsPlatformPayload is the P1 regression: the
// platform payload manifest carries the same package name with a single
// os/cpu, so a name-and-manifest check stops at the platform directory and
// the prefix inference fails. The walk must reach the wrapper root whose
// path ends in node_modules/@quebragalho/cli.
func TestFindNpmPackageRootSkipsPlatformPayload(t *testing.T) {
	prefix, root, binary := realNpmLayout(t, "0.0.11", "0.0.11-darwin-arm64")
	got, ok := findNpmPackageRoot(binary)
	if !ok {
		t.Fatal("findNpmPackageRoot found no package root above the platform binary")
	}
	if got != root {
		t.Fatalf("findNpmPackageRoot = %q, want the wrapper root %q", got, root)
	}
	gotPrefix, ok := npmInstallPrefix(binary)
	if !ok || gotPrefix != prefix {
		t.Fatalf("npmInstallPrefix = %q, %v; want %q, true", gotPrefix, ok, prefix)
	}
}

// TestFindNpmPackageRootUsesMarkerAtWrapperRoot covers the download-fallback
// shape: the binary sits next to the wrapper marker.
func TestFindNpmPackageRootUsesMarkerAtWrapperRoot(t *testing.T) {
	prefix, root, _ := realNpmLayout(t, "0.0.11", "0.0.11-darwin-arm64")
	binary := filepath.Join(root, "qg")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".qg-binary-version"), []byte("0.0.11\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := findNpmPackageRoot(binary)
	if !ok || got != root {
		t.Fatalf("findNpmPackageRoot = %q, %v; want %q, true", got, ok, root)
	}
	if gotPrefix, ok := npmInstallPrefix(binary); !ok || gotPrefix != prefix {
		t.Fatalf("npmInstallPrefix = %q, %v; want %q, true", gotPrefix, ok, prefix)
	}
}

// TestDetectInstallMethodWithRealManifests pins detection on the published
// shapes: the platform payload (same name, single os/cpu) reads as npm, the
// broad wrapper manifest alone reads as standalone.
func TestDetectInstallMethodWithRealManifests(t *testing.T) {
	_, root, binary := realNpmLayout(t, "0.0.11", "0.0.11-darwin-arm64")
	if got := DetectInstallMethod(binary); got != InstallMethodNpm {
		t.Fatalf("platform binary DetectInstallMethod = %q, want npm", got)
	}
	broad := filepath.Join(root, "qg")
	if err := os.WriteFile(broad, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := DetectInstallMethod(broad); got != InstallMethodStandalone {
		t.Fatalf("broad wrapper manifest DetectInstallMethod = %q, want standalone", got)
	}
}
