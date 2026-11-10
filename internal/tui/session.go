package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
	"github.com/quebragalho-dev/quebragalho-cli/internal/modelregistry"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providers"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/usage"
)

const tuiSessionTitleLimit = 80

type pendingSessionEvent struct {
	Type    sessions.EventType
	Payload any
}

func (m model) ensureActiveSession(prompt string) (model, error) {
	if m.activeSession.SessionID != "" {
		return m, nil
	}

	title := strings.TrimSpace(m.pendingSessionTitle)
	manuallyNamed := title != ""
	if !manuallyNamed {
		title = tuiSessionTitle(prompt)
	}
	session, err := m.sessionStore.Create(sessions.CreateInput{
		Title:    title,
		Cwd:      m.cwd,
		ModelID:  m.modelName,
		Provider: m.providerName,
	})
	if err != nil {
		return m, err
	}
	m.activeSession = session
	m.pendingSessionTitle = ""
	m.sessionEvents = []sessions.Event{}
	// Fresh ids are uncontended, but the lease must still be held so a
	// second terminal cannot take over this session mid-conversation.
	if m.sessionStore != nil {
		if lock, err := m.sessionStore.TryAcquireSessionLock(session.SessionID); err == nil {
			m.releaseSessionLock()
			m.sessionLock = lock
		}
	}
	if manuallyNamed {
		if m.titledSessions == nil {
			m.titledSessions = map[string]bool{}
		}
		m.titledSessions[session.SessionID] = true
	}
	m = m.syncPeerIdentity()
	return m, nil
}

// releaseSessionLock drops the held presence lease, if any. Idempotent.
// Callers clear m.sessionLock themselves when the field must not dangle.
func (m *model) releaseSessionLock() {
	if m.sessionLock == nil {
		return
	}
	m.sessionLock.Release()
}

// startNewSession abandons the visible conversation and the agent's in-context
// history and begins a fresh session in place. The current session already lives
// on disk (its events are persisted as they happen), so it stays resumable via
// /resume; here we only clear the in-memory conversation and the per-session
// usage/compaction counters, then let the next prompt lazily create a new session
// through ensureActiveSession — the same seam a cold start uses. Model, provider,
// permission mode, and response style are intentionally preserved: /new starts a
// clean conversation, not a clean configuration.
func (m model) startNewSession() model {
	previousID := m.activeSession.SessionID
	m.releaseSessionLock()
	m.sessionLock = nil

	m.activeSession = sessions.Metadata{}
	// The new session has no shared history with the previous one.
	m.continuation = turnContinuation{}
	m.pendingSessionTitle = ""
	m.sessionEvents = nil

	// Reset the per-session usage + compaction display so the new session starts
	// from qg instead of inheriting the previous conversation's token/cost totals.
	if m.usageTracker != nil {
		m.usageTracker.Reset()
	}
	m.lastUsage = usage.Normalized{}
	m.lastUsageSeen = false
	m.streamUsage = qgruntime.Usage{}
	m.streamEstimateChars = 0
	m.streamEstimateStart = time.Time{}
	m.lastCompletedRate = ""
	m.lastUsageReceipt = transcriptRow{}
	m.meterLive = false
	m.unpricedRequests = 0
	m.unpricedTokens = 0
	m.compactRequests = 0
	m.compactFrame = 0
	m.lastCompactResult = nil
	m.lastCompactError = ""
	m.turnLatencySum = 0
	m.turnLatencyCount = 0
	m.turnTTFTSum = 0
	m.turnTTFTCount = 0

	// Staged input belongs to the previous conversation. Attachments and a queued
	// message are only consumed at prompt-submit, so without clearing them here the
	// fresh session's first prompt would silently inherit the old session's images,
	// documents, or queued text.
	m.pendingImages = nil
	m.pendingImageLabels = nil
	m.pendingImageThumbnails = nil
	m.pendingDocuments = nil
	m.queuedMessage = ""
	// The remembered /retry attachment snapshot belongs to the previous session
	// too — dropping it keeps a post-/new /retry from re-staging old images or
	// documents. lastPrompt is composer history (like inputHistory) and stays.
	m.lastImages = nil
	m.lastImageLabels = nil
	m.lastDocuments = nil

	note := "Nova sessão iniciada."
	if previousID != "" {
		note = "Nova sessão iniciada. Sessão anterior salva como " + previousID +
			" — retome quando quiser com /resume " + previousID + "."
	}
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionClear})
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: note})
	// Scrollback above can't be un-printed; a faint divider marks the boundary and
	// the flush frontier restarts for the fresh transcript (mirrors /clear, /resume).
	m.resetFlushFrontier("· nova sessão ·")
	// Loops belong to the previous session; stop them so they don't fire the old
	// conversation's prompt into the fresh one.
	if updated, cleared := m.clearLoopsForSessionSwitch(); cleared > 0 {
		m = updated
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: fmt.Sprintf("Stopped %d loop(s) tied to the previous session.", cleared)})
	}
	m = m.syncPeerIdentity()
	return m
}

func (m model) appendSessionEvent(eventType sessions.EventType, payload any) (model, error) {
	if m.activeSession.SessionID == "" {
		return m, nil
	}

	event, err := m.sessionStore.AppendEvent(m.activeSession.SessionID, sessions.AppendEventInput{
		Type:    eventType,
		Payload: payload,
	})
	if err != nil {
		return m, err
	}
	m.activeSession.UpdatedAt = event.CreatedAt
	m.activeSession.EventCount = event.Sequence
	m.activeSession.LastEventType = event.Type
	m.sessionEvents = append(m.sessionEvents, event)
	return m, nil
}

func (m model) appendSessionEvents(events []pendingSessionEvent) (model, []transcriptRow) {
	rows := []transcriptRow{}
	if m.activeSession.SessionID == "" || len(events) == 0 {
		return m, rows
	}
	inputs := make([]sessions.AppendEventInput, 0, len(events))
	for _, event := range events {
		inputs = append(inputs, sessions.AppendEventInput{Type: event.Type, Payload: event.Payload})
	}
	appended, err := m.sessionStore.AppendEvents(m.activeSession.SessionID, inputs)
	if err != nil {
		rows = append(rows, transcriptRow{kind: rowError, text: "erro ao gravar a sessão: " + err.Error()})
		return m, rows
	}
	if len(appended) > 0 {
		last := appended[len(appended)-1]
		m.activeSession.UpdatedAt = last.CreatedAt
		m.activeSession.EventCount = last.Sequence
		m.activeSession.LastEventType = last.Type
		m.sessionEvents = append(m.sessionEvents, appended...)
	}
	return m, rows
}

