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

// TestStreamingMemosStayBounded pins the memory bound behaviorally: filling
// both tables with megabytes of wide lines must keep retained bytes flat
// (tables drop and recompute instead of growing with the stream) and every
// value must still render correctly afterwards. Retained bytes are measured by
// scanning the maps, so a mutant that stops counting still fails.
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
	wide := strings.Repeat("👩‍👩‍👧‍👦", 40) + strings.Repeat("x", 200)
	for i := 0; i < 3000; i++ {
		line := fmt.Sprintf("plain %d %s", i, wide)
		if got := streamMemoLine(false, line, func() string { return "v:" + line }); got != "v:"+line {
			t.Fatalf("ink memo returned %q", got)
		}
		mline := fmt.Sprintf("with "+markdownBoldStart+"bold %d %s"+markdownBoldEnd+" inside", i, wide)
		if got := streamMemoLine(true, mline, func() string { return "w:" + mline }); got != "w:"+mline {
			t.Fatalf("marker memo returned %q", got)
		}
	}
	inkBytes, markerBytes := streamingStyledMemoBytes()
	const maxReasonable = 2 << 20
	if inkBytes > maxReasonable || markerBytes > maxReasonable {
		t.Fatalf("styled memos retain %d/%d bytes after overfill, want bounded tables", inkBytes, markerBytes)
	}
	resetStreamingMarkdownCache()
	if ink, _ := streamingStyledMemoBytes(); ink != 0 {
		t.Fatalf("reset must clear styled memos, ink holds %d bytes", ink)
	}
}

// TestStreamingThemeResetWithNilSharedCache pins the theme-reset guarantee
// when the shared LRU is disabled (tests only): the streaming memo still
// holds painted lines, so applyTheme must zero it through its own path.
func TestStreamingThemeResetWithNilSharedCache(t *testing.T) {
	previous := qgTheme
	previousCache := defaultRenderCache
	defer func() {
		qgTheme = previous
		defaultRenderCache = previousCache
		rebuildStreamingFadePalette()
		resetStreamingMarkdownCache()
	}()

	resetStreamingMarkdownCache()
	defaultRenderCache = nil
	m := model{}
	line := "plain prose line"
	before := m.styleStreamingLine(line, 0, 1)
	if before != qgTheme.ink.Render(line) {
		t.Fatalf("setup: unstyled line = %q, want base ink render", before)
	}
	applyTheme("nord", true)
	if got := m.styleStreamingLine(line, 0, 1); got != qgTheme.ink.Render(line) {
		t.Fatalf("after theme change with nil shared cache: got %q, want fresh ink render %q", got, qgTheme.ink.Render(line))
	}
}
