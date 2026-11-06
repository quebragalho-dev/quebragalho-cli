package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// brandMascotEars is the mascot's first cell row, which nothing else draws.
const brandMascotEars = " ▄▀▀▄▀▀▄            "

func brandTestModel(width, height int, dark, noColor bool) model {
	m := newModel(context.Background(), Options{Version: "0.2.0"})
	m.width, m.height = width, height
	m.hasDarkBg = dark
	m.noColor = noColor
	m.gitBranch = "main"
	return m
}

func TestBrandMascotFirstRowIsTheEars(t *testing.T) {
	if got := plainRender(t, brandMascotLines()[0]); got != brandMascotEars {
		t.Fatalf("mascot row 1 = %q, want %q", got, brandMascotEars)
	}
}

// The empty state draws the largest brand art that fits: the welcome card,
// the mascot beside the one-line wordmark, or just the line. The mascot needs
// color output, and its outline lets it show on light terminals too; without
// color the card comes without it.
func TestEmptyStateBrandTiers(t *testing.T) {
	cases := []struct {
		name          string
		width         int
		dark, noColor bool
		tier          brandTier
		mascot        bool
	}{
		{name: "wide dark", width: 160, dark: true, tier: brandTierCard, mascot: true},
		{name: "58 dark", width: 58, dark: true, tier: brandTierCard, mascot: true},
		{name: "57 dark", width: 57, dark: true, tier: brandTierCompact, mascot: true},
		{name: "49 dark", width: 49, dark: true, tier: brandTierCompact, mascot: true},
		{name: "48 dark", width: 48, dark: true, tier: brandTierText},
		{name: "wide light", width: 160, tier: brandTierCard, mascot: true},
		{name: "58 light", width: 58, tier: brandTierCard, mascot: true},
		{name: "wide no color", width: 160, dark: true, noColor: true, tier: brandTierCard},
		{name: "34 no color", width: 34, dark: true, noColor: true, tier: brandTierCard},
		{name: "33 no color", width: 33, dark: true, noColor: true, tier: brandTierText},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := brandTestModel(tc.width, 30, tc.dark, tc.noColor)
			if got := m.emptyStateTier(tc.width); got != tc.tier {
				t.Fatalf("tier = %d, want %d", got, tc.tier)
			}
			view := plainRender(t, m.emptyState(tc.width))
			if got := strings.Contains(view, brandMascotEars); got != tc.mascot {
				t.Fatalf("mascot drawn = %v, want %v:\n%s", got, tc.mascot, view)
			}
			if tc.tier == brandTierCard {
				assertContains(t, view, "╭")
				assertContains(t, view, "quebragalho"+brandDomain)
			} else {
				assertNotContains(t, view, "╭")
				assertContains(t, view, "quebra galho")
			}
			assertContains(t, view, emptyStateTagline+" "+emptyStateHeart)
			assertContains(t, view, "v0.2.0")
		})
	}
}

// A short pane gets smaller art: the tier reads the real terminal height,
// not the 18-row floor the rest of the startup layout uses. The card needs
// the mascot's rows, or its text's without color, plus its borders.
func TestEmptyStateBrandTierFollowsTheHeight(t *testing.T) {
	cases := []struct {
		width, height int
		noColor       bool
		tier          brandTier
	}{
		{width: 120, height: 0, tier: brandTierCard}, // before the first resize
		{width: 120, height: 19, tier: brandTierCard},
		{width: 120, height: 18, tier: brandTierCompact},
		{width: 120, height: 17, tier: brandTierCompact},
		{width: 120, height: 16, tier: brandTierText},
		{width: 50, height: 17, tier: brandTierCompact},
		{width: 50, height: 16, tier: brandTierText},
		{width: 120, height: 17, noColor: true, tier: brandTierCard},
		{width: 120, height: 16, noColor: true, tier: brandTierText},
	}
	for _, tc := range cases {
		m := brandTestModel(tc.width, tc.height, true, tc.noColor)
		if got := m.emptyStateTier(tc.width); got != tc.tier {
			t.Fatalf("%d×%d noColor=%v: tier %d, want %d", tc.width, tc.height, tc.noColor, got, tc.tier)
		}
	}
}

