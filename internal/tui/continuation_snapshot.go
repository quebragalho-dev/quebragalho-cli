package tui

import (
	"strings"
	"sync"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// syncSnapshotWrites forces Queue to persist synchronously on the calling
// goroutine instead of the background loop. Production always leaves it
// false — disk I/O stays off the UI goroutine and quit waits via Flush.
// The test suite sets it true in TestMain so no background write can race
// TempDir cleanup; the runID gate and failure accounting apply identically
// in both modes, and async behavior has dedicated unit tests.
var syncSnapshotWrites = false

// continuationWriter persists exact-resume snapshots off the UI goroutine.
// Queue never blocks: it only records the latest snapshot per session (and
// drops writes from superseded runs, e.g. a cancelled run whose goroutine
// finishes after the next turn started). A background loop writes each
// session's latest snapshot in order; quit waits for it with Flush.
type continuationWriter struct {
	store *sessions.Store

	mu       sync.Mutex
	pending  map[string]pendingWrite
	lastRun  map[string]int64
	lastSeq  map[string]int
	failures map[string]int
	inFlight bool

	// onRepeatedFailure fires (at most once per failure streak) when the
	// same session fails to persist 3 times in a row. Set by the TUI to
	// surface a one-line warning; nil disables the notice.
	onRepeatedFailure func(sessionID string)

	wake chan struct{}
	once sync.Once
}

type pendingWrite struct {
	snap  sessions.ContinuationSnapshot
	runID int64
}

func newContinuationWriter(store *sessions.Store) *continuationWriter {
	return &continuationWriter{
		store:    store,
		pending:  map[string]pendingWrite{},
		lastRun:  map[string]int64{},
		lastSeq:  map[string]int{},
		failures: map[string]int{},
		wake:     make(chan struct{}, 1),
	}
}

// Queue records snap as the session's latest state. Writes from an older run
// than the last accepted one are discarded, so a cancelled run that drains
// late can never overwrite a newer turn. Never blocks and never touches
// disk: the background loop owns all I/O (except in the test-only
// synchronous mode, where the write lands inline on the calling goroutine).
func (w *continuationWriter) Queue(snap sessions.ContinuationSnapshot, runID int64) bool {
	if w == nil || w.store == nil {
		return false
	}
	if strings.TrimSpace(snap.SessionID) == "" || len(snap.Messages) == 0 {
		return false
	}
	if !syncSnapshotWrites {
		w.once.Do(func() { go w.loop() })
	}
	w.mu.Lock()
	if last, ok := w.lastRun[snap.SessionID]; ok {
		if runID < last {
			w.mu.Unlock()
			return false
		}
		if runID == last && snap.EventSeq < w.lastSeq[snap.SessionID] {
			w.mu.Unlock()
			return false
		}
	}
	w.lastRun[snap.SessionID] = runID
	w.lastSeq[snap.SessionID] = snap.EventSeq
	return w.persistLocked(snap, runID)
}

// QueueRewrite persists a snapshot rebuilt from the log after a /rewind or
// /compact. Unlike Queue it carries no run gate and always wins the pending
// slot: the rewrite is built from the current log, which no agent run can be
// racing (rewind and compact refuse to run while a turn is in flight).
// Snapshot writes stay serialized through the same background loop; quit
// waits for them with Flush like any other write. The head is aligned with
// what the log holds after the rewrite, so the next resume reads a
// consistent pair.
func (w *continuationWriter) QueueRewrite(snap sessions.ContinuationSnapshot) bool {
	if w == nil || w.store == nil {
		return false
	}
	if strings.TrimSpace(snap.SessionID) == "" || len(snap.Messages) == 0 {
		return false
	}
	if !syncSnapshotWrites {
		w.once.Do(func() { go w.loop() })
	}
	w.mu.Lock()
	w.lastSeq[snap.SessionID] = snap.EventSeq
	return w.persistLocked(snap, 0)
}

// persistLocked queues snap (or writes it inline in the test-only sync
// mode). Callers hold w.mu and have applied the gate/bookkeeping they
// want; this is the shared tail of Queue and QueueRewrite.
func (w *continuationWriter) persistLocked(snap sessions.ContinuationSnapshot, runID int64) bool {
	if syncSnapshotWrites {
		w.mu.Unlock()
		if err := w.store.WriteContinuation(snap); err != nil {
			w.mu.Lock()
			w.failures[snap.SessionID]++
			notify := w.failures[snap.SessionID] == 3 && w.onRepeatedFailure != nil
			cb := w.onRepeatedFailure
			w.mu.Unlock()
			if notify {
				cb(snap.SessionID)
			}
			return true
		}
		w.mu.Lock()
		w.failures[snap.SessionID] = 0
		w.mu.Unlock()
		return true
	}
	w.pending[snap.SessionID] = pendingWrite{snap: snap, runID: runID}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	return true
}

// Flush waits (up to timeout) for every queued snapshot to reach disk.
// Used on quit so the farewell resume line always names durable state.
func (w *continuationWriter) Flush(timeout time.Duration) bool {
	if w == nil {
		return true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		empty := len(w.pending) == 0 && !w.inFlight
		w.mu.Unlock()
		if empty {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func (w *continuationWriter) loop() {
	for range w.wake {
		for {
			w.mu.Lock()
			if len(w.pending) == 0 {
				w.mu.Unlock()
				break
			}
			batch := make([]pendingWrite, 0, len(w.pending))
			for _, pending := range w.pending {
				batch = append(batch, pending)
			}
			for sessionID := range w.pending {
				delete(w.pending, sessionID)
			}
			w.inFlight = true
			w.mu.Unlock()
			for _, pending := range batch {
				if err := w.store.WriteContinuation(pending.snap); err != nil {
					w.mu.Lock()
					w.failures[pending.snap.SessionID]++
					notify := w.failures[pending.snap.SessionID] == 3 && w.onRepeatedFailure != nil
					w.mu.Unlock()
					if notify {
						w.onRepeatedFailure(pending.snap.SessionID)
					}
					continue
				}
				w.mu.Lock()
				w.failures[pending.snap.SessionID] = 0
				w.mu.Unlock()
			}
			w.mu.Lock()
			w.inFlight = false
			w.mu.Unlock()
		}
	}
}

// buildContinuationSnapshot assembles the snapshot the next turn would send
// had qg never closed: the repaired wire plus the deferred tools loaded.
// Repair happens here, once, so every stored snapshot (and the in-memory
// continuation built alongside it) already pairs every tool call.
func buildContinuationSnapshot(sessionID string, modelID, provider, effort, goalKey string, eventSeq int, messages []qgruntime.Message, loadedTools []string) sessions.ContinuationSnapshot {
	repaired := sessions.RepairOrphanToolCalls(messages)
	msgs := append([]qgruntime.Message(nil), repaired...)
	loaded := append([]string(nil), loadedTools...)
	return sessions.ContinuationSnapshot{
		SessionID: sessionID, ModelID: modelID, Provider: provider,
		ReasoningEffort: effort, GoalKey: goalKey, EventSeq: eventSeq,
		Messages: msgs, LoadedTools: loaded,
	}
}
