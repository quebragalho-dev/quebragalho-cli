package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestRepairOrphanInsertsInlineBeforeNextMessage(t *testing.T) {
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleAssistant, Content: "one", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "read_it", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: "ok", ToolCallID: "c1"},
		{Role: qgruntime.MessageRoleAssistant, Content: "two", ToolCalls: []qgruntime.ToolCall{{ID: "c2", Name: "write_file", Arguments: "{}"}, {ID: "c3", Name: "read_it", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: "ok", ToolCallID: "c2"},
		{Role: qgruntime.MessageRoleAssistant, Content: "three"},
	}
	fixed := RepairOrphanToolCalls(msgs)
	if len(fixed) != len(msgs)+1 {
		t.Fatalf("len = %d, want %d", len(fixed), len(msgs)+1)
	}
	// The synthetic result for c3 must sit right after its owner (index 2),
	// before the following assistant message — not at the end of the list.
	// The answered c2 next to it must not gain a second result.
	synthetic := -1
	c2results := 0
	for i, m := range fixed {
		if m.Role == qgruntime.MessageRoleTool && m.ToolCallID == "c3" {
			synthetic = i
		}
		if m.Role == qgruntime.MessageRoleTool && m.ToolCallID == "c2" {
			c2results++
		}
	}
	if synthetic != 4 {
		t.Fatalf("synthetic result at index %d, want 4 (right after its owner)", synthetic)
	}
	if c2results != 1 {
		t.Fatalf("answered call c2 has %d results, want exactly 1", c2results)
	}
	if !fixed[synthetic].IsError {
		t.Fatal("synthetic result must be IsError")
	}
	// Pairing an already-answered call stays untouched.
	valid := []qgruntime.Message{
		{Role: qgruntime.MessageRoleAssistant, Content: "x", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "read_it", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: "ok", ToolCallID: "c1"},
	}
	if out := RepairOrphanToolCalls(valid); len(out) != len(valid) {
		t.Fatalf("valid conversation must not grow: %#v", out)
	}
}

func TestReconstructContinuationMessagesFiltersAndMerges(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "recon_1"})
	if err != nil {
		t.Fatal(err)
	}
	appendEvent := func(typ EventType, payload map[string]any) {
		t.Helper()
		if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: typ, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent(EventMessage, map[string]any{"role": "user", "content": "do it"})
	appendEvent(EventToolCall, map[string]any{"id": "c1", "name": "read_it", "arguments": "{}"})
	appendEvent(EventToolResult, map[string]any{"toolCallId": "c1", "name": "read_it", "status": "ok", "output": "contents"})
	appendEvent(EventToolCall, map[string]any{"id": "c2", "name": "bash", "arguments": "{}"})
	appendEvent(EventToolResult, map[string]any{"toolCallId": "c2", "name": "bash", "status": "error", "output": "boom"})
	appendEvent(EventMessage, map[string]any{"role": "assistant", "content": "done"})
	appendEvent(EventUsage, map[string]any{"inputTokens": 5})
	appendEvent(EventError, map[string]any{"message": "Run cancelled."})
	events, err := store.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	msgs := ReconstructContinuationMessages(events)
	if len(msgs) != 5 {
		t.Fatalf("len = %d, want 5 (user, merged assistant, tool, tool, assistant; usage/error skipped)", len(msgs))
	}
	if msgs[0].Role != qgruntime.MessageRoleUser || msgs[0].Content != "do it" {
		t.Fatalf("msg0 = %#v", msgs[0])
	}
	if msgs[1].Role != qgruntime.MessageRoleAssistant || len(msgs[1].ToolCalls) != 2 {
		t.Fatalf("tool calls must merge into one assistant message: %#v", msgs[1])
	}
	if msgs[2].ToolCallID != "c1" || msgs[2].Content != "contents" || msgs[2].IsError {
		t.Fatalf("tool result c1 wrong: %#v", msgs[2])
	}
	if msgs[3].ToolCallID != "c2" || !msgs[3].IsError {
		t.Fatalf("tool result c2 must be error: %#v", msgs[3])
	}
	if msgs[4].Content != "done" {
		t.Fatalf("final answer lost: %#v", msgs[4])
	}
}

