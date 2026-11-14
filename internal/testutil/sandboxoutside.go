// TempDirOutsideDefaultTemp creates a scratch directory OUTSIDE the default
// temporary write roots and returns its symlink-resolved absolute path.
//
// The directory is created under the test's working directory (the package
// dir) so sandbox-boundary tests can prove a path escapes the workspace.
// When the checkout itself lives under a default temp write root (/tmp on
// Unix, $TMPDIR, %TEMP%/%TMP% on Windows), that outside directory is writable
// by default: NewScope seeds those roots, so a test expecting a deny would
// see an allow instead of exercising the boundary. That makes the test
// vacuous, not passing, so skip with an explicit reason instead of running
// it: rerun from a checkout outside the temp tree.
//
// The root list mirrors the product rule (sandbox.defaultTempWriteRootCandidates:
// /tmp plus $TMPDIR on Unix, %TEMP% plus %TMP% on Windows), resolved the same
// way the scope resolves them (absolute, symlinks evaluated).
package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TempDirOutsideDefaultTemp creates a scratch directory under the current
// working directory, outside the default temporary write roots. It skips the
// calling test when the checkout lives under one of those roots.
func TempDirOutsideDefaultTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".qg-sandbox-outside-")
	if err != nil {
		t.Fatalf("MkdirTemp outside default temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("Abs(%q): %v", dir, err)
	}
	resolved := filepath.Clean(abs)
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		resolved = eval
	}
	if root, ok := enclosingTempRoot(resolved); ok {
		t.Skipf("qg: checkout %q is under the default temp write root %q: the sandbox grants writes there by default, so boundary tests are vacuous; rerun from a checkout outside the temp tree", resolved, root)
	}
	return resolved
}

// enclosingTempRoot reports the default temporary write root path lies under,
// or "" when it is outside them all. The input is symlink-resolved first,
// mirroring TempDirOutsideDefaultTemp: t.TempDir() hands out the unresolved
// spelling (macOS /var/folders/...) while the roots are stored resolved
// (/private/var/folders/...), and comparing them raw never matches.
func enclosingTempRoot(path string) (string, bool) {
	resolved := path
	if eval, err := filepath.EvalSymlinks(path); err == nil {
		resolved = eval
	}
	for _, root := range defaultTempWriteRoots() {
		if pathWithinRoot(root, resolved) {
			return root, true
		}
	}
	return "", false
}

// defaultTempWriteRoots mirrors the product's default temporary write roots:
// /tmp plus $TMPDIR on Unix, %TEMP% plus %TMP% on Windows. Entries are made
// absolute and symlink-resolved the way the scope resolves them; entries that
// do not resolve to an existing directory are dropped, matching the product
// behavior of ignoring unusable defaults.
func defaultTempWriteRoots() []string {
	var candidates []string
	if runtime.GOOS == "windows" {
		for _, key := range []string{"TEMP", "TMP"} {
			if root := strings.TrimSpace(os.Getenv(key)); root != "" {
				candidates = append(candidates, root)
			}
		}
	} else {
		candidates = append(candidates, "/tmp")
		if tmpdir := strings.TrimSpace(os.Getenv("TMPDIR")); tmpdir != "" {
			candidates = append(candidates, tmpdir)
		}
	}
	var roots []string
	for _, candidate := range candidates {
		resolved := resolveTempRoot(candidate)
		if resolved == "" {
			continue
		}
		roots = append(roots, resolved)
	}
	return roots
}

// resolveTempRoot makes a candidate absolute (~-expanded) and symlink-resolved,
// returning "" when it is not an existing directory or is the filesystem root
// (both are rejected as scope roots by the product).
func resolveTempRoot(candidate string) string {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return ""
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		trimmed = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(trimmed[1:], "/"), string(filepath.Separator)))
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return ""
	}
	if filepath.Dir(resolved) == resolved {
		return ""
	}
	return resolved
}

// pathWithinRoot reports whether candidate sits inside root.
func pathWithinRoot(root string, candidate string) bool {
	if root == "" {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
