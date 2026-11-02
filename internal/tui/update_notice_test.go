package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/update"
	"github.com/quebragalho-dev/quebragalho-cli/internal/updatenotice"
)

func updateNoticeTestConfig(now time.Time) updatenotice.Config {
	return updatenotice.Config{
		CurrentVersion: "0.0.10",
		InstallMethod:  update.InstallMethodStandalone,
		Env:            func(string) string { return "" },
		Now:            func() time.Time { return now },
		// Unroutable: a refresh would fail fast if one ever ran, so any
		// recorded check proves the code path, never the network.
		GitHubEndpoint: "http://127.0.0.1:1/",
		NpmEndpoint:    "http://127.0.0.1:1/",
	}
}

func updateNoticeTestModel(t *testing.T, cfg *updatenotice.Config, state updatenotice.State) model {
	t.Helper()
	m := batchThreeLiveModel(t)
	m.updateNotice = cfg
	if cfg != nil {
		path := t.TempDir() + "/qg/update.json"
		if err := updatenotice.Save(path, state); err != nil {
			t.Fatal(err)
		}
		m.updateNoticeState = path
	}
	m.cwd = ""
	return m
}

func applyUpdateCheckMsg(t *testing.T, m model, state updatenotice.State) (model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(updateCheckMsg{state: state})
	return updated.(model), cmd
}

func TestUpdateNoticeAppearsInFooter(t *testing.T) {
	now := time.Now()
	cfg := updateNoticeTestConfig(now)
	m := updateNoticeTestModel(t, &cfg, updatenotice.State{LastCheck: now, LatestVersion: "0.0.11"})
	m, _ = applyUpdateCheckMsg(t, m, updatenotice.State{LastCheck: now, LatestVersion: "0.0.11"})
	if m.updateAvailable != "0.0.11" {
		t.Fatalf("updateAvailable = %q, want 0.0.11", m.updateAvailable)
	}
	got := plainRender(t, m.statusLine(120))
	if !strings.HasSuffix(got, "nova versão 0.0.11 · reinicie para instalar") {
		t.Fatalf("footer must end with the full notice, got:\n%s", got)
	}
	for _, want := range []string{"auto-approve", "ctx 1.000.000"} {
		assertContains(t, got, want)
	}
}

func TestUpdateNoticeFormsByWidth(t *testing.T) {
	for _, tc := range []struct {
		remaining int
		want      string
	}{
		{100, "nova versão 0.0.11 · reinicie para instalar"},
		{43, "nova versão 0.0.11 · reinicie para instalar"},
		{42, "nova 0.0.11 · reinicie"},
		{22, "nova 0.0.11 · reinicie"},
		{21, "nova 0.0.11"},
		{11, "nova 0.0.11"},
		{10, ""},
		{0, ""},
	} {
		t.Run(fmt.Sprintf("%d", tc.remaining), func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.updateAvailable = "0.0.11"
			if got := plainRender(t, m.updateNoticeSegment(tc.remaining)); got != tc.want {
				t.Fatalf("segment(%d) = %q, want %q", tc.remaining, got, tc.want)
			}
		})
	}
}

func TestUpdateNoticeFitsWholeFormOrNone(t *testing.T) {
	// With the right side present (ctx, 13 cells) each width below sits
	// exactly on a form boundary: left is 22 cells, and the budget keeps a
	// two-cell gap on each crossed side. The line must hold the whole form
	// or nothing — never a cut version — and never overflow the width.
	for _, tc := range []struct {
		width int
		want  string
	}{
		{82, "nova versão 0.0.11 · reinicie para instalar"},
		{81, "nova 0.0.11 · reinicie"},
		{61, "nova 0.0.11 · reinicie"},
		{60, "nova 0.0.11"},
		{50, "nova 0.0.11"},
		{49, ""},
	} {
		t.Run(fmt.Sprintf("%d", tc.width), func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.cwd = ""
			m.updateAvailable = "0.0.11"
			got := plainRender(t, m.statusLine(tc.width))
			if tc.want == "" {
				assertNotContains(t, got, "nova")
			} else if !strings.Contains(got, tc.want) {
				t.Fatalf("width %d must hold the whole form %q, got:\n%s", tc.width, tc.want, got)
			}
			if w := lipgloss.Width(got); w > tc.width {
				t.Fatalf("width %d overflow: line is %d cells:\n%s", tc.width, w, got)
			}
		})
	}
}

func TestUpdateNoticeYieldsOnNarrowWidth(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.cwd = ""
	m.updateAvailable = "0.0.11"
	// Width 34: the right side no longer fits so it drops, and the 11-cell
	// tiny form still loses to the two-cell gap — while mode and effort stay
	// whole on the left.
	got := plainRender(t, m.statusLine(34))
	assertNotContains(t, got, "nova")
	for _, want := range []string{"auto-approve", "n/d"} {
		assertContains(t, got, want)
	}
}

