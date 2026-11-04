package tui

import (
	"context"
	"fmt"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// brandMascotEars is the mascot's first cell row, which nothing else draws.
const brandMascotEars = " ▄▀▀▄▀▀▄            "

func brandTestModel(width, height int, dark, noColor bool) model {
	m := newModel(context.Background(), Options{Version: "0.2.0"})
	m.width, m.height = width, height
	m.hasDarkBg = dark
	m.terminalBgKnown = true
	m.noColor = noColor
	return m
}

// brandWordmarkPlainRow is one row of a wordmark as the model draws it (the
// sticker in color, plain letters without), colors stripped and trailing
// blanks trimmed.
func brandWordmarkPlainRow(t *testing.T, m model, word *brandWordmark, row int) string {
	t.Helper()
	return strings.TrimRight(plainRender(t, m.brandWordmarkRows(word)[row]), " ")
}

func TestBrandMascotFirstRowIsTheEars(t *testing.T) {
	if got := plainRender(t, brandMascotLines()[0]); got != brandMascotEars {
		t.Fatalf("mascot row 1 = %q, want %q", got, brandMascotEars)
	}
}

// The empty state draws the largest brand art that fits: the mascot beside,
// then above, the sticker wordmark; the wordmark alone; the mascot beside the
// one-line wordmark; or just the line. The mascot needs color output; its
// outline lets it show on light terminals too.
func TestEmptyStateBrandTiers(t *testing.T) {
	cases := []struct {
		name          string
		width         int
		dark, noColor bool
		tier          brandTier
		mascot        bool
		word          *brandWordmark
	}{
		{name: "wide dark", width: 160, dark: true, tier: brandTierLockup, mascot: true, word: brandWordmarkSticker},
		{name: "96 dark", width: 96, dark: true, tier: brandTierLockup, mascot: true, word: brandWordmarkSticker},
		{name: "95 dark", width: 95, dark: true, tier: brandTierWordmark, mascot: true, word: brandWordmarkSticker},
		{name: "68 dark", width: 68, dark: true, tier: brandTierWordmark, mascot: true, word: brandWordmarkSticker},
		{name: "67 dark", width: 67, dark: true, tier: brandTierCompact, mascot: true},
		{name: "49 dark", width: 49, dark: true, tier: brandTierCompact, mascot: true},
		{name: "48 dark", width: 48, dark: true, tier: brandTierText},
		{name: "wide light", width: 160, tier: brandTierLockup, mascot: true, word: brandWordmarkSticker},
		{name: "80 light", width: 80, tier: brandTierWordmark, mascot: true, word: brandWordmarkSticker},
		{name: "narrow light", width: 50, tier: brandTierCompact, mascot: true},
		{name: "wide no color", width: 160, dark: true, noColor: true, tier: brandTierWordmark, word: brandWordmarkSticker},
		{name: "68 no color", width: 68, dark: true, noColor: true, tier: brandTierWordmark, word: brandWordmarkSticker},
		{name: "67 no color", width: 67, dark: true, noColor: true, tier: brandTierText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := brandTestModel(tc.width, 30, tc.dark, tc.noColor)
			layout := m.emptyStateLayout(tc.width)
			if layout.tier != tc.tier || layout.word != tc.word {
				t.Fatalf("layout = tier %d word %v, want tier %d word %v", layout.tier, layout.word, tc.tier, tc.word)
			}
			view := plainRender(t, m.emptyState(tc.width))
			if got := strings.Contains(view, brandMascotEars); got != tc.mascot {
				t.Fatalf("mascot drawn = %v, want %v:\n%s", got, tc.mascot, view)
			}
			if tc.word != nil {
				assertContains(t, view, brandWordmarkPlainRow(t, m, tc.word, 2))
			}
			if tc.tier == brandTierLockup && strings.Count(view, brandDomain) != 1 {
				t.Fatalf("want the domain once beside the wordmark:\n%s", view)
			}
			if tc.word == nil {
				assertContains(t, view, "quebra galho")
			}
			assertContains(t, view, emptyStateTagline+" "+emptyStateHeart)
			assertContains(t, view, "v0.2.0")
		})
	}
}

