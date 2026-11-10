package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestCompactRewritesSnapshot(t *testing.T) {
	store := testSessionStore(t)
	m := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("a")}, nil)
	m = runTextTurn(t, m, "hi")
	flushSnapshot(t, m)
	path := filepath.Join(store.RootDir, m.activeSession.SessionID, sessions.ContinuationFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	// Exercise the local compact path: the handler receives the post-compact
	// state and must rewrite the snapshot from it, not delete it.
	m.compactInFlight = true
	updated, _ := m.Update(compactResultMsg{
		result:             CompactResult{Compacted: true, Summary: "talked about hi"},
		activeSession:      m.activeSession,
		sessionEvents:      append([]sessions.Event{}, m.sessionEvents...),
		transcript:         append([]transcriptRow{}, m.transcript...),
		hasSessionSnapshot: true,
	})
	m = updated.(model)
	if len(m.continuation.messages) == 0 {
		t.Fatal("compact must rewrite continuation from the post-compact log")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("compact must rewrite (not delete) the snapshot: %v", err)
	}
	var snap sessions.ContinuationSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("rewritten snapshot must decode: %v", err)
	}
	if snap.EventSeq != m.activeSession.EventCount {
		t.Fatalf("rewritten snapshot head = %d, want log head %d", snap.EventSeq, m.activeSession.EventCount)
	}
	// A resume right after compact continues exact, with no summary fallback.
	fresh := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("x")}, nil)
	fresh, _ = fresh.handleResumeCommand(m.activeSession.SessionID)
	if !fresh.resumedExact || fresh.reconstructedResume {
		t.Fatal("post-compact resume must be exact")
	}
	flushSnapshot(t, fresh)
}

func TestRewindRewritesSnapshot(t *testing.T) {
	store := testSessionStore(t)
	m := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("a", "b")}, nil)
	m = runTextTurn(t, m, "hi")
	m = runTextTurn(t, m, "again")
	flushSnapshot(t, m)
	path := filepath.Join(store.RootDir, m.activeSession.SessionID, sessions.ContinuationFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	m, notice := m.handleRewindCommand("1")
	if !strings.Contains(notice, "Rewound") {
		t.Fatalf("rewind notice = %q", notice)
	}
	if len(m.continuation.messages) == 0 {
		t.Fatal("rewind must rewrite continuation from the post-rewind log")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("rewind must rewrite (not delete) the snapshot: %v", err)
	}
	var snap sessions.ContinuationSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("rewritten snapshot must decode: %v", err)
	}
	if snap.EventSeq != m.activeSession.EventCount {
		t.Fatalf("rewritten snapshot head = %d, want log head %d", snap.EventSeq, m.activeSession.EventCount)
	}
	// A resume right after rewind continues exact, with no summary fallback.
	fresh := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("x")}, nil)
	fresh, _ = fresh.handleResumeCommand(m.activeSession.SessionID)
	if !fresh.resumedExact || fresh.reconstructedResume {
		t.Fatal("post-rewind resume must be exact")
	}
	flushSnapshot(t, fresh)
	flushSnapshot(t, m)
}

func TestModelSwitchFiltersProviderIDs(t *testing.T) {
	store := testSessionStore(t)
	registry := tools.NewRegistry()
	registry.Register(continuationEchoTool{})
	provider := &scriptedProvider{scripts: append([][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-9", ToolCallProviderID: "call_native_1", ToolName: "read_it"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-9", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-9"},
		{Type: qgruntime.StreamEventDone},
	}}, textScripts("Should I continue?", "after switch")...)}
	m := continuationHarness(t, store, provider, registry)
	m = runTextTurn(t, m, "read it")
	m = runTextTurn(t, m, "Sim")
	// Simulate a model switch by changing the model name directly (the
	// dedicated switch path is covered elsewhere; the filter applies at submit
	// for any model change).
	m.modelName = "other-model"
	m = runTextTurn(t, m, "after switch")
	req := provider.requests[len(provider.requests)-1]
	if requestContains(req, "Previous session context") {
		t.Fatal("switched turn must keep real history")
	}
	for _, msg := range req.Messages {
		for _, call := range msg.ToolCalls {
			if call.ProviderCallID != "" {
				t.Fatalf("switched turn must clear provider call ids, got %#v", call)
			}
		}
		if msg.Role == qgruntime.MessageRoleTool && msg.ToolCallProviderID != "" {
			t.Fatalf("switched turn must clear result provider ids, got %#v", msg)
		}
	}
	if !requestContains(req, "file contents here") {
		t.Fatal("switched turn must keep the tool result content")
	}
}

