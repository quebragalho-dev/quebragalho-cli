package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// compactionUsageEvents returns every persisted provider_usage event payload.
func compactionUsageEvents(t *testing.T, store *sessions.Store, sessionID string) []map[string]any {
	t.Helper()
	events, err := store.ReadEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, event := range events {
		if event.Type != sessions.EventUsage {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, payload)
	}
	return out
}

// liveUsageReceipts returns the live transcript's model-usage receipt rows.
func liveUsageReceipts(rows []transcriptRow) []transcriptRow {
	var out []transcriptRow
	for _, row := range rows {
		if row.tool == "model-usage" {
			out = append(out, row)
		}
	}
	return out
}

// resumedUsageReceipts rehydrates transcript rows from persisted events,
// like a session resume does, and returns the model-usage receipt rows.
func resumedUsageReceipts(t *testing.T, store *sessions.Store, sessionID string) []transcriptRow {
	t.Helper()
	events, err := store.ReadEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return liveUsageReceipts(transcriptRowsFromSessionEvents(events))
}

func seedCompactableHistory(t *testing.T, m model, count int) model {
	t.Helper()
	var err error
	for i := 0; i < count; i++ {
		content := "compactable history line for usage accounting"
		m, err = m.appendSessionEvent(sessions.EventMessage, map[string]any{"role": "user", "content": content})
		if err != nil {
			t.Fatal(err)
		}
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowUser, text: content})
	}
	return m
}

func runCompactCommand(t *testing.T, m model) model {
	t.Helper()
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
	return next
}

// A manual /compact whose summarizer call reports usage must record it
// through the agent path: a provider_usage session event with the summary
// tokens (labeled as compaction) and the cost in the usage tracker.
func TestCompactRecordsSummarizerUsage(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "file")
	store := testSessionStore(t)
	const inputTokens, outputTokens = 1200, 80
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "Resumo com uso."},
		{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{InputTokens: inputTokens, OutputTokens: outputTokens}},
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		ModelName:    "muse-spark-1.3-contributor",
		Provider:     provider,
		SessionStore: store,
	})
	var err error
	m, err = m.ensureActiveSession("compact records usage")
	if err != nil {
		t.Fatal(err)
	}
	m = seedCompactableHistory(t, m, 12)
	before := m.usageTracker.Summary()
	if before.RecordCount != 0 {
		t.Fatalf("expected empty usage tracker before compact, got %#v", before)
	}
	m.input.SetValue("/compact")

	next := runCompactCommand(t, m)
	if next.lastCompactError != "" {
		t.Fatalf("compact failed: %s", next.lastCompactError)
	}
	if next.lastCompactResult == nil || !next.lastCompactResult.Compacted {
		t.Fatalf("expected a compacted session, got %#v", next.lastCompactResult)
	}

	payloads := compactionUsageEvents(t, store, next.activeSession.SessionID)
	if len(payloads) != 1 {
		t.Fatalf("expected 1 provider_usage event, got %d", len(payloads))
	}
	payload := payloads[0]
	if payload["promptTokens"] != float64(inputTokens) || payload["completionTokens"] != float64(outputTokens) {
		t.Fatalf("provider_usage tokens = %v/%v, want %d/%d: %#v",
			payload["promptTokens"], payload["completionTokens"], inputTokens, outputTokens, payload)
	}
	if payload["call_label"] != "muse-spark-1.3-contributor" {
		t.Fatalf("provider_usage call_label = %v, want the model id: %#v", payload["call_label"], payload)
	}
	if payload["model"] != "muse-spark-1.3-contributor" {
		t.Fatalf("provider_usage model = %v, want the compacting model: %#v", payload["model"], payload)
	}
	if payload["summarizer"] != true {
		t.Fatalf("provider_usage must carry summarizer=true: %#v", payload)
	}

	receipts := liveUsageReceipts(next.transcript)
	if len(receipts) != 1 {
		t.Fatalf("expected 1 live usage receipt, got %d: %#v", len(receipts), next.transcript)
	}
	if receipts[0].text != "muse-spark-1.3-contributor" {
		t.Fatalf("live receipt text = %q, want the model id", receipts[0].text)
	}

	resumed := resumedUsageReceipts(t, store, next.activeSession.SessionID)
	if len(resumed) != 1 {
		t.Fatalf("expected 1 resumed usage receipt, got %d: %#v", len(resumed), resumed)
	}
	if resumed[0].text != "muse-spark-1.3-contributor" {
		t.Fatalf("resumed receipt text = %q, want the model id", resumed[0].text)
	}
	for _, row := range resumed {
		if strings.Contains(strings.ToLower(row.text), "compaction") {
			t.Fatalf("resumed receipt must not mention compaction: %#v", resumed)
		}
	}

	summary := next.usageTracker.Summary()
	if summary.RecordCount != 1 {
		t.Fatalf("expected 1 usage record, got %#v", summary)
	}
	if summary.TotalTokens != inputTokens+outputTokens {
		t.Fatalf("tracker tokens = %d, want %d", summary.TotalTokens, inputTokens+outputTokens)
	}
	if summary.TotalCost <= 0 {
		t.Fatalf("tracker cost = %v, want the summary cost counted: %#v", summary.TotalCost, summary)
	}
	if len(next.cacheCalls) != 0 {
		t.Fatalf("compaction usage must skip the cache stats, got %#v", next.cacheCalls)
	}
}

