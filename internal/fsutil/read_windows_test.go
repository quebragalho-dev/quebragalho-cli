//go:build windows

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileSharedRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":2}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	data, err := readFileShared(path)
	if err != nil {
		t.Fatalf("readFileShared: %v", err)
	}
	if string(data) != `{"schemaVersion":2}` {
		t.Fatalf("data = %q, want the fixture bytes", data)
	}
}

func TestReadFileSharedMissingFileIsNotExist(t *testing.T) {
	_, err := readFileShared(filepath.Join(t.TempDir(), "missing.json"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error = %v, want os.ErrNotExist", err)
	}
}

// A shared-access open must let a concurrent rename replace the file while
// the read handle is still open: that rename is exactly what failed with
// "Acesso negado" when readers used a plain open.
func TestOpenFileSharedAllowsRenameWhileOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "marker.json")
	if err := os.WriteFile(path, []byte(`v1`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	held, err := openFileShared(path)
	if err != nil {
		t.Fatalf("openFileShared: %v", err)
	}
	defer held.Close()
	replacement := filepath.Join(dir, "new.tmp")
	if err := os.WriteFile(replacement, []byte(`v2`), 0o600); err != nil {
		t.Fatalf("write replacement: %v", err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatalf("rename over a shared open must succeed, got: %v", err)
	}
}
