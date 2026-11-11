package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func sessionSnapshotPath(store *sessions.Store, m model) string {
	return filepath.Join(store.RootDir, m.activeSession.SessionID, sessions.ContinuationFile)
}

func TestAgoraDuringTurnIntercalatesTextNow(t *testing.T) {
	store := testSessionStore(t)
	release := make(chan struct{})
	registry := tools.NewRegistry()
	registry.Register(deafEchoTool{release: release})
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		{
			{Type: qgruntime.StreamEventToolCallStart, ToolCallID: "call-slow", ToolName: "slow_echo"},
			{Type: qgruntime.StreamEventToolCallDelta, ToolCallID: "call-slow", ArgumentsFragment: `{}`},
			{Type: qgruntime.StreamEventToolCallEnd, ToolCallID: "call-slow"},
			{Type: qgruntime.StreamEventDone},
		},
		{
			{Type: qgruntime.StreamEventText, Content: "interleaved answer"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := continuationHarness(t, store, provider, registry)
	m.width = 96

	m.input.SetValue("run A")
	updated, cmdA := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if cmdA == nil {
		t.Fatal("expected an agent command for turn A")
	}
	doneA := make(chan tea.Msg, 1)
	go func() { doneA <- execCmd(cmdA) }()
	waitForSnapshotCall(t, sessionSnapshotPath(store, m), "call-slow")
	m.yieldNow = &fakeSendNowSignal{result: true}

	m.input.SetValue("/agora MARKAGORA interleaved by the command")
	updated, _ = m.Update(testKey(tea.KeyEnter))
	m = updated.(model)

	if m.hasQueuedMessage() {
		t.Fatalf("/agora must not fall into the queue as text, queue = %v", m.queuedMessages)
	}
	if got := m.composerValue(); got != "" {
		t.Fatalf("expected /agora to consume the composer, got %q", got)
	}

	close(release)
	late := <-doneA
	updated, _ = m.Update(late)
	m = updated.(model)

	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (turn + interleaved)", len(provider.requests))
	}
	found := false
	for _, message := range provider.requests[1].Messages {
		if message.Role == qgruntime.MessageRoleUser && strings.Contains(message.Content, "MARKAGORA interleaved by the command") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the /agora message must reach the provider mid-turn, got %#v", provider.requests[1].Messages)
	}
	if !transcriptContains(m.transcript, "MARKAGORA interleaved by the command") {
		t.Fatal("expected the /agora message as a transcript row")
	}
}

func TestAgoraDuringTurnWithoutArgKeepsQueue(t *testing.T) {
	m := continuationHarness(t, testSessionStore(t), &scriptedProvider{scripts: textScripts("answer")}, nil)
	m.width = 96
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.queuedMessages = []string{"/tools"}

	m.input.SetValue("/agora")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	// Send-now leads only common messages; with none pending, /agora has
	// nothing to send and says so. The FIFO waits for the turn to end.
	if !transcriptContains(next.transcript, "Nada na fila para intercalar.") {
		t.Fatalf("expected the short notice, got transcript %#v", next.transcript)
	}
	if got := next.queuedMessages; len(got) != 1 {
		t.Fatalf("the FIFO must stay untouched, got %v", got)
	}
	if next.interleave != nil && next.interleave.pendingCount() != 0 {
		t.Fatalf("nothing may be staged for the running turn, got %v", next.interleave.snapshot())
	}
}

func TestAgoraDuringTurnWithEmptyQueueShowsShortNotice(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.input.SetValue("/agora")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if !transcriptContains(next.transcript, "Nada na fila") {
		t.Fatalf("expected a short notice for the empty queue, got transcript %#v", next.transcript)
	}
	if next.interleave != nil && len(next.interleave.snapshot()) != 0 {
		t.Fatal("nothing must be staged when the queue is empty")
	}
}

func TestAgoraWithoutTurnSendsPromptNormally(t *testing.T) {
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "hello back"},
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.input.SetValue("/agora MARKHELLO plain prompt")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if !next.pending {
		t.Fatal("expected /agora without a turn to launch a run")
	}
	_ = execCmd(cmd)
	if len(provider.requests) != 1 {
		t.Fatalf("expected one provider request, got %d", len(provider.requests))
	}
	messages := provider.requests[0].Messages
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1].Content, "MARKHELLO plain prompt") {
		t.Fatalf("expected the plain prompt as the provider's last message, got %#v", messages)
	}
}