// appendSessionEventsTo persists events into a specific (non-active) session —
// the late flush of a run cancelled before a /resume switched sessions. The
// active session's in-memory metadata is deliberately untouched.
func (m model) appendSessionEventsTo(sessionID string, events []pendingSessionEvent) []transcriptRow {
	rows := []transcriptRow{}
	if m.sessionStore == nil || sessionID == "" {
		return rows
	}
	inputs := make([]sessions.AppendEventInput, 0, len(events))
	for _, event := range events {
		inputs = append(inputs, sessions.AppendEventInput{Type: event.Type, Payload: event.Payload})
	}
	if _, err := m.sessionStore.AppendEvents(sessionID, inputs); err != nil {
		rows = append(rows, transcriptRow{kind: rowError, text: "erro ao gravar a sessão: " + err.Error()})
	}
	return rows
}

// tailSessionEvents returns the run's session events past what write-through
// already persisted, so the end-of-turn flush never writes twice. The flushed
// prefix aligns by count: queueLiveEvents appends and writes each batch in
// order, and stops writing after the first error.
func tailSessionEvents(msg agentResponseMsg) []pendingSessionEvent {
	events := msg.sessionEvents
	flushed := len(msg.flushedEvents)
	if flushed < 0 {
		return events
	}
	if flushed > len(events) {
		return nil
	}
	return events[flushed:]
}

// foldFlushedSessionMetadata advances the active session's log-head metadata
// from write-through events: they reached the store during the run, bypassing
// appendSessionEvents, so without this the in-memory head (and the next
// snapshot's EventSeq) would lag the log and every resume would reconstruct.
func (m *model) foldFlushedSessionMetadata(flushed []sessions.Event) {
	if len(flushed) == 0 {
		return
	}
	last := flushed[len(flushed)-1]
	// Monotonic: a late flush from a cancelled run must never move a newer
	// head backwards.
	if last.Sequence < m.activeSession.EventCount {
		return
	}
	m.activeSession.UpdatedAt = last.CreatedAt
	m.activeSession.EventCount = last.Sequence
	m.activeSession.LastEventType = last.Type
}

// flushableSessionEvents selects the events worth persisting from a run that was
// cancelled mid-flight. The cancel path already records a single "Run cancelled."
// error, so the goroutine's trailing EventError (the ctx-cancellation error) is
// dropped to avoid a duplicate; everything else it accumulated before the cancel
// — tool calls/results, permission events, usage, and the EventSessionCheckpoint
// blobs that /rewind depends on — is kept.
func flushableSessionEvents(events []pendingSessionEvent) []pendingSessionEvent {
	flushable := make([]pendingSessionEvent, 0, len(events))
	for _, event := range events {
		if event.Type == sessions.EventError {
			continue
		}
		flushable = append(flushable, event)
	}
	return flushable
}

func tuiSessionTitle(prompt string) string {
	// cutRunes keeps the cut on a rune boundary — a bare byte slice could split
	// a multi-byte rune and persist invalid UTF-8 into the session metadata.
	title := cutRunes(strings.Join(strings.Fields(prompt), " "), tuiSessionTitleLimit)
	if title == "" {
		return "Sessão TUI do QG"
	}
	return title
}

