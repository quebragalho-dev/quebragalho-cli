package tui

import (
	"context"
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// System no longer guesses a light or dark canvas from the terminal. The legacy
// auto argument is accepted for old configs and scripts, but resolves to the
// terminal-native system preference without issuing a background-color query.
func TestThemeAutoAliasUsesSystemWithoutBackgroundProbe(t *testing.T) {
	defer applyTheme(themeDark, true)
	m := newModel(context.Background(), Options{ModelName: "gpt-4"})
	m.input.SetValue("/theme auto")
	updated, cmd := m.handleSubmit()
	next := updated.(model)
	if cmd == nil {
		t.Fatal("/theme auto should schedule a transient confirmation expiry")
	}
	if next.themeMode != themeSystem {
		t.Fatalf("/theme auto mode = %q, want system", next.themeMode)
	}
	if next.transientNotice.text != "Theme: System" {
		t.Fatalf("/theme auto notice = %q", next.transientNotice.text)
	}
}

func TestSystemThemeReprobesAccentWithoutPaintingCanvas(t *testing.T) {
	previous := qgTheme
	t.Cleanup(func() { qgTheme = previous })
	m := newModel(context.Background(), Options{Theme: "system"})
	for _, tc := range []struct {
		background color.Color
		accent     string
	}{
		{color.White, "#54700a"},
		{color.Black, "#9df723"},
	} {
		updated, _ := m.Update(tea.BackgroundColorMsg{Color: tc.background})
		m = updated.(model)
		if got := colorHex(t, qgTheme.accentColor); got != tc.accent {
			t.Fatalf("system accent after background update = %q, want %q", got, tc.accent)
		}
		if _, ok := qgTheme.bgPanel.(lipgloss.NoColor); !ok {
			t.Fatal("system background update must not paint panel")
		}
		if view := m.View(); view.BackgroundColor != nil || view.ForegroundColor != nil {
			t.Fatal("system background update must leave terminal canvas unchanged")
		}
	}
}