func TestWriteFarewellShowsContinueLine(t *testing.T) {
	store := testSessionStore(t)
	m := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("a")}, nil)
	m = runTextTurn(t, m, "hi")
	var buf bytes.Buffer
	writeFarewell(&buf, m)
	if !strings.Contains(buf.String(), "Para continuar: qg --continue "+m.activeSession.SessionID) {
		t.Fatalf("farewell = %q", buf.String())
	}
	empty := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("a")}, nil)
	var buf2 bytes.Buffer
	writeFarewell(&buf2, empty)
	if strings.Contains(buf2.String(), "Para continuar") {
		t.Fatalf("empty session must not show continue line: %q", buf2.String())
	}
}

// deafEchoTool blocks until released and ignores context cancellation, like
// a slow shell command that outlives Esc: the cancelled run's goroutine keeps
// living and drains late, after the next turn already finished.
type deafEchoTool struct {
	release chan struct{}
}

func (deafEchoTool) Name() string { return "slow_echo" }
func (deafEchoTool) Description() string {
	return "slow echo that ignores cancellation"
}
func (deafEchoTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (deafEchoTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (t deafEchoTool) Run(_ context.Context, _ map[string]any) tools.Result {
	<-t.release
	return tools.Result{Status: tools.StatusOK, Output: "slow ok"}
}

// TestCancelledRunLateWriteKeepsNewerTurn is the B1 scenario from the audit:
// Esc on turn A (slow tool), turn B runs to completion and saves its
// snapshot, then A's tool returns and A's goroutine drains late. The late
// write must not overwrite B: the file keeps B's wire, and A's late events
// still reach the log (for /rewind) through the cancelled-run flush.
func TestCancelledRunLateWriteKeepsNewerTurn(t *testing.T) {
	store := testSessionStore(t)
	release := make(chan struct{})
	registry := tools.NewRegistry()
	registry.Register(deafEchoTool{release: release})
	provider := &scriptedProvider{scripts: append([][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-slow", ToolName: "slow_echo"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-slow", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-slow"},
		{Type: qgruntime.StreamEventDone},
	}}, textScripts("B done")...)}
	m := continuationHarness(t, store, provider, registry)

	m.input.SetValue("run A")
	updated, cmdA := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmdA == nil {
		t.Fatal("expected an agent command for turn A")
	}
	doneA := make(chan tea.Msg, 1)
	go func() { doneA <- execCmd(cmdA) }()
	sessionID := m.activeSession.SessionID
	snapPath := filepath.Join(store.RootDir, sessionID, sessions.ContinuationFile)
	waitForSnapshotCall(t, snapPath, "call-slow")
	runA := m.activeRunID

	// Esc cancels A (context and all); the deaf tool keeps the goroutine alive.
	m.cancelRun()
	m = runTextTurn(t, m, "run B")
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (A advertised, B completed)", len(provider.requests))
	}
	headAfterB := m.activeSession.EventCount

	// A's tool returns late; A's goroutine drains with the old run id.
	close(release)
	late := <-doneA
	updated, _ = m.Update(late)
	m = updated.(model)

	data, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	if strings.Contains(string(data), "slow ok") {
		t.Fatal("stale run A overwrote newer turn B in the snapshot (B1)")
	}
	if !strings.Contains(string(data), "B done") {
		t.Fatal("newer turn B missing from the snapshot after A's late drain")
	}
	var snap sessions.ContinuationSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("snapshot must decode: %v", err)
	}
	// The snapshot keeps B's head: A's late events land in the log (by
	// design, for /rewind) but must not rewrite the file.
	if snap.EventSeq != headAfterB {
		t.Fatalf("snapshot head = %d, want B's head %d", snap.EventSeq, headAfterB)
	}
	// A's late events still reached the log through the cancelled-run flush.
	events, err := store.ReadEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	foundLate := false
	for _, event := range events {
		if event.Type == sessions.EventToolResult && strings.Contains(string(event.Payload), "slow ok") {
			foundLate = true
		}
	}
	if !foundLate {
		t.Fatal("cancelled run's late result must still reach the log for /rewind")
	}
	settleModel(t, m)
	_ = runA
}

