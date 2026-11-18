package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/usage"
)

// The run-details token readout is empty before any request, then shows the
// latest-step tokens against the model window.
func TestSidebarContextGaugeUsesAPIWindow(t *testing.T) {
	m := batchThreeLiveModel(t)
	window := int64(1000000)
	m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &window
	if got := plainRender(t, m.sidebarTokenText()); got != "" {
		t.Fatalf("expected no token readout before any request, got %q", got)
	}
	// 161k latest-step tokens against the 1M window.
	if _, err := m.usageTracker.Record(usage.RecordInput{
		ModelID: "muse-spark-1.3-contributor",
		Usage:   qgruntime.Usage{InputTokens: 160_000, OutputTokens: 1000},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got := plainRender(t, m.sidebarTokenText())
	if got == "" {
		t.Fatal("expected a token readout after recording usage")
	}
	assertContains(t, got, "161")
}

func TestStatusLineDoesNotInferContextFromRegistry(t *testing.T) {
	m := batchThreeLiveModel(t)
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: 160000, OutputTokens: 1000}, transcriptRow{})
	status := plainRender(t, m.statusLine(110))
	// Used tokens without a window render bare, never the old guess.
	assertContains(t, status, "160k")
	assertNotContains(t, status, "ctx")
	if got := plainRender(t, m.sidebarTokenText()); got == "" {
		t.Fatal("expected a token readout from recorded usage")
	}
}

func TestSidebarContextAndResumeAfterManualCompactShowRemainingContext(t *testing.T) {
	t.Setenv("QG_CRED_STORAGE", "file")
	store := testSessionStore(t)
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "Resumo curto da conversa."},
		{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{InputTokens: 155_000, OutputTokens: 500}},
		{Type: qgruntime.StreamEventDone},
	}}
	m := batchThreeLiveModel(t)
	window200k := int64(200_000)
	m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &window200k
	m.provider = provider
	m.sessionStore = store
	m.snapshotWriter = nil

	var err error
	m, err = m.ensureActiveSession("manual compact context size")
	if err != nil {
		t.Fatal(err)
	}
	m = seedCompactableHistory(t, m, 12)
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: 160_000, OutputTokens: 500}, transcriptRow{})
	m, err = m.appendSessionEvent(sessions.EventUsage, map[string]any{
		"promptTokens":     160_000,
		"completionTokens": 500,
		"model":            m.modelName,
	})
	if err != nil {
		t.Fatal(err)
	}

	m.input.SetValue("/compact")
	compacted := runCompactCommand(t, m)
	if compacted.lastCompactError != "" {
		t.Fatalf("compact failed: %s", compacted.lastCompactError)
	}
	wantRemaining := agent.MeasureContext(compacted.continuation.messages, nil, 0).MessageTokens
	if wantRemaining <= 0 || wantRemaining >= 5_000 {
		t.Fatalf("unexpected remaining compacted tokens = %d", wantRemaining)
	}

	gotLive := plainRender(t, compacted.sidebarTokenText())
	wantWindow := humanCount(compacted.modelContextWindow(compacted.modelName))
	wantText := humanCount(wantRemaining) + " / " + wantWindow + " tokens"
	if gotLive != wantText {
		t.Fatalf("sidebarTokenText after /compact = %q, want %q (must not use summarizer 155.5K or pre-compact 160.5K)", gotLive, wantText)
	}

	// Resuming onto the same window must not warn that context is full and must
	// keep showing the remaining compacted context in the sidebar.
	fresh := batchThreeLiveModel(t)
	fresh.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &window200k
	fresh.sessionStore = store
	resumed, notice := fresh.handleResumeCommand(compacted.activeSession.SessionID)
	if notice != "" {
		t.Fatalf("resume notice = %q", notice)
	}
	if transcriptContains(resumed.transcript, "ocupa a maior parte da janela") {
		t.Fatalf("resumed compacted session falsely warned about full context:\n%s", transcriptText(resumed.transcript))
	}
	if gotResumed := plainRender(t, resumed.sidebarTokenText()); gotResumed != wantText {
		t.Fatalf("resumed sidebarTokenText = %q, want %q", gotResumed, wantText)
	}

	// Resuming onto a model whose window makes even the compacted context >= 70%
	// must warn with the remaining compacted token count, not 155500.
	tinyWindow := int64(wantRemaining + 10)
	tiny := batchThreeLiveModel(t)
	tiny.modelName = "custom-tiny-window"
	tiny.providerProfile.Model = "custom-tiny-window"
	tiny.modelPickerLiveByProvider["qg"] = []providermodeldiscovery.Model{{
		ID:            "custom-tiny-window",
		Source:        "live",
		ContextWindow: int(tinyWindow),
		APIModel:      &qgaccount.ModelInfo{ID: "custom-tiny-window", ContextLength: &tinyWindow},
	}}
	tiny.sessionStore = store
	// Point snapshot at custom-tiny-window so loadExactSnapshot checks against tinyWindow.
	if snap, err := store.ReadContinuation(compacted.activeSession.SessionID); err == nil && snap != nil {
		snap.ModelID = "custom-tiny-window"
		_ = store.WriteContinuation(*snap)
	}
	resumedTiny, _ := tiny.handleResumeCommand(compacted.activeSession.SessionID)
	wantWarn := "contexto atual (" + strconv.Itoa(wantRemaining) + " tokens) ocupa a maior parte da janela"
	if !strings.Contains(transcriptText(resumedTiny.transcript), wantWarn) {
		t.Fatalf("expected tiny-window resume warning %q, got:\n%s", wantWarn, transcriptText(resumedTiny.transcript))
	}
	if strings.Contains(transcriptText(resumedTiny.transcript), "155500") || strings.Contains(transcriptText(resumedTiny.transcript), "160500") {
		t.Fatalf("tiny-window resume warning leaked summarizer/pre-compact tokens:\n%s", transcriptText(resumedTiny.transcript))
	}
}

