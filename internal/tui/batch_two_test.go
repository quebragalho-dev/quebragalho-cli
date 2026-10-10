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
		assertContains(t, header, "/workspace/qg")
		assertRenderedLineWidths(t, header, width)
		if len(strings.Split(header, "\n")) != 2 {
			t.Fatalf("header must keep two rows: %q", header)
		}
	}
}

func TestComposerModelUsesPaddingBeforeTruncating(t *testing.T) {
	m := interactivePetTestModel(t)
	m.modelName = "muse-spark-1.3-contributor"
	for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m.width = width
			if m.petComposerReservedColumns(width) != petReservedColumns {
				t.Fatal("fixture must reserve the docked pet columns")
			}
			box := plainRender(t, m.composerBox(width))
			lines := strings.Split(box, "\n")
			divider := lines[len(lines)-1]
			assertContains(t, divider, m.modelName)
			assertNotContains(t, divider, "…")
			assertRenderedLineWidths(t, box, width)
			if !strings.HasSuffix(divider, strings.Repeat(" ", petReservedColumns)) {
				t.Fatalf("model invaded pet reservation: %q", divider)
			}
		})
	}
}

func TestWordmarkSeparatesGlyphsWithoutChangingTheirCells(t *testing.T) {
	expected := []string{
		" ██████╗    ██████╗ ",
		"██╔═══██╗  ██╔══════",
		"██║   ██║  ██║  ███╗",
		"██║▄▄ ██║  ██║   ██║",
		"╚██████╔╝  ╚███████║",
		" ╚══▀▀═╝    ╚═════╝ ",
	}
	plain := strings.Split(Wordmark(), "\n")
	styled := qgWordmarkLines()
	for i, want := range expected {
		for _, got := range []string{plain[i], plainRender(t, styled[i])} {
			if got != want || lipgloss.Width(got) != 20 {
				t.Fatalf("wordmark row %d = %q (%d cells), want %q (20 cells)", i+1, got, lipgloss.Width(got), want)
			}
		}
	}
	assertNotContains(t, Wordmark(), "\x1b")
}

func TestWorkspaceMovesFromEmptyStateToStatus(t *testing.T) {
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, t.TempDir())
	}
	m := newModel(context.Background(), Options{Cwd: "/workspace/qg", Version: "1.2.3"})
	for _, mode := range []agent.PermissionMode{agent.PermissionModeAsk, agent.PermissionModeAuto} {
		m.permissionMode = mode
		for _, width := range []int{40, 57, 58, 79, 80, 99, 100, 120} {
			assertNotContains(t, plainRender(t, m.emptyState(width)), "/workspace/qg")
			status := plainRender(t, m.statusLine(width))
			label, _ := m.modeLabel()
			assertContains(t, status, label+" │ /workspace/qg")
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
	status := plainRender(t, m.statusLine(58))
	assertContains(t, status, m.cwd)
	assertNotContains(t, status, "…")
	assertRenderedLineWidths(t, status, 58)
}
