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
// EventSeq is the event-log head the snapshot was saved against: turn events
// flush only at end of turn, so step snapshots taken mid-turn carry the
// start-of-turn head. TurnBase is the index in Messages of the in-progress
// turn's user message (len(previous history)); messages after TurnBase whose
// events never reached the log (kill mid-turn) are backfilled on resume so the
// screen shows what the model receives.
type ContinuationSnapshot struct {
	Version         int                 `json:"v"`
	SessionID       string              `json:"sessionId"`
	ModelID         string              `json:"modelId"`
	Provider        string              `json:"provider,omitempty"`
	ReasoningEffort string              `json:"reasoningEffort,omitempty"`
	GoalKey         string              `json:"goalKey,omitempty"`
	EventSeq        int                 `json:"eventSeq"`
	TurnBase        int                 `json:"turnBase,omitempty"`
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
// (old sessions). A corrupt file is an error so the caller falls back to
// event reconstruction with a warning instead of silently resuming partial.
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
	if snap.Version != continuationVersion || snap.SessionID != sessionID || len(snap.Messages) == 0 {
		return nil, fmt.Errorf("invalid qg continuation %s: version/session/messages mismatch", sessionID)
	}
	return &snap, nil
}

// InvalidateContinuation drops the snapshot so the next resume reconstructs
// from events. Used after /rewind, /compact, /clear and /new semantics where
// the frozen wire no longer matches the rehydrated history.
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
// log head. Usage/error/permission-only drift keeps it fresh (those events
// never enter the provider wire); any conversation, compaction, rewind, fork,
// goal or spec event after snap.EventSeq makes it stale. Returns the stale
// delta for the caller to inspect.
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
	for _, event := range delta {
		switch event.Type {
		case EventUsage, EventError, EventPermission, EventPermissionRequest, EventPermissionDecision:
			continue
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
// Same model: deep copy unchanged. Different model: clears provider-native
// call ids and reasoning signatures bound to the previous call (Gemini
// thoughtSignature and per-provider native ids must not leak across
// families); reasoning blocks are kept because adapters ignore foreign
// blocks, so the conversation stays real instead of falling back to a text
// summary. Never mutates the input.
func FilterContinuationForModel(messages []qgruntime.Message, fromModel, toModel string) []qgruntime.Message {
	if strings.TrimSpace(fromModel) == strings.TrimSpace(toModel) {
		return deepCopyMessages(messages)
	}
	out := deepCopyMessages(messages)
	for i := range out {
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

// TurnMessagesToBackfillEvents converts snapshot wire messages back into the
// session-event inputs the live turn would have flushed, so a resume after a
// mid-turn kill can rewrite the lost events and the screen shows what the
// model receives. Messages before from (the in-progress turn's first new
// message index) are already logged and skipped; user nudges, image carriers
// and reasoning never rode the event log, so they stay out — exactly like a
// live flush.
func TurnMessagesToBackfillEvents(messages []qgruntime.Message, from int) []AppendEventInput {
	if from < 0 {
		from = 0
	}
	inputs := []AppendEventInput{}
	callNames := map[string]string{}
	for _, message := range messages {
		if message.Role == qgruntime.MessageRoleAssistant {
			for _, call := range message.ToolCalls {
				if call.ID != "" {
					callNames[call.ID] = call.Name
				}
			}
		}
	}
	for index := from; index < len(messages); index++ {
		message := messages[index]
		switch message.Role {
		case qgruntime.MessageRoleAssistant:
			for _, call := range message.ToolCalls {
				if strings.TrimSpace(call.ID) == "" {
					continue
				}
				inputs = append(inputs, AppendEventInput{Type: EventToolCall, Payload: map[string]any{
					"id": call.ID, "name": call.Name, "arguments": call.Arguments,
				}})
			}
			if len(message.ToolCalls) == 0 && strings.TrimSpace(message.Content) != "" {
				inputs = append(inputs, AppendEventInput{Type: EventMessage, Payload: map[string]any{
					"role": "assistant", "content": message.Content,
				}})
			}
		case qgruntime.MessageRoleTool:
			if strings.TrimSpace(message.ToolCallID) == "" {
				continue
			}
			status := "ok"
			if message.IsError {
				status = "error"
			}
			payload := map[string]any{
				"toolCallId": message.ToolCallID,
				"name":       callNames[message.ToolCallID],
				"status":     status,
				"output":     message.Content,
			}
			if len(message.ChangedFiles) > 0 {
				payload["changedFiles"] = message.ChangedFiles
			}
			inputs = append(inputs, AppendEventInput{Type: EventToolResult, Payload: payload})
		}
	}
	return inputs
}

// backfillEventKey identifies a conversation event for idempotency: a resume
// that finds its steps already logged (clean cancel flushed them) appends
// nothing twice.
func backfillEventKey(input AppendEventInput) string {
	payload, _ := input.Payload.(map[string]any)
	str := func(key string) string {
		value, _ := payload[key].(string)
		return value
	}
	switch input.Type {
	case EventToolCall:
		return "call\x00" + firstNonEmpty(str("toolCallId"), str("id"))
	case EventToolResult:
		return "result\x00" + firstNonEmpty(str("toolCallId"), str("id"))
	case EventMessage:
		return "msg\x00" + str("role") + "\x00" + str("content")
	default:
		return string(input.Type) + "\x00" + str("id")
	}
}

// eventKeyOf maps a stored event to the same key space as backfill inputs.
func eventKeyOf(event Event) string {
	var payload map[string]any
	_ = json.Unmarshal(event.Payload, &payload)
	str := func(key string) string {
		value, _ := payload[key].(string)
		return value
	}
	switch event.Type {
	case EventToolCall:
		id := firstNonEmpty(str("toolCallId"), str("id"))
		return "call\x00" + id
	case EventToolResult:
		id := firstNonEmpty(str("toolCallId"), str("id"))
		return "result\x00" + id
	case EventMessage:
		return "msg\x00" + strings.ToLower(str("role")) + "\x00" + str("content")
	default:
		return string(event.Type) + "\x00" + str("id")
	}
}

// DeltaCoveredBySnapshot reports whether every conversation event logged after
// snap.EventSeq is already contained in the snapshot's in-progress turn
// (messages from TurnBase onward). That is the flush-vs-snapshot race: the
// turn flushed, then the process died before the final snapshot rewrote the
// file. Usage, permission, error, checkpoint and goal/spec events never ride
// the wire and are ignored.
func DeltaCoveredBySnapshot(delta []Event, snap *ContinuationSnapshot) bool {
	if snap == nil {
		return false
	}
	base := snap.TurnBase
	if base <= 0 || base > len(snap.Messages) {
		return false
	}
	needles := map[string]bool{}
	for _, input := range TurnMessagesToBackfillEvents(snap.Messages, base) {
		needles[backfillEventKey(input)] = true
	}
	for _, event := range delta {
		switch event.Type {
		case EventUsage, EventError, EventPermission, EventPermissionRequest, EventPermissionDecision,
			EventSessionCheckpoint, EventSessionFork, EventSessionChild,
			EventSpecialistStart, EventSpecialistStop,
			EventSpecDraft, EventSpecApproved, EventSpecRejected,
			EventGoalCreated, EventGoalUpdated, EventGoalCleared,
			EventSessionRewind, EventCompaction:
			continue
		}
		if !needles[eventKeyOf(event)] {
			return false
		}
	}
	return true
}

// InferTurnBase locates the in-progress turn's user message for snapshots
// saved without TurnBase: the last user message matching the last logged
// user prompt (suffix match tolerates the live [context update] drift note).
// Returns -1 when nothing matches; callers then skip the backfill.
func InferTurnBase(messages []qgruntime.Message, prompt string) int {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return -1
	}
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != qgruntime.MessageRoleUser {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content == prompt || strings.HasSuffix(content, prompt) {
			return index
		}
	}
	return -1
}

// BackfillTurnEvents appends the snapshot's unlogged turn messages to the
// session log and returns the new head sequence. It is idempotent: steps
// already logged (clean cancel flushed them) are skipped by key, so a second
// resume appends nothing. Only missing conversation events are written;
// usage, permission and checkpoint payloads cannot be reconstructed and stay
// out (the meter keeps the pre-turn figure, noted on resume). The anchor is
// the turn's logged user prompt: only events after it are eligible, so reused
// tool ids from earlier turns can never collide.
func (store *Store) BackfillTurnEvents(sessionID string, snap *ContinuationSnapshot) (int, error) {
	if snap == nil {
		return 0, nil
	}
	base := snap.TurnBase
	events, err := store.ReadEvents(sessionID)
	if err != nil {
		return 0, err
	}
	head := 0
	if len(events) > 0 {
		head = events[len(events)-1].Sequence
	}
	// TurnBase 0 means unknown (snapshots saved before the field existed);
	// callers resolve it via InferTurnBase first. Without a base there is
	// nothing safe to backfill.
	if base <= 0 || base >= len(snap.Messages) {
		return head, nil
	}
	prompt := ""
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != EventMessage {
			continue
		}
		if role, content := eventMessageRoleContent(events[index]); role == "user" && content != "" {
			prompt = content
			break
		}
	}
	if prompt == "" {
		return head, nil
	}
	anchor := 0
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != EventMessage {
			continue
		}
		if role, content := eventMessageRoleContent(events[index]); role == "user" && (content == prompt || strings.HasSuffix(content, prompt) || strings.HasSuffix(prompt, content)) {
			anchor = events[index].Sequence
			break
		}
	}
	present := map[string]bool{}
	anchored := false
	for _, event := range events {
		if event.Sequence > anchor {
			anchored = true
			present[eventKeyOf(event)] = true
		}
	}
	if !anchored && anchor == 0 {
		return head, nil
	}
	inputs := []AppendEventInput{}
	for _, input := range TurnMessagesToBackfillEvents(snap.Messages, base+1) {
		if present[backfillEventKey(input)] {
			continue
		}
		inputs = append(inputs, input)
	}
	if len(inputs) == 0 {
		return head, nil
	}
	appended, err := store.AppendEvents(sessionID, inputs)
	if err != nil {
		return head, err
	}
	return appended[len(appended)-1].Sequence, nil
}

func eventMessageRoleContent(event Event) (string, string) {
	var payload map[string]any
	_ = json.Unmarshal(event.Payload, &payload)
	role, _ := payload["role"].(string)
	content, _ := payload["content"].(string)
	return strings.ToLower(strings.TrimSpace(role)), strings.TrimSpace(content)
}
