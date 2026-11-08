package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// ContinuationFile holds the exact provider wire for a session: the real
// []qgruntime.Message the next turn would send had qg never closed, plus the
// deferred tools loaded at the end of the last step. It is the only source
// that can resume byte-identical: events.jsonl stores scrubbed previews and
// drops images, reasoning blocks, signatures and transient system context.
const ContinuationFile = "continuation.json"

const continuationVersion = 1

// ContinuationSnapshot is the last-write-wins exact-resume state for one
// session. Only the newest state is kept; there is no history.
//
// EventSeq is the event-log head the snapshot was saved against. RunID
// identifies the agent run that wrote it: a single per-session writer only
// accepts snapshots from the newest run, so a cancelled run that finishes
// late can never overwrite a newer turn.
type ContinuationSnapshot struct {
	Version         int                 `json:"v"`
	SessionID       string              `json:"sessionId"`
	ModelID         string              `json:"modelId"`
	Provider        string              `json:"provider,omitempty"`
	ReasoningEffort string              `json:"reasoningEffort,omitempty"`
	GoalKey         string              `json:"goalKey,omitempty"`
	EventSeq        int                 `json:"eventSeq"`
	RunID           int64               `json:"runId,omitempty"`
	Messages        []qgruntime.Message `json:"messages"`
	LoadedTools     []string            `json:"loadedTools,omitempty"`
	SavedAt         string              `json:"savedAt"`
}

func (store *Store) continuationPath(sessionID string) string {
	return filepath.Join(store.sessionPath(sessionID), ContinuationFile)
}

// WriteContinuation durably replaces the session's snapshot. Atomic
// tmp+rename+fsync like metadata/events: a crash never surfaces a torn file.
// 0600 so only the user reads it: the snapshot carries full tool arguments
// and outputs (already secret-scrubbed on the live wire) plus images. On
// Windows the mode bits are best-effort (only read-only is enforced); the
// session directory itself is 0700.
func (store *Store) WriteContinuation(snap ContinuationSnapshot) error {
	if !ValidSessionID(snap.SessionID) {
		return fmt.Errorf("invalid qg session id %q", snap.SessionID)
	}
	if len(snap.Messages) == 0 {
		return fmt.Errorf("qg continuation requires messages")
	}
	snap.Version = continuationVersion
	if strings.TrimSpace(snap.SavedAt) == "" {
		snap.SavedAt = store.timestamp()
	}
	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("encode qg continuation: %w", err)
	}
	data = append(data, '\n')
	path := store.continuationPath(snap.SessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create qg session dir: %w", err)
	}
	if err := store.writeFileAtomicSync(path, data, 0o600); err != nil {
		return fmt.Errorf("write qg continuation: %w", err)
	}
	return nil
}