func (m model) handleResumeCommand(args string) (model, string) {
	args = strings.TrimSpace(args)
	if args == "" {
		return m, m.resumeText()
	}
	if m.sessionStore == nil {
		return m, "Sessões\narmazenamento indisponível"
	}

	session, err := m.resolveResumeSession(args)
	if err != nil {
		return m, "Sessões\n" + err.Error()
	}
	// A session held open by another live qg process is refused instead of
	// resumed: two writers would interleave events and snapshots. A re-resume
	// of the already-held session keeps the lock. A dead holder's lock is
	// released by the kernel, so its stale PID never blocks a new owner.
	if m.sessionLock == nil || m.sessionLock.SessionID() != session.SessionID {
		lock, err := m.sessionStore.TryAcquireSessionLock(session.SessionID)
		if err != nil {
			var locked *sessions.SessionLockedError
			if errors.As(err, &locked) {
				return m, "Sessões\n" + locked.Error()
			}
			return m, "Sessões\nerro: " + err.Error()
		}
		m.releaseSessionLock()
		m.sessionLock = lock
	}
	events, err := m.resumeEvents(session.SessionID)
	if err != nil {
		return m, "Sessões\nerro: " + err.Error()
	}

	rawUsageEvents, err := m.sessionStore.ReadEvents(session.SessionID)
	if err != nil {
		return m, "Sessões\nerro: " + err.Error()
	}

	// Capture the current session id before switching so loops are only torn down
	// on a real change — `/resume latest` or `/resume <currentID>` can resolve to
	// the already-active session, whose loops belong to it, not a "previous" one.
	previousID := m.activeSession.SessionID
	m.activeSession = *session
	// In-memory history always restarts from disk here; a snapshot below
	// replaces it when the log head still matches.
	m.continuation = turnContinuation{}
	m.resumedExact = false
	m.reconstructedResume = false
	m.pendingSessionTitle = ""
	m.sessionEvents = append([]sessions.Event{}, events...)
	m.lastUsage, m.lastUsageSeen, m.lastUsageReceipt = lastUsageFromSessionEvents(rawUsageEvents)
	// The reloaded receipt IS the resumed session's live context: the meter
	// follows it (and stays dead when the session has no usage), so no value
	// from the previous session can ever leak through.
	m.meterLive = m.lastUsageSeen
	m.streamUsage = qgruntime.Usage{}
	// The border follows the resumed receipt: known rate when the session
	// saved enough to recompute it, model-only otherwise.
	m.lastCompletedRate = strings.TrimSpace(m.lastUsageReceipt.meta["rate"])
	if m.providerName == "" {
		m.providerName = session.Provider
	}
	if m.modelName == "" {
		m.modelName = session.ModelID
	}
	resumeWarnings := []string{}
	// Drain any queued snapshot write first so the resume reads the latest
	// file, not a stale one still sitting in the writer.
	if m.snapshotWriter != nil {
		m.snapshotWriter.Flush(2 * time.Second)
	}
	snap, snapErr := m.sessionStore.ReadContinuation(session.SessionID)
	switch {
	case snapErr != nil:
		// Unreadable snapshot file: structured fallback with its own warning.
		m = m.setReconstructedContinuation(events, "Histórico salvo inválido; retomada com o histórico salvo.")
	case snap == nil:
		// No snapshot (old session): structured history instead of the
		// textual summary — the next turn still sends messages as messages.
		fork := session.SessionKind == sessions.SessionKindFork
		if fork {
			m = m.setReconstructedContinuation(events, "Sessão bifurcada: retomada com o histórico salvo; imagens e raciocínio anteriores não voltam.")
		} else {
			m = m.setReconstructedContinuation(events, "Sessão antiga: retomada com o histórico salvo; imagens e raciocínio anteriores não voltam.")
		}
	default:
		fresh, _, _ := m.sessionStore.CheckContinuationFresh(session.SessionID, snap)
		if fresh && snap.GoalKey == goalKeyForSession(*session) && len(snap.Messages) > 0 {
			m = m.loadExactSnapshot(session, snap, &resumeWarnings)
		} else {
			m = m.setReconstructedContinuation(events, "A conversa mudou desde o último salvamento; retomada com o histórico salvo.")
		}
	}
	if m.resumedExact {
		// Crash-proof the resumed turn: a kill mid-turn never logged the
		// pending tool results, and a cancelled turn's late flush may have
		// landed after the snapshot. Append synthetic interrupted results
		// for any logged call missing one, so log, screen and model agree.
		if recovered, err := m.sessionStore.BackfillOrphanResults(session.SessionID, snapForBackfill(m.continuation)); err == nil && recovered > 0 {
			if refreshed, err := m.sessionStore.Get(session.SessionID); err == nil && refreshed != nil {
				m.activeSession = *refreshed
			}
			if reloaded, err := m.resumeEvents(session.SessionID); err == nil {
				events = reloaded
				m.sessionEvents = append([]sessions.Event{}, events...)
			}
			// Keep the snapshot head aligned with the log so the next resume
			// does not reconstruct over these appended events.
			if file, err := m.sessionStore.ReadContinuation(session.SessionID); err == nil && file != nil {
				file.EventSeq = m.activeSession.EventCount
				_ = m.sessionStore.WriteContinuation(*file)
			}
			resumeWarnings = append(resumeWarnings, formatRecoveredTurnNotice(recovered))
		}
	}
	loopsCleared := 0
	if session.SessionID != previousID {
		m, loopsCleared = m.clearLoopsForSessionSwitch()
	}

	rows := initialTranscript()
	rows = appendRow(rows, rowSystem, m.formatResumeSummary(m.activeSession, len(events)))
	if m.reconstructedResume {
		rows = appendRow(rows, rowSystem, m.reconstructNotice)
	}
	for _, warning := range resumeWarnings {
		rows = appendRow(rows, rowSystem, warning)
	}
	if loopsCleared > 0 {
		rows = appendRow(rows, rowSystem, fmt.Sprintf("Stopped %d loop(s) tied to the previous session.", loopsCleared))
	}
	rows = appendTranscriptRowsDedup(rows, transcriptRowsFromSessionEvents(events))
	m.transcript = rows
	// Every rehydrated row is settled by construction, so resetting the flush
	// frontier sends the whole resumed history to native scrollback in one
	// batch — scrollable, selectable, and O(1) for every later frame.
	m.resetFlushFrontier("· retomada ·")
	m = m.syncPeerIdentity()
	return m, ""
}

// snapForBackfill converts loaded in-memory history to the snapshot shape the
// orphan backfill expects.
func snapForBackfill(c turnContinuation) *sessions.ContinuationSnapshot {
	return &sessions.ContinuationSnapshot{
		SessionID: c.sessionID,
		ModelID:   c.modelID,
		Messages:  c.messages,
	}
}

// loadExactSnapshot restores model, effort and the real wire from a fresh
// snapshot. The model switch goes through the same core path as /model, so
// the provider is really rebuilt; an unknown model keeps the current one
// with a warning. The effort is restored even when the model is unchanged.
func (m model) loadExactSnapshot(session *sessions.Metadata, snap *sessions.ContinuationSnapshot, resumeWarnings *[]string) model {
	m.continuation = turnContinuation{}
	m.resumedExact = false
	effectiveModel := m.modelName
	if target := strings.TrimSpace(snap.ModelID); target != "" && !strings.EqualFold(target, m.modelName) {
		restored, warn, ok := m.restoreSnapshotModel(target)
		if !ok {
			*resumeWarnings = append(*resumeWarnings, warn)
		} else {
			m = restored
			effectiveModel = m.modelName
		}
	}
	if strings.TrimSpace(snap.ReasoningEffort) != "" {
		m.reasoningEffort = modelregistry.ReasoningEffort(snap.ReasoningEffort)
		// The stored effort may predate the model or come from another
		// family: validate it like a manual /model switch instead of
		// sending an unsupported level to the provider.
		if registry, err := modelregistry.DefaultRegistry(); err == nil {
			if target, ok := m.resolveModelSwitchTarget(registry, effectiveModel); ok {
				var dropped bool
				m, dropped = m.reconcileEffortForModelSwitch(target.reasoningEfforts, target.entry != nil)
				if dropped {
					*resumeWarnings = append(*resumeWarnings, "nível de raciocínio "+strings.TrimSpace(snap.ReasoningEffort)+" indisponível no modelo "+effectiveModel+", redefinido para automático")
				}
			}
		}
	}
	// If the restored model’s window is already nearly full, say so now:
	// the next turn still runs, but the operator knows a /compact is due.
	if window := modelregistry.AgentContextWindow(m.modelContextWindow(effectiveModel)); window > 0 && m.lastUsage.TotalTokens > 0 && m.lastUsage.TotalTokens*10 >= window*7 {
		*resumeWarnings = append(*resumeWarnings, fmt.Sprintf("contexto atual (%d tokens) ocupa a maior parte da janela do modelo %s; rode /compact para liberar espaço", m.lastUsage.TotalTokens, effectiveModel))
	}
	filtered := sessions.FilterContinuationForModel(sessions.RepairOrphanToolCalls(snap.Messages), snap.ModelID, effectiveModel)
	m.continuation = turnContinuation{
		sessionID:   session.SessionID,
		modelID:     effectiveModel,
		goalKey:     goalKeyForSession(*session),
		messages:    filtered,
		loadedTools: append([]string(nil), snap.LoadedTools...),
		runID:       snap.RunID,
	}
	m.resumedExact = true
	return m
}