// A short pane gets smaller art: the tier reads the real terminal height,
// not the 18-row floor the rest of the startup layout uses.
func TestEmptyStateBrandTierFollowsTheHeight(t *testing.T) {
	cases := []struct {
		width, height int
		tier          brandTier
		word          *brandWordmark
	}{
		{width: 120, height: 0, tier: brandTierLockup, word: brandWordmarkSticker}, // before the first resize
		{width: 120, height: 18, tier: brandTierLockup, word: brandWordmarkSticker},
		{width: 120, height: 17, tier: brandTierWordmark, word: brandWordmarkSticker},
		{width: 120, height: 15, tier: brandTierWordmark, word: brandWordmarkSticker},
		{width: 120, height: 14, tier: brandTierText},
		{width: 50, height: 17, tier: brandTierCompact},
		{width: 50, height: 16, tier: brandTierText},
	}
	for _, tc := range cases {
		m := brandTestModel(tc.width, tc.height, true, false)
		if got := m.emptyStateLayout(tc.width); got.tier != tc.tier || got.word != tc.word {
			t.Fatalf("%d×%d: tier %d word %v, want tier %d word %v", tc.width, tc.height, got.tier, got.word, tc.tier, tc.word)
		}
	}
}

// Narrower than the lockup, the mascot goes above the wordmark when the pane
// has room for both; a standard 80×24 terminal does, even with the idle
// shortcut hint (see the next test).
func TestEmptyStateMascotAboveTheWordmarkFollowsTheHeight(t *testing.T) {
	for _, tc := range []struct {
		height int
		mascot bool
	}{{24, true}, {22, true}, {21, false}} {
		m := brandTestModel(80, tc.height, true, false)
		if got := m.emptyStateLayout(80); got.tier != brandTierWordmark || got.word != brandWordmarkSticker || got.mascotAbove != tc.mascot {
			t.Fatalf("80×%d: layout = %+v, want the sticker wordmark, mascot above %v", tc.height, got, tc.mascot)
		}
		view := plainRender(t, m.emptyState(80))
		if got := strings.Contains(view, brandMascotEars); got != tc.mascot {
			t.Fatalf("80×%d: mascot drawn = %v, want %v:\n%s", tc.height, got, tc.mascot, view)
		}
		assertContains(t, view, brandWordmarkPlainRow(t, m, brandWordmarkSticker, 2))
		assertContains(t, view, emptyStateTagline)
	}
}

// In the managed (alt-screen) view the idle shortcut hint sits above the
// composer and takes a row from the stream area; the brand must count it, or
// its first line goes under the title bar. An 80×24 terminal keeps the mascot.
func TestEmptyStateCountsTheIdleHint(t *testing.T) {
	m := brandTestModel(80, 24, true, false)
	m.altScreen = true
	if m.composerIdleHint() == "" {
		t.Fatal("expected the idle shortcut hint in the managed view")
	}
	if got, want := m.emptyStateRows(), 24-emptyStateChromeRows-1; got != want {
		t.Fatalf("emptyStateRows = %d, want %d", got, want)
	}
	lines := m.emptyStateLines(80)
	if len(lines) > m.emptyStateRows() {
		t.Fatalf("%d brand lines for %d rows", len(lines), m.emptyStateRows())
	}
	if !strings.Contains(plainRender(t, lines[0]), brandMascotEars) {
		t.Fatalf("first brand line is not the mascot's ears: %q", plainRender(t, lines[0]))
	}
}

// The empty state, centering gaps included, fits the stream area in both
// directions, so nothing is cut off or pushed out of view.
func TestEmptyStateBrandNeverOverflows(t *testing.T) {
	for _, managed := range []bool{false, true} {
		for _, noColor := range []bool{false, true} {
			for _, height := range []int{0, 10, 12, 14, 16, 18, 19, 20, 22, 24, 25, 26, 40} {
				for width := 24; width <= 170; width++ {
					m := brandTestModel(width, height, true, noColor)
					m.altScreen = managed
					m.gitBranch = "feat/um-branch-com-nome-bem-comprido"
					streamRows := height - emptyStateChromeRows
					if height == 0 {
						streamRows = defaultStartupHeight - emptyStateChromeRows
					}
					if m.composerIdleHint() != "" {
						streamRows--
					}
					lines := strings.Split(m.emptyState(width), "\n")
					if len(lines) > streamRows {
						t.Fatalf("managed=%v noColor=%v height=%d width=%d: %d lines for %d stream rows", managed, noColor, height, width, len(lines), streamRows)
					}
					for _, line := range lines {
						if got := lipgloss.Width(line); got > width {
							t.Fatalf("managed=%v noColor=%v height=%d width=%d: line is %d cells: %q", managed, noColor, height, width, got, line)
						}
					}
				}
			}
		}
	}
}

