package tui

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	markdownTableColumnSeparator = " │ "
	markdownTableMinColumnWidth  = 4
	markdownTableHeaderMaxWidth  = 18
	markdownTableRuleBodyRows    = 4
	markdownBoldStart            = "\x1b[1m"
	markdownBoldEnd              = "\x1b[22m"
	markdownCodeStart            = "\x1b[7m"
	markdownCodeEnd              = "\x1b[27m"
)

type markdownDisplayStyle int

const (
	markdownDisplayNormal markdownDisplayStyle = iota
	markdownDisplayBold
	markdownDisplayCode
	markdownDisplayRule
)

type markdownTableAlignment int

const (
	markdownTableAlignLeft markdownTableAlignment = iota
	markdownTableAlignCenter
	markdownTableAlignRight
)

func renderAssistantMarkdownText(text string, proseMeasure int, tableMeasure int, allowHighlight bool) []string {
	if proseMeasure < 16 {
		proseMeasure = 16
	}
	if tableMeasure < proseMeasure {
		tableMeasure = proseMeasure
	}
	raw := strings.Split(strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n"), "\n")
	if len(raw) == 0 {
		return []string{""}
	}
	if allowHighlight && looksLikeBareCodeBlock(raw) {
		if highlighted, ok := highlightCodeAuto(raw, "", tableMeasure); ok {
			return trimMarkdownDisplayBlankEdges(highlighted)
		}
	}

	lines := []string{}
	// blankBefore inserts one separator blank line before a block (heading or
	// paragraph) for vertical breathing room, but never doubles an existing blank
	// and never leads with one (lines is empty at the top; trimMarkdownDisplay-
	// BlankEdges strips any leading blank anyway). Lists, code, and tables are left
	// tight — they don't call it.
	blankBefore := func() {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
	}
	for index := 0; index < len(raw); {
		line := raw[index]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			lines = append(lines, "")
			index++
			continue
		}

		if fence, ok := markdownFenceMarker(trimmed); ok {
			info := strings.TrimSpace(strings.TrimPrefix(trimmed, fence))
			lang := ""
			if fields := strings.Fields(info); len(fields) > 0 {
				lang = fields[0] // first token only: "```go title=x" -> "go"
			}
			code := []string{}
			index++
			for index < len(raw) {
				if closing, ok := markdownFenceMarker(strings.TrimSpace(raw[index])); ok && closing == fence {
					index++
					break
				}
				code = append(code, strings.ReplaceAll(raw[index], "\t", "    "))
				index++
			}
			if allowHighlight {
				if highlighted, ok := highlightCodeAuto(code, lang, tableMeasure); ok {
					lines = append(lines, highlighted...)
					continue
				}
			}
			lines = append(lines, renderMarkdownCodeBlock(code, tableMeasure)...)
			continue
		}

		if index+1 < len(raw) && isMarkdownTableHeader(line, raw[index+1]) {
			tableRows := [][]string{parseMarkdownTableRow(line)}
			alignments := parseMarkdownTableAlignments(raw[index+1])
			index += 2
			for index < len(raw) && isMarkdownTableRow(raw[index]) {
				tableRows = append(tableRows, parseMarkdownTableRow(raw[index]))
				index++
			}
			lines = append(lines, renderMarkdownTable(tableRows, alignments, tableMeasure)...)
			continue
		}

		if isMarkdownHorizontalRule(trimmed) {
			blankBefore()
			lines = append(lines, renderMarkdownHorizontalRule(proseMeasure))
			index++
			continue
		}

		if heading := markdownHeadingText(trimmed); heading != "" {
			blankBefore()
			// Headings are distinguished by weight + underline, not a bright colour —
			// calm for dark-mode terminals (ink, the body colour, not the lime accent).
			headingStyle := qgTheme.ink.Bold(true).Underline(true)
			plain := strings.ReplaceAll(strings.ReplaceAll(heading, "**", ""), "`", "")
			for _, hl := range wrapPlainText(plain, proseMeasure) {
				lines = append(lines, headingStyle.Render(hl))
			}
			index++
			continue
		}

		if isMarkdownListLine(trimmed) || strings.HasPrefix(trimmed, ">") {
			lines = append(lines, wrapMarkdownInline(renderMarkdownStandaloneLine(line), proseMeasure)...)
			index++
			continue
		}

		if allowHighlight && looksLikeBareCodeLine(line) {
			code := []string{}
			probe := index
			for probe < len(raw) {
				next := raw[probe]
				nextTrimmed := strings.TrimSpace(next)
				if nextTrimmed == "" || markdownStartsBlock(raw, probe) || !looksLikeBareCodeLine(next) {
					break
				}
				code = append(code, strings.ReplaceAll(next, "\t", "    "))
				probe++
			}
			if looksLikeBareCodeBlock(code) {
				if highlighted, ok := highlightCodeAuto(code, "", tableMeasure); ok {
					lines = append(lines, highlighted...)
					index = probe
					continue
				}
			}
		}

		blankBefore()
		paragraph := []string{strings.TrimSpace(line)}
		index++
		for index < len(raw) {
			next := raw[index]
			nextTrimmed := strings.TrimSpace(next)
			if nextTrimmed == "" || markdownStartsBlock(raw, index) {
				break
			}
			paragraph = append(paragraph, nextTrimmed)
			index++
		}
		lines = append(lines, wrapMarkdownInline(strings.Join(paragraph, " "), proseMeasure)...)
	}

	return trimMarkdownDisplayBlankEdges(lines)
}

func renderStreamingAssistantMarkdownText(text string, proseMeasure int, tableMeasure int) []string {
	if defaultRenderCache == nil {
		return renderAssistantMarkdownText(streamingMarkdownStablePrefix(text), proseMeasure, tableMeasure, true)
	}
	streamMarkdownCache.mu.Lock()
	defer streamMarkdownCache.mu.Unlock()
	return streamMarkdownCache.render(text, proseMeasure, tableMeasure)
}

