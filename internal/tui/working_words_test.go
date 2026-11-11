package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

func containsWorkingWord(s string) bool {
	for _, word := range workingStatusWords {
		if strings.Contains(s, word) {
			return true
		}
	}
	if strings.Contains(s, workingFireWordAscii) {
		return true
	}
	return false
}

func workingWordIndex(word string) int {
	for i, w := range workingStatusWords {
		if w == word {
			return i
		}
	}
	return -1
}

func fixedProfile(t *testing.T) {
	t.Helper()
	previous := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	t.Cleanup(func() { lipgloss.Writer.Profile = previous })
	clearWorkingLabelCache()
	t.Cleanup(clearWorkingLabelCache)
}

// The first word is drawn from the turn-start time plus the run ID, both
// already injected. Same inputs draw the same word; different starts draw
// different words; with no turn yet the brand word shows.
func TestWorkingWordStartDrawnFromTurnStart(t *testing.T) {
	base := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	first := workingWordStartIndex(base, 41)
	if first < 0 || first >= len(workingStatusWords) {
		t.Fatalf("start index = %d, want in [0,%d)", first, len(workingStatusWords))
	}
	if again := workingWordStartIndex(base, 41); again != first {
		t.Fatalf("same turn start drew %q then %q, want a stable draw", workingStatusWords[again], workingStatusWords[first])
	}
	// A nanosecond later, or the next run ID, moves the draw: the mutant that
	// always returns 0 (brand word every turn) fails here.
	if next := workingWordStartIndex(base.Add(time.Nanosecond), 41); next == first {
		t.Fatalf("adjacent turn start drew the same word %q, want rotation across turns", workingStatusWords[first])
	}
	if next := workingWordStartIndex(base, 42); next == first {
		t.Fatalf("next run ID drew the same word %q, want rotation across turns", workingStatusWords[first])
	}
	if got := workingWordStartIndex(time.Time{}, 7); got != 0 || workingStatusWords[got] != workingStatusText {
		t.Fatalf("zero turn start = %d (%q), want 0 (%q)", got, workingStatusWords[got], workingStatusText)
	}
}

// The word holds ~3s, then steps to the next without repeating the previous.
func TestWorkingWordRotatesEveryThreeSeconds(t *testing.T) {
	fixedProfile(t)
	base := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	m := newModel(t.Context(), Options{})
	m.pending = true
	m.reducedMotion = false
	m.turnStartedAt = base
	m.workingWordSeed = workingWordIndex(workingStatusText)
	at := base
	m.now = func() time.Time { return at }

	wordAt := func(elapsed time.Duration) string {
		at = base.Add(elapsed)
		return m.workingStatusWord()
	}

	first := wordAt(0)
	if first != workingStatusText {
		t.Fatalf("elapsed 0 = %q, want %q", first, workingStatusText)
	}
	if got := wordAt(2999 * time.Millisecond); got != first {
		t.Fatalf("elapsed 2999ms = %q, want still %q", got, first)
	}
	second := wordAt(3 * time.Second)
	if second == first {
		t.Fatalf("elapsed 3s = %q, want a new word", second)
	}
	third := wordAt(6 * time.Second)
	if third == second {
		t.Fatalf("elapsed 6s repeated %q, want no repeat of the previous word", second)
	}
	// Full cycle returns to the start: 9 words, one per 3s slot.
	if got := wordAt(27 * time.Second); got != first {
		t.Fatalf("elapsed 27s = %q, want the cycle back at %q", got, first)
	}
}

// Consecutive slots always differ, for every seed: the no-repeat guard is
// total, not just on the happy path.
func TestWorkingWordNeverRepeatsPrevious(t *testing.T) {
	fixedProfile(t)
	base := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	for seed := range workingStatusWords {
		m := newModel(t.Context(), Options{})
		m.reducedMotion = false
		m.turnStartedAt = base
		m.workingWordSeed = seed
		at := base
		m.now = func() time.Time { return at }
		previous := ""
		for slot := 0; slot < 2*len(workingStatusWords); slot++ {
			at = base.Add(time.Duration(slot) * workingWordSwapInterval)
			got := m.workingStatusWord()
			if slot > 0 && got == previous {
				t.Fatalf("seed %d slot %d repeated %q", seed, slot, got)
			}
			previous = got
		}
	}
}

// Reduced motion pins the brand word: no draw, no rotation.
func TestWorkingWordReducedMotionPinned(t *testing.T) {
	fixedProfile(t)
	base := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	m := newModel(t.Context(), Options{})
	m.pending = true
	m.reducedMotion = true
	m.turnStartedAt = base
	m.workingWordSeed = workingWordIndex("Fuçando")
	at := base
	m.now = func() time.Time { return at }
	for _, elapsed := range []time.Duration{0, 2999 * time.Millisecond, 3 * time.Second, 30 * time.Second} {
		at = base.Add(elapsed)
		m.spinnerPhase = int(elapsed / activeAnimationFrameInterval)
		if got := m.workingStatusWord(); got != workingStatusText {
			t.Fatalf("reduced motion elapsed %s = %q, want %q", elapsed, got, workingStatusText)
		}
		if got := plainRender(t, m.workingStatusLabel()); got != workingStatusText {
			t.Fatalf("reduced motion label = %q, want %q", got, workingStatusText)
		}
	}
	if got := m.workingStatusIndicator(); got != "" {
		t.Fatalf("reduced-motion indicator = %q, want empty", got)
	}
}

