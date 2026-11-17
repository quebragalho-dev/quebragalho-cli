package tui

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// Concluir a última tarefa não deve acionar o modo entre tarefas no modo
// compact (padrão): sem compactação, sem linha "Compactando" e sem comando
// de continuação, mantendo só a linha "Meta concluída".
func TestGoalFinalTaskDoesNotCompact(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	if _, _, err := store.CompleteGoalTask(session.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskScript("feito"),
		{
			{Type: qgruntime.StreamEventText, Content: "resumo final"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	compactor := &fakeGoalCompactor{}
	m := goalTaskTestModel(store, *loaded, provider)
	m.sessionCompactor = compactor

	next, cmd := m.launchGoalContinuationIfReady()
	if cmd == nil || !next.pending {
		t.Fatal("last task run did not launch")
	}
	resp := execCmd(cmd)
	if resp == nil {
		t.Fatal("last run returned no response")
	}
	// A última tarefa fecha a meta e o modelo escreve o resumo no mesmo run:
	// uma chamada para o update_goal e outra para o texto final.
	if len(provider.requests) != 2 {
		t.Fatalf("provider requests = %d, want 2 (task turn plus final summary)", len(provider.requests))
	}
	updated, goalCmd := updatedModel(t, next, resp)
	if !transcriptContains(updated.transcript, "Meta concluída: 2 de 2 tarefas feitas.") {
		t.Fatalf("completion line missing: %#v", updated.transcript)
	}
	if updated.compactRequests != 0 {
		t.Fatalf("compactRequests = %d, want 0 (finished goal must not compact)", updated.compactRequests)
	}
	if compactor.calls != 0 {
		t.Fatalf("compactor calls = %d, want 0 (finished goal must not compact)", compactor.calls)
	}
	if transcriptContains(updated.transcript, "Compactando") {
		t.Fatalf("compact notice present on final task: %#v", updated.transcript)
	}
	// Nenhum passo seguinte: sem compactação pendente e sem continuação (a
	// meta está completa). Comandos de fundo (sweep/título) voltam vazios.
	requestsBefore := len(provider.requests)
	if msg := execCmd(goalCmd); msg != nil {
		t.Fatalf("finished goal launched a follow-up command: %T", msg)
	}
	if len(provider.requests) != requestsBefore {
		t.Fatalf("provider requests = %d, want %d (no follow-up request)", len(provider.requests), requestsBefore)
	}
	if compactor.calls != 0 {
		t.Fatalf("compactor calls = %d, want 0 (no follow-up compaction)", compactor.calls)
	}
	if updated.pending {
		t.Fatal("finished goal must not stay pending")
	}
}

// Concluir a última tarefa não deve mostrar a linha do modo reset, pois não
// há próxima tarefa.
func TestGoalFinalTaskDoesNotReset(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	if _, _, err := store.CompleteGoalTask(session.SessionID, ""); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Get(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskScript("feito"),
		{
			{Type: qgruntime.StreamEventText, Content: "resumo final"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := goalTaskTestModel(store, *loaded, provider)
	m.goalBetweenTasks = "reset"

	next, cmd := m.launchGoalContinuationIfReady()
	if cmd == nil || !next.pending {
		t.Fatal("last task run did not launch")
	}
	resp := execCmd(cmd)
	if resp == nil {
		t.Fatal("last run returned no response")
	}
	updated, goalCmd := updatedModel(t, next, resp)
	if !transcriptContains(updated.transcript, "Meta concluída: 2 de 2 tarefas feitas.") {
		t.Fatalf("completion line missing: %#v", updated.transcript)
	}
	if transcriptContains(updated.transcript, "Contexto limpo entre tarefas") {
		t.Fatalf("reset notice present on final task: %#v", updated.transcript)
	}
	requestsBefore := len(provider.requests)
	if msg := execCmd(goalCmd); msg != nil {
		t.Fatalf("finished goal launched a follow-up command: %T", msg)
	}
	if len(provider.requests) != requestsBefore {
		t.Fatalf("provider requests = %d, want %d (no follow-up request)", len(provider.requests), requestsBefore)
	}
	if updated.pending {
		t.Fatal("finished goal must not stay pending")
	}
}

// Uma troca no meio da meta continua compactando normalmente.
func TestGoalMidTaskStillCompacts(t *testing.T) {
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskScript("feito"),
		goalFillerScript(),
		{
			{Type: qgruntime.StreamEventText, Content: "second task"},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	compactor := &fakeGoalCompactor{}
	m := goalTaskTestModel(store, session, provider)
	m.sessionCompactor = compactor

	next, cmd := m.launchGoalContinuationIfReady()
	resp := execCmd(cmd)
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 (run ends at update_goal)", len(provider.requests))
	}
	updated, goalCmd := updatedModel(t, next, resp)
	if goalCmd == nil {
		t.Fatal("mid-goal switch returned no command")
	}
	if updated.compactRequests != 1 {
		t.Fatalf("compactRequests = %d, want 1 (mid-goal switch compacts)", updated.compactRequests)
	}
	compactMsg := execCmd(goalCmd)
	if compactor.calls != 1 {
		t.Fatalf("compactor calls = %d, want 1 (mid-goal switch compacts)", compactor.calls)
	}
	if compactMsg == nil {
		t.Fatal("compaction returned no message")
	}
	after, _ := updatedModel(t, updated, compactMsg)
	if !after.pending {
		t.Fatal("continuation did not launch after between-tasks compaction")
	}
	if !transcriptContains(after.transcript, "Compactando contexto antes da próxima tarefa") {
		t.Fatalf("compact notice missing: %#v", after.transcript)
	}
}