// streamingMarkdownCache renders the streaming answer incrementally. Every
// frame used to re-parse and re-render the whole accumulated text, so CPU
// grew with the answer (quadratic over the stream). The closed head of the
// answer never changes, so it is rendered once and only the open tail is
// re-rendered per frame. Output is byte-identical to the integral render;
// see TestStreamingIncrementalMatchesIntegralOnEveryPrefix.
type streamingMarkdownCache struct {
	mu           sync.Mutex
	rawText      string
	headText     string
	headLines    []string
	headHasProse bool
	fullText     string
	lines        []string
	proseMeasure int
	tableMeasure int
	tailResume   *streamingParagraphResume
}

var streamMarkdownCache streamingMarkdownCache

// resetStreamingMarkdownCache drops incremental streaming state. Called when
// the theme changes (highlight colors are baked into rendered lines) and from
// staticRenderCache.clear so both caches share one invalidation story.
func resetStreamingMarkdownCache() {
	streamMarkdownCache.mu.Lock()
	defer streamMarkdownCache.mu.Unlock()
	streamMarkdownCache.rawText = ""
	streamMarkdownCache.headText = ""
	streamMarkdownCache.headLines = nil
	streamMarkdownCache.headHasProse = false
	streamMarkdownCache.fullText = ""
	streamMarkdownCache.lines = nil
	streamMarkdownCache.proseMeasure = 0
	streamMarkdownCache.tableMeasure = 0
	streamMarkdownCache.tailResume = nil
}

func (c *streamingMarkdownCache) render(text string, proseMeasure int, tableMeasure int) []string {
	if c.lines != nil && text == c.rawText && proseMeasure == c.proseMeasure && tableMeasure == c.tableMeasure {
		return append([]string(nil), c.lines...)
	}
	stable := streamingMarkdownStablePrefix(text)
	if c.lines != nil && stable == c.fullText && proseMeasure == c.proseMeasure && tableMeasure == c.tableMeasure {
		return append([]string(nil), c.lines...)
	}
	if proseMeasure == c.proseMeasure && tableMeasure == c.tableMeasure &&
		strings.HasPrefix(stable, c.fullText) {
		if lines, ok := c.extend(stable, proseMeasure, tableMeasure); ok {
			c.rawText = text
			c.fullText = stable
			c.lines = lines
			return append([]string(nil), lines...)
		}
	}
	lines := c.refresh(stable, proseMeasure, tableMeasure)
	c.rawText = text
	c.fullText = stable
	c.lines = lines
	c.proseMeasure, c.tableMeasure = proseMeasure, tableMeasure
	return append([]string(nil), lines...)
}

// refresh recomputes from scratch after a reset, a width change, or text that
// does not extend the last served input. It also resets the resume state,
// which only survives append-only growth.
func (c *streamingMarkdownCache) refresh(stable string, proseMeasure int, tableMeasure int) []string {
	headEnd, tailStart, blanks := splitStreamingMarkdownStable(stable)
	head := stable[:headEnd]
	c.headText = ""
	c.headLines = nil
	c.headHasProse = false
	c.tailResume = nil
	if head == "" || !streamingMarkdownHeadValid(head) ||
		looksLikeBareCodeBlock(streamingMarkdownRawLines(stable)) {
		return renderAssistantMarkdownText(stable, proseMeasure, tableMeasure, true)
	}
	c.headText = head
	c.headLines = renderAssistantMarkdownText(head, proseMeasure, tableMeasure, true)
	c.headHasProse = streamingMarkdownHeadHasProse(head)
	tailLines, resume := renderStreamingTail(nil, stable[tailStart:], proseMeasure, tableMeasure)
	lines := make([]string, 0, len(c.headLines)+blanks+len(tailLines))
	lines = append(lines, c.headLines...)
	for i := 0; i < blanks; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, tailLines...)
	c.tailResume = resume
	return trimMarkdownDisplayTrailingBlankLines(lines)
}

// extend re-renders after append-only growth over the last served input. The
// head only ever grows forward (appending text cannot move the last blank run
// backwards), so a changed split means absorbing complete blocks; otherwise
// just the tail is re-rendered. Returns false when the cached head no longer
// applies and the caller must recompute from scratch.
func (c *streamingMarkdownCache) extend(stable string, proseMeasure int, tableMeasure int) ([]string, bool) {
	headEnd, tailStart, blanks := splitStreamingMarkdownStable(stable)
	head := stable[:headEnd]
	if head != c.headText {
		// The head only grows forward: appending text cannot move the last
		// blank run backwards. Anything else (e.g. a trailing blank run
		// absorbed by continued text) needs a recompute.
		if c.headText != "" && !strings.HasPrefix(head, c.headText) {
			return nil, false
		}
		if !streamingMarkdownHeadValid(head) {
			return nil, false
		}
		c.headText = head
		if head == "" {
			c.headLines = nil
		} else {
			c.headLines = renderAssistantMarkdownText(head, proseMeasure, tableMeasure, true)
		}
		c.headHasProse = streamingMarkdownHeadHasProse(head)
		c.tailResume = nil
	}
	if !c.headHasProse && looksLikeBareCodeBlock(streamingMarkdownRawLines(stable)) {
		return nil, false
	}
	tailLines, resume := renderStreamingTail(c.tailResume, stable[tailStart:], proseMeasure, tableMeasure)
	lines := make([]string, 0, len(c.headLines)+blanks+len(tailLines))
	lines = append(lines, c.headLines...)
	for i := 0; i < blanks; i++ {
		lines = append(lines, "")
	}
	lines = append(lines, tailLines...)
	c.tailResume = resume
	return trimMarkdownDisplayTrailingBlankLines(lines), true
}

// streamingMarkdownRawLines splits normalized text the same way
// renderAssistantMarkdownText does, so block predicates agree with it.
func streamingMarkdownRawLines(stable string) []string {
	return strings.Split(strings.TrimRight(stable, "\n"), "\n")
}