func TestUpdateNoticeHiddenDuringPriorityStates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*model)
	}{
		{"exit confirm", func(m *model) { m.exitConfirmActive = true }},
		{"cancel confirm", func(m *model) { m.cancelConfirmActive = true }},
		{"dictation download", func(m *model) {
			m.dictation.downloading = true
			m.dictation.downloadStatus = "Starting download…"
		}},
		{"dictation recording", func(m *model) { m.dictation.phase = dictRecording }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.cwd = ""
			m.updateAvailable = "0.0.11"
			tc.mutate(&m)
			assertNotContains(t, plainRender(t, m.statusLine(120)), "nova")
		})
	}
}

func TestUpdateNoticeHiddenForSkippedVersion(t *testing.T) {
	now := time.Now()
	cfg := updateNoticeTestConfig(now)
	m := updateNoticeTestModel(t, &cfg, updatenotice.State{LastCheck: now, LatestVersion: "0.0.11", SkippedVersion: "0.0.11"})
	m, _ = applyUpdateCheckMsg(t, m, updatenotice.State{LastCheck: now, LatestVersion: "0.0.11", SkippedVersion: "0.0.11"})
	if m.updateAvailable != "" {
		t.Fatalf("updateAvailable = %q, want empty for a skipped version", m.updateAvailable)
	}
	assertNotContains(t, plainRender(t, m.statusLine(120)), "nova")
}

func TestUpdateCheckDisabled(t *testing.T) {
	now := time.Now()
	withEnv := func(env func(string) string) *updatenotice.Config {
		cfg := updateNoticeTestConfig(now)
		cfg.Env = env
		return &cfg
	}
	dev := updateNoticeTestConfig(now)
	dev.CurrentVersion = "dev"
	homebrew := updateNoticeTestConfig(now)
	homebrew.InstallMethod = update.InstallMethodHomebrew
	for _, tc := range []struct {
		name string
		cfg  *updatenotice.Config
	}{
		{"nil config", nil},
		{"dev build", &dev},
		{"homebrew", &homebrew},
		{"QG_NO_UPDATE_CHECK", withEnv(func(key string) string {
			if key == "QG_NO_UPDATE_CHECK" {
				return "1"
			}
			return ""
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := batchThreeLiveModel(t)
			m.updateNotice = tc.cfg
			if m.updateCheckEnabled() {
				t.Fatal("check must be disabled")
			}
			if cmd := m.updateCheckInitCmd(); cmd != nil {
				t.Fatal("disabled check must not schedule a command")
			}
			updated, cmd := m.Update(updateCheckTickMsg{})
			_ = updated.(model)
			if cmd != nil {
				t.Fatal("disabled check must not reschedule on tick")
			}
		})
	}
	t.Run("enabled schedules", func(t *testing.T) {
		cfg := updateNoticeTestConfig(now)
		m := batchThreeLiveModel(t)
		m.updateNotice = &cfg
		if !m.updateCheckEnabled() {
			t.Fatal("check must be enabled")
		}
		if cmd := m.updateCheckInitCmd(); cmd == nil {
			t.Fatal("enabled check must schedule a command")
		}
	})
}

func TestUpdateCheckSkipsRefreshWhenNotDue(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	cfg := updateNoticeTestConfig(now)
	m := updateNoticeTestModel(t, &cfg, updatenotice.State{LastCheck: now, LatestVersion: "0.0.11"})
	msg, ok := execCmd(m.updateCheckCmd()).(updateCheckMsg)
	if !ok {
		t.Fatalf("check command returned %T, want updateCheckMsg", msg)
	}
	if !msg.state.LastCheck.Equal(now) {
		t.Fatalf("fresh state must not refresh, LastCheck = %v", msg.state.LastCheck)
	}
	if msg.state.LatestVersion != "0.0.11" {
		t.Fatalf("LatestVersion = %q, want 0.0.11", msg.state.LatestVersion)
	}
}

func TestUpdateCheckSchedulesHourly(t *testing.T) {
	now := time.Now()
	cfg := updateNoticeTestConfig(now)
	m := updateNoticeTestModel(t, &cfg, updatenotice.State{LastCheck: now, LatestVersion: "0.0.11"})
	_, cmd := applyUpdateCheckMsg(t, m, updatenotice.State{LastCheck: now, LatestVersion: "0.0.11"})
	if cmd == nil {
		t.Fatal("after a check the next hourly check must be scheduled")
	}
	updated, cmd := m.Update(updateCheckTickMsg{})
	_ = updated.(model)
	if cmd == nil {
		t.Fatal("a tick must trigger a new check")
	}
}
