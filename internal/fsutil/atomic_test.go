package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteFileAtomicallySyncsBeforeRename fails if the temp file is renamed
// before it is synced: the injected rename asserts the sync flag is set, so a
// mutant that drops the Sync call (or reorders it after the rename) fails.
func TestWriteFileAtomicallySyncsBeforeRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")

	origCreate, origSync, origRename, origDir := atomicCreateTemp, atomicSyncFile, atomicRename, atomicSyncDir
	defer func() {
		atomicCreateTemp, atomicSyncFile, atomicRename, atomicSyncDir = origCreate, origSync, origRename, origDir
	}()

	synced := false
	atomicSyncFile = func(f *os.File) error {
		synced = true
		return f.Sync()
	}
	atomicRename = func(src, dst string, rename func(src, dst string) error) error {
		if !synced {
			t.Fatalf("rename %s -> %s ran before Sync; a crash would leave an empty file", src, dst)
		}
		return os.Rename(src, dst)
	}
	atomicSyncDir = func(string) error { return nil }

	if err := WriteFileAtomically(target, []byte("{\"v\":1}\n"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomically: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "{\"v\":1}\n" {
		t.Fatalf("read back = %q err=%v", data, err)
	}
}

// TestWriteFileAtomicallySyncErrorFailsClosed proves the Sync result is
// honored: a failing Sync must abort before the rename, leaving no target.
func TestWriteFileAtomicallySyncErrorFailsClosed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")

	origSync, origDir := atomicSyncFile, atomicSyncDir
	defer func() { atomicSyncFile, atomicSyncDir = origSync, origDir }()
	atomicSyncFile = func(*os.File) error { return os.ErrInvalid }
	atomicSyncDir = func(string) error { return nil }

	if err := WriteFileAtomically(target, []byte("x"), 0o600); err == nil {
		t.Fatal("expected Sync error to fail the write")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("target must not exist after Sync failure, stat err=%v", err)
	}
}

// TestWriteFileAtomicallyPreservesPerm checks the 0600/0400 contract callers
// rely on for secrets.
func TestWriteFileAtomicallyPreservesPerm(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	origDir := atomicSyncDir
	atomicSyncDir = func(string) error { return nil }
	defer func() { atomicSyncDir = origDir }()

	if err := WriteFileAtomically(target, []byte("s"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomically: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
}

// TestPublishTempFileSyncsBeforeRename covers the already-open-temp path with
// the same ordering assertion as the full helper.
func TestPublishTempFileSyncsBeforeRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "state.json")
	tmp, err := os.CreateTemp(dir, "src-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString("payload"); err != nil {
		t.Fatal(err)
	}

	origSync, origRename, origDir := atomicSyncFile, atomicRename, atomicSyncDir
	defer func() { atomicSyncFile, atomicRename, atomicSyncDir = origSync, origRename, origDir }()
	synced := false
	atomicSyncFile = func(f *os.File) error {
		synced = true
		return f.Sync()
	}
	atomicRename = func(src, dst string, rename func(src, dst string) error) error {
		if !synced {
			t.Fatal("rename ran before Sync")
		}
		return os.Rename(src, dst)
	}
	atomicSyncDir = func(string) error { return nil }

	if err := PublishTempFile(tmp, target); err != nil {
		t.Fatalf("PublishTempFile: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(data), "payload") {
		t.Fatalf("read back = %q err=%v", data, err)
	}
}