// ReadContinuation loads the session's snapshot, or nil when none was saved
// (old sessions). A missing version, a cross-session file, a malformed wire
// or an unpaired tool call is an error so the caller falls back to event
// reconstruction with a specific warning instead of sending a bad request.
func (store *Store) ReadContinuation(sessionID string) (*ContinuationSnapshot, error) {
	if !ValidSessionID(sessionID) {
		return nil, fmt.Errorf("invalid qg session id %q", sessionID)
	}
	data, err := os.ReadFile(store.continuationPath(sessionID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var snap ContinuationSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("invalid qg continuation %s: %w", sessionID, err)
	}
	if err := ValidateContinuationSnapshot(sessionID, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// ValidateContinuationSnapshot rejects anything the provider would refuse:
// wrong version or session, empty history, a non-conversation opener (the
// wire must start with system or user — a post-/rewind snapshot rebuilt from
// the truncated log carries no frozen system), unknown roles, a tool result
// without an id or an answered call, and any orphaned call (snapshots are
// repaired before every write, so orphans mean corruption).
func ValidateContinuationSnapshot(sessionID string, snap *ContinuationSnapshot) error {
	if snap == nil {
		return fmt.Errorf("invalid qg continuation %s: empty snapshot", sessionID)
	}
	if snap.Version != continuationVersion {
		return fmt.Errorf("invalid qg continuation %s: unsupported version %d", sessionID, snap.Version)
	}
	if strings.TrimSpace(snap.SessionID) == "" || snap.SessionID != sessionID {
		return fmt.Errorf("invalid qg continuation %s: session mismatch %q", sessionID, snap.SessionID)
	}
	if len(snap.Messages) == 0 {
		return fmt.Errorf("invalid qg continuation %s: no messages", sessionID)
	}
	if first := snap.Messages[0].Role; first != qgruntime.MessageRoleSystem && first != qgruntime.MessageRoleUser {
		return fmt.Errorf("invalid qg continuation %s: first message is %q, want system or user", sessionID, first)
	}
	calls := map[string]bool{}
	for index, message := range snap.Messages {
		switch message.Role {
		case qgruntime.MessageRoleSystem, qgruntime.MessageRoleUser, qgruntime.MessageRoleAssistant, qgruntime.MessageRoleTool:
		default:
			return fmt.Errorf("invalid qg continuation %s: message %d has unknown role %q", sessionID, index, message.Role)
		}
		if message.Role == qgruntime.MessageRoleAssistant {
			for _, call := range message.ToolCalls {
				if strings.TrimSpace(call.ID) == "" {
					return fmt.Errorf("invalid qg continuation %s: message %d has a nameless tool call", sessionID, index)
				}
				calls[call.ID] = false
			}
		}
		if message.Role == qgruntime.MessageRoleTool {
			if strings.TrimSpace(message.ToolCallID) == "" {
				return fmt.Errorf("invalid qg continuation %s: message %d is a tool result without a call id", sessionID, index)
			}
			calls[message.ToolCallID] = true
		}
	}
	for id, answered := range calls {
		if !answered {
			return fmt.Errorf("invalid qg continuation %s: tool call %q has no result", sessionID, id)
		}
	}
	return nil
}

// InvalidateContinuation drops the snapshot. Used when a session is abandoned;
// /rewind and /compact rewrite from the new state instead.
func (store *Store) InvalidateContinuation(sessionID string) error {
	if !ValidSessionID(sessionID) {
		return fmt.Errorf("invalid qg session id %q", sessionID)
	}
	err := os.Remove(store.continuationPath(sessionID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// CheckContinuationFresh reports whether the snapshot still covers the event
// log head. Usage/error/permission/checkpoint-only drift keeps it fresh:
// those events never enter the provider wire (checkpoints are file-restore
// blobs for /rewind; a kill landing between the snapshot and the checkpoint
// write must still resume exact). Any conversation, compaction, rewind,
// fork, goal or spec event after snap.EventSeq makes it stale. Returns the
// stale delta for the caller to inspect.
func (store *Store) CheckContinuationFresh(sessionID string, snap *ContinuationSnapshot) (bool, []Event, error) {
	if snap == nil {
		return false, nil, nil
	}
	events, err := store.ReadEvents(sessionID)
	if err != nil {
		return false, nil, err
	}
	if len(events) == 0 {
		return true, nil, nil
	}
	last := events[len(events)-1].Sequence
	if last <= snap.EventSeq {
		// Log rewound to or before the snapshot: stale (rewind changes the
		// rehydrated history the snapshot was built on).
		if last < snap.EventSeq {
			return false, nil, nil
		}
		return true, nil, nil
	}
	var delta []Event
	for _, event := range events {
		if event.Sequence > snap.EventSeq {
			delta = append(delta, event)
		}
	}
	// Call ids the snapshot already carries (advertised calls and their
	// results). A kill can land between the step snapshot and the log
	// catching up with that same step's tool_call/tool_result: the wire
	// already covers those ids, so they don't invalidate the snapshot.
	// Anything else conversational (a new message, an unknown call, a
	// compaction, rewind, fork, goal or spec event) makes it stale.
	covered := map[string]bool{}
	for _, message := range snap.Messages {
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) != "" {
				covered[call.ID] = true
			}
		}
		if message.Role == qgruntime.MessageRoleTool && strings.TrimSpace(message.ToolCallID) != "" {
			covered[message.ToolCallID] = true
		}
	}
	for _, event := range delta {
		switch event.Type {
		case EventUsage, EventError, EventPermission, EventPermissionRequest, EventPermissionDecision, EventSessionCheckpoint:
			continue
		case EventToolCall, EventToolResult:
			if id := eventToolCallID(event.Payload); id != "" && covered[id] {
				continue
			}
			return false, delta, nil
		default:
			return false, delta, nil
		}
	}
	return true, delta, nil
}

// interruptedToolResultNotice is the synthetic tool result closing an orphaned
// tool_call when a turn was interrupted (Ctrl+C, SIGHUP, kill, crash), mirroring
// what Claude/Codex do so the provider still sees a valid tool_use/tool_result
// pair on the next turn.
const interruptedToolResultNotice = "interrupted: run halted before the tool result was recorded"

// RepairOrphanToolCalls inserts a synthetic error tool_result for every
// assistant tool_call with no later matching tool_result. Each synthetic goes
// immediately after its owner's existing results — before any following
// message — so strict providers (Anthropic) see every tool_use answered in
// order. Conversations that already pair every call are returned unchanged.
func RepairOrphanToolCalls(messages []qgruntime.Message) []qgruntime.Message {
	answered := map[string]bool{}
	for _, message := range messages {
		if message.Role == qgruntime.MessageRoleTool && message.ToolCallID != "" {
			answered[message.ToolCallID] = true
		}
	}
	needsRepair := false
	for _, message := range messages {
		if message.Role != qgruntime.MessageRoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.ID != "" && !answered[call.ID] {
				needsRepair = true
			}
		}
	}
	if !needsRepair {
		return messages
	}
	// Rebuild inserting one synthetic tool_result per unanswered call
	// directly after its owner's contiguous result run.
	return spliceOrphanResults(messages, answered)
}

// spliceOrphanResults rebuilds messages inserting one synthetic tool_result
// per unanswered call directly after its owner's contiguous result run.
func spliceOrphanResults(messages []qgruntime.Message, answered map[string]bool) []qgruntime.Message {
	orphanOf := func(call qgruntime.ToolCall) bool {
		return call.ID != "" && !answered[call.ID]
	}
	out := make([]qgruntime.Message, 0, len(messages)+1)
	index := 0
	for index < len(messages) {
		message := messages[index]
		out = append(out, message)
		index++
		if message.Role != qgruntime.MessageRoleAssistant || len(message.ToolCalls) == 0 {
			continue
		}
		// Consume this owner's contiguous tool results.
		for index < len(messages) && messages[index].Role == qgruntime.MessageRoleTool {
			out = append(out, messages[index])
			index++
		}
		for _, call := range message.ToolCalls {
			if !orphanOf(call) {
				continue
			}
			answered[call.ID] = true
			out = append(out, qgruntime.Message{
				Role:               qgruntime.MessageRoleTool,
				Content:            interruptedToolResultNotice,
				ToolCallID:         call.ID,
				ToolCallProviderID: call.ProviderCallID,
				IsError:            true,
			})
		}
	}
	return out
}

// FilterContinuationForModel adapts a snapshot's messages for a model change.
// Same model: deep copy unchanged. Different model: the frozen system prompt
// belongs to the old family, so it is dropped and the loop rebuilds the
// current one; provider-native call ids, reasoning signatures and foreign
// reasoning blocks must not leak across families. Never mutates the input.
func FilterContinuationForModel(messages []qgruntime.Message, fromModel, toModel string) []qgruntime.Message {
	if strings.TrimSpace(fromModel) == strings.TrimSpace(toModel) {
		return deepCopyMessages(messages)
	}
	out := deepCopyMessages(messages)
	if len(out) > 0 && out[0].Role == qgruntime.MessageRoleSystem {
		out = out[1:]
	}
	for i := range out {
		out[i].Reasoning = nil
		for j := range out[i].ToolCalls {
			out[i].ToolCalls[j].ProviderCallID = ""
			out[i].ToolCalls[j].Signature = ""
		}
		if out[i].Role == qgruntime.MessageRoleTool {
			out[i].ToolCallProviderID = ""
		}
	}
	return out
}

func deepCopyMessages(messages []qgruntime.Message) []qgruntime.Message {
	out := make([]qgruntime.Message, len(messages))
	for i, message := range messages {
		out[i] = message
		if message.ToolCalls != nil {
			out[i].ToolCalls = append([]qgruntime.ToolCall(nil), message.ToolCalls...)
		}
		if message.Reasoning != nil {
			out[i].Reasoning = append([]qgruntime.ReasoningBlock(nil), message.Reasoning...)
		}
		if message.ChangedFiles != nil {
			out[i].ChangedFiles = append([]string(nil), message.ChangedFiles...)
		}
		out[i].Images = qgruntime.CloneImageBlocks(message.Images)
	}
	return out
}

// ReconstructContinuationMessages rebuilds a structured provider history from
// durable events for sessions without an exact snapshot. Unlike the 48 KB
// textual summary (FormatExecPrompt), this keeps messages as messages: user
// and assistant texts, tool calls with their arguments, tool results with
// their outputs, and compaction summaries. Provider-native artifacts the
// events never stored (images, reasoning blocks, signatures, transient
// system) cannot come back. Only conversation events are converted;
// permission/usage/error/goal markers never rode the wire, so they stay out.
func ReconstructContinuationMessages(events []Event) []qgruntime.Message {
	filtered := make([]Event, 0, len(events))
	for _, event := range events {
		switch event.Type {
		case EventMessage, EventToolCall, EventToolResult, EventCompaction:
			filtered = append(filtered, event)
		}
	}
	return mergeToolCallAssistants(CompactionMessages(filtered))
}

// mergeToolCallAssistants fuses call/result pairs back into the live wire
// shape: CompactionMessages emits one single-call assistant per tool_call
// event, while the live turn sends one assistant with all of the turn's
// calls followed by their results. A run of assistant(calls)+tool(result)
// pairs is regrouped into one assistant plus the results in order; any other
// message (text, user, system) ends the group.
func mergeToolCallAssistants(messages []qgruntime.Message) []qgruntime.Message {
	out := make([]qgruntime.Message, 0, len(messages))
	index := 0
	for index < len(messages) {
		message := messages[index]
		if message.Role != qgruntime.MessageRoleAssistant || len(message.ToolCalls) == 0 || strings.TrimSpace(message.Content) != "" {
			out = append(out, message)
			index++
			continue
		}
		var calls []qgruntime.ToolCall
		var results []qgruntime.Message
		for index < len(messages) {
			current := messages[index]
			if current.Role == qgruntime.MessageRoleAssistant && len(current.ToolCalls) > 0 && strings.TrimSpace(current.Content) == "" && current.Reasoning == nil && len(current.Images) == 0 {
				calls = append(calls, current.ToolCalls...)
				index++
				continue
			}
			if current.Role == qgruntime.MessageRoleTool && strings.TrimSpace(current.ToolCallID) != "" {
				results = append(results, current)
				index++
				continue
			}
			break
		}
		merged := qgruntime.Message{Role: qgruntime.MessageRoleAssistant, ToolCalls: calls}
		out = append(out, merged)
		out = append(out, results...)
	}
	return out
}

// BackfillOrphanResults appends the missing tool_call event (when the kill
// landed before it was logged) and a synthetic interrupted tool_result for
// every snapshot tool call with no logged result, so log, screen and model
// agree after a mid-turn kill. It is idempotent: logged calls and results are
// skipped by call ID, so a second resume appends nothing. Returns the number
// of appended events.
func (store *Store) BackfillOrphanResults(sessionID string, snap *ContinuationSnapshot) (int, error) {
	if snap == nil || len(snap.Messages) == 0 {
		return 0, nil
	}
	events, err := store.ReadEvents(sessionID)
	if err != nil {
		return 0, err
	}
	loggedCalls := map[string]bool{}
	loggedResults := map[string]bool{}
	for _, event := range events {
		switch event.Type {
		case EventToolCall:
			if id := eventToolCallID(event.Payload); id != "" {
				loggedCalls[id] = true
			}
		case EventToolResult:
			if id := eventToolCallID(event.Payload); id != "" {
				loggedResults[id] = true
			}
		}
	}
	inputs := []AppendEventInput{}
	seen := map[string]bool{}
	for _, message := range snap.Messages {
		if message.Role != qgruntime.MessageRoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if strings.TrimSpace(call.ID) == "" || seen[call.ID] || loggedResults[call.ID] {
				continue
			}
			seen[call.ID] = true
			if !loggedCalls[call.ID] {
				inputs = append(inputs, AppendEventInput{Type: EventToolCall, Payload: map[string]any{
					"id": call.ID, "name": call.Name, "arguments": call.Arguments,
				}})
			}
			inputs = append(inputs, AppendEventInput{Type: EventToolResult, Payload: map[string]any{
				"toolCallId": call.ID,
				"name":       call.Name,
				"status":     "error",
				"output":     interruptedToolResultNotice,
			}})
		}
	}
	if len(inputs) == 0 {
		return 0, nil
	}
	appended, err := store.AppendEvents(sessionID, inputs)
	if err != nil {
		return 0, err
	}
	return len(appended), nil
}

func eventToolCallID(payload json.RawMessage) string {
	var decoded map[string]any
	_ = json.Unmarshal(payload, &decoded)
	for _, key := range []string{"toolCallId", "id"} {
		if value, _ := decoded[key].(string); strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