// streamingMarkdownHeadValid reports whether head ends outside a fenced code
// block, reusing the buffering rule: a head containing an open fence cannot
// be split off.
func streamingMarkdownHeadValid(head string) bool {
	return streamingMarkdownStablePrefix(head) == head
}

// streamingMarkdownHeadHasProse reports whether head holds a line that can
// never be code. A head with prose can never flip the whole-text bare-code
// predicate, so later frames skip that scan while the head stands.
func streamingMarkdownHeadHasProse(head string) bool {
	for _, line := range streamingMarkdownRawLines(head) {
		if strings.TrimSpace(line) != "" && !looksLikeBareCodeLine(line) {
			return true
		}
	}
	return false
}

// splitStreamingMarkdownStable finds the last blank run in stable text,
// returning byte offsets: head ends where the run starts, tail starts where
// it ends, plus the run length in lines. Blocks before a blank run render
// identically standalone and in context, so head is reusable verbatim.
func splitStreamingMarkdownStable(stable string) (headEnd, tailStart, blanks int) {
	runStart, runCount := -1, 0
	lastStart, lastEnd, lastCount := 0, 0, 0
	hasLast := false
	off := 0
	for off <= len(stable) {
		var next int
		var blank bool
		if nl := strings.IndexByte(stable[off:], '\n'); nl < 0 {
			blank = strings.TrimSpace(stable[off:]) == ""
			next = len(stable) + 1
		} else {
			blank = strings.TrimSpace(stable[off:off+nl]) == ""
			next = off + nl + 1
		}
		if blank {
			if runStart < 0 {
				runStart = off
			}
			runCount++
		} else if runStart >= 0 {
			lastStart, lastEnd, lastCount, hasLast = runStart, off, runCount, true
			runStart, runCount = -1, 0
		}
		if next > len(stable) {
			break
		}
		off = next
	}
	if runStart >= 0 {
		lastStart, lastEnd, lastCount, hasLast = runStart, len(stable), runCount, true
	}
	if !hasLast {
		return 0, 0, 0
	}
	return lastStart, lastEnd, lastCount
}

func trimMarkdownDisplayTrailingBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// renderStreamingTail renders the open tail after the last blank run. A
// single-line prose tail reuses the previous frame's wrap (only words after
// the last stable line break are re-wrapped); anything else renders
// integrally, which is cheap because open tails are short.
func renderStreamingTail(prev *streamingParagraphResume, tail string, proseMeasure int, tableMeasure int) ([]string, *streamingParagraphResume) {
	if lines, resume, ok := resumeStreamingParagraph(prev, tail, proseMeasure); ok {
		return lines, resume
	}
	return renderAssistantMarkdownText(tail, proseMeasure, tableMeasure, true), nil
}

// streamingParagraphResume holds one wrapped single-line paragraph tail so
// the next frame re-wraps only the words after the last stable line break.
type streamingParagraphResume struct {
	tailText   string
	words      []markdownInlineSegment
	lineStarts []int
	lines      []string
	measure    int
}

func resumeStreamingParagraph(prev *streamingParagraphResume, tail string, measure int) ([]string, *streamingParagraphResume, bool) {
	if strings.Contains(tail, "\n") {
		return nil, nil, false
	}
	trimmed := strings.TrimSpace(tail)
	if trimmed == "" || !streamingParagraphLineEligible(trimmed) {
		return nil, nil, false
	}
	words := markdownInlineWords(parseMarkdownInline(trimmed))
	if len(words) == 0 {
		return nil, nil, false
	}
	if prev == nil || prev.measure != measure || !strings.HasPrefix(tail, prev.tailText) {
		var starts []int
		lines := appendWrappedMarkdownWords(nil, words, 0, "", 0, "", "", measure, &starts)
		return lines, &streamingParagraphResume{tailText: tail, words: words, lineStarts: starts, lines: lines, measure: measure}, true
	}
	firstDiff := len(prev.words)
	if len(words) < firstDiff {
		firstDiff = len(words)
	}
	for i := 0; i < firstDiff; i++ {
		if prev.words[i] != words[i] {
			firstDiff = i
			break
		}
	}
	resumeAt := 0
	for _, s := range prev.lineStarts {
		if s > firstDiff {
			break
		}
		resumeAt = s
	}
	// A hard-split word starts two visual lines, so resumeAt can repeat; the
	// first occurrence owns the right prefix (first line uses it exactly when
	// it is the first emitted line).
	resumeIndex := 0
	for resumeIndex < len(prev.lineStarts) && prev.lineStarts[resumeIndex] < resumeAt {
		resumeIndex++
	}
	lines := append([]string(nil), prev.lines[:resumeIndex]...)
	starts := append([]int(nil), prev.lineStarts[:resumeIndex]...)
	lines = appendWrappedMarkdownWords(lines, words, resumeAt, "", 0, "", "", measure, &starts)
	return lines, &streamingParagraphResume{tailText: tail, words: words, lineStarts: starts, lines: lines, measure: measure}, true
}

// streamingParagraphLineEligible reports whether a single trimmed line takes
// the paragraph path in the integral renderer, so resuming its wrap is
// exact. Block openers (fences, headings, rules, lists, quotes) render
// through other paths and stay on the integral fallback.
func streamingParagraphLineEligible(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	if _, ok := markdownFenceMarker(trimmed); ok {
		return false
	}
	if markdownHeadingText(trimmed) != "" || isMarkdownHorizontalRule(trimmed) ||
		isMarkdownListLine(trimmed) || strings.HasPrefix(trimmed, ">") {
		return false
	}
	return true
}

func streamingMarkdownStablePrefix(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	offset := 0
	openFenceStart := -1
	openFence := ""
	for offset <= len(text) {
		lineStart := offset
		next := strings.IndexByte(text[offset:], '\n')
		line := ""
		if next < 0 {
			line = text[offset:]
			offset = len(text) + 1
		} else {
			line = text[offset : offset+next]
			offset += next + 1
		}
		if fence, ok := markdownFenceMarker(strings.TrimSpace(line)); ok {
			if openFence == "" {
				openFence = fence
				openFenceStart = lineStart
			} else if fence == openFence {
				openFence = ""
				openFenceStart = -1
			}
		}
		if next < 0 {
			break
		}
	}
	if openFenceStart >= 0 {
		return strings.TrimRight(text[:openFenceStart], "\n")
	}
	return text
}