// "quebra" takes the ink and "galho" the accent, as in the dashboard's
// Wordmark: the accent's color reaches only the second word.
func TestBrandWordmarkPaintsGalhoWithTheAccent(t *testing.T) {
	inkOnly := brandWordmarkSmall.rows(func(word int, cells string) string {
		if word == 0 {
			return cells
		}
		return strings.Repeat(" ", lipgloss.Width(cells))
	})
	galhoOnly := brandWordmarkSmall.rows(func(word int, cells string) string {
		if word == 1 {
			return cells
		}
		return strings.Repeat(" ", lipgloss.Width(cells))
	})
	// The two words never share a column, and "galho" starts after the gap.
	for row := range inkOnly {
		quebraEnd := len([]rune(strings.TrimRight(inkOnly[row], " ")))
		galhoStart := len([]rune(galhoOnly[row])) - len([]rune(strings.TrimLeft(galhoOnly[row], " ")))
		if strings.TrimSpace(galhoOnly[row]) != "" && galhoStart < quebraEnd+brandLetterGap {
			t.Fatalf("row %d: galho starts at %d, quebra ends at %d", row+1, galhoStart, quebraEnd)
		}
	}
	requireDiffViewerTrueColor(t)
	sgr := func(style lipgloss.Style) string {
		return strings.SplitN(style.Render("x"), "x", 2)[0]
	}
	painted := brandWordmarkSmall.rows(paintBrandWord)[1]
	if !strings.Contains(painted, sgr(qgTheme.ink.Bold(false))+"▄▄▄▄ ▄▄ ▄▄") {
		t.Fatalf("quebra is not painted with the ink:\n%q", painted)
	}
	if !strings.Contains(painted, sgr(qgTheme.accent.Bold(false))+"▄▄▄▄ ▄▄▄▄") {
		t.Fatalf("galho is not painted with the accent:\n%q", painted)
	}
}

// The name is a sticker in the mascot's own colors: "quebra" white and
// "galho" lime. On a light terminal, or one that never reports its
// background, the mascot's dark-green outline goes around every letter; on a
// dark one the letters stand alone. Both take the same cells. Without color it
// is the plain letters.
func TestBrandWordmarkIsASticker(t *testing.T) {
	for _, outlined := range []bool{true, false} {
		art := brandWordmarkSticker.stickerArt(outlined)
		for y, row := range art {
			for x := 0; x < len(row); x++ {
				want := byte('.')
				switch word := brandWordmarkSticker.pixels[y][x]; {
				case word == 1:
					want = 'W'
				case word == 2:
					want = 'L'
				case outlined && brandWordmarkSticker.touchesLetter(x, y):
					want = 'O'
				}
				if row[x] != want {
					t.Fatalf("outline=%v: pixel (%d,%d) = %q, want %q", outlined, x, y, row[x], want)
				}
			}
			if row[0] == 'W' || row[0] == 'L' || row[len(row)-1] == 'W' || row[len(row)-1] == 'L' {
				t.Fatalf("row %d = %q: a letter touches the edge, leaving no room for the outline", y+1, row)
			}
		}
		for _, edge := range []string{art[0], art[len(art)-1]} {
			if strings.ContainsAny(edge, "WL") {
				t.Fatalf("edge row %q has a letter, leaving no room for the outline", edge)
			}
		}
	}

	requireDiffViewerTrueColor(t)
	sgr := func(code byte) string {
		return strings.SplitN(lipgloss.NewStyle().Foreground(lipgloss.Color(brandMascotColors[code])).Render("x"), "x", 2)[0]
	}
	outline := sgr('O')
	for _, tc := range []struct {
		name            string
		dark, bgUnknown bool
		outlined        bool
	}{
		{name: "light", outlined: true},
		{name: "dark", dark: true},
		{name: "background never reported", dark: true, bgUnknown: true, outlined: true},
	} {
		m := brandTestModel(160, 30, tc.dark, false)
		m.terminalBgKnown = !tc.bgUnknown
		rows := m.brandWordmarkRows(brandWordmarkSticker)
		joined := strings.Join(rows, "\n")
		for _, code := range []byte{'W', 'L'} {
			if !strings.Contains(joined, sgr(code)) {
				t.Fatalf("%s: sticker has no %q (%s):\n%q", tc.name, code, brandMascotColors[code], joined)
			}
		}
		if got := strings.Contains(joined, outline); got != tc.outlined {
			t.Fatalf("%s: outline drawn = %v, want %v:\n%q", tc.name, got, tc.outlined, joined)
		}
		for i, row := range rows {
			if got := lipgloss.Width(row); got != brandWordmarkSticker.width {
				t.Fatalf("%s: row %d is %d cells, want %d", tc.name, i+1, got, brandWordmarkSticker.width)
			}
		}
	}

	m := brandTestModel(160, 30, false, true)
	plain := brandWordmarkSticker.rows(func(_ int, cells string) string { return cells })
	for i, row := range m.brandWordmarkRows(brandWordmarkSticker) {
		if strings.Contains(row, outline) || plainRender(t, row) != plain[i] {
			t.Fatalf("no color: row %d = %q, want the plain letters %q", i+1, row, plain[i])
		}
	}
}

