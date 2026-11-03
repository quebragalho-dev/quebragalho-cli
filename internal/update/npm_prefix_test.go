package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNpmPrefixFromPackageRoot(t *testing.T) {
	tests := []struct {
		name       string
		root       string
		wantPrefix string
		wantOk     bool
	}{
		{name: "unix global", root: "/opt/qg-test/lib/node_modules/@quebragalho-dev/cli", wantPrefix: "/opt/qg-test", wantOk: true},
		{name: "unix usr local", root: "/usr/local/lib/node_modules/@quebragalho-dev/cli", wantPrefix: "/usr/local", wantOk: true},
		{name: "unix trailing slash", root: "/opt/qg-test/lib/node_modules/@quebragalho-dev/cli/", wantPrefix: "/opt/qg-test", wantOk: true},
		{name: "windows backslashes", root: `C:\npm-global\node_modules\@quebragalho-dev\cli`, wantPrefix: `C:\npm-global`, wantOk: true},
		{name: "windows forward slashes", root: "C:/npm-global/node_modules/@quebragalho-dev/cli", wantPrefix: "C:/npm-global", wantOk: true},
		{name: "windows trailing separator", root: `C:\npm-global\node_modules\@quebragalho-dev\cli\`, wantPrefix: `C:\npm-global`, wantOk: true},
		{name: "unix local install is not a prefix", root: "/home/user/work/myapp/node_modules/@quebragalho-dev/cli", wantOk: false},
		{name: "unrelated directory", root: "/opt/qg-test/lib/something-else", wantOk: false},
		{name: "empty", root: "", wantOk: false},
		{name: "bare scope without prefix", root: "/node_modules/@quebragalho-dev/cli", wantOk: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prefix, ok := npmPrefixFromPackageRoot(tt.root)
			if ok != tt.wantOk {
				t.Fatalf("npmPrefixFromPackageRoot(%q) ok = %v, want %v", tt.root, ok, tt.wantOk)
			}
			if ok && prefix != tt.wantPrefix {
				t.Fatalf("npmPrefixFromPackageRoot(%q) = %q, want %q", tt.root, prefix, tt.wantPrefix)
			}
		})
	}
}

// TestNpmPrefixFromPackageRootRejectsBareLibRoot pins the fail-closed edge: a
// package straight under the filesystem root has no prefix to pass.
func TestNpmPrefixFromPackageRootRejectsBareLibRoot(t *testing.T) {
	if prefix, ok := npmPrefixFromPackageRoot("/lib/node_modules/@quebragalho-dev/cli"); ok {
		t.Fatalf("expected no prefix, got %q", prefix)
	}
}

func writeNpmWrapperPackage(t *testing.T, dir string, version string) {
	t.Helper()
	manifest := `{"name":"@quebragalho-dev/cli","version":"` + version + `","os":["linux"],"cpu":["x64"]}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".qg-binary-version"), []byte(version+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindNpmPackageRootAtBinaryDir(t *testing.T) {
	prefix := t.TempDir()
	root := filepath.Join(prefix, "lib", "node_modules", "@quebragalho-dev", "cli")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNpmWrapperPackage(t, root, "0.1.0")
	binary := filepath.Join(root, "qg")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := findNpmPackageRoot(binary)
	if !ok {
		t.Fatal("findNpmPackageRoot did not find the wrapper root next to the binary")
	}
	if got != root {
		t.Fatalf("findNpmPackageRoot = %q, want %q", got, root)
	}
	if gotPrefix, ok := npmInstallPrefix(binary); !ok || gotPrefix != prefix {
		t.Fatalf("npmInstallPrefix = %q, %v; want %q, true", gotPrefix, ok, prefix)
	}
}

func TestFindNpmPackageRootAbovePlatformBinary(t *testing.T) {
	prefix := t.TempDir()
	root := filepath.Join(prefix, "lib", "node_modules", "@quebragalho-dev", "cli")
	platformDir := filepath.Join(root, "node_modules", "@quebragalho-dev", "cli-linux-x64")
	if err := os.MkdirAll(platformDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNpmWrapperPackage(t, root, "0.1.0")
	if err := os.WriteFile(filepath.Join(platformDir, "package.json"), []byte(`{"name":"@quebragalho-dev/cli-linux-x64","os":["linux"],"cpu":["x64"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(platformDir, "qg")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := findNpmPackageRoot(binary)
	if !ok || got != root {
		t.Fatalf("findNpmPackageRoot = %q, %v; want %q, true", got, ok, root)
	}
}

func TestFindNpmPackageRootRejectsStandaloneDir(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "qg")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := findNpmPackageRoot(binary); ok {
		t.Fatal("findNpmPackageRoot matched a directory with no npm package")
	}
	if _, ok := npmInstallPrefix(binary); ok {
		t.Fatal("npmInstallPrefix inferred a prefix with no package root")
	}
}

// TestIsNpmPackageDirRejectsRepositoryManifest keeps the repository checkout
// (same package name, but a bin entry and broad platform lists) out of the
// prefix inference, mirroring DetectInstallMethod.
func TestIsNpmPackageDirRejectsRepositoryManifest(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"name":"@quebragalho-dev/cli","bin":{"qg":"bin/qg.js"},"os":["linux","darwin","win32","android"],"cpu":["x64","arm64"]}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if isNpmPackageDir(dir) {
		t.Fatal("isNpmPackageDir matched the repository manifest")
	}
	if _, ok := npmPrefixFromPackageRoot(filepath.Join(dir, "lib", "node_modules", "@quebragalho-dev", "cli")); !ok {
		t.Fatal("shape check must not depend on the manifest")
	}
}

func TestReadNpmPackageVersion(t *testing.T) {
	dir := t.TempDir()
	writeNpmWrapperPackage(t, dir, "0.1.0")
	got, err := readNpmPackageVersion(dir)
	if err != nil {
		t.Fatalf("readNpmPackageVersion: %v", err)
	}
	if got != "0.1.0" {
		t.Fatalf("readNpmPackageVersion = %q, want 0.1.0", got)
	}
	if _, err := readNpmPackageVersion(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("expected an error for a root with no package.json")
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readNpmPackageVersion(dir); err == nil || !strings.Contains(err.Error(), "não tem versão") {
		t.Fatalf("expected a missing-version error, got %v", err)
	}
	if _, err := readNpmPackageVersion(dir); err == nil || strings.Contains(err.Error(), "no version") {
		t.Fatalf("missing-version error kept English: %v", err)
	}
}