func renderAssistantMarkdownPlainText(text string, proseMeasure int, tableMeasure int) []string {
	lines := renderAssistantMarkdownText(text, proseMeasure, tableMeasure, false)
	for index := range lines {
		lines[index] = stripMarkdownRenderControls(lines[index])
	}
	return lines
}

func stripMarkdownRenderControls(text string) string {
	// renderAssistantMarkdownText embeds markdownBoldStart/markdownBoldEnd
	// markers for prose text, and highlightCodeAuto embeds ANSI color sequences
	// for fenced code blocks. Strip everything so clipboard text is pure plain text.
	return ansi.Strip(text)
}

func styleAssistantMarkdownLine(line string, base lipgloss.Style) string {
	if hasExternalANSIStyle(line) {
		return line
	}

	var builder strings.Builder
	style := markdownDisplayNormal
	var run strings.Builder

	flush := func() {
		if run.Len() == 0 {
			return
		}
		text := run.String()
		switch style {
		case markdownDisplayBold:
			// Emphasis is weight-only (no colour) — dark-mode-friendly and calm.
			builder.WriteString(qgTheme.ink.Bold(true).Render(text))
		case markdownDisplayCode:
			builder.WriteString(inlineCodeStyle().Render(text))
		case markdownDisplayRule:
			builder.WriteString(qgTheme.lineStrong.Render(text))
		default:
			builder.WriteString(base.Render(text))
		}
		run.Reset()
	}

	for index := 0; index < len(line); {
		switch {
		case strings.HasPrefix(line[index:], markdownBoldStart):
			flush()
			style = markdownDisplayBold
			index += len(markdownBoldStart)
			continue
		case strings.HasPrefix(line[index:], markdownBoldEnd):
			flush()
			style = markdownDisplayNormal
			index += len(markdownBoldEnd)
			continue
		case strings.HasPrefix(line[index:], markdownCodeStart):
			flush()
			style = markdownDisplayCode
			index += len(markdownCodeStart)
			continue
		case strings.HasPrefix(line[index:], markdownCodeEnd):
			flush()
			style = markdownDisplayNormal
			index += len(markdownCodeEnd)
			continue
		case line[index] == '\x1b':
			// Already-styled input (highlighted code, headings, tables) carries real
			// ANSI. Emit each escape verbatim instead of treating its bytes as runes
			// and re-Render()ing them: re-wrapping doubles the SGR density and a
			// flush()/truncate can then slice mid-escape, leaking "[38;2;…" / "[1;4;…"
			// fragments into the visible text. Mirrors truncateStyledLine. The bold
			// markers above are matched first, so their semantic style switch is kept.
			if end := ansiSequenceEnd(line, index); end > index {
				flush()
				builder.WriteString(line[index:end])
				index = end
				continue
			}
		}

		r, size := utf8.DecodeRuneInString(line[index:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		nextStyle := markdownDisplayStyleForRune(r, style)
		if nextStyle != style {
			flush()
			style = nextStyle
		}
		run.WriteRune(r)
		index += size
	}
	flush()
	return builder.String()
}

func hasMarkdownDisplayControls(line string) bool {
	return strings.Contains(line, markdownBoldStart) ||
		strings.Contains(line, markdownBoldEnd) ||
		strings.Contains(line, markdownCodeStart) ||
		strings.Contains(line, markdownCodeEnd)
}

func hasExternalANSIStyle(line string) bool {
	for index := 0; index < len(line); {
		if line[index] != '\x1b' {
			index++
			continue
		}
		end := ansiSequenceEnd(line, index)
		if end <= index {
			index++
			continue
		}
		seq := line[index:end]
		if seq != markdownBoldStart && seq != markdownBoldEnd && seq != markdownCodeStart && seq != markdownCodeEnd {
			return true
		}
		index = end
	}
	return false
}

func markdownDisplayStyleForRune(r rune, current markdownDisplayStyle) markdownDisplayStyle {
	if current == markdownDisplayCode {
		return current
	}
	switch r {
	case '│', '─', '┼', '╭', '╮', '╰', '╯', '├', '┤', '┬', '┴':
		return markdownDisplayRule
	default:
		if current == markdownDisplayRule {
			return markdownDisplayNormal
		}
		return current
	}
}

func markdownStartsBlock(lines []string, index int) bool {
	if index >= len(lines) {
		return false
	}
	trimmed := strings.TrimSpace(lines[index])
	if trimmed == "" {
		return true
	}
	if _, ok := markdownFenceMarker(trimmed); ok {
		return true
	}
	if markdownHeadingText(trimmed) != "" || isMarkdownHorizontalRule(trimmed) || isMarkdownListLine(trimmed) || strings.HasPrefix(trimmed, ">") {
		return true
	}
	return index+1 < len(lines) && isMarkdownTableHeader(lines[index], lines[index+1])
}

func isMarkdownHorizontalRule(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	marker := rune(0)
	count := 0
	for _, r := range trimmed {
		if r == ' ' || r == '\t' {
			continue
		}
		if r != '-' && r != '_' && r != '*' {
			return false
		}
		if marker == 0 {
			marker = r
		} else if marker != r {
			return false
		}
		count++
	}
	return count >= 3
}

func renderMarkdownHorizontalRule(measure int) string {
	width := clampInt(measure, 16, 96)
	return strings.Repeat("─", width)
}

func looksLikeBareCodeLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	switch {
	case strings.HasPrefix(trimmed, "from ") && strings.Contains(trimmed, " import "):
		return true
	case strings.HasPrefix(trimmed, "import ") && !strings.Contains(trimmed, " from "):
		return true
	case strings.HasPrefix(trimmed, "def ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "class ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "if ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "elif ") && strings.HasSuffix(trimmed, ":"):
		return true
	case trimmed == "else:" || trimmed == "try:" || trimmed == "finally:":
		return true
	case strings.HasPrefix(trimmed, "for ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "while ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "with ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "except") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "return "):
		return true
	case strings.HasPrefix(trimmed, "print("):
		return true
	case isSimpleFunctionCall(trimmed):
		return true
	case strings.HasPrefix(trimmed, "package "):
		return true
	case strings.HasPrefix(trimmed, "func ") && strings.Contains(trimmed, "{"):
		return true
	case strings.HasPrefix(trimmed, "const "), strings.HasPrefix(trimmed, "let "), strings.HasPrefix(trimmed, "var "):
		return true
	case strings.HasPrefix(trimmed, "function ") && strings.Contains(trimmed, "{"):
		return true
	case strings.HasPrefix(trimmed, "<!DOCTYPE "), strings.HasPrefix(trimmed, "<html"), strings.HasPrefix(trimmed, "<div"), strings.HasPrefix(trimmed, "<span"):
		return true
	}
	return false
}

func looksLikeBareCodeBlock(lines []string) bool {
	nonBlank := 0
	codeLike := 0
	strongSignals := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonBlank++
		if isIndentedCodeContinuation(line) {
			codeLike++
			strongSignals++
			continue
		}
		if looksLikeBareCodeLine(line) {
			codeLike++
			if looksLikeStrongBareCodeLine(line) {
				strongSignals++
			}
		}
	}
	return nonBlank >= 2 && codeLike == nonBlank && strongSignals > 0
}

func looksLikeStrongBareCodeLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "from ") && strings.Contains(trimmed, " import "):
		return true
	case strings.HasPrefix(trimmed, "import ") && !strings.Contains(trimmed, " from "):
		return true
	case strings.HasPrefix(trimmed, "def ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "class ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "print("):
		return true
	case isSimpleFunctionCall(trimmed):
		return true
	case strings.HasPrefix(trimmed, "package "):
		return true
	case strings.HasPrefix(trimmed, "func ") && strings.Contains(trimmed, "{"):
		return true
	case strings.HasPrefix(trimmed, "const "), strings.HasPrefix(trimmed, "let "), strings.HasPrefix(trimmed, "var "):
		return true
	case strings.HasPrefix(trimmed, "function ") && strings.Contains(trimmed, "{"):
		return true
	case strings.HasPrefix(trimmed, "<!DOCTYPE "), strings.HasPrefix(trimmed, "<html"), strings.HasPrefix(trimmed, "<div"), strings.HasPrefix(trimmed, "<span"):
		return true
	}
	return false
}

