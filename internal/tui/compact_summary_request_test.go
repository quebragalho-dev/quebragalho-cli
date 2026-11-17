package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// emptyUnlessUserProvider imitates the real summarizer behavior behind the
// reported bug: a summarization request whose last message is not a user
// message (for example a raw history ending in an assistant tool call or a
// tool result, with no tools declared) comes back with empty text, while a
// request ending in a user message gets a summary.
type emptyUnlessUserProvider struct {
	requests []qgruntime.CompletionRequest
	summary  string
}

func (provider *emptyUnlessUserProvider) StreamCompletion(
	ctx context.Context,
	request qgruntime.CompletionRequest,
) (<-chan qgruntime.StreamEvent, error) {
	provider.requests = append(provider.requests, request)
	var events []qgruntime.StreamEvent
	if n := len(request.Messages); n > 0 && request.Messages[n-1].Role == qgruntime.MessageRoleUser {
		events = []qgruntime.StreamEvent{
			{Type: qgruntime.StreamEventText, Content: provider.summary},
			{Type: qgruntime.StreamEventDone},
		}
	} else {
		events = []qgruntime.StreamEvent{{Type: qgruntime.StreamEventDone}}
	}
	ch := make(chan qgruntime.StreamEvent, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func lastSummaryRequest(t *testing.T, provider *emptyUnlessUserProvider) qgruntime.CompletionRequest {
	t.Helper()
	if len(provider.requests) == 0 {
		t.Fatal("expected at least one summarization request")
	}
	return provider.requests[len(provider.requests)-1]
}

func assertRenderedSummaryRequest(t *testing.T, request qgruntime.CompletionRequest) {
	t.Helper()
	if len(request.Messages) != 2 {
		t.Fatalf("summary request must be exactly [system, user], got %d messages: %#v", len(request.Messages), request.Messages)
	}
	if request.Messages[0].Role != qgruntime.MessageRoleSystem || request.Messages[0].Content != agent.CompactionSummaryInstructions {
		t.Fatalf("summary request system message changed: %#v", request.Messages[0])
	}
	userMessage := request.Messages[1]
	if userMessage.Role != qgruntime.MessageRoleUser {
		t.Fatalf("summary request must end with a user message, got %q", userMessage.Role)
	}
	if !strings.HasPrefix(userMessage.Content, "Summarize this conversation:") {
		t.Fatalf("summary request must render the conversation after the agent prompt, got %q", userMessage.Content)
	}
}

// The agent and the TUI must build the same summarizer request: [system,
// user] with the conversation rendered to plain text and tool payloads
// clamped. A raw projected history can end in an assistant tool call or a
// tool result, which some providers answer with empty text.
func TestCompactSummaryRequestEndsWithRenderedUserMessage(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "file")
	provider := &emptyUnlessUserProvider{summary: "Resumo renderizado."}
	m := newModel(context.Background(), Options{Provider: provider})

	messages := []qgruntime.Message{
		{Role: qgruntime.MessageRoleUser, Content: "Keep the Postgres database."},
		{Role: qgruntime.MessageRoleAssistant, ToolCalls: []qgruntime.ToolCall{
			{ID: "ask-1", Name: "ask_user", Arguments: `{"questions":[{"question":"Which database should remain?"}]}`},
		}},
		{Role: qgruntime.MessageRoleTool, ToolCallID: "ask-1", Content: "Postgres only"},
	}
	summary, err := m.summarizeCompactionPlan(sessions.CompactionPlan{}, messages)
	if err != nil {
		t.Fatalf("summarizeCompactionPlan() = %v", err)
	}
	if summary.SummaryText != provider.summary {
		t.Fatalf("summary = %q, want %q", summary.SummaryText, provider.summary)
	}
	request := lastSummaryRequest(t, provider)
	assertRenderedSummaryRequest(t, request)
	for _, want := range []string{"Postgres", "Which database should remain?"} {
		if !strings.Contains(request.Messages[1].Content, want) {
			t.Fatalf("rendered summary request lost %q: %q", want, request.Messages[1].Content)
		}
	}
}

// Histories made only of successful tool results project to nothing, so the
// pre-fix TUI forwarded the raw tool messages and the provider answered with
// empty text ("summarize compacted session: empty summary"). The rendered
// request keeps the tool output in shortened form instead.
func TestCompactSummaryRequestRendersToolOnlyHistory(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "file")
	provider := &emptyUnlessUserProvider{summary: "Resumo das ferramentas."}
	m := newModel(context.Background(), Options{Provider: provider})

	bigOutput := strings.Repeat("tool output line that must be shortened\n", 200)
	messages := []qgruntime.Message{
		{Role: qgruntime.MessageRoleTool, ToolCallID: "call-1", Content: "routine ok"},
		{Role: qgruntime.MessageRoleTool, ToolCallID: "call-2", Content: bigOutput},
	}
	summary, err := m.summarizeCompactionPlan(sessions.CompactionPlan{}, messages)
	if err != nil {
		t.Fatalf("summarizeCompactionPlan() = %v", err)
	}
	if summary.SummaryText != provider.summary {
		t.Fatalf("summary = %q, want %q", summary.SummaryText, provider.summary)
	}
	request := lastSummaryRequest(t, provider)
	assertRenderedSummaryRequest(t, request)
	rendered := request.Messages[1].Content
	if !strings.Contains(rendered, "tool result:") {
		t.Fatalf("rendered summary request must include shortened tool results: %q", rendered)
	}
	if !strings.Contains(rendered, "chars omitted for summarization") {
		t.Fatalf("rendered summary request must clamp long tool output: %q", rendered)
	}
	if strings.Contains(rendered, bigOutput) {
		t.Fatal("rendered summary request must not carry the full tool output")
	}
}