// In the managed (alt-screen) view the idle shortcut hint sits above the
// composer and takes a row from the stream area; the brand must count it, or
// its first line goes under the title bar. An 80×24 terminal keeps the card.
func TestEmptyStateCountsTheIdleHint(t *testing.T) {
	m := brandTestModel(80, 24, true, false)
	m.altScreen = true
	if m.composerIdleHint() == "" {
		t.Fatal("expected the idle shortcut hint in the managed view")
	}
	if got, want := m.emptyStateRows(), 24-emptyStateChromeRows-1; got != want {
		t.Fatalf("emptyStateRows = %d, want %d", got, want)
	}
	if got := m.emptyStateTier(80); got != brandTierCard {
		t.Fatalf("80×24 tier = %d, want the card", got)
	}
	lines := m.emptyStateLines(80)
	if len(lines) > m.emptyStateRows() {
		t.Fatalf("%d brand lines for %d rows", len(lines), m.emptyStateRows())
	}
	if first := strings.TrimSpace(plainRender(t, lines[0])); !strings.HasPrefix(first, "╭") {
		t.Fatalf("first brand line is not the card's top border: %q", first)
	}
	if !strings.Contains(plainRender(t, lines[2]), brandMascotEars) {
		t.Fatalf("the mascot's ears do not open the card: %q", plainRender(t, lines[2]))
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

// The card is a closed box: every row is as wide as the borders, and the
// text beside the mascot is the name with its domain, the tagline and the
// session's details, the branch only when there is one. On a narrow pane
// long details are cut inside the border.
func TestBrandCardShowsTheSessionDetails(t *testing.T) {
	m := brandTestModel(120, 30, true, false)
	m.cwd = "/workspace/meu-projeto"
	m.gitBranch = "feat/cartao"
	m.modelName = "muse-spark-1.3-contributor"
	card := m.brandCard(120)
	if got := len(card); got != brandMascotCellRows+4 {
		t.Fatalf("card is %d rows, want the mascot's %d plus 4", got, brandMascotCellRows)
	}
	plain := make([]string, len(card))
	for i, line := range card {
		plain[i] = strings.TrimSpace(plainRender(t, line))
	}
	if !strings.HasPrefix(plain[0], "╭") || !strings.HasSuffix(plain[0], "╮") || !strings.HasPrefix(plain[len(plain)-1], "╰") {
		t.Fatalf("card borders:\n%s", strings.Join(plain, "\n"))
	}
	for i, line := range plain {
		if lipgloss.Width(line) != lipgloss.Width(plain[0]) {
			t.Fatalf("row %d is %d cells, the border %d:\n%s", i+1, lipgloss.Width(line), lipgloss.Width(plain[0]), strings.Join(plain, "\n"))
		}
		if i > 0 && i < len(plain)-1 && (!strings.HasPrefix(line, "│") || !strings.HasSuffix(line, "│")) {
			t.Fatalf("row %d is not closed by the border: %q", i+1, line)
		}
	}
	view := strings.Join(plain, "\n")
	for _, want := range []string{
		"quebragalho.dev",
		emptyStateTagline + " " + emptyStateHeart,
		"diretório  /workspace/meu-projeto",
		"branch     feat/cartao",
		"modelo     muse-spark-1.3-contributor",
		"versão     v0.2.0",
	} {
		assertContains(t, view, want)
	}

	m.gitBranch = ""
	m.modelName = ""
	view = plainRender(t, strings.Join(m.brandCard(120), "\n"))
	assertNotContains(t, view, "branch")
	assertContains(t, view, "modelo     sem modelo")

	m.modelName = "um-modelo-com-um-nome-bem-mais-comprido-que-o-cartao"
	for _, line := range m.brandCard(58) {
		got := strings.TrimSpace(plainRender(t, line))
		if lipgloss.Width(got) != 58-2*brandMargin || !strings.HasSuffix(got, "│") && !strings.HasSuffix(got, "╮") && !strings.HasSuffix(got, "╯") {
			t.Fatalf("narrow card row %q is %d cells, want %d inside the border", got, lipgloss.Width(got), 58-2*brandMargin)
		}
	}
	assertContains(t, plainRender(t, strings.Join(m.brandCard(58), "\n")), "modelo     um-modelo-co…")
}

// The border takes the accent, and the name is "quebra" in bold ink and
// "galho" in the bold accent, like the dashboard's Wordmark.
func TestBrandCardPaintsTheBorderAndTheName(t *testing.T) {
	requireDiffViewerTrueColor(t)
	sgr := func(style lipgloss.Style) string {
		return strings.SplitN(style.Render("x"), "x", 2)[0]
	}
	m := brandTestModel(120, 30, true, false)
	card := strings.Join(m.brandCard(120), "\n")
	for _, want := range []string{
		sgr(qgTheme.accent.Bold(false)) + "╭",
		sgr(qgTheme.ink.Bold(true)) + "quebra",
		sgr(qgTheme.accent.Bold(true)) + "galho",
	} {
		if !strings.Contains(card, want) {
			t.Fatalf("card lacks %q:\n%q", want, card)
		}
	}
}

// The mascot is a head in profile that faces the card's text on its right:
// the nose (the shine on it included) is in the right half of the icon.
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
			t.Fatalf("row %d = %q: the nose is on the left, away from the text", row+1, pixels)
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
