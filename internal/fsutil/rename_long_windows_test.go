//go:build windows

package fsutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// On Windows a rename cannot replace a destination another handle has open:
// MoveFileEx with MOVEFILE_REPLACE_EXISTING refuses even when every handle
// carries FILE_SHARE_DELETE, so the writer side is fixed by retrying, not by
// how readers open the file. This test holds the destination open with a
// plain read handle (a concurrent marker read caught mid-flight) and proves
// the failure is transient: the single rename fails, while RenameWithLongRetry
// issued against the held file completes as soon as the reader closes, inside
// the budget.
func TestRenameWithLongRetryOutlastsOpenReader(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "marker.json")
	if err := os.WriteFile(dst, []byte(`v1`), 0o600); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	src := filepath.Join(dir, "new.tmp")
	if err := os.WriteFile(src, []byte(`v2`), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	held, err := os.Open(dst)
	if err != nil {
		t.Fatalf("hold destination open: %v", err)
	}
	// The single rename over the open destination fails with a transient
	// lock error (this is the "Acesso negado" the marker writers hit when
	// two sandbox runners start together).
	if err := os.Rename(src, dst); err == nil {
		held.Close()
		t.Fatal("rename over an open destination succeeded; want a transient lock failure")
	} else if !os.IsPermission(err) && !isWindowsSharingOrLockViolation(err) {
		held.Close()
		t.Fatalf("rename error = %v; want a transient lock error", err)
	}

	// Rewrite the source in case the failed rename left the tree in any
	// doubt, then retry against the held destination like a racing writer.
	if err := os.WriteFile(src, []byte(`v2`), 0o600); err != nil {
		held.Close()
		t.Fatalf("rewrite source: %v", err)
	}
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- RenameWithLongRetry(src, dst, nil) }()
	// Let the retry spin against the held handle, then release it the way a
	// finished concurrent read does.
	time.Sleep(300 * time.Millisecond)
	if err := held.Close(); err != nil {
		t.Fatalf("release destination: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("RenameWithLongRetry did not finish after the reader closed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > windowsTransientLockRetryBudget {
		t.Fatalf("finished in %v, beyond the %v budget", elapsed, windowsTransientLockRetryBudget)
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read replaced destination: %v", err)
	}
	if string(data) != `v2` {
		t.Fatalf("destination = %q, want the replacement bytes", data)
	}
}
