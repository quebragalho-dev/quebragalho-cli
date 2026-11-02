package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// continuationHistory builds the prior-turn messages for the seed tests: a
// frozen system message followed by a user turn, an assistant tool call with
// its result, and a closing assistant question.
func continuationHistory(system string) []qgruntime.Message {
	return []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: system},
		{Role: qgruntime.MessageRoleUser, Content: "a"},
		{Role: qgruntime.MessageRoleAssistant, Content: "", ToolCalls: []qgruntime.ToolCall{
			{ID: "c1", Name: "bash", Arguments: `{"command":"ls"}`},
		}},
		{Role: qgruntime.MessageRoleTool, Content: "out", ToolCallID: "c1"},
		{Role: qgruntime.MessageRoleAssistant, Content: "pergunta?"},
	}
}

func textAnswerProvider(answer string) *mockProvider {
	return &mockProvider{
		turns: [][]qgruntime.StreamEvent{
			{
				{Type: qgruntime.StreamEventText, Content: answer},
				{Type: qgruntime.StreamEventDone},
			},
		},
	}
}

func TestRunWithContinuationSeedsPriorMessages(t *testing.T) {
	dir := t.TempDir()
	options := Options{Cwd: dir}
	frozen := buildSystemPromptParts(options).prompt
	provider := textAnswerProvider("entendido")

	result, err := Run(context.Background(), "Sim", provider, Options{
		Cwd:          dir,
		Continuation: continuationHistory(frozen),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, want exactly 1", len(provider.requests))
	}
	got := provider.requests[0].Messages
	wantContents := []string{frozen, "a", "", "out", "pergunta?", "Sim"}
	wantRoles := []qgruntime.MessageRole{
		qgruntime.MessageRoleSystem,
		qgruntime.MessageRoleUser,
		qgruntime.MessageRoleAssistant,
		qgruntime.MessageRoleTool,
		qgruntime.MessageRoleAssistant,
		qgruntime.MessageRoleUser,
	}
	if len(got) != len(wantContents) {
		t.Fatalf("request messages = %d, want %d", len(got), len(wantContents))
	}
	for i := range wantContents {
		if got[i].Role != wantRoles[i] || got[i].Content != wantContents[i] {
			t.Fatalf("message %d = (%q, %q), want (%q, %q)", i, got[i].Role, got[i].Content, wantRoles[i], wantContents[i])
		}
	}
	if got[2].ToolCalls == nil || len(got[2].ToolCalls) != 1 || got[2].ToolCalls[0].ID != "c1" {
		t.Fatalf("assistant tool call not preserved: %#v", got[2].ToolCalls)
	}
	if got[3].ToolCallID != "c1" {
		t.Fatalf("tool result call id = %q, want c1", got[3].ToolCallID)
	}
	if len(result.Messages) == 0 || result.Messages[0].Role != qgruntime.MessageRoleSystem || result.Messages[0].Content != frozen {
		t.Fatalf("result must carry the frozen system at [0]")
	}
}

// conciseStyleSection is the product copy responseStyleContext renders for
// ResponseStyle "concise". Hardcoded so the test pins the wire bytes.
const conciseStyleSection = "Response style: concise. Lead with the result and keep answers short and direct; omit preamble, restating the question, and any explanation not needed to be correct."

func TestRunWithContinuationAnnouncesChangedSections(t *testing.T) {
	dir := t.TempDir()
	base := Options{Cwd: dir}
	frozen := buildSystemPromptParts(base).prompt
	provider := textAnswerProvider("ok")

	_, err := Run(context.Background(), "Sim", provider, Options{
		Cwd:           dir,
		ResponseStyle: "concise",
		Continuation:  continuationHistory(frozen),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, want exactly 1", len(provider.requests))
	}
	got := provider.requests[0].Messages
	if len(got) != 6 {
		t.Fatalf("request messages = %d, want 6", len(got))
	}
	if got[0].Role != qgruntime.MessageRoleSystem || got[0].Content != frozen {
		t.Fatalf("system message must stay the frozen one byte-for-byte")
	}
	last := got[5]
	if last.Role != qgruntime.MessageRoleUser {
		t.Fatalf("last message role = %q, want user", last.Role)
	}
	want := "[context update: style]\n" + conciseStyleSection + "\n" + contextUpdateEndMarker + "\n\nSim"
	if last.Content != want {
		t.Fatalf("last user message = %q, want %q", last.Content, want)
	}
}

func TestRunWithContinuationNoChangeHasNoNote(t *testing.T) {
	dir := t.TempDir()
	options := Options{Cwd: dir}
	frozen := buildSystemPromptParts(options).prompt
	provider := textAnswerProvider("ok")

	_, err := Run(context.Background(), "Sim", provider, Options{
		Cwd:          dir,
		Continuation: continuationHistory(frozen),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := provider.requests[0].Messages
	if len(got) != 6 {
		t.Fatalf("request messages = %d, want 6", len(got))
	}
	if last := got[5]; last.Content != "Sim" {
		t.Fatalf("last user message = %q, want the bare prompt", last.Content)
	}
	for _, message := range got {
		if strings.Contains(message.Content, "[context update]") {
			t.Fatalf("unexpected context-update note in %q message", message.Role)
		}
	}
}

// continuationLoaderTool stands in for tool_search: it reports deferred tool
// "T" as loaded through the same Meta["load_tools"] signal.
type continuationLoaderTool struct{}

func (continuationLoaderTool) Name() string { return "tool_search" }
func (continuationLoaderTool) Description() string {
	return "load deferred tool schemas"
}
func (continuationLoaderTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (continuationLoaderTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectNone, Permission: tools.PermissionAllow, AdvertiseInAuto: true}
}
func (continuationLoaderTool) Run(_ context.Context, _ map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "loaded", Meta: map[string]string{"load_tools": "T"}}
}

func TestRunCarriesLoadedDeferredTools(t *testing.T) {
	dir := t.TempDir()
	newRegistry := func() *tools.Registry {
		registry := tools.NewRegistry()
		registry.Register(fakeDeferredTool{name: "T", desc: "tool T full description"})
		registry.Register(continuationLoaderTool{})
		return registry
	}
	loadTurn := []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-1", ToolName: "tool_search"},
		{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-1", ArgumentsFragment: `{}`},
		{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-1"},
		{Type: qgruntime.StreamEventDone},
	}
	answerTurn := []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "done"},
		{Type: qgruntime.StreamEventDone},
	}

	// Run 1 loads T via tool_search.
	firstProvider := &mockProvider{turns: [][]qgruntime.StreamEvent{loadTurn, answerTurn}}
	first, err := Run(context.Background(), "load T", firstProvider, Options{
		Cwd: dir, Registry: newRegistry(), DeferThreshold: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.LoadedDeferredTools) != 1 || first.LoadedDeferredTools[0] != "T" {
		t.Fatalf("run 1 LoadedDeferredTools = %#v, want [T]", first.LoadedDeferredTools)
	}
	if def := toolDefinitionByName(firstProvider.requests[0].Tools, "T"); def != nil && def.Description == "tool T full description" {
		t.Fatalf("run 1 must not expose T's full schema before it is loaded")
	}

	// Run 2 seeds the loaded map: T's full schema is exposed on the 1st request.
	frozen := buildSystemPromptParts(Options{Cwd: dir, DeferThreshold: 1}).prompt
	secondProvider := textAnswerProvider("done")
	_, err = Run(context.Background(), "second", secondProvider, Options{
		Cwd:                 dir,
		Registry:            newRegistry(),
		DeferThreshold:      1,
		Continuation:        []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: frozen}},
		LoadedDeferredTools: []string{"T"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondProvider.requests) != 1 {
		t.Fatalf("run 2 requests = %d, want exactly 1", len(secondProvider.requests))
	}
	def := toolDefinitionByName(secondProvider.requests[0].Tools, "T")
	if def == nil {
		t.Fatalf("run 2 must expose T, got tools %#v", secondProvider.requests[0].Tools)
	}
	if def.Description != "tool T full description" {
		t.Fatalf("run 2 T description = %q, want the full schema", def.Description)
	}
}

// orderRecordingSummarizer returns a Summarizer factory that records how many
// completion requests the main provider had already served when the
// summarizer ran, and serves a fixed summary through its own stub provider.
func orderRecordingSummarizer(main *mockProvider, mainRequestsAtSummarize *int) func(context.Context, string) (Provider, error) {
	return func(context.Context, string) (Provider, error) {
		*mainRequestsAtSummarize = len(main.requests)
		return &mockProvider{turns: [][]qgruntime.StreamEvent{{
			{Type: qgruntime.StreamEventText, Content: "prior work summary"},
			{Type: qgruntime.StreamEventDone},
		}}}, nil
	}
}

func TestRunWithContinuationCompactsBeforeFirstRequest(t *testing.T) {
	dir := t.TempDir()
	options := Options{Cwd: dir, ContextWindow: 1000}
	frozen := buildSystemPromptParts(options).prompt
	bigBody := strings.Repeat("x", 4000)
	continuation := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: frozen},
		{Role: qgruntime.MessageRoleUser, Content: "a1"},
		{Role: qgruntime.MessageRoleAssistant, Content: "r1"},
		{Role: qgruntime.MessageRoleUser, Content: "a2"},
		{Role: qgruntime.MessageRoleAssistant, Content: "r2"},
		{Role: qgruntime.MessageRoleUser, Content: "a3"},
		{Role: qgruntime.MessageRoleAssistant, Content: "r3"},
		{Role: qgruntime.MessageRoleUser, Content: "a4"},
		{Role: qgruntime.MessageRoleAssistant, Content: "r4"},
		{Role: qgruntime.MessageRoleUser, Content: bigBody},
		{Role: qgruntime.MessageRoleAssistant, Content: "r5"},
	}
	provider := textAnswerProvider("ok")
	mainRequestsAtSummarize := -1

	_, err := Run(context.Background(), "Sim", provider, Options{
		Cwd:           dir,
		ContextWindow: 1000,
		Summarizer:    orderRecordingSummarizer(provider, &mainRequestsAtSummarize),
		Continuation:  continuation,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mainRequestsAtSummarize != 0 {
		t.Fatalf("summarizer ran after %d main requests, want before the 1st", mainRequestsAtSummarize)
	}
	if len(provider.requests) == 0 {
		t.Fatalf("expected at least one completion request after compaction")
	}
}

func TestRunWithoutContinuationUnchanged(t *testing.T) {
	dir := t.TempDir()
	options := Options{Cwd: dir}
	wantSystem := buildSystemPromptParts(options).prompt
	provider := textAnswerProvider("ok")

	result, err := Run(context.Background(), "hello", provider, Options{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, want exactly 1", len(provider.requests))
	}
	got := provider.requests[0].Messages
	if len(got) != 2 {
		t.Fatalf("request messages = %d, want [system, user]", len(got))
	}
	if got[0].Role != qgruntime.MessageRoleSystem || got[0].Content != wantSystem {
		t.Fatalf("system message drifted from buildSystemPromptParts")
	}
	if got[1].Role != qgruntime.MessageRoleUser || got[1].Content != "hello" {
		t.Fatalf("user message = (%q, %q), want (user, hello)", got[1].Role, got[1].Content)
	}
	if len(result.Messages) != 3 || result.Messages[0].Role != qgruntime.MessageRoleSystem {
		t.Fatalf("result must start with the system message")
	}
	if result.Messages[2].Role != qgruntime.MessageRoleAssistant || result.Messages[2].Content != "ok" {
		t.Fatalf("result assistant message = (%q, %q), want (assistant, ok)", result.Messages[2].Role, result.Messages[2].Content)
	}
}
