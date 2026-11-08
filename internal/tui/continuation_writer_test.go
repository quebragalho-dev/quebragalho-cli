package tui

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// withAsyncWriter runs fn with the production async writer path enabled.
// The suite default (TestMain) is synchronous for determinism; these tests
// prove the background loop itself: gating, coalescing, Flush and failure
// accounting.
func withAsyncWriter(t *testing.T, fn func()) {
	t.Helper()
	prev := syncSnapshotWrites
	syncSnapshotWrites = false
	t.Cleanup(func() { syncSnapshotWrites = prev })
	fn()
}

func writerTestMessages(tag string) []qgruntime.Message {
	return []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: tag},
	}
}

func readWriterSnapshot(t *testing.T, store *sessions.Store, sessionID string) sessions.ContinuationSnapshot {
	t.Helper()
	snap, err := store.ReadContinuation(sessionID)
	if err != nil {
		t.Fatalf("ReadContinuation: %v", err)
	}
	if snap == nil {
		t.Fatal("expected a snapshot on disk")
	}
	return *snap
}

// TestContinuationWriterDropsStaleRun is the B1 gate at the writer seam: a
// cancelled run whose goroutine drains after the next turn started must not
// overwrite the newer snapshot.
func TestContinuationWriterDropsStaleRun(t *testing.T) {
	withAsyncWriter(t, func() {
		store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
		w := newContinuationWriter(store)
		newer := buildContinuationSnapshot("sess_b1", "m", "p", "", "", 10, writerTestMessages("new"), nil)
		if !w.Queue(newer, 2) {
			t.Fatal("newer run must queue")
		}
		if !w.Flush(10 * time.Second) {
			t.Fatal("flush timed out")
		}
		stale := buildContinuationSnapshot("sess_b1", "m", "p", "", "", 11, writerTestMessages("stale"), nil)
		if w.Queue(stale, 1) {
			t.Fatal("older run must be refused even with a newer EventSeq")
		}
		sameRunOlder := buildContinuationSnapshot("sess_b1", "m", "p", "", "", 9, writerTestMessages("older"), nil)
		if w.Queue(sameRunOlder, 2) {
			t.Fatal("same run with older seq must be refused")
		}
		if !w.Flush(10 * time.Second) {
			t.Fatal("flush timed out")
		}
		got := readWriterSnapshot(t, store, "sess_b1")
		if got.Messages[1].Content != "new" || got.EventSeq != 10 {
			t.Fatalf("stale write overwrote the snapshot: %#v", got)
		}
	})
}

// TestContinuationWriterCoalescesPerSession proves only the latest snapshot
// per session reaches disk when several queue before the loop drains.
func TestContinuationWriterCoalescesPerSession(t *testing.T) {
	withAsyncWriter(t, func() {
		store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
		w := newContinuationWriter(store)
		for seq := 1; seq <= 5; seq++ {
			snap := buildContinuationSnapshot("sess_c", "m", "p", "", "", seq, writerTestMessages(fmt.Sprintf("v%d", seq)), nil)
			if !w.Queue(snap, 7) {
				t.Fatalf("seq %d must queue", seq)
			}
		}
		if !w.Flush(10 * time.Second) {
			t.Fatal("flush timed out")
		}
		got := readWriterSnapshot(t, store, "sess_c")
		if got.EventSeq != 5 || got.Messages[1].Content != "v5" {
			t.Fatalf("expected only the latest snapshot, got seq=%d content=%q", got.EventSeq, got.Messages[1].Content)
		}
	})
}

// TestContinuationWriterConcurrentQueue hammers the writer from several
// goroutines (run with -race): no torn state, and the file always holds a
// complete snapshot from one of the queued runs.
func TestContinuationWriterConcurrentQueue(t *testing.T) {
	withAsyncWriter(t, func() {
		store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
		w := newContinuationWriter(store)
		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 25; i++ {
					snap := buildContinuationSnapshot("sess_r", "m", "p", "", "", g*25+i, writerTestMessages("x"), nil)
					w.Queue(snap, int64(g))
				}
			}(g)
		}
		wg.Wait()
		if !w.Flush(10 * time.Second) {
			t.Fatal("flush timed out")
		}
		got := readWriterSnapshot(t, store, "sess_r")
		if len(got.Messages) != 2 {
			t.Fatalf("torn snapshot: %#v", got.Messages)
		}
	})
}

// BenchmarkContinuationWriterQueueLarge measures the UI-goroutine work of
// persisting a >=6MB snapshot (I1): building the snapshot and Queueing it
// must return without touching disk. The background loop's marshal+fsync
// cost is reported separately as ns/flush-op.
func BenchmarkContinuationWriterQueueLarge(b *testing.B) {
	prev := syncSnapshotWrites
	syncSnapshotWrites = false
	defer func() { syncSnapshotWrites = prev }()
	store := sessions.NewStore(sessions.StoreOptions{RootDir: b.TempDir()})
	w := newContinuationWriter(store)
	big := make([]byte, 1024*1024)
	for i := range big {
		big[i] = byte(i)
	}
	images := make([]qgruntime.ImageBlock, 6)
	for i := range images {
		images[i] = qgruntime.ImageBlock{MediaType: "image/png", Data: append([]byte(nil), big...)}
	}
	base := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "look", Images: images},
		{Role: qgruntime.MessageRoleAssistant, Content: "done"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap := buildContinuationSnapshot("sess_big", "m", "p", "", "", i, base, nil)
		w.Queue(snap, int64(i))
	}
	start := time.Now()
	if !w.Flush(60 * time.Second) {
		b.Fatal("flush timed out")
	}
	elapsed := time.Since(start)
	b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N), "ns/flush-op")
	info, err := os.Stat(store.RootDir + "/sess_big/" + sessions.ContinuationFile)
	if err != nil {
		b.Fatalf("snapshot file missing: %v", err)
	}
	b.ReportMetric(float64(info.Size()), "snapshot-bytes")
}

// the third consecutive failure for a session fires the hook once per
// streak, and a later success resets it.
func TestContinuationWriterReportsRepeatedFailure(t *testing.T) {
	withAsyncWriter(t, func() {
		// A store whose RootDir is a file: every write fails.
		blocker := t.TempDir() + "/blocker"
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		store := sessions.NewStore(sessions.StoreOptions{RootDir: blocker})
		w := newContinuationWriter(store)
		var calls []string
		w.onRepeatedFailure = func(sessionID string) { calls = append(calls, sessionID) }
		for i := 0; i < 4; i++ {
			snap := buildContinuationSnapshot("sess_f", "m", "p", "", "", i, writerTestMessages("x"), nil)
			if !w.Queue(snap, int64(i)) {
				t.Fatalf("run %d must queue", i)
			}
			if !w.Flush(10 * time.Second) {
				t.Fatal("flush timed out")
			}
		}
		if len(calls) != 1 || calls[0] != "sess_f" {
			t.Fatalf("warning must fire once on the third failure, got %v", calls)
		}
	})
}