// restoreSnapshotModel switches to the snapshot's model through the same
// core path as /model (rebuilding provider and profile), then reconciles the
// effort like a manual switch. Returns the updated model, or a pt-BR reason
// when the model cannot be restored.
func (m model) restoreSnapshotModel(targetModelID string) (model, string, bool) {
	registry, err := modelregistry.DefaultRegistry()
	if err != nil {
		return m, "modelo " + targetModelID + " indisponível (" + err.Error() + "), mantido " + displayValue(m.modelName, "atual"), false
	}
	target, ok := m.resolveModelSwitchTarget(registry, targetModelID)
	if !ok {
		return m, "modelo QG desconhecido " + strconv.Quote(targetModelID) + ", mantido " + displayValue(m.modelName, "atual"), false
	}
	if !config.HasProviderProfile(m.providerProfile) {
		return m, "nenhum perfil de provedor disponível para trocar de modelo, mantido " + displayValue(m.modelName, "atual"), false
	}
	if m.newProvider == nil {
		return m, "provedor não pode ser reconstruído nesta sessão, mantido " + displayValue(m.modelName, "atual"), false
	}
	nextProfile := m.providerProfile
	if provider, ok := m.activeProviderDescriptor(); ok {
		nextProfile = m.normalizeProfileForProvider(provider)
	}
	nextProfile.Model = target.modelID
	nextProfile = m.profileWithCredential(nextProfile)
	metadata, err := providers.ResolveRuntimeMetadata(nextProfile, providers.Options{})
	if err != nil {
		return m, "modelo " + target.modelID + " indisponível (" + err.Error() + "), mantido " + displayValue(m.modelName, "atual"), false
	}
	nextProvider, err := m.newProvider(nextProfile)
	if err != nil {
		return m, "modelo " + target.modelID + " indisponível (" + err.Error() + "), mantido " + displayValue(m.modelName, "atual"), false
	}
	m, err = m.applySwitchedProvider(nextProfile, metadata, nextProvider, target.modelID)
	if err != nil {
		return m, "modelo " + target.modelID + " indisponível (" + err.Error() + "), mantido " + displayValue(m.modelName, "atual"), false
	}
	m.activeSession.ModelID = target.modelID
	m.activeSession.Provider = m.providerName
	var dropped bool
	m, dropped = m.reconcileEffortForModelSwitch(target.reasoningEfforts, target.entry != nil)
	_ = dropped
	return m, "", true
}

// rewriteSnapshotFromEvents rebuilds the exact-resume snapshot from the
// current event log and persists it synchronously, so quitting right after a
// /rewind or /compact still resumes exact on the next launch. The rebuilt
// wire carries no frozen system (the next turn's submit rebuilds it from the
// current model, per I4); when the log holds no conversation the snapshot is
// dropped instead — there is nothing exact to resume.
func (m model) rewriteSnapshotFromEvents() model {
	if m.sessionStore == nil || m.activeSession.SessionID == "" {
		return m
	}
	msgs := sessions.RepairOrphanToolCalls(sessions.ReconstructContinuationMessages(m.sessionEvents))
	if len(msgs) == 0 {
		m.continuation = turnContinuation{}
		_ = m.sessionStore.InvalidateContinuation(m.activeSession.SessionID)
		return m
	}
	// The rebuild does not know which deferred tools the model had loaded —
	// only the in-memory (or on-disk) state does. Carry them over, or a
	// tool the model loaded via tool_search silently disappears.
	loaded := append([]string(nil), m.continuation.loadedTools...)
	if len(loaded) == 0 {
		if snap, err := m.sessionStore.ReadContinuation(m.activeSession.SessionID); err == nil && snap != nil && len(snap.LoadedTools) > 0 {
			loaded = append([]string(nil), snap.LoadedTools...)
		}
	}
	m.continuation = turnContinuation{
		sessionID:   m.activeSession.SessionID,
		modelID:     m.modelName,
		goalKey:     goalKeyForSession(m.activeSession),
		messages:    msgs,
		loadedTools: loaded,
	}
	_ = m.sessionStore.WriteContinuation(sessions.ContinuationSnapshot{
		SessionID: m.activeSession.SessionID, ModelID: m.modelName,
		Provider: m.providerName, ReasoningEffort: string(m.reasoningEffort),
		GoalKey: goalKeyForSession(m.activeSession), EventSeq: m.activeSession.EventCount,
		Messages: msgs, LoadedTools: loaded,
	})
	return m
}

// setReconstructedContinuation rebuilds a structured history from events for
// sessions without a usable snapshot. Unlike the textual summary, messages
// stay messages — the next turn replays them instead of a 48 KB digest.
func (m model) setReconstructedContinuation(events []sessions.Event, notice string) model {
	m.continuation = turnContinuation{}
	m.resumedExact = false
	msgs := sessions.RepairOrphanToolCalls(sessions.ReconstructContinuationMessages(events))
	if len(msgs) > 0 {
		m.continuation = turnContinuation{
			sessionID: m.activeSession.SessionID,
			modelID:   m.modelName,
			goalKey:   goalKeyForSession(m.activeSession),
			messages:  msgs,
		}
	}
	m.reconstructedResume = true
	m.reconstructNotice = notice
	return m
}

func formatRecoveredTurnNotice(recovered int) string {
	if recovered == 1 {
		return "Turno interrompido recuperado: 1 passo restaurado."
	}
	return fmt.Sprintf("Turno interrompido recuperado: %d passos restaurados.", recovered)
}

