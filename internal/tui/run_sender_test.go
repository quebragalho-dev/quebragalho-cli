package tui

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// senderProbeMsg carries an order marker through a programSender.
type senderProbeMsg struct{ seq int }

// senderProbeDoneMsg ends the probe program.
type senderProbeDoneMsg struct{}

type senderProbeModel struct {
	got []int
}

func (m *senderProbeModel) Init() tea.Cmd { return nil }

func (m *senderProbeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case senderProbeMsg:
		m.got = append(m.got, msg.seq)
	case senderProbeDoneMsg:
		return m, tea.Quit
	}
	return m, nil
}

func (m *senderProbeModel) View() tea.View { return tea.NewView("") }

// TestProgramSenderDeliversPreRunMessagesInOrder pins the startup contract:
// sends issued before the event loop starts must neither block their caller
// nor get lost or reordered. A direct program.Send here would hang the test
// until the go test timeout fires (fatal "all goroutines are asleep" in a
// real launch), because the unbuffered message channel has no reader yet.
func TestProgramSenderDeliversPreRunMessagesInOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var in bytes.Buffer
	var out bytes.Buffer
	program := tea.NewProgram(&senderProbeModel{},
		tea.WithContext(ctx),
		tea.WithInput(&in),
		tea.WithOutput(&out),
	)
	sender := newProgramSender(program)
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		sender.send(senderProbeMsg{seq: 1})
		sender.send(senderProbeMsg{seq: 2})
		sender.send(senderProbeMsg{seq: 3})
		sender.send(senderProbeDoneMsg{})
	}()
	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("sends before Run must return immediately, not wait for the event loop")
	}
	final, err := program.Run()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := final.(*senderProbeModel).got
	if !slices.Equal(got, []int{1, 2, 3}) {
		t.Fatalf("delivered out of order or lost: got %v, want [1 2 3]", got)
	}
}
