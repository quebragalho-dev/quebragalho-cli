package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

func TestBatchThreeWorkspaceOnlyInStatus(t *testing.T) {
	m := model{cwd: "/workspace/qg", gitBranch: "feature/user-branch"}
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		assertNotContains(t, plainRender(t, m.titleBar(width)), m.cwd)
		assertNotContains(t, plainRender(t, m.emptyState(width)), m.cwd)
		assertContains(t, plainRender(t, m.statusLine(width)), "/w")
	}
}

func TestBatchThreeSolidWordmark(t *testing.T) {
	expected := [][2]string{
		{" ███████ ", " ███████ "},
		{"██     ██", "██       "},
		{"██     ██", "██       "},
		{"██  ██ ██", "██  █████"},
		{"██   ███ ", "██     ██"},
		{" █████ ██", " ███████ "},
	}
	lines := strings.Split(Wordmark(), "\n")
	for i, row := range expected {
		if lipgloss.Width(row[0]) != 9 || lipgloss.Width(row[1]) != 9 {
			t.Fatal("fixture must be 6x9 per glyph")
		}
		want := row[0] + "  " + row[1]
		if lines[i] != want {
			t.Fatalf("row %d=%q, want clean blocks %q", i, lines[i], want)
		}
		if got := plainRender(t, qgWordmarkLines()[i]); got != want {
			t.Fatalf("styled row=%q, want %q", got, want)
		}
	}
	assertNotContains(t, Wordmark(), "\x1b")
}

func batchThreeLiveModel(t *testing.T) model {
	t.Helper()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, t.TempDir())
	}
	m := newModel(context.Background(), Options{Cwd: "/workspace/qg", ProviderName: "qg", ModelName: "muse-spark-1.3-contributor", ProviderProfile: config.ProviderProfile{CatalogID: "qg"}})
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{"qg": {{ID: m.modelName, Source: "live", APIModel: &qgaccount.ModelInfo{ID: m.modelName, PricingBRL: qgaccount.ModelPricing{Currency: "BRL", Input: "1.648", Output: "4.9955", CachedInput: "0.16995"}}}}}
	return m
}

func TestBatchThreeStatusUsesAPIFacts(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.reasoningEffort = "high"
	m.gitBranch = "feature/user-branch"
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{OutputTokens: 10, Elapsed: time.Second}, transcriptRow{})
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{OutputTokens: 90, Elapsed: 3 * time.Second}, transcriptRow{})
	status := plainRender(t, m.statusLine(120))
	for _, want := range []string{"high", "feature/user-branch", "ctx 1.000.000"} {
		assertContains(t, status, want)
	}
	assertNotContains(t, status, "1M")
	n := int64(131072)
	m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &n
	assertContains(t, plainRender(t, m.statusLine(120)), "ctx 131.072")
}

func TestBatchThreeCallCostFrozenAndInline(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.provider = &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: "Resposta"},
		{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{InputTokens: 1000, CachedInputTokens: 250, OutputTokens: 100}},
		{Type: qgruntime.StreamEventDone},
	}}}
	response := execCmd(m.runAgentWithOptions(1, context.Background(), "teste", nil, tuiAgentRunOptions{})).(agentResponseMsg)
	var rows []transcriptRow
	for _, row := range response.rows {
		if row.tool == "model-usage" {
			rows = append(rows, row)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("want one inline receipt for one call, got %d", len(rows))
	}
	if rows[0].meta["amount"] != "142243/80000000" {
		t.Fatalf("exact call amount lost: %+v", rows[0])
	}
	m.modelPickerLiveByProvider["qg"][0].APIModel.PricingBRL.Input = "999"
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		got := plainRender(t, m.renderRow(rows[0], width, rowContext{}))
		assertContains(t, got, "< R$ 0,01")
		for _, line := range strings.Split(got, "\n") {
			if lipgloss.Width(line) > width {
				t.Fatalf("receipt overflow: %q", line)
			}
		}
	}
}