func (m model) sessionPrompt(prompt string) string {
	if m.activeSession.SessionID == "" || len(m.sessionEvents) == 0 {
		return prompt
	}
	formatted := sessions.FormatExecPrompt(prompt, sessions.PreparedExec{
		Mode:          sessions.ModeResume,
		Session:       m.activeSession,
		ContextEvents: append([]sessions.Event{}, m.sessionEvents...),
	})
	if m.activeSession.SessionKind == sessions.SessionKindSide {
		return btwContextBoundary + "\n\n" + formatted
	}
	return formatted
}

func (m model) resolveResumeSession(args string) (*sessions.Metadata, error) {
	if strings.EqualFold(args, "latest") {
		// Latest *resumable* conversation IN THIS WORKSPACE, so "latest" never lands
		// on a child, a spec sub-run, or a session from another project (matching the
		// workspace-scoped picker). An explicit `/resume <id>` below still resolves
		// any session regardless of workspace.
		latest, err := m.latestResumableInWorkspace()
		if err != nil {
			return nil, err
		}
		if latest == nil {
			return nil, errors.New("nenhuma sessão do qg disponível para retomar")
		}
		return latest, nil
	}

	session, err := m.sessionStore.Get(args)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("sessão do qg não encontrada: %s", args)
	}
	if !sessions.IsResumableKind(session.SessionKind) {
		return nil, fmt.Errorf("sessão do qg não pode ser retomada: %s", args)
	}
	return session, nil
}

// resumeEvents reads a session's events for resume, preferring the rehydrated
// (compaction-aware) view so a resumed session honors a prior /compact — matching
// the CLI's `qg exec --resume` (readExecContextEvents) and the in-TUI /compact
// reload. Falls back to the raw log if rehydration fails.
func (m model) resumeEvents(sessionID string) ([]sessions.Event, error) {
	events, err := m.sessionStore.ReadRehydratedEvents(sessionID)
	if err == nil {
		return events, nil
	}
	raw, rawErr := m.sessionStore.ReadEvents(sessionID)
	if rawErr != nil {
		// Surface the raw-read failure (the actual fallback error), not the earlier
		// rehydration error, so the caller sees why the fallback itself failed.
		return nil, rawErr
	}
	return raw, nil
}

// formatResumeSummary reports what the resumed conversation will actually continue
// with (the active model/provider), noting the session's recorded model/provider
// when it differs — resume keeps the current model rather than switching.
func (m model) formatResumeSummary(session sessions.Metadata, eventCount int) string {
	modelLine := "modelo: " + displayValue(m.modelName, "nenhum")
	if recorded := strings.TrimSpace(session.ModelID); recorded != "" && !strings.EqualFold(recorded, m.modelName) {
		modelLine += "  (registrado: " + recorded + ")"
	}
	providerLine := "provedor: " + displayValue(m.providerName, "nenhum")
	if recorded := strings.TrimSpace(session.Provider); recorded != "" && !strings.EqualFold(recorded, m.providerName) {
		providerLine += "  (registrado: " + recorded + ")"
	}
	lines := []string{
		"id: " + session.SessionID,
		"título: " + displayValue(session.Title, "sem título"),
		modelLine,
		providerLine,
		fmt.Sprintf("eventos: %d", eventCount),
	}
	if session.Goal != nil {
		goalLine := "goal: " + string(session.Goal.Status) + " — " + session.Goal.Objective
		if session.Goal.Status == sessions.GoalStatusActive {
			goalLine += " (run /goal resume to continue)"
		}
		lines = append(lines, goalLine)
	}
	return renderCommandOutput(commandOutput{
		Title:  "Sessão do QG retomada",
		Status: commandStatusOK,
		Sections: []commandSection{{
			Title: "Sessão",
			Lines: lines,
		}},
	})
}

func (m model) newSessionPicker() *commandPicker {
	if m.sessionStore == nil {
		return nil
	}
	metas, err := m.sessionStore.ListResumable()
	if err != nil || len(metas) == 0 {
		return nil
	}
	now := m.now()
	items := make([]pickerItem, 0, len(metas))
	for _, meta := range metas {
		// Workspace-scoped: hide sessions from other project directories so /resume
		// lists this workspace's history, not every project's. Checked BEFORE the
		// per-session event read below, so a large global history doesn't pay 50
		// full file reads to build one workspace's list. Sessions with no recorded
		// Cwd (older runs) stay visible rather than vanishing.
		if !sessionMatchesWorkspace(meta.Cwd, m.cwd) {
			continue
		}
		// A qg-event session has nothing to resume — skip it without a file read.
		if meta.EventCount == 0 {
			continue
		}
		// Skip empty/failed runs (no assistant output, no tool calls) — e.g. the
		// same prompt retried while the model wasn't responding. They have nothing
		// to resume and otherwise flood the list with identical rows. Still on disk.
		if !m.sessionHasResumableContent(meta.SessionID) {
			continue
		}
		// Lead with a fixed-width timestamp so titles form one scannable column.
		// The raw id remains the selection/search value but stays out of the row:
		// rendering it consumed half the picker and truncated the useful title.
		// Session metadata is persisted, user-controlled text — a title from
		// --session-title or /rename can legally contain newlines, which would
		// break the picker's fixed row geometry.
		label := displayValue(sanitizeCardField(meta.Title), "sem título")
		if when := relativeAge(meta.UpdatedAt, now); when != "" {
			label = sessionPickerLabel(when, label)
		}
		items = append(items, pickerItem{
			Label:  label,
			Value:  meta.SessionID,
			Detail: m.sessionPickerDetail(meta),
		})
	}
	if len(items) == 0 {
		return nil // every resumable session was an empty/failed run
	}
	return &commandPicker{
		kind:     pickerSession,
		title:    "Retomar uma sessão",
		items:    items,
		allItems: append([]pickerItem{}, items...),
		selected: 0,
	}
}

const sessionPickerTimeWidth = len("2006-01-02")

func sessionPickerLabel(when, title string) string {
	return fmt.Sprintf("%-*s  %s", sessionPickerTimeWidth, when, title)
}

