package tui

import (
	"strings"
	"sync"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// continuationWriter persists exact-resume snapshots with a mutex so
// concurrent background writers (agent steps and the turn-final save) never
// interleave tmp files. Every write is atomic tmp+rename+fsync and 0600.
// Queue blocks the caller for one small write: agent steps run on the agent
// goroutine and the turn-final save runs once per completed turn, so the UI
// goroutine never stalls mid-stream. Writes are sequential per session in
// practice; cross-thread races resolve last-wins on an always-valid prefix,
// so a crash loses at most the in-progress turn.
type continuationWriter struct {
	store *sessions.Store

	mu sync.Mutex
}

func newContinuationWriter(store *sessions.Store) *continuationWriter {
	return &continuationWriter{store: store}
}

// Queue repairs and durably writes the snapshot.
func (w *continuationWriter) Queue(snap sessions.ContinuationSnapshot) {
	if w == nil || w.store == nil {
		return
	}
	if strings.TrimSpace(snap.SessionID) == "" || len(snap.Messages) == 0 {
		return
	}
	snap.Messages = sessions.RepairOrphanToolCalls(snap.Messages)
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.store.WriteContinuation(snap)
}

// buildContinuationSnapshot assembles the snapshot the next turn would send
// had qg never closed: the repaired wire plus the deferred tools loaded.
// turnBase is the index of the in-progress turn's user message in messages
// (len(previous history), or 1 on a history-less first turn).
func buildContinuationSnapshot(sessionID, modelID, provider, effort, goalKey string, eventSeq, turnBase int, messages []qgruntime.Message, loadedTools []string) sessions.ContinuationSnapshot {
	repaired := sessions.RepairOrphanToolCalls(messages)
	msgs := append([]qgruntime.Message(nil), repaired...)
	loaded := append([]string(nil), loadedTools...)
	return sessions.ContinuationSnapshot{
		SessionID: sessionID, ModelID: modelID, Provider: provider,
		ReasoningEffort: effort, GoalKey: goalKey, EventSeq: eventSeq, TurnBase: turnBase,
		Messages: msgs, LoadedTools: loaded,
	}
}
