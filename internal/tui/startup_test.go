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
	assertContains(t, view, brandWordmarkPlainRow(t, m, brandWordmarkSticker, 2))
	assertContains(t, view, emptyStateTagline+" "+emptyStateHeart)
	assertNotContains(t, view, "QUEBRA-GALHO")
	assertNotContains(t, view, "Any model. Every tool.")
	assertNotContains(t, view, "running qg against ")
	assertNotContains(t, view, "add a --version flag")
	assertNotContains(t, view, "explain internal/agent/loop.go")
	assertNotContains(t, view, "fix the failing test in internal/tools")
}

// wantBrandWordmark is the uncolored small pixel wordmark, "quebragalho" in
// the dashboard's lowercase, as `qg --version` prints it (trailing blanks
// trimmed).
var wantBrandWordmark = []string{
	"                  ▄▄                           ▄▄ ▄▄",
	" ▄▄▄▄ ▄▄ ▄▄  ▄▄▄  ██▄▄  ▄▄ ▄ ▄▄▄▄   ▄▄▄▄ ▄▄▄▄  ██ ██▄▄   ▄▄▄",
	"██ ██ ██ ██ ██▄██ ██ ██ ██▀   ▄▄██ ██ ██  ▄▄██ ██ ██ ██ ██ ██",
	"▀█▄██ ▀█▄██ ▀█▄▄▄ ██▄█▀ ██   ▀█▄██ ▀█▄██ ▀█▄██ ██ ██ ██ ▀█▄█▀",
	"   ██                              ▄▄▄█▀",
}

// The brand lockup replaces the QG block: the pixel "quebragalho" and the
// landing's tagline — the --version banner must carry both, uncolored.
func TestWordmarkLockupIsTheBrand(t *testing.T) {
	wordmark := Wordmark()
	if strings.Contains(wordmark, "\x1b") {
		t.Fatalf("expected uncolored wordmark, got %q", wordmark)
	}
	if strings.Contains(wordmark, "ZER") || strings.Contains(wordmark, "Any model. Every tool.") || strings.Contains(wordmark, "QUEBRA-GALHO") {
		t.Fatalf("old lockup leaked into the brand: %q", wordmark)
	}
	lines := strings.Split(wordmark, "\n")
	if len(lines) != len(wantBrandWordmark)+2 {
		t.Fatalf("lockup lines = %d, want wordmark %d + blank + tagline", len(lines), len(wantBrandWordmark))
	}
	for index, want := range wantBrandWordmark {
		if lines[index] != want {
			t.Fatalf("wordmark row %d:\n got %q\nwant %q", index+1, lines[index], want)
		}
	}
	if got, want := lines[len(lines)-1], "democratizando a ia. ♥"; got != want {
		t.Fatalf("tagline = %q, want %q", got, want)
	}
}

// Every glyph of a pixel font shares its grid: the same rows, each of one
// width, so letters line up on the baseline and the x-height. The x-height's
// bottom row is the font's baseline: ink there, none in the row below except
// for descenders.
func TestBrandFontGlyphsShareTheGrid(t *testing.T) {
	for _, tc := range []struct {
		name          string
		font          brandFont
		word          *brandWordmark
		width, height int
	}{
		{"sticker", brandFontSticker, brandWordmarkSticker, 64, 5},
		{"small", brandFontSmall, brandWordmarkSmall, 61, 5},
	} {
		rows := len(tc.font.glyphs['q'])
		for _, word := range brandWords {
			for _, letter := range word {
				glyph, ok := tc.font.glyphs[letter]
				if !ok {
					t.Fatalf("%s font has no %q", tc.name, letter)
				}
				if len(glyph) != rows {
					t.Fatalf("%s %q has %d rows, want %d", tc.name, letter, len(glyph), rows)
				}
				for row, pixels := range glyph {
					if len(pixels) != len(glyph[0]) {
						t.Fatalf("%s %q row %d is %d pixels wide, want %d", tc.name, letter, row+1, len(pixels), len(glyph[0]))
					}
					if strings.Trim(pixels, "#.") != "" {
						t.Fatalf("%s %q row %d = %q, want only # and .", tc.name, letter, row+1, pixels)
					}
				}
			}
		}
		for _, letter := range "eo" {
			glyph := tc.font.glyphs[letter]
			if !strings.Contains(glyph[tc.font.baseline], "#") || strings.Contains(glyph[tc.font.baseline+1], "#") {
				t.Fatalf("%s %q does not sit on baseline row %d", tc.name, letter, tc.font.baseline+1)
			}
		}
		if tc.word.width != tc.width || tc.word.cellRows != tc.height {
			t.Fatalf("%s wordmark = %d×%d cells, want %d×%d", tc.name, tc.word.width, tc.word.cellRows, tc.width, tc.height)
		}
	}
}

// The sticker's letters keep the heavy pixel font's weight: "l" and "r"
// stand on two-pixel stems, and "q" and "g" share one bowl, told apart by
// their descenders.
func TestBrandFontStickerIsConsistent(t *testing.T) {
	glyphs := brandFontSticker.glyphs
	baseline := brandFontSticker.baseline
	for _, letter := range "lr" {
		if got := strings.Count(glyphs[letter][baseline], "#"); got != 2 {
			t.Fatalf("%q stem is %d pixels at the baseline, want 2", letter, got)
		}
	}
	for row := baseline - 4; row <= baseline; row++ {
		if glyphs['q'][row] != glyphs['g'][row] {
			t.Fatalf("row %d: q %q and g %q do not share the bowl", row+1, glyphs['q'][row], glyphs['g'][row])
		}
	}
	if glyphs['q'][baseline+1] == glyphs['g'][baseline+1] {
		t.Fatalf("q and g share the descender %q", glyphs['q'][baseline+1])
	}
}

// The lockup appends the domain to the baseline row; drawing it again never
// repeats ".dev".
func TestBrandLockupDrawsTheDomainOnce(t *testing.T) {
	m := brandTestModel(160, 30, true, false)
	for range 3 {
		view := plainRender(t, m.emptyState(160))
		if got := strings.Count(view, brandDomain); got != 1 {
			t.Fatalf("domain drawn %d times:\n%s", got, view)
		}
	}
}

// The system theme takes the QG accent for the terminal's own contrast direction
// instead of the ANSI cyan role color.
func TestSystemThemeAccentFollowsTerminalPalette(t *testing.T) {
	dark := buildSystemThemeForTerminal(true)
	light := buildSystemThemeForTerminal(false)
	if got := colorHex(t, dark.accentColor); !strings.EqualFold(got, darkPalette.accent) {
		t.Fatalf("dark system accent = %s, want %s", got, darkPalette.accent)
	}
	if got := colorHex(t, light.accentColor); !strings.EqualFold(got, lightPalette.accent) {
		t.Fatalf("light system accent = %s, want %s", got, lightPalette.accent)
	}
}

// The dark palette carries the brand lime and ink of the dashboard and the
// landing (apps/web .qg-theme: --qg-lime, --qg-tx).
func TestDarkPaletteAccentIsQGBrand(t *testing.T) {
	if !strings.EqualFold(darkPalette.accent, "#b7fe60") {
		t.Fatalf("darkPalette.accent = %q, want #b7fe60", darkPalette.accent)
	}
	if !strings.EqualFold(darkPalette.ink, "#fafafa") {
		t.Fatalf("darkPalette.ink = %q, want #fafafa", darkPalette.ink)
	}
	if !strings.EqualFold(brandMascotColors['L'], darkPalette.accent) {
		t.Fatalf("mascot lime = %q, want the brand accent %q", brandMascotColors['L'], darkPalette.accent)
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