// sessionPickerDetail composes the faint second line under a /resume row:
// the session's project directory (~/-contracted), the model it ran on, and a
// short status chip when the session is not a plain mainline run (forks,
// tagged sessions such as btw side-chats). Missing fields are simply omitted —
// the picker is workspace-scoped, so the project column is a confirmation,
// not a disambiguator.
func (m model) sessionPickerDetail(meta sessions.Metadata) string {
	parts := make([]string, 0, 3)
	if project := displayPath(m.cwd, meta.Cwd); project != "" {
		parts = append(parts, project)
	}
	if modelID := strings.TrimSpace(meta.ModelID); modelID != "" {
		parts = append(parts, modelID)
	}
	if status := sessionPickerStatus(meta); status != "" {
		parts = append(parts, status)
	}
	return sanitizeCardField(strings.Join(parts, " · "))
}

// sessionPickerStatus is the concise status chip for a /resume row: an
// explicit tag when the session carries one, otherwise a marker for forked
// sessions. Plain mainline sessions get none — their shape is the default.
func sessionPickerStatus(meta sessions.Metadata) string {
	if tag := strings.TrimSpace(meta.Tag); tag != "" {
		return tag
	}
	if meta.SessionKind == sessions.SessionKindFork {
		return "fork"
	}
	return ""
}

// sessionHasResumableContent reports whether a session has anything worth
// resuming: a tool call/result, or a non-user message with real content (not the
// no-output guardrail stop). Empty/failed runs return false and are hidden from
// the picker (they stay on disk). Errors fail open (the session is kept).
// latestResumableInWorkspace returns the most-recently-updated resumable session
// belonging to the current workspace, or nil when none exist. ListResumable is
// ordered latest-first, so the first qualifying match is the latest. It applies
// the SAME filters as newSessionPicker — workspace membership, non-empty
// metadata, and real resumable content — so `/resume latest` never lands on a
// qg-event or empty/failed run the picker would have hidden.
func (m model) latestResumableInWorkspace() (*sessions.Metadata, error) {
	metas, err := m.sessionStore.ListResumable()
	if err != nil {
		return nil, err
	}
	for i := range metas {
		if !sessionMatchesWorkspace(metas[i].Cwd, m.cwd) {
			continue
		}
		if metas[i].EventCount == 0 {
			continue
		}
		if !m.sessionHasResumableContent(metas[i].SessionID) {
			continue
		}
		return &metas[i], nil
	}
	return nil, nil
}