func TestBatchThreeReceiptPersistsProviderModelAndExactUSD(t *testing.T) {
	m := batchThreeLiveModel(t)
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "exact-call"})
	if err != nil {
		t.Fatal(err)
	}
	m.sessionStore, m.activeSession = store, session
	m.modelPickerLiveByProvider["qg"] = append(m.modelPickerLiveByProvider["qg"], providermodeldiscovery.Model{ID: "actual-provider-model", Source: "live", APIModel: &qgaccount.ModelInfo{ID: "actual-provider-model", Pricing: qgaccount.ModelPricing{Currency: "USD", Input: "1", CachedInput: "0", Output: "2"}}})
	m.provider = &scriptedProvider{scripts: [][]qgruntime.StreamEvent{{
		{Type: qgruntime.StreamEventText, Content: "Resposta"},
		{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{ModelID: "actual-provider-model", InputTokens: 1000000, CachedInputTokens: 250000, OutputTokens: 100000, Elapsed: time.Second}},
		{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{ModelID: "actual-provider-model", InputTokens: 1000000, CachedInputTokens: 250000, OutputTokens: 100000, Elapsed: 2 * time.Second}},
		{Type: qgruntime.StreamEventDone},
	}}}
	response := execCmd(m.runAgentWithOptions(1, context.Background(), "teste", nil, tuiAgentRunOptions{})).(agentResponseMsg)
	if response.err != nil {
		t.Fatal(response.err)
	}
	if len(response.usageEvents) != 1 {
		t.Fatalf("usage snapshots duplicated call: %d", len(response.usageEvents))
	}
	m, failures := m.appendSessionEvents(response.sessionEvents)
	if len(failures) != 0 {
		t.Fatalf("persist: %+v", failures)
	}
	events, err := store.ReadEvents(session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range transcriptRowsFromSessionEvents(events) {
		if row.tool != "model-usage" {
			continue
		}
		count++
		if row.meta["amount"] != "19/20" || row.meta["model"] != "actual-provider-model" {
			t.Fatalf("wrong resumed receipt: %+v", row)
		}
		assertContains(t, plainRender(t, m.renderRow(row, 40, rowContext{})), "US$ 0.95")
	}
	if count != 1 {
		t.Fatalf("resumed receipts=%d, want one", count)
	}
	resumed, notice := m.handleResumeCommand(session.SessionID)
	if notice != "" {
		t.Fatal(notice)
	}
	if !resumed.lastUsageSeen || resumed.lastUsage.OutputTokens != 100000 {
		t.Fatalf("resumed usage: %+v", resumed.lastUsage)
	}
}

func TestBatchThreeMissingPriceNeverDisplaysFreeCall(t *testing.T) {
	m := batchThreeLiveModel(t)
	row := callUsageRow(1, 1, m.modelName, qgruntime.Usage{InputTokens: 100}, qgaccount.ModelInfo{})
	rendered := plainRender(t, m.renderRow(row, 40, rowContext{}))
	assertContains(t, strings.Join(strings.Fields(rendered), " "), "custo não informado")
	assertNotContains(t, rendered, "0,00")
}

func TestBatchThreeCallUsageIsSessionLocal(t *testing.T) {
	m := newBTWTestModel(t)
	m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{OutputTokens: 10, Elapsed: time.Second}, transcriptRow{})
	side, _ := m.handleBTWCommand("")
	if !side.btw.active {
		t.Fatal("BTW did not open")
	}
	side, _ = side.recordUsageEvent(side.modelName, qgruntime.Usage{OutputTokens: 90, Elapsed: 3 * time.Second}, transcriptRow{})
	restored, _ := side.leaveBTW()
	if !restored.lastUsageSeen || restored.lastUsage.TotalTokens != 10 {
		t.Fatalf("side usage leaked: %+v", restored.lastUsage)
	}
	fresh := restored.startNewSession()
	if fresh.lastUsageSeen {
		t.Fatalf("new session inherited usage: %+v", fresh.lastUsage)
	}
	resumed, _ := restored.handleResumeCommand(restored.activeSession.SessionID)
	if resumed.lastUsageSeen {
		t.Fatalf("session without persisted usage must show unknown: %+v", resumed.lastUsage)
	}
}

func TestBatchThreeNarrowHeaderRetainsLongBranch(t *testing.T) {
	m := model{cwd: "/workspace/qg", gitBranch: "feature/" + strings.Repeat("branch", 10), prState: PrState{Status: PrFound, PrNumber: "12345", Additions: 12345, Deletions: 12345}}
	header := plainRender(t, m.titleBar(40))
	assertContains(t, header, "feature/")
	assertNotContains(t, header, m.cwd)
	assertRenderedLineWidths(t, header, 40)
}
