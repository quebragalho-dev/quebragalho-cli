package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

func TestBatchFiveSmallCostIsNotFreeAndCounterIsHidden(t *testing.T) {
	m := batchThreeLiveModel(t)
	row := callUsageRow(1, 7, m.modelName, qgruntime.Usage{InputTokens: 1000, OutputTokens: 100}, *m.apiModel(m.modelName))
	row.text = "Chamada 7 · " + m.modelName
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		got := plainRender(t, renderCallUsage(row, width))
		assertContains(t, got, "< R$ 0,01")
		assertContains(t, got, m.modelName)
		assertNotContains(t, got, "Chamada")
	}
	for _, tc := range []struct{ amount, currency, want string }{{"0", "BRL", "R$ 0,00"}, {"1/20", "BRL", "R$ 0,05"}, {"1/1000", "USD", "< US$ 0.01"}, {"", "BRL", "custo não informado"}} {
		row.meta["amount"], row.meta["currency"] = tc.amount, tc.currency
		assertContains(t, plainRender(t, renderCallUsage(row, 120)), tc.want)
	}
}

func TestBatchFiveRightComposerAvoidsUnicodeRuleOverdraw(t *testing.T) {
	m := interactivePetTestModel(t)
	m.modelName = "muse-spark-1.3-contributor"
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		m.width = width
		got := plainRender(t, m.composerDividerLine(width))
		before, after, ok := strings.Cut(got, m.modelName)
		if !ok || strings.Contains(before, "─") || strings.ContainsAny(after, "─-") || !strings.Contains(before, "-") {
			t.Fatalf("unsafe or left-aligned model at %d: %q", width, got)
		}
		assertRenderedLineWidths(t, got, width)
	}
}

func TestBatchFiveUsageCommandSurvivesClearWithFrozenCost(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.provider = &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{InputTokens: 12000, OutputTokens: 771}}, {Type: qgruntime.StreamEventDone}}}}
	m.activeRunID = 1
	response := execCmd(m.runAgentWithOptions(1, context.Background(), "teste", nil, tuiAgentRunOptions{}))
	next, _ := m.Update(response)
	m = next.(model)
	updated, _ := m.dispatchCommand(parseCommand("/clear"))
	m = updated.(model)
	m.modelPickerLiveByProvider["qg"][0].APIModel.PricingBRL.Input = "999"
	updated, _ = m.dispatchCommand(parseCommand("/usage"))
	m = updated.(model)
	text := plainRender(t, m.pickerOverlay(120))
	for _, want := range []string{"12.771", m.modelName, "R$ 0,02", "fallback/docs"} {
		assertContains(t, text, want)
	}
	assertNotContains(t, plainRender(t, m.statusLine(120)), "12.771 tok")
	assertNotContains(t, plainRender(t, m.statusLine(120)), "toks/s")
	assertContains(t, helpText(), "/usage")
}

func TestBatchFiveEffortAutoExplainsOmittedParameter(t *testing.T) {
	m := batchThreeLiveModel(t)
	_, text := m.handleEffortCommand("auto")
	assertContains(t, plainRender(t, text), "não envia reasoning_effort")
	assertContains(t, plainRender(t, m.effortText()), "preferência local")
}

func TestBatchFiveUsageSnapshotReplacesUnknownAndResumesExactCost(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.activeRunID = 1
	event := qgruntime.Usage{InputTokens: 1000, OutputTokens: 100}
	row := callUsageRow(1, 1, m.modelName, event, *m.apiModel(m.modelName))
	next, _ := m.Update(agentUsageMsg{runID: 1, modelID: m.modelName, usage: event, receipt: row})
	m = next.(model)
	assertContains(t, batchSixUsageText(m), "< R$ 0,01")
	next, _ = m.Update(agentUsageMsg{runID: 1, modelID: "unpriced", usage: qgruntime.Usage{InputTokens: 1}})
	m = next.(model)
	assertContains(t, batchSixUsageText(m), "custo não informado")
	assertNotContains(t, batchSixUsageText(m), "< R$")
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "usage-price"})
	if err != nil {
		t.Fatal(err)
	}
	appendTestEvent(t, store, session.SessionID, sessions.EventUsage, map[string]any{"promptTokens": 12000, "completionTokens": 771, "model": "historic-model", "cost_exact": "1/20", "cost_currency": "BRL"})
	m.sessionStore = store
	m, notice := m.handleResumeCommand(session.SessionID)
	if notice != "" {
		t.Fatal(notice)
	}
	text := batchSixUsageText(m)
	for _, want := range []string{"historic-model", "12.771", "R$ 0,05"} {
		assertContains(t, text, want)
	}
	m = m.startNewSession()
	assertNotContains(t, batchSixUsageText(m), "R$ 0,05")
}

func TestBatchFiveCancelledOldRunDoesNotReplaceActiveUsage(t *testing.T) {
	m := batchThreeLiveModel(t)
	store := testSessionStore(t)
	old, err := store.Create(sessions.CreateInput{SessionID: "old"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.Create(sessions.CreateInput{SessionID: "current"})
	if err != nil {
		t.Fatal(err)
	}
	m.sessionStore, m.activeSession = store, current
	m.activeRunID = 2
	m.flushRunIDs = map[int]string{1: old.SessionID}
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: 12}, transcriptRow{})
	next, _ := m.Update(agentResponseMsg{runID: 1, usageModelID: m.modelName, usageEvents: []qgruntime.Usage{{InputTokens: 999}}})
	if next.(model).lastUsage.TotalTokens != 12 {
		t.Fatalf("old run contaminated active session: %+v", next.(model).lastUsage)
	}
}

func TestBatchFiveUsageSurvivesCompactedResume(t *testing.T) {
	m := batchThreeLiveModel(t)
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "compacted-usage"})
	if err != nil {
		t.Fatal(err)
	}
	appendTestEvent(t, store, session.SessionID, sessions.EventUsage, map[string]any{"promptTokens": 12000, "completionTokens": 771, "model": "historic-model", "cost_exact": "1/20", "cost_currency": "BRL"})
	for i := 0; i < 8; i++ {
		appendTestEvent(t, store, session.SessionID, sessions.EventMessage, map[string]string{"role": "assistant", "content": "after usage"})
	}
	plan, err := store.PlanCompaction(session.SessionID, sessions.CompactionOptions{PreserveLast: 6, MaxPromptChars: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordCompaction(session.SessionID, sessions.RecordCompactionInput{Plan: plan, Summary: "compacted"}); err != nil {
		t.Fatal(err)
	}
	m.sessionStore = store
	m, notice := m.handleResumeCommand(session.SessionID)
	if notice != "" {
		t.Fatal(notice)
	}
	for _, want := range []string{"historic-model", "12.771", "R$ 0,05"} {
		assertContains(t, batchSixUsageText(m), want)
	}
}
