package tui

import (
	"context"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestEmptyStateShowsBrandAndTaglineOnly(t *testing.T) {
	m := newModel(context.Background(), Options{ProviderName: "anthropic", ModelName: "claude-sonnet-4.5"})
	m.width, m.height = 100, 30

	view := plainRender(t, m.View())
	assertContains(t, view, "██████╗  ██████╗")
	assertContains(t, view, "QUEBRA-GALHO")
	assertContains(t, view, emptyStateTagline)
	assertNotContains(t, view, "Any model. Every tool.")
	assertNotContains(t, view, "running qg against ")
	assertNotContains(t, view, "add a --version flag")
	assertNotContains(t, view, "explain internal/agent/loop.go")
	assertNotContains(t, view, "fix the failing test in internal/tools")
}

// The QG lockup replaces ZERO: brand accent, QG block, QUEBRA-GALHO and the
// QG tagline — the --version banner must carry all of it, uncolored.
func TestWordmarkLockupIsQGBrand(t *testing.T) {
	wordmark := Wordmark()
	if strings.Contains(wordmark, "\x1b") {
		t.Fatalf("expected uncolored wordmark, got %q", wordmark)
	}
	if strings.Contains(wordmark, "ZER") || strings.Contains(wordmark, "Any model. Every tool.") {
		t.Fatalf("Zero-era wordmark leaked into the QG lockup: %q", wordmark)
	}
	lines := strings.Split(wordmark, "\n")
	if len(lines) != len(qgWordmarkGlyphLines)+4 {
		t.Fatalf("lockup lines = %d, want glyph %d + blank + brand + blank + tagline",
			len(lines), len(qgWordmarkGlyphLines))
	}
	for index, row := range qgWordmarkGlyphLines {
		if want := row[0] + row[1]; lines[index] != want {
			t.Fatalf("glyph line %d: got %q, want %q", index, lines[index], want)
		}
	}
	if lines[len(qgWordmarkGlyphLines)+1] != "QUEBRA-GALHO" {
		t.Fatalf("brand line = %q, want QUEBRA-GALHO", lines[len(qgWordmarkGlyphLines)+1])
	}
	if lines[len(qgWordmarkGlyphLines)+3] != "IA QUE TRABALHA COM VOCE" {
		t.Fatalf("tagline = %q, want IA QUE TRABALHA COM VOCE", lines[len(qgWordmarkGlyphLines)+3])
	}
}

// The dark palette carries the QG brand accent.
func TestDarkPaletteAccentIsQGBrand(t *testing.T) {
	if darkPalette.accent != "#9df723" {
		t.Fatalf("darkPalette.accent = %q, want #9df723", darkPalette.accent)
	}
}

func TestEmptyStateShowsVersion(t *testing.T) {
	m := newModel(context.Background(), Options{Version: "0.2.0"})
	m.width, m.height = 100, 30

	view := plainRender(t, m.View())
	assertContains(t, view, "v0.2.0")
}

func TestDisplayVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0.2.0", "v0.2.0"},
		{"v0.2.0", "v0.2.0"},
		{"dev", "dev"},
		{"  ", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := displayVersion(tc.in); got != tc.want {
			t.Fatalf("displayVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEmptyStateDisappearsAfterFirstRow(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.width, m.height = 100, 30
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendUser, text: "hello"})

	view := viewString(m.View())
	if strings.Contains(view, emptyStateTagline) {
		t.Fatal("empty state must disappear once the transcript has content")
	}
	assertContains(t, view, "hello")
}

func TestDigitsTypeNormallyOnEmptySurface(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.width, m.height = 100, 30

	m = typeRunes(t, m, "2")
	if got := m.input.Value(); got != "2" {
		t.Fatalf("digit should type normally on the empty surface, got %q", got)
	}

	// With text already in the composer the digit keeps typing normally.
	m = newModel(context.Background(), Options{})
	m.input.SetValue("count to ")
	m.resetComposerFromInput()
	m = typeRunes(t, m, "3")
	if got := m.input.Value(); got != "count to 3" {
		t.Fatalf("digit should append to a non-empty composer, got %q", got)
	}

	// Once the transcript has content, a bare digit types normally too.
	fresh := newModel(context.Background(), Options{})
	fresh.transcript = reduceTranscript(fresh.transcript, transcriptAction{kind: actionAppendUser, text: "hi"})
	fresh = typeRunes(t, fresh, "1")
	if got := fresh.input.Value(); got != "1" {
		t.Fatalf("digit should type normally after the first turn, got %q", got)
	}
}

func TestBorderedBlockFitsLongPlainLines(t *testing.T) {
	block := borderedBlock(24, []string{"this line should truncate inside the border"})

	assertContains(t, block, "\u2026")
	assertRenderedLineWidths(t, block, 24)
}

func TestBorderedBlockFitsLongStyledLines(t *testing.T) {
	block := borderedBlock(26, []string{
		qgTheme.accent.Render("styled line should truncate inside the border"),
	})

	assertContains(t, block, "\u2026")
	assertRenderedLineWidths(t, block, 26)
}

func assertRenderedLineWidths(t *testing.T, block string, width int) {
	t.Helper()

	for _, line := range strings.Split(block, "\n") {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("expected line width %d, got %d for %q", width, got, line)
		}
	}
}