// sessionMatchesWorkspace reports whether a session recorded in sessionCwd
// belongs to the current workspaceCwd. A session with no recorded Cwd (older
// runs) or an unknown current workspace is kept visible rather than filtered out,
// so the scoping never hides history it can't confidently place elsewhere. On
// Windows the comparison is case-insensitive, since the filesystem is and the
// same workspace can be spelled with different casing (C:\Proj vs c:\proj).
func sessionMatchesWorkspace(sessionCwd, workspaceCwd string) bool {
	sessionCwd = strings.TrimSpace(sessionCwd)
	workspaceCwd = strings.TrimSpace(workspaceCwd)
	if sessionCwd == "" || workspaceCwd == "" {
		return true
	}
	a := filepath.Clean(sessionCwd)
	b := filepath.Clean(workspaceCwd)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (m model) sessionHasResumableContent(sessionID string) bool {
	events, err := m.sessionStore.ReadEvents(sessionID)
	if err != nil {
		return true
	}
	return eventsHaveResumableContent(events)
}

// eventsHaveResumableContent reports whether already-loaded events contain
// anything worth resuming: a tool call/result, or a non-user message with real
// content (not the no-output guardrail stop). It is the pure core of
// sessionHasResumableContent so callers that already hold the events (e.g. the
// session picker refresh) don't re-read them.
func eventsHaveResumableContent(events []sessions.Event) bool {
	for _, event := range events {
		switch event.Type {
		case sessions.EventToolCall, sessions.EventToolResult:
			return true
		case sessions.EventMessage:
			payload := sessionPayload(event)
			if strings.EqualFold(payloadString(payload, "role"), "user") {
				continue
			}
			content := strings.TrimSpace(payloadString(payload, "content"))
			if content != "" && !agent.IsNoProgressStop(content) {
				return true
			}
		}
	}
	return false
}

// openSessionPicker opens the /resume picker; ok is false when there is nothing to
// resume (the caller then falls back to the text list / "none" message).
func (m model) openSessionPicker() (model, bool) {
	picker := m.newSessionPicker()
	if picker == nil {
		return m, false
	}
	m.picker = picker
	return m, true
}

func transcriptRowsFromSessionEvents(events []sessions.Event) []transcriptRow {
	rows := []transcriptRow{}
	// Rehydrated rows all carry runID 0, so repeated provider tool-call ids
	// (e.g. Gemini's per-turn gemini_tool_N) get the same per-occurrence
	// disambiguation the live runner applies — without it, dedup would drop
	// every tool card after the first occurrence of an id.
	callSeq := map[string]int{}
	// Pre-pass: collect the tool-call ids of Task delegations that actually started
	// a specialist (each renders as a card below). Only those Task tool-call/result
	// rows are redundant and skipped; a Task that failed before a specialist started
	// has no card, so its rows are kept — otherwise the failed delegation vanishes
	// on resume (M10).
	specialistToolCalls := map[string]bool{}
	for _, event := range events {
		if event.Type == sessions.EventSpecialistStart {
			if id := payloadString(sessionPayload(event), "toolCallId"); id != "" {
				specialistToolCalls[id] = true
			}
		}
	}
	for _, event := range events {
		payload := sessionPayload(event)
		switch event.Type {
		case sessions.EventUsage:
			if label := payloadString(payload, "call_label"); label != "" {
				meta := map[string]string{"model": payloadString(payload, "model"), "amount": payloadString(payload, "cost_exact"), "currency": payloadString(payload, "cost_currency"), "tokens": payloadString(payload, "call_tokens")}
				// The saved payload already carries what the live rate needs
				// (output tokens + elapsed nanoseconds), so a resumed receipt
				// shows the same speed with no schema change.
				if rate := resumeUsageRate(payload); rate != "" {
					meta["rate"] = rate
				}
				rows = append(rows, transcriptRow{kind: rowSystem, tool: "model-usage", id: event.ID, text: label, meta: meta})
			}

		case sessions.EventMessage:
			role := strings.ToLower(payloadString(payload, "role"))
			if role == "user" && payloadString(payload, "origin") == "cross_session" {
				from := payloadString(payload, "from")
				content := payloadString(payload, "displayContent")
				if content == "" {
					content = payloadString(payload, "content")
				}
				rows = append(rows, peerTranscriptRow(from, content))
				continue
			}
			switch role {
			case "ask_user":
				rows = append(rows, askUserTranscriptRow(askUserRequestFromPayload(payload)))
				continue
			case "ask_user_answers":
				if text := askUserAnswersText(payload); text != "" {
					rows = append(rows, transcriptRow{kind: rowSystem, text: text})
				}
				continue
			}
			content := payloadString(payload, "content")
			if content == "" {
				continue
			}
			switch role {
			case "user":
				rows = append(rows, transcriptRow{kind: rowUser, text: content, attachments: attachmentSummaryFromPayload(payload)})
			case "assistant":
				// A persisted assistant message was a turn's final answer. Tool/timing
				// counters were not recorded; the completion line omits those segments.
				rows = append(rows, transcriptRow{kind: rowAssistant, text: content, final: true})
			default:
				rows = append(rows, transcriptRow{kind: rowSystem, text: content})
			}
		case sessions.EventToolCall:
			name := payloadString(payload, "name")
			if name == "" {
				name = "unknown"
			}
			// Extract the id exactly as the tool-result branch and the specialist
			// pre-pass do (toolCallId first, then id) so call and result rows key
			// callSeq/effectiveToolRowID on the same string and the specialist-skip
			// lookup matches — otherwise a payload that carries toolCallId (not id)
			// desyncs call→result dedup and the M10 skip (L20).
			id := firstNonEmptyString(payloadString(payload, "toolCallId"), payloadString(payload, "id"))
			if name == "Task" && specialistToolCalls[id] {
				// A specialist card renders this delegation; skip the redundant
				// "tool call: Task" row. A Task with no specialist (it failed before
				// one started) keeps its row so the failure stays visible (M10).
				continue
			}
			callSeq[id]++
			rows = append(rows, transcriptRow{
				kind:   rowToolCall,
				id:     effectiveToolRowID(id, callSeq[id]),
				text:   "tool call: " + name,
				tool:   name,
				detail: argHint(payloadString(payload, "arguments")),
				arg:    argHintSecondary(payloadString(payload, "arguments")),
			})
		case sessions.EventPermission, sessions.EventPermissionRequest, sessions.EventPermissionDecision:
			// Mirror the live path: a silently auto-approved call recorded its
			// audit event but rendered no row, so don't resurrect one on resume.
			event := permissionEventFromPayload(payload)
			if permissionEventIsNoteworthy(event) {
				rows = append(rows, permissionTranscriptRow(event))
			}
		case sessions.EventToolResult:
			name := payloadString(payload, "name")
			if name == "" {
				name = "unknown"
			}
			id := firstNonEmptyString(payloadString(payload, "toolCallId"), payloadString(payload, "id"))
			if name == "Task" && specialistToolCalls[id] {
				// The specialist card carries this Task's result; skip the redundant
				// "tool result: Task" row. A Task with no specialist keeps its result
				// row so a failed delegation's error stays visible (M10).
				continue
			}
			status := tools.Status(payloadString(payload, "status"))
			if status == "" {
				status = tools.StatusOK
			}
			output := payloadString(payload, "output")
			detail := payloadString(payload, "displayPreview")
			if detail == "" {
				detail = output
			}
			rows = append(rows, transcriptRow{
				kind:            rowToolResult,
				id:              effectiveToolRowID(id, callSeq[id]),
				text:            fmt.Sprintf("tool result: %s %s %s", name, status, truncateTUIOutput(output, tuiToolOutputLimit)),
				tool:            name,
				status:          status,
				detail:          detail,
				meta:            payloadStringMap(payload, "meta"),
				changedFiles:    payloadStringSlice(payload, "changedFiles"),
				changeSummaries: payloadExecutionChanges(payload, "changeSummaries"),
			})
		case sessions.EventError:
			if message := payloadString(payload, "message"); message != "" {
				rows = append(rows, transcriptRow{kind: rowError, text: message})
			}
		case sessions.EventCompaction:
			if summary := payloadString(payload, "summary"); summary != "" {
				rows = append(rows, transcriptRow{kind: rowSystem, text: summary})
			}
		case sessions.EventSessionFork:
			parentID := payloadString(payload, "parentSessionId")
			if parentID != "" {
				rows = append(rows, transcriptRow{kind: rowSystem, text: "sessão derivada de: " + parentID})
			}
		case sessions.EventSpecialistStart:
			info := specialistInfoFromPayload(payload)
			if info != nil {
				rows = append(rows, transcriptRow{kind: rowSpecialist, specialistInfo: info})
			}
		case sessions.EventSpecialistStop:
			info := specialistInfoFromPayload(payload)
			if info != nil {
				// Reconcile: update the existing Start row with the same
				// childSessionID instead of appending a duplicate. On resume
				// this prevents two cards per specialist (running + completed).
				found := false
				for i := range rows {
					if rows[i].kind == rowSpecialist && rows[i].specialistInfo != nil &&
						rows[i].specialistInfo.childSessionID == info.childSessionID {
						rows[i].specialistInfo = info
						found = true
						break
					}
				}
				if !found {
					rows = append(rows, transcriptRow{kind: rowSpecialist, specialistInfo: info})
				}
			}
		}
	}
	return rows
}

func sessionPayload(event sessions.Event) map[string]any {
	payload := map[string]any{}
	if len(event.Payload) == 0 {
		return payload
	}
	_ = json.Unmarshal(event.Payload, &payload)
	return payload
}

func permissionEventFromPayload(payload map[string]any) agent.PermissionEvent {
	name := payloadString(payload, "name")
	if name == "" {
		name = payloadString(payload, "toolName")
	}
	event := agent.PermissionEvent{
		ToolCallID:        firstNonEmptyString(payloadString(payload, "toolCallId"), payloadString(payload, "id")),
		ToolName:          name,
		Action:            agent.PermissionAction(payloadString(payload, "action")),
		DecisionAction:    agent.PermissionDecisionAction(payloadString(payload, "decisionAction")),
		Permission:        payloadString(payload, "permission"),
		PermissionGranted: payloadBool(payload, "permissionGranted"),
		PermissionMode:    agent.PermissionMode(payloadString(payload, "permissionMode")),
		Autonomy:          payloadString(payload, "autonomy"),
		SideEffect:        payloadString(payload, "sideEffect"),
		Reason:            payloadString(payload, "reason"),
		Scope:             payloadString(payload, "scope"),
		DecisionReason:    payloadString(payload, "decisionReason"),
		GrantMatched:      payloadBool(payload, "grantMatched"),
	}
	if risk, ok := payloadMap(payload, "risk"); ok {
		event.Risk = sandbox.Risk{
			Level:  sandbox.RiskLevel(payloadString(risk, "level")),
			Reason: payloadString(risk, "reason"),
		}
	}
	if block, ok := payloadMap(payload, "block"); ok {
		event.Block = &sandbox.Block{
			Code:        sandbox.BlockCode(payloadString(block, "code")),
			ToolName:    payloadString(block, "toolName"),
			Action:      sandbox.Action(payloadString(block, "action")),
			Risk:        event.Risk,
			Path:        payloadString(block, "path"),
			Reason:      payloadString(block, "reason"),
			Recoverable: payloadBool(block, "recoverable"),
		}
		if nestedRisk, ok := payloadMap(block, "risk"); ok {
			event.Block.Risk = sandbox.Risk{
				Level:  sandbox.RiskLevel(payloadString(nestedRisk, "level")),
				Reason: payloadString(nestedRisk, "reason"),
			}
		}
	}
	return event
}

// askUserRequestFromPayload rebuilds the request persisted by
// askUserSessionPayload, so ask_user exchanges survive /resume instead of
// silently vanishing from rehydrated history.
func askUserRequestFromPayload(payload map[string]any) agent.AskUserRequest {
	request := agent.AskUserRequest{
		ToolCallID: payloadString(payload, "toolCallId"),
		Header:     payloadString(payload, "header"),
	}
	raw, ok := payload["questions"].([]any)
	if !ok {
		return request
	}
	for _, entry := range raw {
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		question := agent.AskUserQuestion{
			Question:    payloadString(fields, "question"),
			MultiSelect: payloadBool(fields, "multiSelect"),
		}
		if options, ok := fields["options"].([]any); ok {
			for _, option := range options {
				if text, ok := option.(string); ok {
					question.Options = append(question.Options, text)
				}
			}
		}
		request.Questions = append(request.Questions, question)
	}
	return request
}

// askUserAnswersText renders persisted ask_user answers for rehydration.
func askUserAnswersText(payload map[string]any) string {
	raw, ok := payload["answers"].([]any)
	if !ok {
		return ""
	}
	lines := make([]string, 0, len(raw))
	for index, entry := range raw {
		text, _ := entry.(string)
		if strings.TrimSpace(text) == "" {
			text = "(skipped)"
		}
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, text))
	}
	if len(lines) == 0 {
		return ""
	}
	return "Answers\n" + strings.Join(lines, "\n")
}