func isIndentedCodeContinuation(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t"))
}

func isSimpleFunctionCall(trimmed string) bool {
	open := strings.Index(trimmed, "(")
	return open > 0 &&
		strings.HasSuffix(trimmed, ")") &&
		isIdentifierText(trimmed[:open])
}

func isIdentifierText(text string) bool {
	if text == "" {
		return false
	}
	for index, r := range text {
		if r == '_' || unicode.IsLetter(r) || (index > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return true
}

func markdownFenceMarker(trimmed string) (string, bool) {
	for _, marker := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, marker) {
			return marker, true
		}
	}
	return "", false
}

func renderMarkdownCodeBlock(code []string, measure int) []string {
	if len(code) == 0 {
		return []string{""}
	}
	lines := []string{}
	for _, line := range code {
		if line == "" {
			lines = append(lines, "")
			continue
		}
		for lipgloss.Width(line) > measure {
			head, tail := splitAtWidth(line, measure)
			lines = append(lines, head)
			line = tail
		}
		lines = append(lines, line)
	}
	return lines
}

func isMarkdownTableHeader(header string, separator string) bool {
	headerCells := parseMarkdownTableRow(header)
	separatorCells := parseMarkdownTableRow(separator)
	if len(headerCells) < 2 || len(headerCells) != len(separatorCells) {
		return false
	}
	for _, cell := range separatorCells {
		if !isMarkdownTableSeparatorCell(cell) {
			return false
		}
	}
	return true
}

func isMarkdownTableRow(line string) bool {
	cells := parseMarkdownTableRow(line)
	return len(cells) >= 2
}

func parseMarkdownTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.Contains(trimmed, "|") {
		return nil
	}
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, normalizeMarkdownTableCell(part))
	}
	return cells
}

func normalizeMarkdownTableCell(cell string) string {
	replacer := strings.NewReplacer(
		"<br />", "\n",
		"<br/>", "\n",
		"<br>", "\n",
		"<BR />", "\n",
		"<BR/>", "\n",
		"<BR>", "\n",
	)
	return strings.TrimSpace(replacer.Replace(cell))
}

func isMarkdownTableSeparatorCell(cell string) bool {
	cell = strings.TrimSpace(cell)
	cell = strings.Trim(cell, ":")
	if len(cell) < 3 {
		return false
	}
	for _, r := range cell {
		if r != '-' {
			return false
		}
	}
	return true
}

func parseMarkdownTableAlignments(separator string) []markdownTableAlignment {
	cells := parseMarkdownTableRow(separator)
	alignments := make([]markdownTableAlignment, len(cells))
	for index, cell := range cells {
		cell = strings.TrimSpace(cell)
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		switch {
		case left && right:
			alignments[index] = markdownTableAlignCenter
		case right:
			alignments[index] = markdownTableAlignRight
		default:
			alignments[index] = markdownTableAlignLeft
		}
	}
	return alignments
}

