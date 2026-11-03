package tui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// meterFixture builds a model with a live API window and, optionally, a
// recorded call receipt for receiptModel.
func meterFixture(t *testing.T, window int64, used int, receiptModel string) model {
	t.Helper()
	m := batchThreeLiveModel(t)
	if window > 0 {
		m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &window
	}
	if used > 0 {
		m, _ = m.recordUsageEvent(receiptModel, qgruntime.Usage{InputTokens: used, OutputTokens: 10}, transcriptRow{})
		if !m.meterLive {
			t.Fatal("meter must go live after a recorded call")
		}
	}
	return m
}

func TestFormatContextTokensTable(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{
		{850, "850"},
		{999, "999"},
		{1000, "1k"},
		{1001, "1k"},
		{300000, "300k"},
		{483900, "483.9k"},
		{483949, "483.9k"},
		{999900, "999.9k"},
		{999949, "999.9k"},
		{999950, "1m"},
		{999960, "1m"},
		{1000000, "1m"},
		{1000001, "1m"},
		{1499999, "1.5m"},
		{1449999, "1.4m"},
		{1500000, "1.5m"},
		{2000000, "2m"},
	} {
		if got := formatContextTokens(tc.in); got != tc.want {
			t.Fatalf("formatContextTokens(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestContextMeterFillBounds(t *testing.T) {
	for _, tc := range []struct {
		used, window, want int
	}{
		{300000, 1000000, 30},
		{720000, 1000000, 72},
		{940000, 1000000, 94},
		{595000, 1000000, 60}, // .5 rounds half away from zero
		{1, 1000000, 1},       // never 0 with use
		{4999, 1000000, 1},
		{849999, 1000000, 85},
		{994999, 1000000, 99},
		{999999, 1000000, 99}, // never 100 unless full
		{1000000, 1000000, 100},
		{1500000, 1000000, 100},
	} {
		if got := contextMeterFill(tc.used, tc.window); got != tc.want {
			t.Fatalf("contextMeterFill(%d, %d) = %d, want %d", tc.used, tc.window, got, tc.want)
		}
	}
}

// The level mapping is theme-independent; rendering resolves the tokens per
// theme (covered with dark and light themes below).
func TestContextLevelBoundaries(t *testing.T) {
	for _, tc := range []struct {
		pct  int
		want contextLevel
	}{
		{0, contextLevelLow},
		{59, contextLevelLow},
		{60, contextLevelMid},
		{84, contextLevelMid},
		{85, contextLevelHigh},
		{100, contextLevelHigh},
	} {
		if got := contextFillLevel(tc.pct); got != tc.want {
			t.Fatalf("contextFillLevel(%d) = %v, want %v", tc.pct, got, tc.want)
		}
	}
}

func TestContextLevelStylesResolveInDarkAndLightThemes(t *testing.T) {
	previous := qgTheme
	defer func() { qgTheme = previous }()
	for _, tc := range []struct {
		name  string
		dark  bool
		level contextLevel
		want  lipgloss.Style
	}{
		{"dark low", true, contextLevelLow, darkThemeAccent()},
		{"dark mid", true, contextLevelMid, darkThemeAmber()},
		{"dark high", true, contextLevelHigh, darkThemeRed()},
		{"light low", false, contextLevelLow, lightThemeAccent()},
		{"light mid", false, contextLevelMid, lightThemeAmber()},
		{"light high", false, contextLevelHigh, lightThemeRed()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, qgTheme = themeForMode(themeSystem, tc.dark)
			if got, want := tc.level.style().Render("━"), tc.want.Render("━"); got != want {
				t.Fatalf("level %v rendered %q, want token %q", tc.level, got, want)
			}
		})
	}
}

func darkThemeAccent() lipgloss.Style  { _, th := themeForMode(themeSystem, true); return th.accent }
func darkThemeAmber() lipgloss.Style   { _, th := themeForMode(themeSystem, true); return th.amber }
func darkThemeRed() lipgloss.Style     { _, th := themeForMode(themeSystem, true); return th.red }
func lightThemeAccent() lipgloss.Style { _, th := themeForMode(themeSystem, false); return th.accent }
func lightThemeAmber() lipgloss.Style  { _, th := themeForMode(themeSystem, false); return th.amber }
func lightThemeRed() lipgloss.Style    { _, th := themeForMode(themeSystem, false); return th.red }

// The border fill counts round(pct/100 * inner): 30% and 94% of 78 cells.
func TestContextMeterBorderFillCounts(t *testing.T) {
	m := meterFixture(t, 1000000, 300000, batchMeterModelName(t))
	got := plainRender(t, m.composerDividerLine(80))
	if n := strings.Count(got, "━"); n != 23 {
		t.Fatalf("30%% border has %d fill cells, want 23:\n%s", n, got)
	}
	m94 := meterFixture(t, 1000000, 940000, batchMeterModelName(t))
	got94 := plainRender(t, m94.composerDividerLine(80))
	if n := strings.Count(got94, "━"); n != 73 {
		t.Fatalf("94%% border has %d fill cells, want 73:\n%s", n, got94)
	}
	// A trace of use paints at least one cell.
	mTrace := meterFixture(t, 1000000, 1, batchMeterModelName(t))
	if n := strings.Count(plainRender(t, mTrace.composerDividerLine(20)), "━"); n != 1 {
		t.Fatalf("trace border has %d fill cells, want 1", n)
	}
	// Non-full borders keep a plain closing corner; the opening corner takes
	// the level color as soon as there is fill.
	raw30 := m.composerDividerLine(80)
	if !strings.Contains(raw30, qgTheme.accent.Render("╰")) {
		t.Fatalf("partial border should paint ╰ accent:\n%q", raw30)
	}
	if strings.Contains(raw30, qgTheme.accent.Render("╯")) {
		t.Fatalf("partial border must not paint ╯ accent:\n%q", raw30)
	}
	// Full window paints the closing corner in the level color.
	mFull := meterFixture(t, 1000000, 1000000, batchMeterModelName(t))
	raw := mFull.composerDividerLine(80)
	if !strings.Contains(raw, qgTheme.red.Render("╯")) {
		t.Fatalf("full border should paint ╯ red:\n%q", raw)
	}
	if !strings.Contains(raw, qgTheme.red.Render("╰")) {
		t.Fatalf("full border should paint ╰ red:\n%q", raw)
	}
	if n := strings.Count(plainRender(t, raw), "─"); n != 0 {
		t.Fatalf("full border should have no unfilled cells, got %d", n)
	}
}

func batchMeterModelName(t *testing.T) string {
	t.Helper()
	return batchThreeLiveModel(t).modelName
}

// Switching models keeps the session usage and renders it against the new
// window; the next completed call corrects the figure.
func TestContextMeterFollowsUsageAcrossModelSwitch(t *testing.T) {
	baseProfile := config.ProviderProfile{
		Name:         "qg",
		CatalogID:    "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      "https://api.quebragalho.dev/v1",
		APIKey:       "sk-test",
		Model:        "muse-spark-1.3-contributor",
	}
	m := newModel(context.Background(), Options{
		ProviderName:    "qg",
		ModelName:       "muse-spark-1.3-contributor",
		ProviderProfile: baseProfile,
		Provider:        &fakeProvider{},
		NewProvider: func(config.ProviderProfile) (qgruntime.Provider, error) {
			return &fakeProvider{}, nil
		},
	})
	window1M := int64(1000000)
	window500k := int64(500000)
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{
		"qg": {
			{ID: "muse-spark-1.3-contributor", Source: "live", APIModel: &qgaccount.ModelInfo{ID: "muse-spark-1.3-contributor", ContextLength: &window1M}},
			{ID: "other-model", Source: "live", APIModel: &qgaccount.ModelInfo{ID: "other-model", ContextLength: &window500k}},
		},
	}
	m, _ = m.recordUsageEvent("muse-spark-1.3-contributor", qgruntime.Usage{InputTokens: 300000, OutputTokens: 10}, transcriptRow{})
	if got := plainRender(t, m.statusLine(120)); !strings.Contains(got, "30%") {
		t.Fatalf("precondition: status should show 30%%, got %q", got)
	}
	next, _, ok, err := m.handleModelCommandScope("other-model", false)
	if err != nil || !ok {
		t.Fatalf("switch refused: %v", err)
	}
	if !next.meterLive {
		t.Fatal("model switch must keep the meter live on the session usage")
	}
	if got := plainRender(t, next.statusLine(120)); !strings.Contains(got, "60% · 300k/500k") {
		t.Fatalf("switched status should recalculate against the new window, got %q", got)
	}
}

// A compaction before the switch still zeroes the meter: the switch must
// not resurrect the old fill, and the next call sets the new value.
func TestContextMeterSwitchAfterCompactionStaysZeroed(t *testing.T) {
	baseProfile := config.ProviderProfile{
		Name:         "qg",
		CatalogID:    "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      "https://api.quebragalho.dev/v1",
		APIKey:       "sk-test",
		Model:        "muse-spark-1.3-contributor",
	}
	m := newModel(context.Background(), Options{
		ProviderName:    "qg",
		ModelName:       "muse-spark-1.3-contributor",
		ProviderProfile: baseProfile,
		Provider:        &fakeProvider{},
		NewProvider: func(config.ProviderProfile) (qgruntime.Provider, error) {
			return &fakeProvider{}, nil
		},
	})
	window1M := int64(1000000)
	window500k := int64(500000)
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{
		"qg": {
			{ID: "muse-spark-1.3-contributor", Source: "live", APIModel: &qgaccount.ModelInfo{ID: "muse-spark-1.3-contributor", ContextLength: &window1M}},
			{ID: "other-model", Source: "live", APIModel: &qgaccount.ModelInfo{ID: "other-model", ContextLength: &window500k}},
		},
	}
	m, _ = m.recordUsageEvent("muse-spark-1.3-contributor", qgruntime.Usage{InputTokens: 300000, OutputTokens: 10}, transcriptRow{})
	m.compactInFlight = true
	updated, _ := m.Update(compactResultMsg{result: CompactResult{Compacted: true}})
	m = updated.(model)
	next, _, ok, err := m.handleModelCommandScope("other-model", false)
	if err != nil || !ok {
		t.Fatalf("switch refused: %v", err)
	}
	if next.meterLive {
		t.Fatal("a compaction before the switch must keep the meter zeroed")
	}
	if got := plainRender(t, next.statusLine(120)); strings.Contains(got, "%") || !strings.Contains(got, "janela 500k") {
		t.Fatalf("switched status should show only the new window, got %q", got)
	}
	next, _ = next.recordUsageEvent(next.modelName, qgruntime.Usage{InputTokens: 100000, OutputTokens: 10}, transcriptRow{})
	if got := plainRender(t, next.statusLine(120)); !strings.Contains(got, "20% · 100k/500k") {
		t.Fatalf("next call should set the new fill, got %q", got)
	}
}

// /new zeroes the meter.
func TestContextMeterResetsOnNewSession(t *testing.T) {
	m := meterFixture(t, 1000000, 300000, batchMeterModelName(t))
	next := m.startNewSession()
	if next.meterLive {
		t.Fatal("/new must zero the meter")
	}
	if got := plainRender(t, next.statusLine(120)); strings.Contains(got, "30%") {
		t.Fatalf("/new status must not show the old fill, got %q", got)
	}
}

// /clear starts a fresh session like /new: usage, panel and meter reset,
// and the previous session stays available under /resume.
func TestContextMeterClearsWithNewSession(t *testing.T) {
	m := meterFixture(t, 1000000, 300000, batchMeterModelName(t))
	store := testSessionStore(t)
	session, err := store.Create(sessions.CreateInput{SessionID: "before-clear"})
	if err != nil {
		t.Fatal(err)
	}
	m.sessionStore = store
	m.activeSession = session
	updated, _ := m.dispatchCommand(parseCommand("/clear"))
	next := updated.(model)
	if next.meterLive || next.lastUsageSeen {
		t.Fatal("/clear must zero usage and the meter with the fresh session")
	}
	if next.activeSession.SessionID != "" {
		t.Fatalf("/clear must leave no active session, got %q", next.activeSession.SessionID)
	}
	if got := transcriptText(next.transcript); !strings.Contains(got, "Sessão anterior salva como before-clear") || !strings.Contains(got, "/resume before-clear") {
		t.Fatalf("/clear must point at the saved previous session, got %q", got)
	}
	if got := plainRender(t, next.statusLine(120)); strings.Contains(got, "30%") || !strings.Contains(got, "janela 1m") {
		t.Fatalf("cleared status must restart from the window, got %q", got)
	}
	if divider := plainRender(t, next.composerDividerLine(80)); strings.Contains(divider, "━") {
		t.Fatalf("cleared border must have no fill:\n%s", divider)
	}
	if got, err := store.Get("before-clear"); err != nil || got == nil {
		t.Fatalf("previous session must stay resumable: %v (%v)", got, err)
	}
}

// Usage belongs to the session, not to the model: a receipt tagged with
// another model still feeds this session's meter.
func TestContextMeterCountsForeignModelReceipt(t *testing.T) {
	m := meterFixture(t, 1000000, 0, "")
	m, _ = m.recordUsageEvent("other-model", qgruntime.Usage{InputTokens: 300000, OutputTokens: 10}, transcriptRow{})
	if !m.meterLive {
		t.Fatal("a recorded call must go live even with another model's tag")
	}
	got := plainRender(t, m.statusLine(120))
	if !strings.Contains(got, "30% · 300k/1m") {
		t.Fatalf("foreign receipt must fill the session meter, got %q", got)
	}
}

// Resuming adopts the session's own receipt whatever its model tag: usage
// belongs to the session. A session without usage leaves the meter empty,
// and the previous live value never leaks through the reload.
func TestContextMeterFollowsResumedReceipt(t *testing.T) {
	resumeWithUsage := func(t *testing.T, payload string) model {
		t.Helper()
		m := batchThreeLiveModel(t)
		window := int64(1000000)
		m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &window
		store := testSessionStore(t)
		session, err := store.Create(sessions.CreateInput{SessionID: "resume-meter"})
		if err != nil {
			t.Fatal(err)
		}
		if payload != "" {
			if _, err := store.AppendEvents(session.SessionID, []sessions.AppendEventInput{
				{Type: sessions.EventUsage, Payload: json.RawMessage(payload)},
			}); err != nil {
				t.Fatal(err)
			}
		}
		m.sessionStore = store
		// A live meter before the switch must not leak into the resumed session.
		m, _ = m.recordUsageEvent(m.modelName, qgruntime.Usage{InputTokens: 100, OutputTokens: 5}, transcriptRow{})
		if !m.meterLive {
			t.Fatal("precondition: meter must be live before resume")
		}
		m, _ = m.handleResumeCommand(session.SessionID)
		return m
	}

	t.Run("same model receipt", func(t *testing.T) {
		m := resumeWithUsage(t, `{"promptTokens":400000,"completionTokens":10,"model":"`+batchMeterModelName(t)+`"}`)
		if !m.meterLive {
			t.Fatal("resume must adopt the session's own receipt")
		}
		if got := plainRender(t, m.statusLine(120)); !strings.Contains(got, "40% · 400k/1m") {
			t.Fatalf("resumed status should show 40%%, got %q", got)
		}
	})

	t.Run("other model receipt", func(t *testing.T) {
		m := resumeWithUsage(t, `{"promptTokens":400000,"completionTokens":10,"model":"other-model"}`)
		if !m.lastUsageSeen {
			t.Fatal("resume must reload the panel's last call from session events")
		}
		if !m.meterLive {
			t.Fatal("resume must adopt the restored receipt regardless of its model tag")
		}
		if got := plainRender(t, m.statusLine(120)); !strings.Contains(got, "40% · 400k/1m") {
			t.Fatalf("resumed status should show the restored 40%%, got %q", got)
		}
	})

	t.Run("no usage", func(t *testing.T) {
		m := resumeWithUsage(t, "")
		if m.meterLive {
			t.Fatal("a session without usage must leave the meter dead")
		}
		if m.lastUsageSeen {
			t.Fatal("panel must have nothing to reload")
		}
		if got := plainRender(t, m.statusLine(120)); !strings.Contains(got, "janela 1m") {
			t.Fatalf("resumed status should fall back to the window, got %q", got)
		}
	})

	t.Run("adopts receipt from a cold start", func(t *testing.T) {
		m := batchThreeLiveModel(t)
		window := int64(1000000)
		m.modelPickerLiveByProvider["qg"][0].APIModel.ContextLength = &window
		store := testSessionStore(t)
		session, err := store.Create(sessions.CreateInput{SessionID: "resume-meter-cold"})
		if err != nil {
			t.Fatal(err)
		}
		payload := `{"promptTokens":400000,"completionTokens":10,"model":"` + m.modelName + `"}`
		if _, err := store.AppendEvents(session.SessionID, []sessions.AppendEventInput{
			{Type: sessions.EventUsage, Payload: json.RawMessage(payload)},
		}); err != nil {
			t.Fatal(err)
		}
		m.sessionStore = store
		if m.meterLive {
			t.Fatal("precondition: meter must be dead before resume")
		}
		m, _ = m.handleResumeCommand(session.SessionID)
		if !m.meterLive {
			t.Fatal("resume must adopt the session's own receipt even from a cold start")
		}
		if got := plainRender(t, m.statusLine(120)); !strings.Contains(got, "40% · 400k/1m") {
			t.Fatalf("resumed status should show 40%%, got %q", got)
		}
	})
}

// Compaction zeroes the meter; the next call's receipt sets the new value.
func TestContextMeterResetsOnCompaction(t *testing.T) {
	m := meterFixture(t, 1000000, 300000, batchMeterModelName(t))
	m.compactInFlight = true
	updated, _ := m.Update(compactResultMsg{result: CompactResult{Compacted: true}})
	next := updated.(model)
	if next.meterLive {
		t.Fatal("compaction must zero the meter")
	}
	if got := plainRender(t, next.statusLine(120)); strings.Contains(got, "30%") {
		t.Fatalf("compacted status must not show the old fill, got %q", got)
	}
	next, _ = next.recordUsageEvent(next.modelName, qgruntime.Usage{InputTokens: 100000, OutputTokens: 10}, transcriptRow{})
	if got := plainRender(t, next.statusLine(120)); !strings.Contains(got, "10%") {
		t.Fatalf("status should show the new 10%% fill, got %q", got)
	}
}

// Used tokens without a known window render bare; the border stays plain.
func TestContextMeterUnknownWindow(t *testing.T) {
	m := meterFixture(t, 0, 483900, batchMeterModelName(t))
	got := plainRender(t, m.statusLine(120))
	if !strings.Contains(got, "483.9k") {
		t.Fatalf("status should show the bare figure, got %q", got)
	}
	for _, unwanted := range []string{"%", "483.9k/", "janela", "ctx"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("status must not show a fill without a window, got %q", got)
		}
	}
	divider := plainRender(t, m.composerDividerLine(80))
	if strings.Contains(divider, "━") {
		t.Fatalf("border must stay unfilled without a window:\n%s", divider)
	}
	rawPlain := m.composerDividerLine(80)
	inner := strings.Repeat("─", 78)
	if !strings.Contains(rawPlain, qgTheme.lineStrong.Render("╰"+inner+"╯")) {
		t.Fatalf("border without use must stay the plain line border:\n%q", rawPlain)
	}
}

// The right side of the status line follows the meter state.
func TestContextStatusTextShapes(t *testing.T) {
	// Use plus window.
	m := meterFixture(t, 1000000, 300000, batchMeterModelName(t))
	if got := plainRender(t, m.statusLine(120)); !strings.Contains(got, "30% · 300k/1m") {
		t.Fatalf("status should show pct and used/window, got %q", got)
	}
	// Narrow: only the percent.
	if got := plainRender(t, m.statusLine(50)); !strings.Contains(got, "30%") || strings.Contains(got, "300k") {
		t.Fatalf("narrow status should show only the percent, got %q", got)
	}
	// A live rate leaves the status line and moves to the composer border.
	m.streamUsage = qgruntime.Usage{OutputTokens: 100, Elapsed: 2 * time.Second}
	if got := plainRender(t, m.statusLine(120)); strings.Contains(got, "tok/s") {
		t.Fatalf("status must not carry the rate, got %q", got)
	}
	// Usage arrival settles the faint rate; emitting wakes the live one.
	fixed := time.Unix(2_000_000, 0)
	m.now = func() time.Time { return fixed }
	next, _ := m.Update(agentUsageProgressMsg{runID: m.activeRunID, usage: qgruntime.Usage{OutputTokens: 100, Elapsed: 2 * time.Second}})
	m = next.(model)
	if got := plainRender(t, m.composerTopBorder(120)); strings.Contains(got, "●") {
		t.Fatalf("settled usage must not flash live, got %q", got)
	}
	m.pending = true
	next, _ = m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("x", 4000)})
	m = next.(model)
	m.now = func() time.Time { return fixed.Add(2 * time.Second) }
	if got := plainRender(t, m.composerTopBorder(120)); !strings.Contains(got, "● 50 tok/s") {
		t.Fatalf("border should show the live rate, got %q", got)
	}
	// No use yet, window known.
	bare := meterFixture(t, 1000000, 0, "")
	if got := plainRender(t, bare.statusLine(120)); !strings.Contains(got, "janela 1m") {
		t.Fatalf("status should name the window before the first response, got %q", got)
	}
	// Nothing known: the old ctx readout is gone.
	empty := meterFixture(t, 0, 0, "")
	if got := plainRender(t, empty.statusLine(120)); strings.Contains(got, "ctx") || strings.Contains(got, "janela") {
		t.Fatalf("status should show no context readout, got %q", got)
	}
}

