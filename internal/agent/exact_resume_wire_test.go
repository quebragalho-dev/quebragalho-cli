package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	openai "github.com/quebragalho-dev/quebragalho-cli/internal/providers/openai"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

type exactEchoTool struct{}

func (exactEchoTool) Name() string { return "exact_echo" }
func (exactEchoTool) Description() string {
	return "echo"
}
func (exactEchoTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (exactEchoTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (exactEchoTool) Run(_ context.Context, _ map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "echo-output-12345"}
}

func exactSSE(w http.ResponseWriter, payloads ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, p := range payloads {
		_, _ = w.Write([]byte("data: " + p + "\n\n"))
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// TestExactWireSurvivesSnapshotRoundTrip proves the resumed turn sends the
// same provider wire as a turn that never closed: the snapshot is the
// repaired message history, and the next request replays it byte-identical.
func TestExactWireSurvivesSnapshotRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	scripts := [][]string{
		{`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"exact_echo","arguments":"{}"}}]}}]}`, `[DONE]`},
		{`{"choices":[{"delta":{"content":"answer one"}}]}`, `[DONE]`},
		{`{"choices":[{"delta":{"content":"answer two"}}]}`, `[DONE]`},
		{`{"choices":[{"delta":{"content":"answer three"}}]}`, `[DONE]`},
		{`{"choices":[{"delta":{"content":"answer three"}}]}`, `[DONE]`},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		canonical, _ := json.Marshal(raw)
		mu.Lock()
		n := len(bodies)
		bodies = append(bodies, string(canonical))
		mu.Unlock()
		if n < len(scripts) {
			exactSSE(w, scripts[n]...)
		} else {
			exactSSE(w, `[DONE]`)
		}
	}))
	defer server.Close()

	newProvider := func() *openai.Provider {
		p, err := openai.New(openai.Options{APIKey: "sk-fake", BaseURL: server.URL + "/", Model: "proof-model"})
		if err != nil {
			t.Fatalf("openai.New: %v", err)
		}
		return p
	}
	registry := tools.NewRegistry()
	registry.Register(exactEchoTool{})
	dir := t.TempDir()
	run := func(provider *openai.Provider, prompt string, cont []qgruntime.Message, loaded []string) Result {
		t.Helper()
		res, err := Run(context.Background(), prompt, provider, Options{
			Registry: registry, Cwd: dir, Model: "proof-model",
			Continuation: cont, LoadedDeferredTools: loaded,
		})
		if err != nil {
			t.Fatalf("Run(%q): %v", prompt, err)
		}
		return res
	}

	p1 := newProvider()
	r1 := run(p1, "do echo", nil, nil)
	r2 := run(p1, "next", r1.Messages, r1.LoadedDeferredTools)

	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	sess, err := store.Create(sessions.CreateInput{SessionID: "exact_wire_1", ModelID: "proof-model"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteContinuation(sessions.ContinuationSnapshot{
		SessionID: sess.SessionID, ModelID: "proof-model",
		Messages: append([]qgruntime.Message(nil), r2.Messages...),
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.ReadContinuation(sess.SessionID)
	if err != nil || loaded == nil {
		t.Fatalf("ReadContinuation: %#v %v", loaded, err)
	}
	p2 := newProvider()
	run(p2, "and then?", sessions.RepairOrphanToolCalls(loaded.Messages), loaded.LoadedTools)
	mu.Lock()
	resumedBody := bodies[len(bodies)-1]
	mu.Unlock()

	run(p1, "and then?", r2.Messages, r2.LoadedDeferredTools)
	mu.Lock()
	controlBody := bodies[len(bodies)-1]
	mu.Unlock()

	if resumedBody != controlBody {
		t.Fatal("resumed request body differs from the no-close control body")
	}
}

// TestInterruptedHistoryRepairedBeforeSend proves an orphaned tool_call is
// closed with an interrupted result so the provider still accepts the replay.
// TestReconstructedWireSkipsTextSummary proves an old session (events but no
// snapshot) still resumes with messages as messages: the reconstructed
// continuation replays history instead of the textual summary.
func TestReconstructedWireSkipsTextSummary(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		canonical, _ := json.Marshal(raw)
		mu.Lock()
		bodies = append(bodies, string(canonical))
		mu.Unlock()
		exactSSE(w, `{"choices":[{"delta":{"content":"later"}}]}`, `[DONE]`)
	}))
	defer server.Close()
	provider, err := openai.New(openai.Options{APIKey: "sk-fake", BaseURL: server.URL + "/", Model: "proof-model"})
	if err != nil {
		t.Fatal(err)
	}
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{SessionID: "recon_wire_1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []sessions.AppendEventInput{
		{Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "first request"}},
		{Type: sessions.EventToolCall, Payload: map[string]any{"id": "c1", "name": "exact_echo", "arguments": "{}"}},
		{Type: sessions.EventToolResult, Payload: map[string]any{"toolCallId": "c1", "name": "exact_echo", "status": "ok", "output": "echo-output"}},
		{Type: sessions.EventMessage, Payload: map[string]any{"role": "assistant", "content": "first answer"}},
	} {
		if _, err := store.AppendEvent(session.SessionID, input); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := store.ReadContinuation(session.SessionID); got != nil {
		t.Fatal("expected no snapshot for an old session")
	}
	events, err := store.ReadRehydratedEvents(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	history := sessions.RepairOrphanToolCalls(sessions.ReconstructContinuationMessages(events))
	if len(history) == 0 {
		t.Fatal("reconstruction must yield messages")
	}
	registry := tools.NewRegistry()
	registry.Register(exactEchoTool{})
	res, err := Run(context.Background(), "second question", provider, Options{
		Registry: registry, Cwd: t.TempDir(), Model: "proof-model", Continuation: history,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalAnswer != "later" {
		t.Fatalf("answer = %q", res.FinalAnswer)
	}
	mu.Lock()
	body := bodies[len(bodies)-1]
	mu.Unlock()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(decoded["messages"])
	wire := string(raw)
	for _, banned := range []string{"Previous session context", "Continuing QG session"} {
		if strings.Contains(wire, banned) {
			t.Fatalf("reconstructed turn must not use the textual summary, found %q", banned)
		}
	}
	for _, want := range []string{"first request", "echo-output", "first answer", "second question"} {
		if !strings.Contains(wire, want) {
			t.Fatalf("reconstructed wire must carry %q", want)
		}
	}
}

func TestInterruptedHistoryRepairedBeforeSend(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		canonical, _ := json.Marshal(raw)
		mu.Lock()
		bodies = append(bodies, string(canonical))
		mu.Unlock()
		exactSSE(w, `{"choices":[{"delta":{"content":"recovered"}}]}`, `[DONE]`)
	}))
	defer server.Close()
	provider, err := openai.New(openai.Options{APIKey: "sk-fake", BaseURL: server.URL + "/", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	registry.Register(exactEchoTool{})
	interrupted := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: "sys"},
		{Role: qgruntime.MessageRoleUser, Content: "go"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{
			{ID: "c1", Name: "exact_echo", Arguments: "{}"},
			{ID: "c2", Name: "exact_echo", Arguments: "{}"},
		}},
		{Role: qgruntime.MessageRoleTool, Content: "echo-output-12345", ToolCallID: "c1"},
	}
	filtered := sessions.FilterContinuationForModel(sessions.RepairOrphanToolCalls(interrupted), "m-a", "m-b")
	res, err := Run(context.Background(), "continue", provider, Options{
		Registry: registry, Cwd: t.TempDir(), Model: "m-b", Continuation: filtered,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.FinalAnswer != "recovered" {
		t.Fatalf("answer = %q", res.FinalAnswer)
	}
	mu.Lock()
	body := bodies[len(bodies)-1]
	mu.Unlock()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range decoded["messages"].([]any) {
		if mm := m.(map[string]any); mm["role"] == "tool" && mm["tool_call_id"] == "c2" {
			found = true
			content, _ := mm["content"].(string)
			if !strings.Contains(content, "interrupted") {
				t.Fatalf("synthetic result must report the interruption, got %q", content)
			}
		}
	}
	if !found {
		t.Fatal("synthetic tool_result for the orphaned call missing from the wire")
	}
}