// The exported agent builder is the single source of the summarizer request:
// [system, user] with tool-call arguments and tool results clamped.
func TestAgentCompactionSummaryRequestClampsToolPayloads(t *testing.T) {
	bigArgs := `{"file_path":"main.go","content":"` + strings.Repeat("x", 4000) + `"}`
	bigOutput := strings.Repeat("tool output line\n", 500)
	request := qgruntime.CompletionRequest{Messages: agent.CompactionSummaryRequestMessages([]qgruntime.Message{
		{Role: qgruntime.MessageRoleUser, Content: "Ship it."},
		{Role: qgruntime.MessageRoleAssistant, Content: "Working on it.", ToolCalls: []qgruntime.ToolCall{
			{ID: "write-1", Name: "write_file", Arguments: bigArgs},
		}},
		{Role: qgruntime.MessageRoleTool, ToolCallID: "write-1", Content: bigOutput},
	})}
	assertRenderedSummaryRequest(t, request)
	rendered := request.Messages[1].Content
	if !strings.Contains(rendered, "[tool calls:") || !strings.Contains(rendered, "tool result:") {
		t.Fatalf("rendered request must keep shortened tool calls and results: %q", rendered)
	}
	if strings.Contains(rendered, bigArgs) || strings.Contains(rendered, bigOutput) {
		t.Fatal("rendered request must not carry full tool payloads")
	}
}

// Manual /compact over a tool-heavy history must survive a provider that
// answers non-user-final requests with empty text.
func TestCompactCommandSurvivesToolOnlyHistory(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "file")
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	releaseSessionLocksUnder(t, store.RootDir)
	provider := &emptyUnlessUserProvider{summary: "Resumo das ferramentas executadas."}
	m := newModel(context.Background(), Options{
		ModelName:    "gpt-4.1",
		Provider:     provider,
		SessionStore: store,
	})
	var err error
	m, err = m.ensureActiveSession("compact tool-only history")
	if err != nil {
		t.Fatal(err)
	}
	for range 10 {
		m, err = m.appendSessionEvent(sessions.EventToolResult, map[string]any{
			"toolCallId": "call",
			"name":       "exec",
			"status":     "ok",
			"output":     "routine output",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, content := range []string{
		"recent one", "recent two", "recent three", "recent four",
		"recent five", "recent six", "recent seven", "recent eight",
	} {
		m, err = m.appendSessionEvent(sessions.EventMessage, map[string]any{"role": "user", "content": content})
		if err != nil {
			t.Fatal(err)
		}
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowUser, text: content})
	}
	m.input.SetValue("/compact")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd == nil {
		t.Fatal("expected /compact to start a session-backed compaction command")
	}
	msg := execCmd(cmd)
	if msg == nil {
		t.Fatal("expected compact command to return a completion message")
	}
	updated, cmd = next.Update(msg)
	next = updated.(model)
	if cmd != nil {
		t.Fatal("expected compact completion to be handled without starting another command")
	}
	if next.lastCompactError != "" {
		t.Fatalf("compact failed: %s", next.lastCompactError)
	}
	if next.lastCompactResult == nil || !next.lastCompactResult.Compacted {
		t.Fatalf("expected a compacted session, got %#v", next.lastCompactResult)
	}
	assertRenderedSummaryRequest(t, lastSummaryRequest(t, provider))
}

// The between-tasks compaction of /goal in compact mode funnels through the
// same summarizer request as the manual /compact.
func TestGoalCompactBetweenTasksUsesRenderedSummaryRequest(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "file")
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskScript("feito"),
		goalFillerScript(),
		{
			{Type: qgruntime.StreamEventText, Content: "Resumo entre tarefas."},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := goalTaskTestModel(store, session, provider)
	var err error
	for _, content := range []string{
		"older one", "older two", "older three", "older four", "older five",
		"older six", "older seven", "older eight", "older nine", "older ten",
	} {
		m, err = m.appendSessionEvent(sessions.EventMessage, map[string]any{"role": "user", "content": content})
		if err != nil {
			t.Fatal(err)
		}
	}

	next, cmd := m.launchGoalContinuationIfReady()
	resp := execCmd(cmd)
	updated, goalCmd := updatedModel(t, next, resp)
	if goalCmd == nil {
		t.Fatal("compact-mode switch returned no command")
	}
	compactMsg := execCmd(goalCmd)
	if compactMsg == nil {
		t.Fatal("compaction returned no message")
	}
	after, nextCmd := updatedModel(t, updated, compactMsg)
	_ = nextCmd
	if !after.pending {
		t.Fatal("continuation did not launch after between-tasks compaction")
	}
	if after.lastCompactError != "" {
		t.Fatalf("between-tasks compaction failed: %s", after.lastCompactError)
	}
	if !transcriptContains(after.transcript, "Resumo entre tarefas.") {
		t.Fatalf("between-tasks summary missing from transcript: %#v", after.transcript)
	}
	var summaryRequest *qgruntime.CompletionRequest
	for index := range provider.requests {
		request := provider.requests[index]
		if len(request.Messages) > 0 && request.Messages[0].Content == agent.CompactionSummaryInstructions {
			candidate := provider.requests[index]
			summaryRequest = &candidate
		}
	}
	if summaryRequest == nil {
		t.Fatalf("between-tasks compaction did not call the summarizer: %d requests", len(provider.requests))
	}
	assertRenderedSummaryRequest(t, *summaryRequest)
}
