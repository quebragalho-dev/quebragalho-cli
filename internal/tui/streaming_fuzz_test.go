package tui

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// Permanent differential fuzz for the streaming markdown renderer: every
// streamed frame must render byte-identical to the integral render of the
// same prefix. Fixed seed and size in CI (fast); FUZZ_SEED/FUZZ_DOCS override
// for wider manual sweeps, FUZZ_BYTES=1 splits chunks at raw bytes. Prose
// stays capped at 96 while the table measure roams (100/150/200), so a
// table-only resize mid-stream exercises the tableMeasure guard; the theme
// flips once per doc to pin the reset path.
var fuzzWords = []string{"a", "palavra", "comprida", "**negrito**", "*ênfase*", "`code`", "[link](http://x.y)",
	"中文字符", "👍🏽", "👨‍👩‍👧", "é", "ação", "x", "supercalifragilisticexpialidocious-muito-longa-sem-espaco",
	"**abre", "fecha**", "`abre", "\t", "  ", "1.", "-", ">", "#", "|", "~~~", "```"}

func fuzzLine(r *rand.Rand, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(fuzzWords[r.Intn(len(fuzzWords))])
	}
	return b.String()
}

func fuzzBlock(r *rand.Rand) string {
	fences := []string{"```", "```go", "```zzlang", "```text", "````", "~~~", "```python"}
	codeLines := []string{"", "x = 1", "func main() {", "    return nil", "}", "\t\tindent", "  ", "中文 = 2", "if a < b && c > d {"}
	k := r.Intn(12)
	switch k {
	case 0, 1:
		f := fences[r.Intn(len(fences))]
		closeF := strings.TrimRight(f, "abcdefghijklmnopqrstuvwxyz")
		var lines []string
		if r.Intn(3) == 0 {
			lines = append(lines, "")
		}
		for i, n := 0, r.Intn(5); i < n; i++ {
			lines = append(lines, codeLines[r.Intn(len(codeLines))])
		}
		for i, n := 0, r.Intn(3); i < n; i++ {
			lines = append(lines, "")
		}
		body := f + "\n" + strings.Join(lines, "\n")
		if len(lines) > 0 {
			body += "\n"
		}
		if r.Intn(8) != 0 {
			body += closeF
		}
		return body
	case 2:
		var items []string
		for i, n := 0, 1+r.Intn(4); i < n; i++ {
			marker := []string{"- ", "* ", "1. ", "2) ", "  - ", "    1. "}[r.Intn(6)]
			items = append(items, marker+fuzzLine(r, 1+r.Intn(8)))
			if r.Intn(4) == 0 {
				items = append(items, "")
			}
		}
		return strings.Join(items, "\n")
	case 3:
		rows := []string{"| a | b | c |", "|---|---|:---:|"}
		for i, n := 0, r.Intn(4); i < n; i++ {
			rows = append(rows, "| "+fuzzLine(r, 1+r.Intn(1))+" | "+fuzzLine(r, 1)+" | 中 |")
		}
		return strings.Join(rows, "\n")
	case 4:
		return []string{"# ", "## ", "### "}[r.Intn(3)] + fuzzLine(r, 1+r.Intn(5))
	case 5:
		return "> " + fuzzLine(r, 1+r.Intn(10)) + "\n> " + fuzzLine(r, r.Intn(5))
	case 6:
		return []string{"---", "***", "___"}[r.Intn(3)]
	case 7:
		return fuzzLine(r, 1+r.Intn(4)) + "\n" + []string{"===", "---"}[r.Intn(2)]
	case 8:
		return strings.Join([]string{"func main() {", "\tx := 1", "}"}[:1+r.Intn(3)], "\n")
	case 9:
		return "    " + fuzzLine(r, 1+r.Intn(4)) + "\n    " + fuzzLine(r, 2)
	default:
		var ls []string
		for i, n := 0, 1+r.Intn(3); i < n; i++ {
			ls = append(ls, fuzzLine(r, 1+r.Intn(30)))
		}
		return strings.Join(ls, "\n")
	}
}

func fuzzDoc(r *rand.Rand) string {
	var b strings.Builder
	if r.Intn(5) == 0 {
		b.WriteString(strings.Repeat("\n", 1+r.Intn(2)))
	}
	for i, n := 0, 1+r.Intn(8); i < n; i++ {
		if i > 0 {
			seps := []string{"\n\n", "\n", "\n\n\n", "\n  \n", "\n\n\n\n"}
			b.WriteString(seps[r.Intn(len(seps))])
		}
		b.WriteString(fuzzBlock(r))
	}
	if r.Intn(3) == 0 {
		b.WriteString(strings.Repeat("\n", 1+r.Intn(3)))
	}
	s := b.String()
	if r.Intn(6) == 0 {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	return s
}

func fuzzEnvInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

func TestStreamingDifferentialFuzz(t *testing.T) {
	seed := fuzzEnvInt("FUZZ_SEED", 7)
	docs := fuzzEnvInt("FUZZ_DOCS", 300)
	byteMode := os.Getenv("FUZZ_BYTES") == "1"
	r := rand.New(rand.NewSource(int64(seed)))
	proseWidths := []int{16, 33, 50, 80, 96}
	previousTheme := qgTheme
	defer func() {
		qgTheme = previousTheme
		rebuildStreamingFadePalette()
		resetStreamingMarkdownCache()
	}()
	frames, fails := 0, 0
	var first []string
	inNord := false
	for d := 0; d < docs; d++ {
		doc := fuzzDoc(r)
		if r.Intn(4) != 0 {
			resetStreamingMarkdownCache()
		}
		if inNord {
			qgTheme = previousTheme
			rebuildStreamingFadePalette()
			resetStreamingMarkdownCache()
			inNord = false
		}
		pw := proseWidths[r.Intn(len(proseWidths))]
		twChoices := []int{pw, 100, 150, 200}
		tw := twChoices[r.Intn(len(twChoices))]
		half := len(doc) / 2
		themeSwitched := false
		pos := 0
		for pos < len(doc) {
			step := 1 + r.Intn(40)
			if r.Intn(5) == 0 {
				step = 1
			}
			next := pos + step
			if next > len(doc) {
				next = len(doc)
			}
			snap := !byteMode
			if snap && r.Intn(9) == 0 {
				// Split inside a multi-byte rune on purpose now and then.
				snap = false
			}
			if snap {
				for next < len(doc) && !utf8.RuneStart(doc[next]) {
					next++
				}
			}
			pos = next
			if r.Intn(50) == 0 {
				tw = twChoices[r.Intn(len(twChoices))]
			}
			if !themeSwitched && pos >= half {
				applyTheme("nord", true)
				themeSwitched = true
				inNord = true
			}
			prefix := doc[:pos]
			got := strings.Join(renderStreamingAssistantMarkdownText(prefix, pw, tw), "\n")
			want := strings.Join(renderAssistantMarkdownText(streamingMarkdownStablePrefix(prefix), pw, tw, true), "\n")
			frames++
			if got != want {
				fails++
				if len(first) < 5 {
					first = append(first, fmt.Sprintf("seed=%d doc=%d pw=%d tw=%d prefix=%q\n got: %q\nwant: %q", seed, d, pw, tw, prefix, got, want))
				}
			}
		}
	}
	t.Logf("fuzz seed=%d docs=%d frames=%d fails=%d bytes=%v", seed, docs, frames, fails, byteMode)
	for _, f := range first {
		t.Errorf("%s", f)
	}
}
