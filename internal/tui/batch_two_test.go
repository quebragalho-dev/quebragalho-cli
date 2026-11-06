package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
)

func TestHeaderOmitsContextCapacity(t *testing.T) {
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, t.TempDir())
	}
	m := newModel(context.Background(), Options{Cwd: "/workspace/qg", ModelName: "muse-spark-1.3-contributor"})
	if m.modelContextWindow(m.modelName) != 1000000 {
		t.Fatal("fixture must exercise the 1M context header")
	}
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		header := plainRender(t, m.titleBar(width))
		assertNotContains(t, header, "1M")
		assertNotContains(t, header, "/workspace/qg")
		assertRenderedLineWidths(t, header, width)
		if len(strings.Split(header, "\n")) != 2 {
			t.Fatalf("header must keep two rows: %q", header)
		}
	}
}

func TestComposerModelUsesPaddingBeforeTruncating(t *testing.T) {
	m := mouseTestModel()
	m.modelName = "muse-spark-1.3-contributor"
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m.width = width
			box := plainRender(t, m.composerBox(width))
			lines := strings.Split(box, "\n")
			top := lines[0]
			assertContains(t, top, m.modelName)
			assertNotContains(t, top, "…")
			assertRenderedLineWidths(t, box, width)
		})
	}
}

// The pixel wordmark fills whole cells: one row per cell row, every row as
// wide as the wordmark, and no color in `qg --version`.
func TestWordmarkRowsFillTheirCells(t *testing.T) {
	word := brandWordmarkSmall
	rows := word.rows()
	if len(rows) != word.cellRows {
		t.Fatalf("rows = %d, want %d", len(rows), word.cellRows)
	}
	for i, row := range rows {
		if got := lipgloss.Width(row); got != word.width {
			t.Fatalf("row %d is %d cells, want %d", i+1, got, word.width)
		}
	}
	assertNotContains(t, Wordmark(), "\x1b")
}

// The status line always carries the workspace; the empty state shows it
// only inside the welcome card, among the session's details.
func TestWorkspaceIsInTheStatusAndOnlyTheCard(t *testing.T) {
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, t.TempDir())
	}
	m := newModel(context.Background(), Options{Cwd: "/workspace/qg", Version: "1.2.3"})
	for _, mode := range []agent.PermissionMode{agent.PermissionModeAsk, agent.PermissionModeAuto} {
		m.permissionMode = mode
		for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
			if m.emptyStateTier(width) == brandTierCard {
				assertContains(t, plainRender(t, m.emptyState(width)), "/workspace/qg")
			} else {
				assertNotContains(t, plainRender(t, m.emptyState(width)), "/workspace/qg")
			}
			status := plainRender(t, m.statusLine(width))
			label, _ := m.modeLabel()
			assertContains(t, status, label+" · n/d")
			assertContains(t, status, "/w")
			if got := lipgloss.Width(status); got > width {
				t.Fatalf("status width = %d, want <= %d: %q", got, width, status)
			}
		}
	}
	assertContains(t, plainRender(t, m.emptyState(120)), "v1.2.3")
	m.cwd = "/workspace/" + strings.Repeat("界", 100)
	if got := lipgloss.Width(m.statusLine(40)); got > 40 {
		t.Fatalf("long workspace status width = %d, want <= 40", got)
	}
	m.exitConfirmActive = true
	status := plainRender(t, m.statusLine(40))
	assertContains(t, status, ctrlCExitConfirmText)
	assertNotContains(t, status, "/workspace")
}

func TestStatusWorkspaceUsesLastAvailableCell(t *testing.T) {
	m := model{permissionMode: agent.PermissionModeAsk, cwd: "/" + strings.Repeat("a", 47)}
	status := plainRender(t, m.appendStatusWorkspace("  ● ask", "", 58))
	assertContains(t, status, m.cwd)
	assertNotContains(t, status, "…")
	assertRenderedLineWidths(t, status, 58)
}