// Status line and composer box never exceed the terminal width.
func TestContextMeterWidths(t *testing.T) {
	for _, width := range []int{50, 80, 120} {
		for name, m := range map[string]model{
			"empty":   meterFixture(t, 0, 0, ""),
			"window":  meterFixture(t, 1000000, 0, ""),
			"partial": meterFixture(t, 1000000, 300000, batchMeterModelName(t)),
			"full":    meterFixture(t, 1000000, 1000000, batchMeterModelName(t)),
		} {
			m.width = width
			for line, block := range map[string]string{
				"status":  plainRender(t, m.statusLine(width)),
				"top":     plainRender(t, m.composerTopBorder(width)),
				"divider": plainRender(t, m.composerDividerLine(width)),
				"box":     plainRender(t, m.composerBox(width)),
			} {
				for index, row := range strings.Split(block, "\n") {
					if got := lipgloss.Width(row); got > width {
						t.Fatalf("%s %s line %d is %d cells, want <= %d", name, line, index, got, width)
					}
				}
			}
		}
	}
	// Tiny widths keep the frame intact, model label included.
	long := batchThreeLiveModel(t)
	long.modelName = "cogito-2.1:671b-extra-long-model-name-beyond-forty-columns"
	for _, width := range []int{0, 1, 4, 7, 8} {
		for name, block := range map[string]string{
			"top":     plainRender(t, long.composerTopBorder(width)),
			"divider": plainRender(t, long.composerDividerLine(width)),
		} {
			if got := lipgloss.Width(block); got > width {
				t.Fatalf("%s at %d is %d cells, want <= %d: %q", name, width, got, width, block)
			}
		}
	}
}