// The /goal between-tasks compaction funnels through the same summarizer
// request, so its provider call must be recorded the same way.
func TestGoalBetweenTasksCompactRecordsSummarizerUsage(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "file")
	store, session := goalTaskTestSetup(t, "Work\n1. First\n2. Second")
	const inputTokens, outputTokens = 900, 60
	provider := &scriptedProvider{scripts: [][]qgruntime.StreamEvent{
		completeCurrentTaskScript("feito"),
		{
			{Type: qgruntime.StreamEventText, Content: "Resumo entre tarefas."},
			{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{InputTokens: inputTokens, OutputTokens: outputTokens}},
			{Type: qgruntime.StreamEventDone},
		},
	}}
	m := goalTaskTestModel(store, session, provider)
	m.modelName = "muse-spark-1.3-contributor"
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
	after, _ := updatedModel(t, updated, compactMsg)
	if after.lastCompactError != "" {
		t.Fatalf("between-tasks compaction failed: %s", after.lastCompactError)
	}

	payloads := compactionUsageEvents(t, store, after.activeSession.SessionID)
	if len(payloads) != 1 {
		t.Fatalf("expected 1 provider_usage event, got %d", len(payloads))
	}
	payload := payloads[0]
	if payload["promptTokens"] != float64(inputTokens) || payload["completionTokens"] != float64(outputTokens) {
		t.Fatalf("provider_usage tokens = %v/%v, want %d/%d: %#v",
			payload["promptTokens"], payload["completionTokens"], inputTokens, outputTokens, payload)
	}
	if payload["call_label"] != "muse-spark-1.3-contributor" {
		t.Fatalf("provider_usage call_label = %v, want the model id: %#v", payload["call_label"], payload)
	}
	receipts := liveUsageReceipts(after.transcript)
	if len(receipts) != 1 {
		t.Fatalf("expected 1 live usage receipt, got %d: %#v", len(receipts), after.transcript)
	}
	if receipts[0].text != "muse-spark-1.3-contributor" {
		t.Fatalf("live receipt text = %q, want the model id", receipts[0].text)
	}
	resumed := resumedUsageReceipts(t, store, after.activeSession.SessionID)
	if len(resumed) != 1 {
		t.Fatalf("expected 1 resumed usage receipt, got %d: %#v", len(resumed), resumed)
	}
	if resumed[0].text != "muse-spark-1.3-contributor" {
		t.Fatalf("resumed receipt text = %q, want the model id", resumed[0].text)
	}
	for _, row := range resumed {
		if strings.Contains(strings.ToLower(row.text), "compaction") {
			t.Fatalf("resumed receipt must not mention compaction: %#v", resumed)
		}
	}
	summary := after.usageTracker.Summary()
	if summary.RecordCount != 1 {
		t.Fatalf("expected 1 usage record, got %#v", summary)
	}
	if summary.TotalTokens != inputTokens+outputTokens {
		t.Fatalf("tracker tokens = %d, want %d", summary.TotalTokens, inputTokens+outputTokens)
	}
	if summary.TotalCost <= 0 {
		t.Fatalf("tracker cost = %v, want the summary cost counted: %#v", summary.TotalCost, summary)
	}
}

// When the summarizer provider reports no usage, compaction must leave the
// session and the tracker untouched (no empty event).
func TestCompactWithoutProviderUsageRecordsNothing(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "file")
	store := testSessionStore(t)
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "Resumo sem uso."},
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		ModelName:    "muse-spark-1.3-contributor",
		Provider:     provider,
		SessionStore: store,
	})
	var err error
	m, err = m.ensureActiveSession("compact without usage")
	if err != nil {
		t.Fatal(err)
	}
	m = seedCompactableHistory(t, m, 12)
	m.input.SetValue("/compact")

	next := runCompactCommand(t, m)
	if next.lastCompactError != "" {
		t.Fatalf("compact failed: %s", next.lastCompactError)
	}
	if payloads := compactionUsageEvents(t, store, next.activeSession.SessionID); len(payloads) != 0 {
		t.Fatalf("expected no provider_usage event, got %#v", payloads)
	}
	if receipts := liveUsageReceipts(next.transcript); len(receipts) != 0 {
		t.Fatalf("expected no live usage receipt, got %#v", receipts)
	}
	if summary := next.usageTracker.Summary(); summary.RecordCount != 0 {
		t.Fatalf("expected empty usage tracker, got %#v", summary)
	}
}
