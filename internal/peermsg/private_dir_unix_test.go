//go:build unix

package peermsg

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

func TestEnsurePrivateDirRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("expected symlink runtime directory to be rejected")
	}
}

func TestEnsurePrivateDirRejectsSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(filepath.Join(link, "peers")); err == nil {
		t.Fatal("expected symlinked parent to be rejected")
	}
}

func countOpenFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestEnsurePrivateDirDoesNotLeakFDs(t *testing.T) {
	root := t.TempDir()
	// macOS points $TMPDIR through the /var -> /private/var symlink, which
	// ensurePrivateDir (correctly) refuses to traverse.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "a", "b", "c")
	before := countOpenFDs(t)
	for range 50 {
		if err := ensurePrivateDir(target); err != nil {
			t.Fatal(err)
		}
	}
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(filepath.Join(blocker, "sub")); err == nil {
		t.Fatal("expected non-directory component to be rejected")
	}
	if after := countOpenFDs(t); after != before {
		t.Fatalf("fd leak: %d open before, %d after", before, after)
	}
}

func TestEnsurePrivateDirNeverClosesForeignFDs(t *testing.T) {
	root := t.TempDir()
	// See above: resolve the /var symlink so the traversal stays on directories.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "a", "b", "c")
	var ebadf atomic.Int64
	stop := make(chan struct{})
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				fd, err := unix.Open("/dev/null", unix.O_RDONLY|unix.O_CLOEXEC, 0)
				if err != nil {
					continue
				}
				var stat unix.Stat_t
				if err := unix.Fstat(fd, &stat); err != nil {
					if err == unix.EBADF {
						ebadf.Add(1)
					}
					unix.Close(fd)
					continue
				}
				unix.Close(fd)
			}
		}()
	}
	for range 200 {
		if err := ensurePrivateDir(target); err != nil {
			close(stop)
			workers.Wait()
			t.Fatal(err)
		}
	}
	close(stop)
	workers.Wait()
	if n := ebadf.Load(); n != 0 {
		t.Fatalf("foreign fds closed underneath workers: %d EBADF", n)
	}
}
