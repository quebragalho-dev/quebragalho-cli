package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPathWithinRoot(t *testing.T) {
	for _, tc := range []struct {
		root, candidate string
		want            bool
	}{
		{"/tmp", "/tmp/foo", true},
		{"/tmp", "/tmp", true},
		{"/tmp", "/other", false},
		{"/tmp", "/tmp-other", false},
		{"/a", "/a/../b", false},
		{"", "/tmp/foo", false},
	} {
		if got := pathWithinRoot(tc.root, tc.candidate); got != tc.want {
			t.Errorf("pathWithinRoot(%q, %q) = %v, want %v", tc.root, tc.candidate, got, tc.want)
		}
	}
}

func TestResolveTempRoot(t *testing.T) {
	dir := t.TempDir()
	if got := resolveTempRoot(dir); got == "" {
		t.Fatalf("resolveTempRoot(%q) = empty, want the resolved dir", dir)
	}
	if got := resolveTempRoot(filepath.Join(dir, "does-not-exist")); got != "" {
		t.Fatalf("resolveTempRoot(missing) = %q, want empty", got)
	}
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveTempRoot(file); got != "" {
		t.Fatalf("resolveTempRoot(file) = %q, want empty", got)
	}
	if got := resolveTempRoot(string(filepath.Separator)); got != "" {
		t.Fatalf("resolveTempRoot(filesystem root) = %q, want empty", got)
	}
}

// TestEnclosingTempRootDetectsCheckoutUnderTemp pins the skip trigger: a path
// under $TMPDIR (%TEMP% on Windows) is reported as enclosed. The full skip is
// proven by running a suite from a /tmp checkout (see the CI job notes).
func TestEnclosingTempRootDetectsCheckoutUnderTemp(t *testing.T) {
	base := t.TempDir()
	sub := filepath.Join(base, "pkg")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		t.Setenv("TEMP", base)
		t.Setenv("TMP", "")
	} else {
		t.Setenv("TMPDIR", base)
	}
	root, ok := enclosingTempRoot(sub)
	resolvedSub := sub
	if eval, err := filepath.EvalSymlinks(sub); err == nil {
		resolvedSub = eval
	}
	if !ok || !pathWithinRoot(root, resolvedSub) {
		t.Fatalf("enclosingTempRoot(%q) = %q,%v, want an enclosing temp root", sub, root, ok)
	}
}