// TestKillBeforeFirstStepResumesExact (M10): the opening emitStep lands the
// initial wire before the provider unblocks, so a kill before the first
// tool call still resumes exact.
func TestKillBeforeFirstStepResumesExact(t *testing.T) {
	store := testSessionStore(t)
	release := make(chan struct{})
	provider := &scriptedProvider{scripts: textScripts("late answer")}
	provider.beforeCall = func(int) { <-release }
	m := continuationHarness(t, store, provider, nil)

	m.input.SetValue("early prompt")
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected an agent command")
	}
	sessionID := m.activeSession.SessionID
	snapPath := filepath.Join(store.RootDir, sessionID, sessions.ContinuationFile)
	done := make(chan tea.Msg, 1)
	go func() { done <- execCmd(cmd) }()
	waitForSnapshotContent(t, snapPath, "early prompt")

	// Kill before the first step: abandon the blocked run.
	freshProvider := &scriptedProvider{scripts: textScripts("after")}
	fresh := continuationHarness(t, store, freshProvider, nil)
	fresh, _ = fresh.handleResumeCommand(sessionID)
	if !fresh.resumedExact || fresh.reconstructedResume {
		t.Fatal("kill before the first step must resume exact")
	}
	fresh = runTextTurn(t, fresh, "and then?")
	if len(freshProvider.requests) != 1 {
		t.Fatalf("resumed requests = %d, want 1", len(freshProvider.requests))
	}
	req := freshProvider.requests[0]
	if requestContains(req, "Previous session context") {
		t.Fatal("resumed turn must not use the textual summary")
	}
	if !requestContains(req, "early prompt") {
		t.Fatal("resumed turn must carry the killed turn's prompt")
	}
	// Let the abandoned run finish after the assertions so nothing leaks.
	close(release)
	<-done
	settleModel(t, m)
}

// TestResumeRestoresModelThroughProviderRebuild (B4): a snapshot recorded
// under another model switches the live provider/profile through the same
// core path as /model, so the next request really goes out on the restored
// model — and a stored effort the new model does not support is dropped.
func TestResumeRestoresModelThroughProviderRebuild(t *testing.T) {
	const restored = "muse-spark-1.3-contributor"
	store := testSessionStore(t)
	oldProvider := &scriptedProvider{scripts: textScripts("answer on old")}
	var liveProvider *scriptedProvider
	var built []config.ProviderProfile
	newProviderStub := func(p config.ProviderProfile) (qgruntime.Provider, error) {
		built = append(built, p)
		if liveProvider != nil {
			return liveProvider, nil
		}
		return oldProvider, nil
	}
	mkModel := func(provider *scriptedProvider) model {
		return newModel(context.Background(), Options{
			Cwd:             t.TempDir(),
			ProviderName:    "test",
			ModelName:       "test-model",
			Provider:        provider,
			Registry:        tools.NewRegistry(),
			SessionStore:    store,
			ProviderProfile: config.ProviderProfile{Name: "test", APIKey: "test-key"},
			NewProvider:     newProviderStub,
		})
	}
	m := mkModel(oldProvider)
	m = runTextTurn(t, m, "hi")
	sessionID := m.activeSession.SessionID
	if len(oldProvider.requests) != 1 {
		t.Fatalf("setup requests = %d, want 1", len(oldProvider.requests))
	}

	// Rewrite the snapshot as recorded under another model with a bogus effort.
	raw, err := os.ReadFile(filepath.Join(store.RootDir, sessionID, sessions.ContinuationFile))
	if err != nil {
		t.Fatal(err)
	}
	var snap sessions.ContinuationSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	snap.ModelID = restored
	snap.ReasoningEffort = "xhigh"
	if err := store.WriteContinuation(snap); err != nil {
		t.Fatal(err)
	}

	newProvider := &scriptedProvider{scripts: textScripts("answer on new")}
	liveProvider = newProvider
	fresh := mkModel(newProvider)
	fresh, _ = fresh.handleResumeCommand(sessionID)
	if !fresh.resumedExact || fresh.reconstructedResume {
		t.Fatal("model-restore resume must be exact")
	}
	if fresh.modelName != restored {
		t.Fatalf("modelName = %q, want restored %q", fresh.modelName, restored)
	}
	if len(built) != 1 || built[0].Model != restored {
		t.Fatalf("provider must be rebuilt with the restored model, got %#v", built)
	}
	if fresh.reasoningEffort != "" {
		t.Fatalf("unsupported stored effort must be dropped, got %q", fresh.reasoningEffort)
	}
	if fresh.provider != qgruntime.Provider(newProvider) {
		t.Fatal("live provider must be the rebuilt one")
	}
	fresh = runTextTurn(t, fresh, "and then?")
	if len(newProvider.requests) != 1 {
		t.Fatalf("resumed requests = %d, want 1 through the rebuilt provider", len(newProvider.requests))
	}
	if len(oldProvider.requests) != 1 {
		t.Fatalf("old provider must see no new request, got %d", len(oldProvider.requests))
	}
	req := newProvider.requests[0]
	if requestContains(req, "Previous session context") {
		t.Fatal("resumed turn must not use the textual summary")
	}
	if !requestContains(req, "answer on old") {
		t.Fatal("resumed turn must carry the prior turn's answer on the restored model")
	}
}

