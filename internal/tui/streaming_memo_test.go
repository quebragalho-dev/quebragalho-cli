package tui

import (
	"fmt"
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

// TestStreamingMemosStayBounded pins the memory caps: past 4096 distinct
// entries each table drops and recomputes instead of growing with the stream.
// Values stay correct across the drop.
func TestStreamingMemosStayBounded(t *testing.T) {
	for i := 0; i < 5000; i++ {
		cachedDisplayWidth(fmt.Sprintf("word-%d", i))
	}
	displayWidthMemo.Lock()
	widthEntries := len(displayWidthMemo.entries)
	displayWidthMemo.Unlock()
	if widthEntries > displayWidthMemoMaxEntries {
		t.Fatalf("width memo holds %d entries, want at most %d", widthEntries, displayWidthMemoMaxEntries)
	}
	if got, want := cachedDisplayWidth("word-42"), lipgloss.Width("word-42"); got != want {
		t.Fatalf("width after overflow = %d, want %d", got, want)
	}

	resetStreamingMarkdownCache()
	for i := 0; i < 5000; i++ {
		streamMemoLine(false, fmt.Sprintf("plain line %d", i), func() string { return "x" })
		streamMemoLine(true, fmt.Sprintf("marker line %d", i), func() string { return "y" })
	}
	streamMarkdownCache.mu.Lock()
	inkEntries, markerEntries := len(streamMarkdownCache.inkLines), len(streamMarkdownCache.markerLines)
	streamMarkdownCache.mu.Unlock()
	if inkEntries > streamingStyledMemoMaxEntries || markerEntries > streamingStyledMemoMaxEntries {
		t.Fatalf("styled memos hold %d/%d entries, want at most %d each", inkEntries, markerEntries, streamingStyledMemoMaxEntries)
	}
	if got := streamMemoLine(false, "plain line 7", func() string { return "x" }); got != "x" {
		t.Fatalf("styled memo after overflow = %q, want recomputed value", got)
	}
	resetStreamingMarkdownCache()
}