func renderMarkdownTable(rows [][]string, alignments []markdownTableAlignment, measure int) []string {
	rows, columns := normalizeMarkdownTableRows(rows)
	if columns == 0 {
		return nil
	}
	alignments = normalizeMarkdownTableAlignments(alignments, columns)
	widths := markdownTableWidths(rows, columns, measure)
	renderedRows := make([][]string, len(rows))
	for rowIndex, row := range rows {
		renderedRows[rowIndex] = renderMarkdownTableRow(row, widths, alignments, rowIndex == 0)
	}
	separateBodyRows := markdownTableNeedsBodyRules(renderedRows)
	out := []string{markdownTableTopRule(widths)}
	for rowIndex, rowLines := range renderedRows {
		if separateBodyRows && rowIndex > 1 {
			out = append(out, markdownTableRule(widths))
		}
		out = append(out, rowLines...)
		if rowIndex == 0 && len(rows) > 1 {
			out = append(out, markdownTableRule(widths))
		}
	}
	out = append(out, markdownTableBottomRule(widths))
	return out
}

func markdownTableNeedsBodyRules(renderedRows [][]string) bool {
	if len(renderedRows) > markdownTableRuleBodyRows {
		return true
	}
	for _, row := range renderedRows[1:] {
		if len(row) > 1 {
			return true
		}
	}
	return false
}

func wrapMarkdownTableCellPart(prefix string, text string, measure int) []string {
	continuationPrefix := strings.Repeat(" ", lipgloss.Width(prefix))
	lines := []string{}
	for index, segment := range strings.Split(text, "\n") {
		firstPrefix := prefix
		if index > 0 {
			firstPrefix = continuationPrefix
		}
		lines = append(lines, wrapMarkdownInlineWithPrefixes(firstPrefix, continuationPrefix, segment, measure)...)
	}
	if len(lines) == 0 {
		return []string{prefix}
	}
	return lines
}

type markdownTableCellPart struct {
	text   string
	bullet bool
}

func markdownTableCellParts(value string) []markdownTableCellPart {
	value = strings.TrimSpace(value)
	starts := markdownInlineBulletStarts(value)
	if len(starts) == 0 {
		return []markdownTableCellPart{{text: value}}
	}

	parts := []markdownTableCellPart{}
	if starts[0] > 0 {
		if text := strings.TrimSpace(value[:starts[0]]); text != "" {
			parts = append(parts, markdownTableCellPart{text: text})
		}
	}
	for index, start := range starts {
		end := len(value)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		text := strings.TrimSpace(value[start:end])
		text = strings.TrimSpace(strings.TrimPrefix(text, "-"))
		if text != "" {
			parts = append(parts, markdownTableCellPart{text: text, bullet: true})
		}
	}
	if len(parts) == 0 {
		return []markdownTableCellPart{{text: value}}
	}
	return parts
}

func markdownInlineBulletStarts(value string) []int {
	starts := []int{}
	if strings.HasPrefix(value, "- ") {
		starts = append(starts, 0)
	}
	offset := 0
	for {
		index := strings.Index(value[offset:], " - ")
		if index < 0 {
			break
		}
		hyphen := offset + index + 1
		candidate := hyphen + 2
		if isProbableInlineBulletLabel(value[candidate:]) {
			starts = append(starts, hyphen)
		}
		offset = candidate
	}
	return starts
}

func isProbableInlineBulletLabel(text string) bool {
	colon := strings.IndexByte(text, ':')
	if colon <= 0 || colon > 48 {
		return false
	}
	label := text[:colon]
	return !strings.ContainsAny(label, ".!?;")
}

func normalizeMarkdownTableRows(rows [][]string) ([][]string, int) {
	columns := 0
	for _, row := range rows {
		if len(row) > columns {
			columns = len(row)
		}
	}
	if columns == 0 {
		return nil, 0
	}
	normalized := make([][]string, 0, len(rows))
	for _, row := range rows {
		next := make([]string, columns)
		copy(next, row)
		normalized = append(normalized, next)
	}
	return normalized, columns
}

func normalizeMarkdownTableAlignments(alignments []markdownTableAlignment, columns int) []markdownTableAlignment {
	normalized := make([]markdownTableAlignment, columns)
	copy(normalized, alignments)
	return normalized
}

func markdownTableWidths(rows [][]string, columns int, measure int) []int {
	separatorWidth := lipgloss.Width(markdownTableColumnSeparator)*maxInt(0, columns-1) + 4
	contentWidth := maxInt(columns*markdownTableMinColumnWidth, measure-separatorWidth)
	widths := make([]int, columns)
	minWidths := make([]int, columns)
	for column := range widths {
		headerWidth := 0
		if len(rows) > 0 && column < len(rows[0]) {
			headerWidth = lipgloss.Width(markdownInlinePlain(rows[0][column]))
		}
		minWidths[column] = clampInt(headerWidth, markdownTableMinColumnWidth, markdownTableHeaderMaxWidth)
		widths[column] = minWidths[column]
	}
	for _, row := range rows {
		for column, cell := range row {
			if column >= len(widths) {
				continue
			}
			for _, line := range markdownTableCellWidthParts(cell) {
				plainLine := markdownInlinePlain(line)
				lineWidth := lipgloss.Width(plainLine)
				if wordWidth := longestMarkdownWordWidth(plainLine); wordWidth > lineWidth {
					lineWidth = wordWidth
				}
				if lineWidth > widths[column] {
					widths[column] = minInt(lineWidth, contentWidth)
				}
			}
		}
	}
	for sumInts(widths) > contentWidth && shrinkMarkdownTableWidth(widths, minWidths) {
	}
	for sumInts(widths) > contentWidth {
		largest := 0
		for index, width := range widths {
			if width > widths[largest] {
				largest = index
			}
		}
		if widths[largest] <= markdownTableMinColumnWidth {
			break
		}
		widths[largest]--
	}
	return widths
}

func shrinkMarkdownTableWidth(widths []int, minWidths []int) bool {
	largest := -1
	for index, width := range widths {
		if width <= minWidths[index] {
			continue
		}
		if largest < 0 || width > widths[largest] {
			largest = index
		}
	}
	if largest < 0 {
		return false
	}
	widths[largest]--
	return true
}

