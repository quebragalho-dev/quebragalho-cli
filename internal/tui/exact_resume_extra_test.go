package tui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestCompactInvalidatesSnapshot(t *testing.T) {
	store := testSessionStore(t)
	m := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("a")}, nil)
	m = runTextTurn(t, m, "hi")
	writeFinalSnapshot(t, store, m, len(m.continuation.messages)-2)
	path := filepath.Join(store.RootDir, m.activeSession.SessionID, sessions.ContinuationFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	m.compactInFlight = true
	updated, _ := m.Update(compactResultMsg{})
	m = updated.(model)
	if len(m.continuation.messages) != 0 {
		t.Fatal("compact must clear continuation")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("compact must delete snapshot, stat err = %v", err)
	}
}

func TestRewindInvalidatesSnapshot(t *testing.T) {
	store := testSessionStore(t)
	m := continuationHarness(t, store, &scriptedProvider{scripts: textScripts("a", "b")}, nil)
	m = runTextTurn(t, m, "hi")
	m = runTextTurn(t, m, "again")
	writeFinalSnapshot(t, store, m, len(m.continuation.messages)-2)
	path := filepath.Join(store.RootDir, m.activeSession.SessionID, sessions.ContinuationFile)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("snapshot missing: %v", err)
	}
	m, notice := m.handleRewindCommand("1")
	if !strings.Contains(notice, "Rewound") {
		t.Fatalf("rewind notice = %q", notice)
	}
	if len(m.continuation.messages) != 0 {
		t.Fatal("rewind must clear continuation")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rewind must delete snapshot, stat err = %v", err)
	}
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

// blockEchoTool blocks until released, simulating a tool killed mid-execution.
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