func payloadString(payload map[string]any, key string) string {
	value := payload[key]
	switch typed := value.(type) {
	case string:
		return typed
	case float64, bool:
		return fmt.Sprint(typed)
	case nil:
		return ""
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

// payloadInt reads a persisted integer (JSON round-trips numbers as
// float64). 0 when absent or unparseable.
func payloadInt(payload map[string]any, key string) int {
	return int(payloadInt64(payload, key))
}

func payloadInt64(payload map[string]any, key string) int64 {
	switch typed := payload[key].(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

// resumeUsageRate recomputes a saved call's speed from its payload, the same
// usageRateText math as the live receipt. "" when the session predates the
// elapsed-nanoseconds field.
func resumeUsageRate(payload map[string]any) string {
	return usageRateText(payloadInt(payload, "completionTokens"), time.Duration(payloadInt64(payload, "call_elapsed_ns")))
}

// payloadStringSlice reads a []string persisted into a session payload (JSON
// round-trips it as []any), skipping non-string entries. Nil when absent.
func payloadStringSlice(payload map[string]any, key string) []string {
	switch typed := payload[key].(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, v := range typed {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func payloadStringMap(payload map[string]any, key string) map[string]string {
	value, ok := payloadMap(payload, key)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(value))
	for name, raw := range value {
		if text, ok := raw.(string); ok {
			out[name] = text
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func payloadExecutionChanges(payload map[string]any, key string) []execution.Change {
	value, ok := payload[key]
	if !ok {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var changes []execution.Change
	if err := json.Unmarshal(data, &changes); err != nil {
		return nil
	}
	return changes
}

func payloadBool(payload map[string]any, key string) bool {
	value := payload[key]
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func attachmentSummaryFromPayload(payload map[string]any) transcriptAttachmentSummary {
	attachments, ok := payloadMap(payload, "attachments")
	if !ok {
		return transcriptAttachmentSummary{}
	}
	return transcriptAttachmentSummary{
		images:    payloadNonNegativeInt(attachments, "images"),
		documents: payloadNonNegativeInt(attachments, "documents"),
	}
}

func payloadNonNegativeInt(payload map[string]any, key string) int {
	value, ok := payload[key].(float64)
	if !ok || value <= 0 || value != math.Trunc(value) {
		return 0
	}
	if value > persistedAttachmentCountLimit {
		return persistedAttachmentCountLimit
	}
	return int(value)
}

func payloadMap(payload map[string]any, key string) (map[string]any, bool) {
	value, ok := payload[key].(map[string]any)
	return value, ok
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// specialistInfoFromPayload builds a specialistInfo from a specialist_start or
// specialist_stop session event payload. Returns nil if the payload lacks a
// childSessionId (the minimum required field).
func specialistInfoFromPayload(payload map[string]any) *specialistInfo {
	childSessionID := payloadString(payload, "childSessionId")
	if childSessionID == "" {
		return nil
	}
	info := &specialistInfo{
		name:           payloadString(payload, "specialist"),
		description:    payloadString(payload, "description"),
		childSessionID: childSessionID,
	}
	statusStr := payloadString(payload, "status")
	switch statusStr {
	case "running":
		info.status = specialistRunning
	case "success":
		info.status = specialistCompleted
	case "completed":
		info.status = specialistCompleted
	default:
		info.status = parseSpecialistStatus(statusStr)
	}
	if errMsg := payloadString(payload, "error"); errMsg != "" {
		info.errorMsg = errMsg
	}
	return info
}
