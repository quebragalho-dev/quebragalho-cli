package tui

import (
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
)

// brand_art.go draws the quebragalho brand with terminal cells: a small shaded
// icon of the mascot beside the name. Both use half blocks (▀ ▄ █): one cell
// holds two stacked pixels, and a terminal cell is about twice as tall as it
// is wide, so every pixel comes out square. The name is a sticker like the
// mascot: heavy pixel letters, "quebra" in the white of its eyes and "galho"
// in its lime. On a light terminal they are cut out with the mascot's own
// dark-green outline; on a dark one they stand alone on the terminal's
// background. Without color the letters are drawn plain; `qg --version`
// prints the name in a taller cut of the same font.

// brandFont is a lowercase pixel font for the wordmark; only the letters of
// "quebragalho" exist. Every glyph has the same rows (ascender, x-height,
// descender) and one width for all its rows. '#' is ink and '.' is empty.
type brandFont struct {
	glyphs map[rune][]string
	// baseline is the glyph row at the bottom of the x-height.
	baseline int
	// wordGap is the empty pixel columns between "quebra" and "galho".
	wordGap int
	// outline keeps a blank pixel around the name for the sticker's outline.
	outline bool
}

// brandFontSticker is the sticker's lettering: the heavy pixel letters of
// brandFontSmall (two-pixel stems, one-pixel bars, five-pixel x-height) with
// one-row ascenders and descenders, so the outlined name stays short. Rows:
// one ascender, five x-height, one descender.
var brandFontSticker = brandFont{baseline: 5, wordGap: 2, outline: true, glyphs: map[rune][]string{
	'q': {".....", ".####", "##.##", "##.##", "##.##", ".####", "...##"},
	'u': {".....", "##.##", "##.##", "##.##", "##.##", ".####", "....."},
	'e': {".....", ".###.", "##.##", "#####", "##...", ".####", "....."},
	'b': {"##...", "####.", "##.##", "##.##", "##.##", "####.", "....."},
	'r': {"....", "##.#", "###.", "##..", "##..", "##..", "...."},
	'a': {".....", "####.", "...##", ".####", "##.##", ".####", "....."},
	'g': {".....", ".####", "##.##", "##.##", "##.##", ".####", "####."},
	'l': {"##", "##", "##", "##", "##", "##", ".."},
	'h': {"##...", "####.", "##.##", "##.##", "##.##", "##.##", "....."},
	'o': {".....", ".###.", "##.##", "##.##", "##.##", ".###.", "....."},
}}

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

// brandWords are the two halves of the wordmark, painted ink and accent.
var brandWords = [2]string{"quebra", "galho"}

// brandLetterGap is the empty pixel columns between the letters of a word.
const brandLetterGap = 1

// brandMascotArt is the mascot's head in profile, as on the brand sticker,
// turned to face the name and drawn as an icon: light from the top in a dotted
// band, a green shade along the back and the jaw, the eyes with their shine,
// the dark nose at the tip of the snout and a dark-green outline that keeps
// the lime shape apart from a light background. Letters index
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
	// pixels holds the word of each pixel: 0 empty, 1 "quebra", 2 "galho".
	pixels [][]uint8
	// width and cellRows are its size in cells.
	width, cellRows int
	// baselineRow is the cell row at the bottom of the x-height, where the
	// domain sits.
	baselineRow int

	// sticker caches the sticker without (0) and with (1) its outline.
	stickerOnce [2]sync.Once
	sticker     [2][]string
}

var (
	brandWordmarkSticker = newBrandWordmark(brandFontSticker)
	brandWordmarkSmall   = newBrandWordmark(brandFontSmall)
)

