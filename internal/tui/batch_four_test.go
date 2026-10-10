package tui

import (
	"charm.land/lipgloss/v2"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestBatchFourEmptyStateKeepsBrandWithoutPromptExamples(t *testing.T) {
	m := batchThreeLiveModel(t)
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		got := plainRender(t, m.emptyState(width))
		assertContains(t, got, "Democratizando o acesso à IA")
		assertNotContains(t, got, "Try ")
		assertNotContains(t, got, "Press ?")
		for _, line := range strings.Split(got, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("overflow: %q", line)
			}
		}
	}
}

func TestBatchFourComposerLabelAvoidsUnicodeRuleOverdraw(t *testing.T) {
	m := interactivePetTestModel(t)
	m.modelName = "muse-spark-1.3-contributor"
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		m.width = width
		got := plainRender(t, m.composerDividerLine(width))
		before, _, found := strings.Cut(got, m.modelName)
		if !found || strings.Contains(before, "─") {
			t.Fatalf("width %d: rule overdraws model label: %q", width, got)
		}
		assertRenderedLineWidths(t, got, width)
		if !strings.HasSuffix(got, strings.Repeat(" ", petReservedColumns)) {
			t.Fatalf("pet reservation lost: %q", got)
		}
	}
}

func TestBatchFourStatusShowsLatestCallIncludingZero(t *testing.T) {
	m := batchThreeLiveModel(t)
	assertContains(t, plainRender(t, m.statusLine(120)), "última n/d tok")
	for _, event := range []qgruntime.Usage{{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 50, ReasoningTokens: 5}, {InputTokens: 2, OutputTokens: 3}, {}} {
		m, _ = m.recordUsageEvent(m.modelName, event, transcriptRow{})
		got := plainRender(t, m.statusLine(120))
		want := map[int]string{120: "última 120 tok", 5: "última 5 tok", 0: "última 0 tok"}[event.TotalTokens()]
		assertContains(t, got, want)
		assertNotContains(t, got, "toks/s")
	}
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: -1}, transcriptRow{})
	assertContains(t, plainRender(t, m.statusLine(120)), "última 0 tok")
}

func TestBatchFourEffortUnknownAndAPIDefault(t *testing.T) {
	m := batchThreeLiveModel(t)
	assertContains(t, plainRender(t, m.statusLine(120)), "esf n/d")
	assertContains(t, plainRender(t, m.effortText()), "não informa")
	live := m.modelPickerLiveByProvider["qg"]
	live[0].ReasoningEfforts = []string{"low", "high"}
	live[0].DefaultReasoningEffort = "low"
	assertContains(t, plainRender(t, m.statusLine(120)), "esf low (API)")
	m, _ = m.handleEffortCommand("high")
	assertContains(t, plainRender(t, m.statusLine(120)), "esf high")
	if string(m.reasoningEffort) != "high" {
		t.Fatal("selection was not stored")
	}
}

func TestBatchFourResumeRestoresLatestZeroUsage(t *testing.T) {
	m := batchThreeLiveModel(t)
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "latest-zero"})
	if err != nil {
		t.Fatal(err)
	}
	m.sessionStore = store
	for _, payload := range []map[string]any{{"promptTokens": 90, "completionTokens": 10}, {"promptTokens": 0, "completionTokens": 0}} {
		if _, err := store.AppendEvent(session.SessionID, sessions.AppendEventInput{Type: sessions.EventUsage, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: 777}, transcriptRow{})
	resumed, notice := m.handleResumeCommand(session.SessionID)
	if notice != "" {
		t.Fatal(notice)
	}
	assertContains(t, plainRender(t, resumed.statusLine(120)), "última 0 tok")
}