func TestBackfillOrphanResultsAppendsMissingCallAndResult(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "orphan_1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "go"}}); err != nil {
		t.Fatal(err)
	}
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "go"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{{ID: "c9", Name: "write_file", Arguments: "{}"}}},
	}
	snap := ContinuationSnapshot{SessionID: session.SessionID, ModelID: "m", EventSeq: 1, Messages: msgs}
	added, err := store.BackfillOrphanResults(session.SessionID, &snap)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("appended = %d, want 2 (missing tool_call + synthetic result)", added)
	}
	events, _ := store.ReadEvents(session.SessionID)
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if events[1].Type != EventToolCall || events[2].Type != EventToolResult {
		t.Fatalf("types = %v %v", events[1].Type, events[2].Type)
	}
	var payload map[string]any
	_ = json.Unmarshal(events[2].Payload, &payload)
	if payload["status"] != "error" || payload["toolCallId"] != "c9" {
		t.Fatalf("synthetic result wrong: %v", payload)
	}
	// Idempotent: a second resume appends nothing.
	added, err = store.BackfillOrphanResults(session.SessionID, &snap)
	if err != nil || added != 0 {
		t.Fatalf("second backfill: added=%d err=%v, want 0, nil", added, err)
	}
	if events, _ := store.ReadEvents(session.SessionID); len(events) != 3 {
		t.Fatalf("events = %d after no-op, want 3", len(events))
	}
}

func TestBackfillOrphanResultsSkipsAnsweredCalls(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "orphan_2"})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []AppendEventInput{
		{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "go"}},
		{Type: EventToolCall, Payload: map[string]any{"id": "c1", "name": "read_it", "arguments": "{}"}},
		{Type: EventToolResult, Payload: map[string]any{"toolCallId": "c1", "name": "read_it", "status": "ok", "output": "out"}},
	} {
		if _, err := store.AppendEvent(session.SessionID, input); err != nil {
			t.Fatal(err)
		}
	}
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "go"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "read_it", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: "out", ToolCallID: "c1"},
	}
	snap := ContinuationSnapshot{SessionID: session.SessionID, ModelID: "m", EventSeq: 3, Messages: msgs}
	added, err := store.BackfillOrphanResults(session.SessionID, &snap)
	if err != nil || added != 0 {
		t.Fatalf("answered history must not grow: added=%d err=%v", added, err)
	}
}

func TestValidateContinuationSnapshot(t *testing.T) {
	valid := ContinuationSnapshot{Version: continuationVersion, SessionID: "s1", ModelID: "m", EventSeq: 2, Messages: []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "hi"},
	}}
	if err := ValidateContinuationSnapshot("s1", &valid); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	// Post-/rewind snapshots rebuilt from the truncated log carry no frozen
	// system: user-first is a valid wire opener.
	userFirst := valid
	userFirst.Messages = append([]qgruntime.Message(nil), valid.Messages[1:]...)
	if err := ValidateContinuationSnapshot("s1", &userFirst); err != nil {
		t.Fatalf("user-first snapshot rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ContinuationSnapshot){
		"empty messages": func(s *ContinuationSnapshot) { s.Messages = nil },
		"non-conversation opener": func(s *ContinuationSnapshot) {
			s.Messages[0] = qgruntime.Message{Role: qgruntime.MessageRoleTool, Content: "x", ToolCallID: "c0"}
		},
		"unknown role": func(s *ContinuationSnapshot) {
			s.Messages = append(s.Messages, qgruntime.Message{Role: "wizard", Content: "hi"})
		},
		"tool result without call id": func(s *ContinuationSnapshot) {
			s.Messages = append(s.Messages, qgruntime.Message{Role: qgruntime.MessageRoleTool, Content: "x"})
		},
		"orphan call": func(s *ContinuationSnapshot) {
			s.Messages = append(s.Messages, qgruntime.Message{Role: qgruntime.MessageRoleAssistant, ToolCalls: []qgruntime.ToolCall{{ID: "zzz", Name: "x", Arguments: "{}"}}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			cp := valid
			cp.Messages = append([]qgruntime.Message(nil), valid.Messages...)
			mutate(&cp)
			if err := ValidateContinuationSnapshot("s1", &cp); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestReadContinuationRejectsForeignSessionID(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	a, err := store.Create(CreateInput{SessionID: "sess_aaa"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(CreateInput{SessionID: "sess_bbb"}); err != nil {
		t.Fatal(err)
	}
	// Hand-place a snapshot for A inside B's directory (copied folder).
	if _, err := os.ReadFile(filepath.Join(store.RootDir, a.SessionID, ContinuationFile)); err == nil {
		t.Fatal("expected no snapshot yet")
	}
	snap := ContinuationSnapshot{SessionID: "sess_aaa", ModelID: "m", EventSeq: 0, Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: "s"}}}
	if err := store.WriteContinuation(snap); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(store.RootDir, "sess_aaa", ContinuationFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.RootDir, "sess_bbb", ContinuationFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadContinuation("sess_bbb"); err == nil || !strings.Contains(err.Error(), "sess_bbb") {
		t.Fatalf("cross-session snapshot must be rejected, got %v", err)
	}
}

func TestContinuationFreshRewindIsStale(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "fresh_rew"})
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"a", "b", "c"} {
		if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": content}}); err != nil {
			t.Fatal(err)
		}
	}
	snap := ContinuationSnapshot{SessionID: session.SessionID, ModelID: "m", EventSeq: 99, Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: "s"}}}
	fresh, _, err := store.CheckContinuationFresh(session.SessionID, &snap)
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Fatal("log rewound before the snapshot must be stale")
	}
}

