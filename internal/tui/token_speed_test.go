package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providermodeldiscovery"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgaccount"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// liveBorderModel opens a run with an injectable clock so the estimate math
// is deterministic: every assertion below controls time, never sleeps.
func liveBorderModel(t *testing.T, now time.Time) (model, *time.Time) {
	t.Helper()
	m := batchThreeLiveModel(t)
	m = m.beginRun(nil)
	clock := now
	m.now = func() time.Time { return clock }
	return m, &clock
}

func TestLiveEstimateAppearsAfterOneSecond(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m, clock := liveBorderModel(t, now)
	next, _ := m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("a", 4000)})
	m = next.(model)
	if got := plainRender(t, m.composerTopBorder(120)); strings.Contains(got, "tok/s") {
		t.Fatalf("estimate must wait ~1s of output, got %q", got)
	}
	*clock = now.Add(2 * time.Second)
	got := plainRender(t, m.composerTopBorder(120))
	if !strings.Contains(got, "● 500 tok/s") {
		t.Fatalf("estimate must follow received output, got %q", got)
	}
	next, _ = m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("b", 4000)})
	m = next.(model)
	if got := plainRender(t, m.composerTopBorder(120)); !strings.Contains(got, "● 1.000 tok/s") {
		t.Fatalf("estimate must grow with output, got %q", got)
	}
}

func TestExactRateReplacesEstimate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m, clock := liveBorderModel(t, now)
	next, _ := m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("a", 4000)})
	m = next.(model)
	*clock = now.Add(2 * time.Second)
	next, _ = m.Update(agentUsageProgressMsg{runID: m.activeRunID, usage: qgruntime.Usage{OutputTokens: 850, Elapsed: time.Second}})
	m = next.(model)
	// The usage arrival ends the live state: exact rate, faint, no dot.
	got := plainRender(t, m.composerTopBorder(120))
	if !strings.Contains(got, "850 tok/s") || strings.Contains(got, "●") {
		t.Fatalf("usage arrival must settle faint, got %q", got)
	}
	// More output opens a new live window: the exact partial wins over the
	// fresh estimate.
	next, _ = m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("b", 4000)})
	m = next.(model)
	*clock = now.Add(4 * time.Second)
	got = plainRender(t, m.composerTopBorder(120))
	if !strings.Contains(got, "● 850 tok/s") {
		t.Fatalf("exact partial must win, got %q", got)
	}
	if strings.Contains(got, "500 tok/s") {
		t.Fatalf("estimate must not shadow the exact rate, got %q", got)
	}
}

func TestBorderShowsLastRateFaintAfterFinish(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.lastCompletedRate = "42 tok/s"
	got := plainRender(t, m.composerTopBorder(120))
	if !strings.Contains(got, "42 tok/s · "+m.modelName) {
		t.Fatalf("finished border must keep the exact rate, got %q", got)
	}
	if strings.Contains(got, "●") {
		t.Fatalf("finished border must not carry the live dot, got %q", got)
	}
	m.lastCompletedRate = ""
	if got := plainRender(t, m.composerTopBorder(120)); strings.Contains(got, "tok/s") {
		t.Fatalf("border without a rate is model-only, got %q", got)
	}
}

func TestRateClearedOnNewSession(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.lastCompletedRate = "42 tok/s"
	m = m.startNewSession()
	if got := plainRender(t, m.composerTopBorder(120)); strings.Contains(got, "tok/s") {
		t.Fatalf("/new must drop the last rate, got %q", got)
	}
}

func TestBorderWidthsNeverBreak(t *testing.T) {
	longName := "a-very-long-model-name-that-needs-truncation-on-narrow-screens-12345-and-keeps-going-past-one-hundred-cells-total"
	for _, width := range []int{50, 80, 120} {
		now := time.Unix(1_000_000, 0)
		m, clock := liveBorderModel(t, now)
		m.modelName = longName
		next, _ := m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("a", 4000)})
		m = next.(model)
		*clock = now.Add(2 * time.Second)
		got := plainRender(t, m.composerTopBorder(width))
		for _, line := range strings.Split(got, "\n") {
			if w := lipgloss.Width(line); w != width {
				t.Fatalf("width %d: border line is %d cells: %q", width, w, line)
			}
		}
		if !strings.Contains(got, "…") {
			t.Fatalf("width %d: long model must truncate, got %q", width, got)
		}
		if !strings.Contains(got, "tok/s") {
			t.Fatalf("width %d: rate must survive truncation, got %q", width, got)
		}
	}
	// Too narrow for model plus rate: the rate drops, the model stays.
	now := time.Unix(1_000_000, 0)
	m, clock := liveBorderModel(t, now)
	m.modelName = longName
	next, _ := m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("a", 4000)})
	m = next.(model)
	*clock = now.Add(2 * time.Second)
	got := plainRender(t, m.composerTopBorder(16))
	if strings.Contains(got, "tok/s") {
		t.Fatalf("tiny border must drop the rate, got %q", got)
	}
	if !strings.Contains(got, "a-very") {
		t.Fatalf("tiny border must keep the model stub, got %q", got)
	}
	if w := lipgloss.Width(got); w != 16 {
		t.Fatalf("tiny border line is %d cells, want 16: %q", w, got)
	}
}

