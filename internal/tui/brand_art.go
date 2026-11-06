package tui

import (
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
)

// brand_art.go draws the quebragalho brand with terminal cells: the
// mascot's head in profile, the icon of the empty state's welcome card, and
// the name in heavy pixel letters, which `qg --version` prints. Both use half
// blocks (▀ ▄ █): one cell holds two stacked pixels, and a terminal cell is
// about twice as tall as it is wide, so every pixel comes out square.

// brandFont is a lowercase pixel font for the wordmark; only the letters of
// "quebragalho" exist. Every glyph has the same rows (ascender, x-height,
// descender) and one width for all its rows. '#' is ink and '.' is empty.
type brandFont struct {
	glyphs map[rune][]string
	// baseline is the glyph row at the bottom of the x-height.
	baseline int
	// wordGap is the empty pixel columns between "quebra" and "galho".
	wordGap int
}

// brandFontSmall is a heavy pixel cut of the lettering at a five-pixel
// x-height: two-pixel stems, one-pixel bars. `qg --version` prints the name
// in it. Rows: two ascender, five x-height, two descender.
var brandFontSmall = brandFont{baseline: 6, wordGap: brandLetterGap, glyphs: map[rune][]string{
	'q': {".....", ".....", ".####", "##.##", "##.##", "##.##", ".####", "...##", "...##"},
	'u': {".....", ".....", "##.##", "##.##", "##.##", "##.##", ".####", ".....", "....."},
	'e': {".....", ".....", ".###.", "##.##", "#####", "##...", ".####", ".....", "....."},
	'b': {"##...", "##...", "####.", "##.##", "##.##", "##.##", "####.", ".....", "....."},
	'r': {"....", "....", "##.#", "###.", "##..", "##..", "##..", "....", "...."},
	'a': {".....", ".....", "####.", "...##", ".####", "##.##", ".####", ".....", "....."},
	'g': {".....", ".....", ".####", "##.##", "##.##", "##.##", ".####", "...##", "####."},
	'l': {"##", "##", "##", "##", "##", "##", "##", "..", ".."},
	'h': {"##...", "##...", "####.", "##.##", "##.##", "##.##", "##.##", ".....", "....."},
	'o': {".....", ".....", ".###.", "##.##", "##.##", "##.##", ".###.", ".....", "....."},
}}

// brandWords are the two words of the wordmark.
var brandWords = [2]string{"quebra", "galho"}

// brandLetterGap is the empty pixel columns between the letters of a word.
const brandLetterGap = 1

// brandMascotArt is the mascot's head in profile, as on the brand sticker,
// turned to face the card's text and drawn as an icon: light from the top in
// a dotted band, a green shade along the back and the jaw, the eyes with
// their shine, the dark nose at the tip of the snout and a dark-green outline
// that keeps the lime shape apart from a light background. Letters index
// brandMascotColors; '.' is transparent.
var brandMascotArt = [...]string{
	"..OO.OO.............",
	".OLLOLLO............",
	"OLLLOLgyO...........",
	"OLgLOLgyO...........",
	"OGgLOLgLOOO.........",
	".OLLLlLlLlLOOO......",
	".OGLLKKLLKKLlLOO....",
	".OGLKLLLLLLKLLlLOO..",
	".OGLLKWLLKWLLLLLlLO.",
	".OGLLKKLLKKLLLKKKyO.",
	".OGLLLLLLLLLLLKKSKyO",
	".OGLLLLLLLLLLLKKSKyO",
	".OGGLKLLLLLLLLKKKKyO",
	".OGLLKLLLLLLLLKKKKyO",
	".OGGLLKKLLLLLLLKKyO.",
	"..OGGLLLKKLGLyyyyO..",
	"...OOGgGgGgGOOOOO...",
	".....OOOOOOO........",
}

// brandMascotWidth is the mascot's cell width; brandMascotCellRows its height.
var brandMascotWidth = len(brandMascotArt[0])