func TestFilterContinuationForModelDropsSystemAndReasoningOnSwitch(t *testing.T) {
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "frozen system of family A"},
		{Role: qgruntime.MessageRoleUser, Content: "hi"},
		{Role: qgruntime.MessageRoleAssistant, Content: "working",
			Reasoning: []qgruntime.ReasoningBlock{{Provider: "anthropic", Type: "thinking", Text: "family A thoughts", Signature: "sig-r"}},
			ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "read_it", Arguments: "{}", ProviderCallID: "native-1", Signature: "sig"}}},
		{Role: qgruntime.MessageRoleTool, Content: "out", ToolCallID: "c1", ToolCallProviderID: "native-1"},
	}
	// Same model: deep copy, nothing stripped.
	kept := FilterContinuationForModel(msgs, "model-a", "model-a")
	if len(kept) != len(msgs) || kept[0].Content != "frozen system of family A" {
		t.Fatalf("same-model filter must keep the wire, got %#v", kept)
	}
	if len(kept[2].Reasoning) != 1 {
		t.Fatal("same-model filter must keep reasoning blocks")
	}
	// Family switch: the frozen system is dropped (the loop rebuilds the
	// current one), foreign reasoning never crosses, native ids are cleared,
	// and the conversation itself survives.
	switched := FilterContinuationForModel(msgs, "model-a", "model-b")
	if len(switched) == 0 || switched[0].Role == qgruntime.MessageRoleSystem {
		t.Fatalf("switched wire must drop the frozen system, got %#v", switched[0])
	}
	for _, message := range switched {
		if len(message.Reasoning) != 0 {
			t.Fatalf("foreign reasoning must not cross families: %#v", message)
		}
		for _, call := range message.ToolCalls {
			if call.ProviderCallID != "" || call.Signature != "" {
				t.Fatalf("provider-native ids must not cross families: %#v", call)
			}
		}
		if message.Role == qgruntime.MessageRoleTool && message.ToolCallProviderID != "" {
			t.Fatalf("tool provider id must not cross families: %#v", message)
		}
	}
	foundCall, foundResult := false, false
	for _, message := range switched {
		for _, call := range message.ToolCalls {
			if call.ID == "c1" && call.Name == "read_it" && call.Arguments == "{}" {
				foundCall = true
			}
		}
		if message.Role == qgruntime.MessageRoleTool && message.ToolCallID == "c1" && message.Content == "out" {
			foundResult = true
		}
	}
	if !foundCall || !foundResult {
		t.Fatal("switch must keep the conversation, only strip provider-native parts")
	}
	// The input is never mutated.
	if msgs[0].Role != qgruntime.MessageRoleSystem || len(msgs[2].Reasoning) != 1 || msgs[2].ToolCalls[0].ProviderCallID != "native-1" {
		t.Fatal("filter must not mutate its input")
	}
}

func TestContinuationFreshCoversSameStepCallDrift(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "fresh_cov"})
	if err != nil {
		t.Fatal(err)
	}
	// A turn's user message, then the step snapshot lands before the log
	// catches up with the advertised tool call (kill-window ordering).
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventMessage, Payload: map[string]any{"role": "user", "content": "go"}}); err != nil {
		t.Fatal(err)
	}
	msgs := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "go"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{{ID: "c1", Name: "bash", Arguments: "{}"}}},
		{Role: qgruntime.MessageRoleTool, Content: interruptedToolResultNotice, ToolCallID: "c1", IsError: true},
	}
	snap := ContinuationSnapshot{SessionID: session.SessionID, ModelID: "m", EventSeq: 1, Messages: msgs}
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventToolCall, Payload: map[string]any{"id": "c1", "name": "bash", "arguments": "{}"}}); err != nil {
		t.Fatal(err)
	}
	fresh, _, err := store.CheckContinuationFresh(session.SessionID, &snap)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Fatal("same-step call drift covered by the snapshot must stay fresh")
	}
	// A genuinely new turn after the snapshot (unknown call id) is stale.
	if _, err := store.AppendEvent(session.SessionID, AppendEventInput{Type: EventToolCall, Payload: map[string]any{"id": "c2", "name": "bash", "arguments": "{}"}}); err != nil {
		t.Fatal(err)
	}
	fresh, _, err = store.CheckContinuationFresh(session.SessionID, &snap)
	if err != nil {
		t.Fatal(err)
	}
	if fresh {
		t.Fatal("a later turn's call must make the snapshot stale")
	}
}