// TestResumeRejectsForeignGoalKey (M16): a snapshot recorded under another
// goal must not resume exact.
func TestResumeRejectsForeignGoalKey(t *testing.T) {
	store := testSessionStore(t)
	m := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("hi back")}, nil)
	m = runTextTurn(t, m, "hi")
	sessionID := m.activeSession.SessionID

	raw, err := os.ReadFile(filepath.Join(store.RootDir, sessionID, sessions.ContinuationFile))
	if err != nil {
		t.Fatal(err)
	}
	var snap sessions.ContinuationSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	snap.GoalKey = "another goal"
	if err := store.WriteContinuation(snap); err != nil {
		t.Fatal(err)
	}
	fresh := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("x")}, nil)
	fresh, _ = fresh.handleResumeCommand(sessionID)
	if fresh.resumedExact || !fresh.reconstructedResume {
		t.Fatal("foreign goal snapshot must reconstruct, not resume exact")
	}
	found := false
	for _, row := range fresh.transcript {
		if strings.Contains(row.text, "A conversa mudou desde o último salvamento") {
			found = true
		}
	}
	if !found {
		t.Fatal("goal drift must warn with the stale-snapshot notice")
	}
	if len(fresh.continuation.messages) == 0 {
		t.Fatal("reconstructed resume must still set a structured continuation")
	}
}

// TestExactResumeMultiToolWithImage proves the wire-identical claim beyond
// text: one turn with two tool calls plus an attached image round-trips
// through kill + resume, and the next request still carries both results
// and the image (an event reconstruction would drop the image).
func TestExactResumeMultiToolWithImage(t *testing.T) {
	const visionModel = "muse-spark-1.3-contributor"
	store := testSessionStore(t)
	registry := tools.NewRegistry()
	registry.Register(continuationEchoTool{})
	provider := &scriptedProvider{scripts: append([][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "read_it"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-1", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-2", ToolName: "read_it"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-2", ArgumentsFragment: `{"path":"other"}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-2"},
		{Type: qgruntime.StreamEventDone},
	}}, textScripts("looks good", "third")...)}
	m := continuationHarness(t, store, provider, registry)
	m.modelName = visionModel
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00}
	m.pendingImages = []qgruntime.ImageBlock{{MediaType: "image/png", Data: png}}
	m.pendingImageLabels = []string{"photo.png"}
	m = runTextTurn(t, m, "look twice")
	m = runTextTurn(t, m, "again")
	// Turn 1 takes two completion calls (tool advertisement, then the final
	// answer), so turn 2's request is the last one.
	if len(provider.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(provider.requests))
	}
	assertMultiToolImageWire(t, provider.requests[len(provider.requests)-1], "looks good")

	sessionID := m.activeSession.SessionID
	freshProvider := &scriptedProvider{scripts: textScripts("after")}
	fresh := continuationHarness(t, store, freshProvider, registry)
	fresh.modelName = visionModel
	fresh, notice := fresh.handleResumeCommand(sessionID)
	if strings.TrimSpace(notice) != "" {
		t.Fatalf("resume notice = %q", notice)
	}
	if !fresh.resumedExact || fresh.reconstructedResume {
		t.Fatal("multi-tool+image resume must be exact")
	}
	fresh = runTextTurn(t, fresh, "once more")
	if len(freshProvider.requests) != 1 {
		t.Fatalf("resumed requests = %d, want 1", len(freshProvider.requests))
	}
	assertMultiToolImageWire(t, freshProvider.requests[0], "third")
}

