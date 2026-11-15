package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// atomicCreateTemp, atomicSyncFile, atomicRename and atomicSyncDir are the
// filesystem operations WriteFileAtomically is built from, held in variables
// so tests can observe and fault-inject the durability sequence (Sync before
// rename, retry on transient sharing violations) without a real power loss.
var (
	atomicCreateTemp = os.CreateTemp
	atomicSyncFile   = func(f *os.File) error { return f.Sync() }
	atomicRename     = RenameWithRetry
	atomicSyncDir    = syncParentDir
)

// WriteFileAtomically durably publishes data at path: the bytes are written
// to a sibling temporary file, fsynced before the atomic rename, and the
// parent directory is fsynced afterwards so the rename itself survives a
// crash. The temp file and the destination carry perm (callers pass 0600).
// The temporary directory gets 0700 when created, matching every caller.
// The file Sync is fail-closed; the parent-directory sync is best-effort and
// its error is ignored (some network/FUSE filesystems reject dir fsync while
// the published file itself is fine).
func WriteFileAtomically(path string, data []byte, perm os.FileMode) error {
	return WriteFileAtomicallyInDir(filepath.Dir(path), path, data, perm)
}

// WriteFileAtomicallyInDir is WriteFileAtomically with an explicit temporary
// directory. Stores that must publish through a dedicated directory (e.g. the
// OAuth token store's "<path>.publish", which sandbox profiles deny by name)
// pass it here so the random temp name stays inside it; the rename still lands
// on path atomically (same filesystem) and is retried on transient Windows
// sharing violations.
func WriteFileAtomicallyInDir(tempDir, path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return err
	}
	tmp, err := atomicCreateTemp(tempDir, ".qg-atomic-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Best effort: the rename below either succeeds (temp gone) or fails (temp
	// removed here); a crash between leaves a hidden temp callers already ignore.
	defer func() { _ = os.Remove(tmpPath) }()
	if perm != 0 {
		if err := tmp.Chmod(perm); err != nil {
			_ = tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := atomicSyncFile(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := atomicRename(tmpPath, path, nil); err != nil {
		return err
	}
	// Best-effort: the file is published; a dir-sync failure must not turn a
	// successful write into an error.
	_ = atomicSyncDir(filepath.Dir(path))
	return nil
}

// PublishTempFile durably publishes an already-written temporary file at
// target: it fsyncs the file, closes it, renames it over target with retry on
// transient Windows sharing violations, and fsyncs the parent directory.
// Callers keep ownership of temp-file placement, permissions and content;
// this only enforces the Sync-before-rename + retry + dir-sync sequence.
// The file Sync is fail-closed; the parent-directory sync is best-effort and
// its error is ignored.
func PublishTempFile(tmp *os.File, target string) error {
	if tmp == nil {
		return fmt.Errorf("fsutil: publish nil temp file for %s", target)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := atomicSyncFile(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := atomicRename(tmpPath, target, nil); err != nil {
		return err
	}
	_ = atomicSyncDir(filepath.Dir(target))
	return nil
}

// syncParentDir fsyncs dir so a rename inside it is durable across a crash. A
// platform that cannot open a directory for syncing (Windows) or a dir that
// cannot be opened reports no error: the rename is best-effort durable there.
func syncParentDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	syncErr := d.Sync()
	closeErr := d.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