func TestSidebarContextAndResumeAfterAutoCompactShowRemainingContext(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(continuationEchoTool{})
	// Window 100k (70% = 70k), /autocompact 20k: initial 25k-token user prompt
	// triggers proactive compaction on step 2, summarizing the 25k prompt into
	// "RESUMO CURTO" while keeping only the short tool call + result (~30 tokens).
	first := acceptAttrToolCall("read_it", acceptAttrUsage("", 80_000, 0, 50))
	postCompactNoUsage := []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "feito após compactar"},
		{Type: qgruntime.StreamEventDone},
	}
	p := &acceptRoutingProvider{
		main:    [][]qgruntime.StreamEvent{first, postCompactNoUsage},
		summary: acceptAttrText("RESUMO CURTO", acceptAttrUsage("", 75_000, 0, 300)),
	}
	live := acceptAttrLiveModelWithLimit(t, p, registry, 100_000, agent.Options{CompactionPreserveLast: 2}, "", 20_000)
	live = runTextTurn(t, live, strings.Repeat("y", 100_000))
	if p.summarizeCalls != 1 {
		t.Fatalf("precondition: expected 1 automatic compaction summary call, got %d", p.summarizeCalls)
	}
	flushSnapshot(t, live)

	wantRemaining := agent.MeasureContext(live.continuation.messages, nil, 0).MessageTokens
	if wantRemaining <= 0 || wantRemaining >= 2_000 {
		t.Fatalf("unexpected remaining auto-compacted tokens = %d", wantRemaining)
	}
	wantWindow := humanCount(live.modelContextWindow(live.modelName))
	wantText := humanCount(wantRemaining) + " tokens"
	if live.modelContextWindow(live.modelName) > 0 {
		wantText = humanCount(wantRemaining) + " / " + wantWindow + " tokens"
	}
	if got := plainRender(t, live.sidebarTokenText()); got != wantText {
		t.Fatalf("live sidebarTokenText after auto-compact = %q, want %q (must not use summarizer 75.3K)", got, wantText)
	}

	idle := &acceptRoutingProvider{main: [][]qgruntime.StreamEvent{acceptAttrText("ok", acceptAttrUsage("", 3_000, 0, 100))}}
	fresh := acceptAttrLiveModelWithLimit(t, idle, registry, 100_000, agent.Options{}, "", 20_000)
	fresh.sessionStore = live.sessionStore
	resumed, notice := fresh.handleResumeCommand(live.activeSession.SessionID)
	if notice != "" {
		t.Fatalf("resume notice = %q", notice)
	}
	if transcriptContains(resumed.transcript, "ocupa a maior parte da janela") {
		t.Fatalf("resumed auto-compacted session falsely warned about full context:\n%s", transcriptText(resumed.transcript))
	}
	if got := plainRender(t, resumed.sidebarTokenText()); got != wantText {
		t.Fatalf("resumed sidebarTokenText after auto-compact = %q, want %q", got, wantText)
	}

	// Next normal call updates the sidebar back to the real provider usage.
	resumed = runTextTurn(t, resumed, "próximo passo")
	wantAfterTurn := "3.1K tokens"
	if live.modelContextWindow(live.modelName) > 0 {
		wantAfterTurn = "3.1K / " + wantWindow + " tokens"
	}
	if got := plainRender(t, resumed.sidebarTokenText()); got != wantAfterTurn {
		t.Fatalf("sidebarTokenText after next turn = %q, want %q", got, wantAfterTurn)
	}
}
