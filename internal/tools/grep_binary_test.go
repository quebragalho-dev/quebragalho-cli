package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGrepSkipsNullByteFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "keep.txt"), "needle here")
	// Binary content with NUL byte but a plain extension: ripgrep skips these.
	binary := append([]byte("needle hidden"), 0, 'x', 'y')
	if err := os.WriteFile(filepath.Join(root, "binary.txt"), binary, 0o644); err != nil {
		t.Fatal(err)
	}
	pyc := append([]byte("needle hidden"), 0, 1, 2, 3)
	if err := os.WriteFile(filepath.Join(root, "module.pyc"), pyc, 0o644); err != nil {
		t.Fatal(err)
	}

	res := NewScopedGrepTool(root, nil).Run(context.Background(), map[string]any{
		"pattern":     "needle",
		"output_mode": "files_with_matches",
	})
	if res.Status != StatusOK {
		t.Fatalf("status=%s output=%s", res.Status, res.Output)
	}
	if strings.Contains(res.Output, "binary.txt") {
		t.Fatalf("grep must skip NUL-byte files, got:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "module.pyc") {
		t.Fatalf("grep must skip .pyc files, got:\n%s", res.Output)
	}
	if strings.TrimSpace(res.Output) != "keep.txt" {
		t.Fatalf("expected only keep.txt, got:\n%s", res.Output)
	}
}