func TestTryAcquireSessionLock(t *testing.T) {
	store := NewStore(StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(CreateInput{SessionID: "lock_1"})
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.TryAcquireSessionLock(session.SessionID)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if held.SessionID() != session.SessionID {
		t.Fatalf("lock session = %q", held.SessionID())
	}
	// Same-process re-acquire shares the lease (another model instance in
	// this process, e.g. a test resume or the CLI-to-TUI handoff): no
	// self-conflict, and the PID file still names this process.
	second, err := store.TryAcquireSessionLock(session.SessionID)
	if err != nil {
		t.Fatalf("same-process re-acquire must share, got %v", err)
	}
	// Releasing one holder keeps the lease: the other holder still owns it.
	second.Release()
	second.Release() // idempotent
	again, err := store.TryAcquireSessionLock(session.SessionID)
	if err != nil {
		t.Fatalf("acquire while first holder lives: %v", err)
	}
	again.Release()
	held.Release()
	held.Release() // idempotent
	after, err := store.TryAcquireSessionLock(session.SessionID)
	if err != nil {
		t.Fatalf("acquire after full release: %v", err)
	}
	after.Release()
}

func TestSessionLockedErrorMessage(t *testing.T) {
	locked := &SessionLockedError{SessionID: "sess_x", PID: 1234}
	if !strings.Contains(locked.Error(), "1234") || !strings.Contains(locked.Error(), "outro terminal") {
		t.Fatalf("refusal must name the PID in pt-BR, got %q", locked.Error())
	}
	bare := &SessionLockedError{SessionID: "sess_x"}
	if !strings.Contains(bare.Error(), "outro terminal") {
		t.Fatalf("refusal without PID must stay in pt-BR, got %q", bare.Error())
	}
	var target *SessionLockedError
	if !errors.As(locked, &target) || target.PID != 1234 {
		t.Fatal("SessionLockedError must survive errors.As")
	}
}

// TestSessionLockRefusesOtherProcess proves B2 across real processes: a
// child holding the lease makes the parent's acquire fail with a
// *SessionLockedError naming the child's PID, and killing the child frees
// the lease (the kernel releases a dead holder's flock).
func TestSessionLockRefusesOtherProcess(t *testing.T) {
	if os.Getenv("QG_TEST_LOCK_CHILD") == "1" {
		root := os.Getenv("QG_TEST_LOCK_ROOT")
		sid := os.Getenv("QG_TEST_LOCK_SESSION")
		ready := os.Getenv("QG_TEST_LOCK_READY")
		store := NewStore(StoreOptions{RootDir: root})
		lock, err := store.TryAcquireSessionLock(sid)
		if err != nil {
			t.Fatalf("child acquire: %v", err)
		}
		defer lock.Release()
		if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatalf("child ready: %v", err)
		}
		time.Sleep(60 * time.Second) // hold until killed (a timer keeps the runtime's deadlock detector quiet)
	}
	root := t.TempDir()
	store := NewStore(StoreOptions{RootDir: root})
	session, err := store.Create(CreateInput{SessionID: "lock_xproc"})
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "child-ready")
	cmd := exec.Command(os.Args[0], "-test.run=TestSessionLockRefusesOtherProcess")
	cmd.Env = append(os.Environ(),
		"QG_TEST_LOCK_CHILD=1",
		"QG_TEST_LOCK_ROOT="+root,
		"QG_TEST_LOCK_SESSION="+session.SessionID,
		"QG_TEST_LOCK_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never signalled readiness")
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err = store.TryAcquireSessionLock(session.SessionID)
	if err == nil {
		t.Fatal("acquire must fail while another process holds the lease")
	}
	var locked *SessionLockedError
	if !errors.As(err, &locked) {
		t.Fatalf("want *SessionLockedError, got %#v", err)
	}
	if locked.PID != cmd.Process.Pid {
		t.Fatalf("locked PID = %d, want child pid %d", locked.PID, cmd.Process.Pid)
	}
	if !strings.Contains(locked.Error(), "outro terminal") {
		t.Fatalf("refusal must be in pt-BR, got %q", locked.Error())
	}
	// A dead holder's lock is released by the kernel: killing the child
	// frees the lease without any cleanup.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	_, _ = cmd.Process.Wait()
	acquired, err := store.TryAcquireSessionLock(session.SessionID)
	if err != nil {
		t.Fatalf("acquire after holder death: %v", err)
	}
	acquired.Release()
}