func assertMultiToolImageWire(t *testing.T, req qgruntime.CompletionRequest, priorAnswer string) {
	t.Helper()
	calls := map[string]int{}
	results := map[string]int{}
	images := 0
	for _, message := range req.Messages {
		for _, call := range message.ToolCalls {
			calls[call.ID]++
		}
		if message.Role == qgruntime.MessageRoleTool && message.ToolCallID != "" {
			results[message.ToolCallID]++
			if !strings.Contains(message.Content, "file contents here") {
				t.Fatalf("tool result must carry the echo output, got %q", message.Content)
			}
		}
		images += len(message.Images)
	}
	if calls["call-1"] != 1 || calls["call-2"] != 1 {
		t.Fatalf("both tool calls must survive, got %v", calls)
	}
	if results["call-1"] != 1 || results["call-2"] != 1 {
		t.Fatalf("both tool results must survive, got %v", results)
	}
	if images == 0 {
		t.Fatal("attached image must survive the round trip in the wire")
	}
	if requestContains(req, "Previous session context") {
		t.Fatal("multi-tool turn must not use the textual summary")
	}
	if !requestContains(req, priorAnswer) {
		t.Fatalf("turn must carry the prior answer %q", priorAnswer)
	}
}

func waitForSnapshotContent(t *testing.T, path, substr string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), substr) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("snapshot %s never contained %q", path, substr)
}

type blockEchoTool struct {
	release chan struct{}
}

func (blockEchoTool) Name() string { return "block_echo" }
func (blockEchoTool) Description() string {
	return "blocking echo"
}
func (blockEchoTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (blockEchoTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (t blockEchoTool) Run(ctx context.Context, _ map[string]any) tools.Result {
	select {
	case <-t.release:
		return tools.Result{Status: tools.StatusOK, Output: "released"}
	case <-ctx.Done():
		return tools.Result{Status: tools.StatusError, Output: "Error: " + ctx.Err().Error()}
	}
}

// TestKillMidTurnResumesExactWithBackfill simulates kill -9 in the middle of
// a tool call: the agent goroutine is abandoned (its in-memory turn is lost
// like a killed process), then a fresh model resumes the same session. The
// snapshot on disk holds the advertised call; the resume backfills its events
// so log, screen and model agree, and the next request carries the recovered
// steps plus an "interrupted" result for the pending call.
func TestKillMidTurnResumesExactWithBackfill(t *testing.T) {
	store := testSessionStore(t)
	release := make(chan struct{})
	registry := tools.NewRegistry()
	registry.Register(blockEchoTool{release: release})
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-block-1", ToolName: "block_echo"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-block-1", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-block-1"},
		{Type: qgruntime.StreamEventDone},
	}}}
	m := continuationHarness(t, store, provider, registry)
	m.input.SetValue("run the blocking tool")
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmd == nil {
		t.Fatal("expected an agent command")
	}
	sessionID := m.activeSession.SessionID
	// Abandon the agent goroutine mid-tool: this is the kill -9. Its
	// in-memory messages vanish; only the on-disk snapshot survives.
	done := make(chan struct{})
	go func() {
		defer close(done)
		execCmd(cmd)
	}()
	snapPath := filepath.Join(store.RootDir, sessionID, sessions.ContinuationFile)
	waitForSnapshotCall(t, snapPath, "call-block-1")

	freshProvider := &scriptedProvider{scripts: textScripts("after the kill")}
	fresh := continuationHarness(t, store, freshProvider, nil)
	fresh, _ = fresh.handleResumeCommand(sessionID)
	if !fresh.resumedExact || fresh.reconstructedResume {
		t.Fatal("post-kill resume must be exact, not reconstructed")
	}
	// The backfilled tool card must show on screen what the model receives.
	seenTool, seenRecovery := false, false
	for _, row := range fresh.transcript {
		if strings.Contains(row.text, "Turno interrompido recuperado") {
			seenRecovery = true
		}
		if (row.kind == rowToolCall || row.kind == rowToolResult) && strings.Contains(row.text+row.tool, "block_echo") {
			seenTool = true
		}
	}
	if !seenTool {
		t.Fatal("screen must show the recovered tool step")
	}
	if !seenRecovery {
		t.Fatal("screen must note the recovered interrupted turn")
	}
	fresh = runTextTurn(t, fresh, "and then?")
	if len(freshProvider.requests) != 1 {
		t.Fatalf("resumed requests = %d, want 1", len(freshProvider.requests))
	}
	req := freshProvider.requests[0]
	if requestContains(req, "Previous session context") || requestContains(req, "Continuing QG session") {
		t.Fatal("post-kill turn must not use the textual summary")
	}
	foundCall, foundInterrupted := false, false
	for _, msg := range req.Messages {
		for _, call := range msg.ToolCalls {
			if call.ID == "call-block-1" {
				foundCall = true
			}
		}
		if msg.Role == qgruntime.MessageRoleTool && msg.ToolCallID == "call-block-1" && strings.Contains(msg.Content, "interrupted") {
			foundInterrupted = true
		}
	}
	if !foundCall {
		t.Fatal("next request must contain the recovered tool call")
	}
	if !foundInterrupted {
		t.Fatal("next request must close the pending call with an interrupted result")
	}
	// Let the abandoned pre-kill run finish so nothing leaks past the test.
	close(release)
	<-done
	settleModel(t, m)
}