func TestAgoraWithoutTurnAndNoTextShowsShortNotice(t *testing.T) {
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.input.SetValue("/agora")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if next.pending {
		t.Fatal("a bare /agora without a turn must not launch a run")
	}
	if len(provider.requests) != 0 {
		t.Fatalf("expected no provider request, got %d", len(provider.requests))
	}
	if !transcriptContains(next.transcript, "Nenhum turno em andamento") {
		t.Fatalf("expected a short notice, got transcript %#v", next.transcript)
	}
}

func TestAgoraDuringTurnRejectsSlashCommandArgument(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.input.SetValue("/agora /model gpt-4o")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if !transcriptContains(next.transcript, "Só prompts comuns") {
		t.Fatalf("expected a short notice refusing the slash command, got transcript %#v", next.transcript)
	}
	if next.modelName != "gpt-4.1" && next.modelName != "" {
		t.Fatalf("/agora must not execute the slash command, model = %q", next.modelName)
	}
	if next.interleave != nil && len(next.interleave.snapshot()) != 0 {
		t.Fatal("a slash command must never be staged as an interleave")
	}
}

func TestAgoraWithSpecialistMentionDuringTurnQueues(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.agentOptions.Specialists = []agent.SpecialistInfo{{Name: "reviewer"}}
	m.input.SetValue("/agora @reviewer fix this")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if len(next.queuedMessages) != 1 || next.queuedMessages[0] != "@reviewer fix this" {
		t.Fatalf("expected the specialist mention queued for the next turn, got %#v", next.queuedMessages)
	}
	if next.interleave != nil && len(next.interleave.snapshot()) != 0 {
		t.Fatal("a leading @specialist mention must not be interleaved unexpanded")
	}
}

func TestAgoraDuringTurnWithAttachmentsShowsShortNotice(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.pendingImages = []qgruntime.ImageBlock{previewImageBlock(t)}
	m.input.SetValue("/agora analyze this")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if !transcriptContains(next.transcript, "Anexos não vão com /agora") {
		t.Fatalf("expected the attachments notice, got transcript %#v", next.transcript)
	}
	if len(next.pendingImages) != 1 {
		t.Fatal("the staged image must stay pending")
	}
}

func TestAgoraRegisteredInHelpAndCommands(t *testing.T) {
	if command, ok := resolveCommand("/agora"); !ok {
		t.Fatal("/agora must resolve as a command")
	} else if command.kind != commandAgora {
		t.Fatalf("/agora must resolve to commandAgora, got kind %v", command.kind)
	}
	if !strings.Contains(helpText(), "/agora") {
		t.Fatal("/agora must appear in the help text")
	}
	if !strings.Contains(formatGroupedCommandHelp(), "/agora [texto]") {
		t.Fatal("/agora must appear in the grouped command help")
	}
}

func TestAgoraWithAttachmentsAndEmptyComposerSendsQueueHead(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.pendingImages = []qgruntime.ImageBlock{previewImageBlock(t)}
	m.queuedMessages = []string{"/tools"}
	m.yieldNow = &fakeSendNowSignal{result: true}
	m.input.SetValue("/agora")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if transcriptContains(next.transcript, "Anexos não vão com /agora") {
		t.Fatal("an empty composer carries no attachments; the notice must not fire")
	}
	if len(next.queuedMessages) != 1 {
		t.Fatalf("the FIFO must stay untouched, queue = %v", next.queuedMessages)
	}
	if next.interleave != nil && next.interleave.pendingCount() != 0 {
		t.Fatalf("nothing may be staged for the running turn, got %v", next.interleave.snapshot())
	}
	if len(next.pendingImages) != 1 {
		t.Fatal("the staged image must stay pending")
	}
}

func TestAgoraWithoutTurnDoesNotDuplicateHistory(t *testing.T) {
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "hello back"},
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.input.SetValue("/agora MARKSIDE plain prompt")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	count := 0
	for _, entry := range next.inputHistory {
		if strings.Contains(entry, "MARKSIDE plain prompt") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one history entry containing the prompt, got %d: %v", count, next.inputHistory)
	}
}

func TestAgoraWithoutTurnRejectsSlashCommandArgument(t *testing.T) {
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "hello back"},
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		ProviderName: "openai",
		ModelName:    "gpt-4.1",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.input.SetValue("/agora /model gpt-4o")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if !transcriptContains(next.transcript, "Só prompts comuns") {
		t.Fatalf("expected the refusal notice, got transcript %#v", next.transcript)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("the slash command must never reach the provider as raw prompt text, got %d request(s)", len(provider.requests))
	}
	if next.pending {
		t.Fatal("a refused /agora must not start a run")
	}
}