func renderMarkdownTableRow(row []string, widths []int, alignments []markdownTableAlignment, header bool) []string {
	wrapped := make([][]string, len(widths))
	height := 1
	for column, width := range widths {
		cell := ""
		if column < len(row) {
			cell = row[column]
		}
		wrapped[column] = markdownTableCellLines(cell, width)
		if len(wrapped[column]) == 0 {
			wrapped[column] = []string{""}
		}
		if len(wrapped[column]) > height {
			height = len(wrapped[column])
		}
	}

	lines := make([]string, 0, height)
	for lineIndex := 0; lineIndex < height; lineIndex++ {
		parts := make([]string, len(widths))
		for column, width := range widths {
			cell := ""
			if lineIndex < len(wrapped[column]) {
				cell = wrapped[column][lineIndex]
			}
			if header && cell != "" {
				cell = renderMarkdownBoldText(cell)
			}
			parts[column] = alignMarkdownTableCell(cell, width, alignments[column])
		}
		lines = append(lines, "│ "+strings.Join(parts, markdownTableColumnSeparator)+" │")
	}
	return lines
}

func markdownTableCellWidthParts(cell string) []string {
	parts := markdownTableCellParts(cell)
	lines := []string{}
	for _, part := range parts {
		line := part.text
		if part.bullet {
			line = "- " + line
		}
		for _, segment := range strings.Split(line, "\n") {
			if segment != "" {
				lines = append(lines, segment)
			}
		}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func markdownTableCellLines(cell string, width int) []string {
	parts := markdownTableCellParts(cell)
	if len(parts) == 1 && !parts[0].bullet {
		return wrapMarkdownInline(cell, width)
	}
	lines := []string{}
	for _, part := range parts {
		prefix := ""
		if part.bullet {
			prefix = "- "
		}
		lines = append(lines, wrapMarkdownTableCellPart(prefix, part.text, width)...)
	}
	return lines
}

func alignMarkdownTableCell(text string, width int, alignment markdownTableAlignment) string {
	padding := width - lipgloss.Width(text)
	if padding <= 0 {
		return text
	}
	switch alignment {
	case markdownTableAlignRight:
		return strings.Repeat(" ", padding) + text
	case markdownTableAlignCenter:
		left := padding / 2
		right := padding - left
		return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
	default:
		return text + strings.Repeat(" ", padding)
	}
}

func markdownTableRule(widths []int) string {
	return markdownTableBorderRule(widths, "├", "┼", "┤")
}

func markdownTableTopRule(widths []int) string {
	return markdownTableBorderRule(widths, "╭", "┬", "╮")
}

func markdownTableBottomRule(widths []int) string {
	return markdownTableBorderRule(widths, "╰", "┴", "╯")
}

func markdownTableBorderRule(widths []int, left string, separator string, right string) string {
	parts := make([]string, len(widths))
	for index, width := range widths {
		parts[index] = strings.Repeat("─", maxInt(1, width+2))
	}
	return left + strings.Join(parts, separator) + right
}

func markdownHeadingText(trimmed string) string {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(trimmed) || trimmed[level] != ' ' {
		return ""
	}
	return strings.TrimSpace(trimmed[level:])
}

func isMarkdownListLine(trimmed string) bool {
	if len(trimmed) >= 2 {
		if strings.Contains("-*+", string(trimmed[0])) && trimmed[1] == ' ' {
			return true
		}
	}
	dot := strings.IndexByte(trimmed, '.')
	if dot <= 0 || dot+1 >= len(trimmed) || trimmed[dot+1] != ' ' {
		return false
	}
	for _, r := range trimmed[:dot] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func renderMarkdownStandaloneLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, ">") {
		return "│ " + strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
	}
	return strings.TrimRight(line, " ")
}

type markdownInlineSegment struct {
	text string
	bold bool
	code bool
}

func wrapMarkdownInline(text string, measure int) []string {
	if measure < 1 {
		measure = 1
	}
	out := []string{}
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(paragraph) == "" {
			out = append(out, "")
			continue
		}
		body := strings.TrimLeft(paragraph, " \t")
		indent := strings.ReplaceAll(paragraph[:len(paragraph)-len(body)], "\t", "    ")
		if len(indent) >= measure {
			indent = strings.Repeat(" ", measure/2)
		}
		out = append(out, wrapMarkdownInlineWithPrefixes(indent, indent, body, measure)...)
	}
	return out
}

func wrapMarkdownInlineWithPrefixes(firstPrefix string, continuationPrefix string, text string, measure int) []string {
	words := markdownInlineWords(parseMarkdownInline(text))
	if len(words) == 0 {
		return []string{firstPrefix}
	}
	return appendWrappedMarkdownWords(nil, words, 0, "", 0, firstPrefix, continuationPrefix, measure, nil)
}

// appendWrappedMarkdownWords runs the greedy wrap over words[start:] carrying
// the in-progress line state, so a streaming paragraph resumes where the last
// frame stopped instead of re-wrapping from scratch. When starts is non-nil it
// collects the word index starting each emitted visual line, parallel to the
// returned lines, for the next resume point.
func appendWrappedMarkdownWords(lines []string, words []markdownInlineSegment, start int, line string, lineWidth int, prefix string, continuationPrefix string, measure int, starts *[]int) []string {
	available := maxInt(1, measure-lipgloss.Width(prefix))
	lineStart := start
	record := func() {
		if starts != nil {
			*starts = append(*starts, lineStart)
		}
	}
	flush := func(next int) {
		lines = append(lines, prefix+line)
		record()
		line = ""
		lineWidth = 0
		prefix = continuationPrefix
		available = maxInt(1, measure-lipgloss.Width(prefix))
		lineStart = next
	}

	for wi := start; wi < len(words); wi++ {
		word := words[wi]
		for lipgloss.Width(word.text) > available {
			if line != "" {
				flush(wi)
			}
			head, tail := splitAtWidth(word.text, available)
			lines = append(lines, prefix+renderMarkdownInlineSegment(markdownInlineSegment{text: head, bold: word.bold, code: word.code}))
			record()
			word.text = tail
			prefix = continuationPrefix
			available = maxInt(1, measure-lipgloss.Width(prefix))
			lineStart = wi
		}
		wordWidth := lipgloss.Width(word.text)
		rendered := renderMarkdownInlineSegment(word)
		separator := " "
		separatorWidth := 1
		if joinsPreviousMarkdownWord(word.text) {
			separator = ""
			separatorWidth = 0
		}
		switch {
		case line == "":
			line = rendered
			lineWidth = wordWidth
		case lineWidth+separatorWidth+wordWidth <= available:
			line += separator + rendered
			lineWidth += separatorWidth + wordWidth
		default:
			flush(wi)
			line = rendered
			lineWidth = wordWidth
		}
	}
	if line != "" {
		lines = append(lines, prefix+line)
		record()
	}
	return lines
}

