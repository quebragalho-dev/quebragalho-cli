// Send-now scenarios pinned from the Sonda audit: slash commands and
// @specialist mentions enqueued mid-turn must never become a raw user
// message on the wire via Ctrl+Enter / /agora. Common prompts (including
// stranded ones requeued by a cancel) ride the send, in order.
package tui

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func sendNowScenarioModel(t *testing.T, queue []string) model {
	t.Helper()
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.agentOptions.Specialists = []agent.SpecialistInfo{{Name: "reviewer"}}
	m.queuedMessages = queue
	return m
}

func lastProviderUserMessages(provider *fakeProvider) []string {
	if len(provider.requests) == 0 {
		return nil
	}
	var out []string
	for _, message := range provider.requests[len(provider.requests)-1].Messages {
		if message.Role == qgruntime.MessageRoleUser {
			out = append(out, message.Content)
		}
	}
	return out
}

// Caminho FALSE (sem signal → interrompe e relança): "/model x" enfileirado
// durante o turno + Ctrl+Enter NÃO pode ir cru ao provider.
func TestSendNowNoSignalDoesNotSendRawSlashCommand(t *testing.T) {
	m := sendNowScenarioModel(t, []string{"/model gpt-4o"})
	m.provider = &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "answer"},
		{Type: qgruntime.StreamEventDone},
	}}
	cancelled := false
	m.runCancel = func() { cancelled = true }
	m.input.SetValue("MARKDRAFT plain")

	updated, cmd := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)
	if !cancelled {
		t.Fatal("precond: sem signal, o turno deve ser interrompido")
	}
	if cmd != nil {
		_ = execCmd(cmd)
	}
	provider := m.provider.(*fakeProvider)
	for _, content := range lastProviderUserMessages(provider) {
		if strings.Contains(content, "/model") {
			t.Errorf("BLOQUEANTE: slash command cru chegou ao provider via send-now sem signal: %q", content)
		}
	}
	// O slash command não pode sumir: deve continuar na FIFO para o fim do turno.
	found := false
	for _, q := range next.queuedMessages {
		if strings.Contains(q, "/model") {
			found = true
		}
	}
	if !found {
		t.Errorf("BLOQUEANTE: slash command não ficou na FIFO após send-now: queue=%v", next.queuedMessages)
	}
}

// Caminho TRUE (signal confirma → tudo para o buffer): "/model x" enfileirado
// + Ctrl+Enter NÃO pode ir ao buffer de intercalação.
func TestSendNowConfirmingSignalDoesNotBufferRawSlashCommand(t *testing.T) {
	m := sendNowScenarioModel(t, []string{"/model gpt-4o"})
	m.yieldNow = &fakeSendNowSignal{result: true}
	m.input.SetValue("MARKDRAFT plain")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if next.interleave != nil {
		for _, text := range next.interleave.snapshot() {
			if strings.Contains(text, "/model") {
				t.Errorf("BLOQUEANTE: slash command cru entrou no buffer de intercalação: %q", text)
			}
		}
	}
	found := false
	for _, q := range next.queuedMessages {
		if strings.Contains(q, "/model") {
			found = true
		}
	}
	if !found {
		t.Errorf("BLOQUEANTE: slash command não ficou na FIFO após send-now: queue=%v", next.queuedMessages)
	}
}

// Caminho FALSE: "@reviewer y" enfileirado + Ctrl+Enter NÃO pode ir sem a
// expansão de especialista (o launchPrompt aplica; o send-now não).
func TestSendNowNoSignalDoesNotSendRawSpecialistMention(t *testing.T) {
	m := sendNowScenarioModel(t, []string{"@reviewer fix this"})
	m.provider = &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "answer"},
		{Type: qgruntime.StreamEventDone},
	}}
	m.input.SetValue("MARKDRAFT plain")

	updated, cmd := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)
	if cmd != nil {
		_ = execCmd(cmd)
	}
	provider := m.provider.(*fakeProvider)
	for _, content := range lastProviderUserMessages(provider) {
		if strings.Contains(content, "@reviewer") {
			t.Errorf("BLOQUEANTE: @especialista cru chegou ao provider sem expansão via send-now: %q", content)
		}
	}
	found := false
	for _, q := range next.queuedMessages {
		if strings.Contains(q, "@reviewer") {
			found = true
		}
	}
	if !found {
		t.Errorf("BLOQUEANTE: @especialista não ficou na FIFO após send-now: queue=%v", next.queuedMessages)
	}
}

// Caminho TRUE: "@reviewer y" enfileirado + Ctrl+Enter NÃO pode ir ao buffer.
func TestSendNowConfirmingSignalDoesNotBufferRawSpecialistMention(t *testing.T) {
	m := sendNowScenarioModel(t, []string{"@reviewer fix this"})
	m.yieldNow = &fakeSendNowSignal{result: true}
	m.input.SetValue("MARKDRAFT plain")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)

	if next.interleave != nil {
		for _, text := range next.interleave.snapshot() {
			if strings.Contains(text, "@reviewer") {
				t.Errorf("BLOQUEANTE: @especialista cru entrou no buffer de intercalação: %q", text)
			}
		}
	}
	found := false
	for _, q := range next.queuedMessages {
		if strings.Contains(q, "@reviewer") {
			found = true
		}
	}
	if !found {
		t.Errorf("BLOQUEANTE: @especialista não ficou na FIFO após send-now: queue=%v", next.queuedMessages)
	}
}

// Controle: prompt comum na FIFO (caso legítimo: veio de sobras reenfileiradas
// por cancel) + draft DEVE ir pelo send-now, em ordem. Este passa já em
// f677f90b e deve continuar passando após a correção.
func TestSendNowStillSendsPlainQueueAndDraft(t *testing.T) {
	m := sendNowScenarioModel(t, []string{"MARKQUEUED plain"})
	m.provider = &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "answer"},
		{Type: qgruntime.StreamEventDone},
	}}
	m.runCancel = func() {}
	m.input.SetValue("MARKDRAFT plain")

	updated, _ := m.Update(testInterleaveEnter(t, true))
	next := updated.(model)
	// The batch parks until the cancelled run drains; drive the drain.
	flushed, flushCmd := next.Update(agentResponseMsg{runID: 1})
	_ = flushed
	if flushCmd == nil {
		t.Fatal("expected the parked send-now batch to launch when the cancelled run drains")
	}
	_ = execCmd(flushCmd)
	provider := m.provider.(*fakeProvider)
	got := lastProviderUserMessages(provider)
	want := []string{"MARKQUEUED plain", "MARKDRAFT plain"}
	if len(got) != len(want) {
		t.Fatalf("PROBE controle: user messages = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PROBE controle: msg %d = %q, want %q", i, got[i], want[i])
		}
	}
}