const brandMascotCellRows = (len(brandMascotArt) + 1) / 2

// brandWordmark is the wordmark at one size, as a pixel grid with an even
// number of rows.
type brandWordmark struct {
	// pixels holds whether each pixel is ink.
	pixels [][]bool
	// width and cellRows are its size in cells.
	width, cellRows int
}

var brandWordmarkSmall = newBrandWordmark(brandFontSmall)

// newBrandWordmark lays out the wordmark in a pixel font. A grid with an odd
// number of rows gets a blank pixel row on top, so the art fills whole cells.
func newBrandWordmark(font brandFont) *brandWordmark {
	height := len(font.glyphs['q'])
	rows := make([][]bool, height)
	for _, word := range brandWords {
		for letterIndex, letter := range word {
			gap := brandLetterGap
			if letterIndex == 0 {
				gap = font.wordGap
			}
			for y, glyphRow := range font.glyphs[letter] {
				if len(rows[y]) > 0 {
					rows[y] = append(rows[y], make([]bool, gap)...)
				}
				for x := 0; x < len(glyphRow); x++ {
					rows[y] = append(rows[y], glyphRow[x] == '#')
				}
			}
		}
	}
	width := len(rows[0])
	pad := height % 2
	pixels := make([][]bool, pad, height+pad)
	if pad == 1 {
		pixels[0] = make([]bool, width)
	}
	pixels = append(pixels, rows...)
	return &brandWordmark{pixels: pixels, width: width, cellRows: len(pixels) / 2}
}

// halfBlock picks the glyph for a cell whose upper and lower pixels are set
// or not.
func halfBlock(upper, lower bool) string {
	switch {
	case upper && lower:
		return "█"
	case upper:
		return "▀"
	case lower:
		return "▄"
	default:
		return " "
	}
}

// rows renders the wordmark's letters, uncolored, as w.cellRows lines all
// w.width cells wide.
func (w *brandWordmark) rows() []string {
	rows := make([]string, 0, w.cellRows)
	for y := 0; y+1 < len(w.pixels); y += 2 {
		var row strings.Builder
		for x := range w.pixels[y] {
			row.WriteString(halfBlock(w.pixels[y][x], w.pixels[y+1][x]))
		}
		rows = append(rows, row.String())
	}
	return rows
}

// brandMascotLines renders the mascot once: its colors are artwork, not theme
// tokens, so they never change at runtime.
var brandMascotLines = sync.OnceValue(func() []string {
	return renderBrandPixels(brandMascotArt[:])
})

// renderBrandPixels draws pixel art in brandMascotColors codes ('.' is
// transparent), two pixel rows per cell. Cells whose two pixels differ paint
// the upper one as foreground and the lower one as background.
func renderBrandPixels(art []string) []string {
	color := func(code byte) (string, bool) {
		hex, ok := brandMascotColors[code]
		return hex, ok
	}
	lines := make([]string, 0, (len(art)+1)/2)
	for y := 0; y < len(art); y += 2 {
		upperRow := art[y]
		lowerRow := strings.Repeat(".", len(upperRow))
		if y+1 < len(art) {
			lowerRow = art[y+1]
		}
		var line strings.Builder
		for x := 0; x < len(upperRow); x++ {
			upper, upperSet := color(upperRow[x])
			lower, lowerSet := color(lowerRow[x])
			style := lipgloss.NewStyle()
			glyph := halfBlock(upperSet, lowerSet)
			switch {
			case upperSet && lowerSet && upper != lower:
				style = style.Foreground(lipgloss.Color(upper)).Background(lipgloss.Color(lower))
				glyph = "▀"
			case upperSet:
				style = style.Foreground(lipgloss.Color(upper))
			case lowerSet:
				style = style.Foreground(lipgloss.Color(lower))
			default:
				line.WriteString(glyph)
				continue
			}
			line.WriteString(style.Render(glyph))
		}
		lines = append(lines, line.String())
	}
	return lines
}