func markdownInlineWords(segments []markdownInlineSegment) []markdownInlineSegment {
	words := []markdownInlineSegment{}
	for _, segment := range segments {
		if segment.code {
			if segment.text != "" {
				words = append(words, segment)
			}
			continue
		}
		for _, word := range strings.Fields(segment.text) {
			words = append(words, markdownInlineSegment{text: word, bold: segment.bold, code: segment.code})
		}
	}
	return words
}

func joinsPreviousMarkdownWord(text string) bool {
	return text != "" && strings.Trim(text, ".,!?;:%)]}") == ""
}

func renderMarkdownInlineSegment(segment markdownInlineSegment) string {
	if segment.text == "" {
		return ""
	}
	if segment.code {
		return markdownCodeStart + segment.text + markdownCodeEnd
	}
	if segment.bold {
		return renderMarkdownBoldText(segment.text)
	}
	return segment.text
}

func renderMarkdownBoldText(text string) string {
	return markdownBoldStart + text + markdownBoldEnd
}

func markdownInlinePlain(text string) string {
	segments := parseMarkdownInline(text)
	var builder strings.Builder
	for _, segment := range segments {
		builder.WriteString(segment.text)
	}
	return strings.TrimSpace(builder.String())
}

func parseMarkdownInline(text string) []markdownInlineSegment {
	segments := []markdownInlineSegment{}
	var builder strings.Builder
	bold := false
	emphasis := false
	code := false
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		segments = append(segments, markdownInlineSegment{text: builder.String(), bold: bold && !code, code: code})
		builder.Reset()
	}

	for index := 0; index < len(text); {
		switch {
		case strings.HasPrefix(text[index:], "**"):
			if !code && ((bold && canCloseMarkdownDelimiter(text, index, "**")) || (!bold && canOpenMarkdownDelimiter(text, index, "**") && hasClosingMarkdownDelimiter(text, index+2, "**"))) {
				flush()
				bold = !bold
				index += 2
				continue
			}
		case strings.HasPrefix(text[index:], "__"):
			if !code && ((bold && canCloseMarkdownDelimiter(text, index, "__")) || (!bold && canOpenMarkdownDelimiter(text, index, "__") && hasClosingMarkdownDelimiter(text, index+2, "__"))) {
				flush()
				bold = !bold
				index += 2
				continue
			}
		case text[index] == '`':
			if code || strings.Contains(text[index+1:], "`") {
				flush()
				code = !code
				index++
				continue
			}
		case text[index] == '*':
			if !code && !strings.HasPrefix(text[index:], "**") && ((emphasis && canCloseMarkdownDelimiter(text, index, "*")) || (!emphasis && canOpenMarkdownDelimiter(text, index, "*") && hasClosingMarkdownDelimiter(text, index+1, "*"))) {
				flush()
				emphasis = !emphasis
				index++
				continue
			}
		}

		r, size := utf8.DecodeRuneInString(text[index:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		builder.WriteRune(r)
		index += size
	}
	flush()
	return segments
}

func hasClosingMarkdownDelimiter(text string, start int, marker string) bool {
	for index := start; index < len(text); index++ {
		if !strings.HasPrefix(text[index:], marker) {
			continue
		}
		if marker == "*" && index+1 < len(text) && text[index+1] == '*' {
			index++
			continue
		}
		if canCloseMarkdownDelimiter(text, index, marker) {
			return true
		}
		index += len(marker) - 1
	}
	return false
}

func canOpenMarkdownDelimiter(text string, index int, marker string) bool {
	before, hasBefore := markdownRuneBefore(text, index)
	after, hasAfter := markdownRuneAfter(text, index+len(marker))
	if !hasAfter || unicode.IsSpace(after) {
		return false
	}
	if hasBefore && markdownIsWordRune(before) {
		return false
	}
	if marker == "__" && isMarkdownDunderIdentifier(text, index) {
		return false
	}
	return true
}

func canCloseMarkdownDelimiter(text string, index int, marker string) bool {
	before, hasBefore := markdownRuneBefore(text, index)
	after, hasAfter := markdownRuneAfter(text, index+len(marker))
	if !hasBefore || unicode.IsSpace(before) {
		return false
	}
	if hasAfter && markdownIsWordRune(after) {
		return false
	}
	return true
}

func isMarkdownDunderIdentifier(text string, index int) bool {
	end := strings.Index(text[index+2:], "__")
	if end < 0 {
		return false
	}
	body := text[index+2 : index+2+end]
	if body == "" {
		return false
	}
	for _, r := range body {
		if !markdownIsWordRune(r) {
			return false
		}
	}
	return true
}

func markdownRuneBefore(text string, index int) (rune, bool) {
	if index <= 0 {
		return 0, false
	}
	r, _ := utf8.DecodeLastRuneInString(text[:index])
	return r, r != utf8.RuneError
}

func markdownRuneAfter(text string, index int) (rune, bool) {
	if index >= len(text) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(text[index:])
	return r, r != utf8.RuneError
}

func markdownIsWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func longestMarkdownWordWidth(text string) int {
	longest := 0
	for _, word := range strings.Fields(text) {
		if width := lipgloss.Width(word); width > longest {
			longest = width
		}
	}
	return longest
}

func sumInts(values []int) int {
	sum := 0
	for _, value := range values {
		sum += value
	}
	return sum
}

func trimMarkdownDisplayBlankEdges(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
