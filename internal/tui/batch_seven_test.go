package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"context"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"strings"
	"testing"
	"time"
)

func TestBatchSevenDocumentedContext(t *testing.T) {
	for _, tc := range []struct{ id, want string }{{"muse-spark-1.3-contributor", "1.000.000"}, {"grok-4.7", "500.000"}} {
		t.Run(tc.id, func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.modelName = tc.id
			m.modelPickerLiveByProvider["qg"][0].ID = tc.id
			m.modelPickerLiveByProvider["qg"][0].APIModel.ID = tc.id
			// Without a live API window the status shows no context figure
			// instead of the old fixed guess; the /usage panel below still
			// reports the documented fallback.
			assertNotContains(t, plainRender(t, m.statusLine(120)), "ctx")
			m = batchSixOpen(m, "/usage")
			assertContains(t, plainRender(t, m.pickerOverlay(120)), tc.want+" tokens")
			n := int64(131072)
			m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &n
			assertContains(t, plainRender(t, m.statusLine(120)), "janela 131.1k")
			assertContains(t, plainRender(t, m.pickerOverlay(120)), "131.072 tokens")
			assertNotContains(t, plainRender(t, m.pickerOverlay(120)), "fallback/docs")
		})
	}
}

func TestBatchSevenEffortWithoutAbbreviation(t *testing.T) {
	m := batchThreeLiveModel(t)
	for _, tc := range []struct{ local, api, want string }{{"", "", "n/d"}, {"", "high", "alto (API)"}, {"max", "high", "máximo"}, {"", "made-up", "n/d"}} {
		m.reasoningEffort = ""
		m.modelPickerLiveByProvider["qg"][0].DefaultReasoningEffort = tc.api
		if tc.local == "max" {
			m.reasoningEffort = "max"
		}
		if got := m.statusEffort(); got != tc.want {
			t.Errorf("effort=%q want %q", got, tc.want)
		}
	}
}

func TestBatchSevenTokenCountAndRate(t *testing.T) {
	m := batchThreeLiveModel(t)
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{{time.Second, "850 tok/s"}, {time.Millisecond, "850 tok/s"}, {2 * time.Second, "425 tok/s"}, {0, ""}, {-time.Second, ""}} {
		m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: 11785, OutputTokens: 850, Elapsed: tc.elapsed}, transcriptRow{})
		next, _ := m.Update(agentUsageProgressMsg{runID: m.activeRunID, usage: qgruntime.Usage{InputTokens: 11785, OutputTokens: 850, Elapsed: tc.elapsed}})
		m = next.(model)
		got := plainRender(t, m.statusLine(120))
		assertNotContains(t, got, "tk/s")
		assertNotContains(t, got, "tok/s")
		assertNotContains(t, got, "12.635 tok")
		receipt := callUsageRow(1, 1, m.modelName, qgruntime.Usage{InputTokens: 11785, OutputTokens: 850, Elapsed: tc.elapsed}, *m.apiModel(m.modelName))
		rendered := plainRender(t, renderCallUsage(receipt, 120))
		if tc.want != "" {
			assertContains(t, rendered, m.modelName+" · "+tc.want+" · ")
		} else {
			assertNotContains(t, rendered, "tok/s")
			assertNotContains(t, rendered, " tok ")
		}
		assertNotContains(t, got, "última")
		for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
			if got := plainRender(t, m.statusLine(width)); lipgloss.Width(got) > width {
				t.Fatalf("overflow width %d: %q", width, got)
			}
		}
	}
}

func TestBatchSevenLocalEffortIsNotAPISupport(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.modelPickerLiveByProvider["qg"][0].APIModel.Capabilities.Reasoning = true
	m = batchSixOpen(m, "/effort")
	got := plainRender(t, m.pickerOverlay(120))
	assertNotContains(t, got, "não confirmado")
	assertNotContains(t, got, " (API)")
	if strings.Contains(m.statusEffort(), "auto") {
		t.Fatal("unknown was invented as auto")
	}
}

func TestBatchSevenStreamingProgressIsUnbilledAndIsolated(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.activeRunID = 3
	m.pending = true
	event := qgruntime.Usage{InputTokens: 11785, OutputTokens: 850, Elapsed: time.Second}
	next, _ := m.Update(agentUsageProgressMsg{runID: 3, usage: event})
	m = next.(model)
	assertContains(t, plainRender(t, m.composerTopBorder(120)), "850 tok/s")
	assertNotContains(t, plainRender(t, m.composerTopBorder(120)), "●")
	if m.lastUsageSeen || m.usageTracker.Summary().LastRecord != nil {
		t.Fatal("progress created accounting")
	}
	next, _ = m.Update(agentUsageProgressMsg{runID: 2, usage: qgruntime.Usage{OutputTokens: 999, Elapsed: time.Second}})
	m = next.(model)
	assertContains(t, plainRender(t, m.composerTopBorder(120)), "850 tok/s")
	next, _ = m.Update(agentUsageProgressMsg{runID: 3})
	m = next.(model)
	got := plainRender(t, m.composerTopBorder(120))
	assertContains(t, got, "850 tok/s")
	assertNotContains(t, got, "●")
}

func TestBatchSevenRuntimeStreamsBeforeReceipt(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.activeRunID = 1
	m.provider = &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: "first"},
		{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{InputTokens: 12000, OutputTokens: 400}},
		{Type: qgruntime.StreamEventText, Content: "second"},
		{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{OutputTokens: 850}},
		{Type: qgruntime.StreamEventDone},
	}}}
	seen := false
	m.pending = true
	m.agentOptions.OnText = func(delta string) {
		if delta == "second" {
			seen = true
			assertContains(t, plainRender(t, m.composerTopBorder(120)), "400 tok/s")
			if m.lastUsageSeen {
				t.Fatal("snapshot was billed before completion")
			}
		}
	}
	m.runtimeMessageSink = func(msg tea.Msg) { next, _ := m.Update(msg); m = next.(model) }
	response := execCmd(m.runAgentWithOptions(1, context.Background(), "test", nil, tuiAgentRunOptions{})).(agentResponseMsg)
	if !seen || len(response.usageEvents) != 1 {
		t.Fatalf("stream=%v bills=%d", seen, len(response.usageEvents))
	}
	assertContains(t, plainRender(t, renderCallUsage(response.usageReceiptAt(0), 120)), "850 tok/s")
	m.pending = false
	assertContains(t, plainRender(t, m.composerTopBorder(120)), "850 tok/s")
	for _, event := range response.sessionEvents {
		if event.Type == sessions.EventUsage {
			payload := event.Payload.(map[string]any)
			if payload["call_tokens"] != "12.850" {
				t.Fatalf("receipt loses total on resume: %v", payload)
			}
		}
	}
	m = m.startNewSession()
	assertNotContains(t, plainRender(t, m.statusLine(120)), "tk/s")
	assertNotContains(t, plainRender(t, m.composerTopBorder(120)), "tok/s")
}

func TestBatchSevenWorkingLineDoesNotInventTokens(t *testing.T) {
	m := batchThreeLiveModel(t).beginRun(nil)
	next, _ := m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("a", 4000)})
	m = next.(model)
	assertNotContains(t, plainRender(t, m.workingStatusLine()), " tok")
	assertNotContains(t, plainRender(t, m.statusLine(120)), "tk/s")
}