// The gateway catalog fills the per-model window the meter reads: a fake
// /v1/models shaped like the qg gateway resolves apiContextWindow end to end.
func TestContextMeterWindowFromGatewayCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[` +
			`{"id":"muse-spark-1.3-contributor","context_length":1000000,"capabilities":{"tools":true,"reasoning":true}},` +
			`{"id":"legacy-model","capabilities":{"tools":false}}` +
			`]}`))
	}))
	defer server.Close()
	profile := config.ProviderProfile{
		Name:         "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      server.URL + "/v1",
		APIKey:       "fake",
	}
	models, err := providermodeldiscovery.Discover(context.Background(), profile, providermodeldiscovery.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var withWindow, withoutWindow *providermodeldiscovery.Model
	for i := range models {
		switch models[i].ID {
		case "muse-spark-1.3-contributor":
			withWindow = &models[i]
		case "legacy-model":
			withoutWindow = &models[i]
		}
	}
	if withWindow == nil || withoutWindow == nil {
		t.Fatalf("discovery returned %#v", models)
	}
	if withWindow.APIModel == nil || withWindow.APIModel.ContextLength == nil || *withWindow.APIModel.ContextLength != 1000000 {
		t.Fatalf("window not filled from gateway catalog: %+v", withWindow.APIModel)
	}
	if withoutWindow.APIModel == nil || withoutWindow.APIModel.ContextLength != nil {
		t.Fatalf("model without context_length must stay unknown: %+v", withoutWindow.APIModel)
	}
	m := batchThreeLiveModel(t)
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{"qg": {*withWindow}}
	if got := m.apiContextWindow(); got != 1000000 {
		t.Fatalf("apiContextWindow = %d, want 1000000", got)
	}
}