// The fire emoji keeps its own color: the wave styles every letter around it,
// never the emoji itself, which still occupies its 2 columns.
func TestWorkingLabelFireEmojiKeepsOwnColor(t *testing.T) {
	fixedProfile(t)
	m := newModel(t.Context(), Options{})
	m.reducedMotion = false
	m.spinnerPhase = 7
	m.workingWordSeed = workingWordIndex(workingFireWord)
	if m.workingWordSeed < 0 {
		t.Fatalf("%q not in the approved words", workingFireWord)
	}

	got := m.workingStatusLabel()
	if plain := plainRender(t, got); plain != workingFireWord {
		t.Fatalf("fire label plain = %q, want %q", plain, workingFireWord)
	}
	if lipgloss.Width("🔥") != 2 {
		t.Fatalf("fire width = %d, want 2", lipgloss.Width("🔥"))
	}
	if lipgloss.Width(plainRender(t, got)) != lipgloss.Width(workingFireWord) {
		t.Fatalf("fire label width mismatch: %q", plainRender(t, got))
	}
	// The emoji is the last rune: the bytes before it must end the previous
	// letter's style instead of opening one for the emoji.
	index := strings.Index(got, "🔥")
	if index < 0 {
		t.Fatalf("fire label missing the emoji: %q", got)
	}
	before := got[:index]
	if !strings.HasSuffix(before, "\x1b[m") && !strings.HasSuffix(before, "\x1b[0m") {
		t.Fatalf("emoji follows %q, want a style reset (unstyled emoji)", before)
	}
}

// Without color (Ascii profile) or with m.noColor, the emoji drops and only
// "Cozinhando" shows.
func TestWorkingWordAsciiDropsEmoji(t *testing.T) {
	previous := lipgloss.Writer.Profile
	t.Cleanup(func() { lipgloss.Writer.Profile = previous })
	clearWorkingLabelCache()
	t.Cleanup(clearWorkingLabelCache)

	base := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	newFireModel := func() model {
		m := newModel(t.Context(), Options{})
		m.reducedMotion = false
		m.turnStartedAt = base
		m.now = func() time.Time { return base }
		m.workingWordSeed = workingWordIndex(workingFireWord)
		return m
	}

	m := newFireModel()
	m.noColor = true
	if got := m.workingStatusWord(); got != workingFireWordAscii {
		t.Fatalf("noColor word = %q, want %q", got, workingFireWordAscii)
	}
	if got := plainRender(t, m.workingStatusLabel()); got != workingFireWordAscii {
		t.Fatalf("noColor label = %q, want %q", got, workingFireWordAscii)
	}

	m = newFireModel()
	lipgloss.Writer.Profile = colorprofile.Ascii
	if got := m.workingStatusWord(); got != workingFireWordAscii {
		t.Fatalf("ascii word = %q, want %q", got, workingFireWordAscii)
	}
}

// The longest word must survive narrow screens through the same cut rule the
// final render applies: fitStyledLine truncates, never wraps or overflows.
func TestWorkingStatusLineWidths(t *testing.T) {
	fixedProfile(t)
	base := time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	longest := "Quebrando o Galho"
	for _, width := range []int{50, 80, 120} {
		m := newModel(t.Context(), Options{})
		m.width = width
		m.pending = true
		m.reducedMotion = false
		m.spinnerPhase = 7
		m.turnStartedAt = base
		m.now = func() time.Time { return base }
		m.workingWordSeed = workingWordIndex(longest)

		line := m.workingStatusLine()
		if strings.Contains(line, "\n") {
			t.Fatalf("width %d: working line wrapped:\n%q", width, line)
		}
		fitted := fitStyledLine(line, width)
		if got := lipgloss.Width(plainRender(t, fitted)); got > width {
			t.Fatalf("width %d: fitted line is %d cells: %q", width, got, plainRender(t, fitted))
		}
		if width == 120 && !strings.Contains(plainRender(t, fitted), longest) {
			t.Fatalf("width 120 should keep the whole %q:\n%q", longest, plainRender(t, fitted))
		}
		// The live suffix (elapsed clock) must still be there after the cut.
		if width == 120 && !strings.Contains(plainRender(t, fitted), "0s") {
			t.Fatalf("width 120 lost the elapsed clock:\n%q", plainRender(t, fitted))
		}
	}
}

// The cache is a pure function of (phase, word, profile): a hit returns
// exactly what a fresh wave render produces, and a theme swap clears it.
func TestWorkingLabelCacheHitMatchesMiss(t *testing.T) {
	fixedProfile(t)
	m := newModel(t.Context(), Options{})
	m.reducedMotion = false
	m.spinnerPhase = 7
	m.turnStartedAt = time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return m.turnStartedAt }
	m.workingWordSeed = workingWordIndex(workingStatusText)

	first := m.workingStatusLabel()
	if got := m.workingStatusLabel(); got != first {
		t.Fatalf("cache hit differs from miss:\nmiss %q\nhit  %q", first, got)
	}
	if plain := plainRender(t, first); plain != workingStatusText {
		t.Fatalf("cached Trabalhando label = %q, want %q", plain, workingStatusText)
	}
	// Same phase, different word: no cross-word collision.
	m.workingWordSeed = workingWordIndex("Fuçando")
	if got := m.workingStatusLabel(); got == first {
		t.Fatalf("different word returned the cached %q", first)
	}
	// A theme swap invalidates: the table is empty right after applyTheme.
	m.workingWordSeed = workingWordIndex(workingStatusText)
	_ = m.workingStatusLabel()
	if len(workingLabelCache) == 0 {
		t.Fatalf("expected a populated label cache before the theme swap")
	}
	previous := qgTheme
	defer func() {
		qgTheme = previous
		rebuildStreamingFadePalette()
		clearWorkingLabelCache()
	}()
	applyTheme("nord", true)
	if len(workingLabelCache) != 0 {
		t.Fatalf("theme swap left %d cached labels, want a cleared table", len(workingLabelCache))
	}
}