func TestToolStartMutesLiveRate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m, clock := liveBorderModel(t, now)
	next, _ := m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("a", 4000)})
	m = next.(model)
	*clock = now.Add(2 * time.Second)
	m.lastCompletedRate = "99 tok/s"
	next, _ = m.Update(toolCallStreamStartMsg{runID: m.activeRunID, id: "call-1", name: "bash"})
	m = next.(model)
	got := plainRender(t, m.composerTopBorder(120))
	if strings.Contains(got, "●") || strings.Contains(got, "500 tok/s") {
		t.Fatalf("tool run must mute the live rate, got %q", got)
	}
	if !strings.Contains(got, "99 tok/s") {
		t.Fatalf("tool run must keep the last exact rate faint, got %q", got)
	}
}

func TestApprovalWaitShowsFaintRate(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m, clock := liveBorderModel(t, now)
	next, _ := m.Update(agentTextMsg{runID: m.activeRunID, delta: strings.Repeat("a", 4000)})
	m = next.(model)
	*clock = now.Add(2 * time.Second)
	m.lastCompletedRate = "99 tok/s"
	m.pendingPermission = &pendingPermissionPrompt{}
	got := plainRender(t, m.composerTopBorder(120))
	if strings.Contains(got, "●") || strings.Contains(got, "500 tok/s") {
		t.Fatalf("approval wait must not flash live, got %q", got)
	}
	if !strings.Contains(got, "99 tok/s") {
		t.Fatalf("approval wait must keep the last exact rate faint, got %q", got)
	}
}

func TestRateClearedOnModelSwitch(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.providerProfile.Name = "qg"
	m.providerProfile.ProviderKind = config.ProviderKindOpenAICompatible
	m.providerProfile.Model = m.modelName
	m.providerProfile.APIKey = "fixture"
	m.userConfigPath = filepath.Join(t.TempDir(), "config.json")
	if _, err := config.UpsertProvider(m.userConfigPath, m.providerProfile, true); err != nil {
		t.Fatal(err)
	}
	info := qgaccount.ModelInfo{ID: "other-live-model"}
	m.modelPickerLiveByProvider["qg"] = append(m.modelPickerLiveByProvider["qg"], providermodeldiscovery.Model{ID: info.ID, Source: "live", APIModel: &info})
	m.newProvider = func(config.ProviderProfile) (qgruntime.Provider, error) { return &fakeProvider{}, nil }
	m.lastCompletedRate = "42 tok/s"
	m = batchSixOpen(m, "/model")
	m.picker.selected = 1
	m = batchSixKey(m, tea.KeyEnter, "")
	if m.modelName != "other-live-model" {
		t.Fatalf("switch failed, model = %s", m.modelName)
	}
	if got := plainRender(t, m.composerTopBorder(120)); strings.Contains(got, "tok/s") {
		t.Fatalf("switch must drop the previous rate, got %q", got)
	}
}

func TestResumeUsageRate(t *testing.T) {
	payload := map[string]any{"completionTokens": float64(850), "call_elapsed_ns": float64(time.Second.Nanoseconds())}
	if got := resumeUsageRate(payload); got != "850 tok/s" {
		t.Fatalf("resume rate = %q, want 850 tok/s", got)
	}
	if got := resumeUsageRate(map[string]any{}); got != "" {
		t.Fatalf("old session without timing must stay rateless, got %q", got)
	}
	if got := resumeUsageRate(map[string]any{"completionTokens": 850}); got != "" {
		t.Fatalf("zero elapsed must stay rateless, got %q", got)
	}
}