func waitForSnapshotCall(t *testing.T, path, callID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), callID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("snapshot %s never recorded call %s", path, callID)
}

// TestNewModelWarnsAndLogsOnRepeatedSnapshotFailure (I2): a snapshot writer
// that fails three times in a row surfaces one notice through the runtime
// sink (rendered by the TUI as a system row) instead of degrading silently.
func TestNewModelWarnsAndLogsOnRepeatedSnapshotFailure(t *testing.T) {
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "snapshot_fail_session"})
	if err != nil {
		t.Fatal(err)
	}
	// A directory where continuation.json goes makes every write fail
	// deterministically.
	snapPath := filepath.Join(store.RootDir, session.SessionID, sessions.ContinuationFile)
	if err := os.MkdirAll(snapPath, 0o700); err != nil {
		t.Fatal(err)
	}

	var sinkMsgs []tea.Msg
	sink := func(msg tea.Msg) { sinkMsgs = append(sinkMsgs, msg) }
	provider := &scriptedProvider{scripts: textScripts("hi")}
	m := newModel(context.Background(), Options{
		Cwd:                t.TempDir(),
		ProviderName:       "test",
		ModelName:          "test-model",
		Provider:           provider,
		Registry:           tools.NewRegistry(),
		SessionStore:       store,
		RuntimeMessageSink: sink,
	})
	for index := 0; index < 3; index++ {
		snap := sessions.ContinuationSnapshot{
			SessionID: session.SessionID,
			ModelID:   "test-model",
			EventSeq:  index + 1,
			Messages: []qgruntime.Message{
				{Role: qgruntime.MessageRoleUser, Content: "hi"},
			},
		}
		m.snapshotWriter.Queue(snap, int64(index+1))
	}

	if len(sinkMsgs) != 1 {
		t.Fatalf("sink messages = %d, want 1 (the third consecutive failure fires the notice)", len(sinkMsgs))
	}
	msg, ok := sinkMsgs[0].(snapshotWriteFailedMsg)
	if !ok {
		t.Fatalf("sink message = %T, want snapshotWriteFailedMsg", sinkMsgs[0])
	}
	if msg.sessionID != session.SessionID {
		t.Fatalf("sessionID = %q, want %q", msg.sessionID, session.SessionID)
	}

	updated, _ := m.Update(msg)
	next := updated.(model)
	if !transcriptContains(next.transcript, "histórico exato") {
		t.Fatal("the failure notice must reach the transcript")
	}
}