// newBrandWordmark lays out the wordmark in a pixel font, with a blank pixel
// around it when the font has an outline. A grid with an odd number of rows
// gets a blank pixel row on top, so the art fills whole cells.
func newBrandWordmark(font brandFont) *brandWordmark {
	margin := 0
	if font.outline {
		margin = 1
	}
	glyphRows := len(font.glyphs['q'])
	rows := make([][]uint8, glyphRows)
	for wordIndex, word := range brandWords {
		for letterIndex, letter := range word {
			gap := brandLetterGap
			if letterIndex == 0 {
				gap = font.wordGap
			}
			for y, glyphRow := range font.glyphs[letter] {
				if len(rows[y]) > 0 {
					rows[y] = append(rows[y], make([]uint8, gap)...)
				}
				for x := 0; x < len(glyphRow); x++ {
					ink := uint8(0)
					if glyphRow[x] == '#' {
						ink = uint8(wordIndex + 1)
					}
					rows[y] = append(rows[y], ink)
				}
			}
		}
	}
	width := len(rows[0]) + 2*margin
	height := glyphRows + 2*margin
	pad := height % 2
	pixels := make([][]uint8, height+pad)
	for y := range pixels {
		pixels[y] = make([]uint8, width)
		if glyphRow := y - pad - margin; glyphRow >= 0 && glyphRow < glyphRows {
			copy(pixels[y][margin:], rows[glyphRow])
		}
	}
	return &brandWordmark{
		pixels:      pixels,
		width:       width,
		cellRows:    (height + pad) / 2,
		baselineRow: (font.baseline + margin + pad) / 2,
	}
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

// rows renders the wordmark's letters as w.cellRows lines, all w.width cells
// wide, without the sticker's colors. paint styles one run of cells of a word
// (0 = "quebra", 1 = "galho"); the gaps between words stay unstyled.
func (w *brandWordmark) rows(paint func(word int, cells string) string) []string {
	pixels := w.pixels
	rows := make([]string, 0, w.cellRows)
	for y := 0; y+1 < len(pixels); y += 2 {
		upper, lower := pixels[y], pixels[y+1]
		var row strings.Builder
		var run strings.Builder
		runWord := uint8(0)
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if runWord == 0 {
				row.WriteString(run.String())
			} else {
				row.WriteString(paint(int(runWord-1), run.String()))
			}
			run.Reset()
		}
		for x := range upper {
			// A column belongs to one word (or to none), so its color comes
			// from whichever half is set.
			word := upper[x]
			if word == 0 {
				word = lower[x]
			}
			if word != runWord && word != 0 {
				flush()
				runWord = word
			}
			run.WriteString(halfBlock(upper[x] != 0, lower[x] != 0))
		}
		flush()
		rows = append(rows, row.String())
	}
	return rows
}

// stickerRows renders the wordmark as a sticker in the mascot's colors, with
// or without its outline, as w.cellRows lines all w.width cells wide. Each
// version never changes, so it is drawn once; callers get their own copy to
// append to.
func (w *brandWordmark) stickerRows(outline bool) []string {
	i := 0
	if outline {
		i = 1
	}
	w.stickerOnce[i].Do(func() { w.sticker[i] = renderBrandPixels(w.stickerArt(outline)) })
	return append([]string(nil), w.sticker[i]...)
}

// stickerArt is the wordmark as pixel art in the mascot's color codes:
// "quebra" in the white of its eyes and "galho" in its lime. With the
// outline, every blank pixel next to a letter, diagonals included, takes the
// mascot's own outline color; without it, those pixels stay transparent.
func (w *brandWordmark) stickerArt(outline bool) []string {
	fill := [3]byte{'.', 'W', 'L'}
	art := make([]string, len(w.pixels))
	for y, row := range w.pixels {
		line := make([]byte, len(row))
		for x, word := range row {
			switch {
			case word != 0:
				line[x] = fill[word]
			case outline && w.touchesLetter(x, y):
				line[x] = 'O'
			default:
				line[x] = '.'
			}
		}
		art[y] = string(line)
	}
	return art
}

// touchesLetter reports whether a letter pixel is next to pixel (x, y).
func (w *brandWordmark) touchesLetter(x, y int) bool {
	for ny := y - 1; ny <= y+1; ny++ {
		for nx := x - 1; nx <= x+1; nx++ {
			if ny >= 0 && ny < len(w.pixels) && nx >= 0 && nx < len(w.pixels[ny]) && w.pixels[ny][nx] != 0 {
				return true
			}
		}
	}
	return false
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
