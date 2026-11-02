package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestCachedDisplayWidthMatchesLipgloss(t *testing.T) {
	samples := []string{
		"",
		"lorem",
		"  indent",
		"日本語テスト",
		"emoji 🎉🚀",
		"café naïve façade",
		"│ table │ cell │",
		"```go",
		qgTheme.ink.Render("styled"),
		strings.Repeat("x", 200),
		strings.Repeat("日本", 100),
	}
	for _, s := range samples {
		if got, want := cachedDisplayWidth(s), lipgloss.Width(s); got != want {
			t.Errorf("cachedDisplayWidth(%q) = %d, want %d", s, got, want)
		}
	}
	// A repeated word measures once per distinct string, not per frame.
	for i := 0; i < 100; i++ {
		cachedDisplayWidth("lorem")
	}
}

func TestStreamingStyledLinesSurviveThemeChange(t *testing.T) {
	previous := qgTheme
	defer func() {
		qgTheme = previous
		rebuildStreamingFadePalette()
		resetStreamingMarkdownCache()
	}()

	resetStreamingMarkdownCache()
	m := model{}
	line := "plain prose line"
	marker := "with " + markdownBoldStart + "bold" + markdownBoldEnd + " inside"

	plainBefore := m.styleStreamingLine(line, 0, 1)
	markerBefore := m.styleStreamingLine(marker, 0, 1)
	if plainBefore != m.styleStreamingLine(line, 0, 1) || markerBefore != m.styleStreamingLine(marker, 0, 1) {
		t.Fatalf("repeat style differs: %q vs %q", plainBefore, markerBefore)
	}

	applyTheme("nord", true)
	if got := m.styleStreamingLine(line, 0, 1); got != qgTheme.ink.Render(line) {
		t.Fatalf("after theme change: got %q, want fresh ink render %q", got, qgTheme.ink.Render(line))
	}
	if got, want := m.styleStreamingLine(marker, 0, 1), styleAssistantMarkdownLine(marker, qgTheme.ink); got != want {
		t.Fatalf("after theme change marker: got %q, want %q", got, want)
	}
}