// The mascot is a head in profile that faces the name on its right: the nose
// (the shine on it included) is in the right half of the icon.
func TestBrandMascotArtIsWellFormed(t *testing.T) {
	if len(brandMascotArt)%2 != 0 {
		t.Fatalf("mascot has %d pixel rows, want an even count for half blocks", len(brandMascotArt))
	}
	for row, pixels := range brandMascotArt {
		if len(pixels) != brandMascotWidth {
			t.Fatalf("row %d is %d pixels, want %d", row+1, len(pixels), brandMascotWidth)
		}
		for _, code := range []byte(pixels) {
			if _, ok := brandMascotColors[code]; !ok && code != '.' {
				t.Fatalf("row %d uses %q, which has no color", row+1, code)
			}
		}
		if shine := strings.IndexByte(pixels, 'S'); shine >= 0 && shine < brandMascotWidth/2 {
			t.Fatalf("row %d = %q: the nose is on the left, away from the name", row+1, pixels)
		}
	}
	if !strings.Contains(strings.Join(brandMascotArt[:], ""), "S") {
		t.Fatal("mascot has no nose shine")
	}
	for _, line := range brandMascotLines() {
		if got := lipgloss.Width(line); got != brandMascotWidth {
			t.Fatalf("mascot line is %d cells, want %d: %q", got, brandMascotWidth, line)
		}
	}
	if len(brandMascotLines()) != brandMascotCellRows {
		t.Fatalf("mascot has %d lines, want %d", len(brandMascotLines()), brandMascotCellRows)
	}
}

// The nose and the eyes are painted near-black instead of left transparent,
// so they stay dark whatever the terminal background is.
func TestBrandMascotPaintsItsFeatures(t *testing.T) {
	joined := strings.Join(brandMascotLines(), "\n")
	r, g, b := hexToRGB(t, brandMascotColors['K'])
	if !strings.Contains(joined, fmt.Sprintf("%d;%d;%d", r, g, b)) {
		t.Fatalf("mascot never paints its features with %s:\n%q", brandMascotColors['K'], joined)
	}
}

func hexToRGB(t *testing.T, hexColor string) (int, int, int) {
	t.Helper()
	var r, g, b int
	if _, err := fmt.Sscanf(hexColor, "#%02x%02x%02x", &r, &g, &b); err != nil {
		t.Fatalf("bad hex %q: %v", hexColor, err)
	}
	return r, g, b
}

// The wordmark keeps its outline until the terminal reports its background,
// and drops it only once that background is dark.
func TestBrandStickerOutlineFollowsTheReportedBackground(t *testing.T) {
	previous := qgTheme
	t.Cleanup(func() { qgTheme = previous })
	m := newModel(context.Background(), Options{Version: "0.2.0"})
	if !m.brandStickerOutlined() {
		t.Fatal("before the terminal answers, the wordmark must keep its outline")
	}
	for _, tc := range []struct {
		background color.Color
		outlined   bool
	}{
		{color.Black, false},
		{color.White, true},
	} {
		updated, _ := m.Update(tea.BackgroundColorMsg{Color: tc.background})
		m = updated.(model)
		if got := m.brandStickerOutlined(); got != tc.outlined {
			t.Fatalf("background %v: outlined = %v, want %v", tc.background, got, tc.outlined)
		}
	}
}
