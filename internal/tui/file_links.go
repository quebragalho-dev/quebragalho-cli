package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// file_links.go turns file paths in the transcript into clickable links.
//
// Assistant answers and tool outputs mention real files ("warrior_pixelart.png",
// "└ saved /home/user/a.png", "Gravação de Tela ... .mov") but render them as
// plain styled text; only tool-card targets get OSC 8 hyperlinks today. This
// file scans plain text for paths that exist on disk, wraps them in OSC 8
// (zero-width, so wrapping/truncation/copy math is unaffected), and opens the
// hit on a modifier+click — with the mouse captured the terminal surrenders
// clicks to the app, so waiting for the terminal to open links never works.
//
// Scope: absolute paths, ~/... paths and paths relative to the session cwd,
// plus markdown-link targets, backtick spans and quoted spans. Only paths that
// exist (file or directory) become links; invented URLs and missing files
// never do. Directories open in the system file viewer, same as files.

// fileLinkSpan is one linkable path occurrence: byte offsets into the plain
// line, the span text as shown, and the resolved absolute path to open.
type fileLinkSpan struct {
	start int
	end   int
	raw   string
	abs   string
}

// fileLinkCacheTTL bounds how long a negative existence probe is trusted: the
// model creates files mid-session, so a "missing" answer must expire.
const fileLinkCacheTTL = 5 * time.Second

type fileLinkCacheEntry struct {
	exists bool
	at     time.Time
}

var fileLinkExistsCache = struct {
	sync.Mutex
	entries map[string]fileLinkCacheEntry
}{entries: map[string]fileLinkCacheEntry{}}

// fileLinkCacheNow is overridable in tests to advance the cache clock.
var fileLinkCacheNow = time.Now

func fileLinkExists(path string) bool {
	now := fileLinkCacheNow()
	fileLinkExistsCache.Lock()
	defer fileLinkExistsCache.Unlock()
	if entry, ok := fileLinkExistsCache.entries[path]; ok && now.Sub(entry.at) < fileLinkCacheTTL {
		return entry.exists
	}
	_, err := os.Stat(path)
	entry := fileLinkCacheEntry{exists: err == nil, at: now}
	fileLinkExistsCache.entries[path] = entry
	return entry.exists
}

// resolveFileLink maps a raw span to the absolute path to open. Tilde expands
// against the home directory, bare names resolve against the session cwd;
// only existing files or directories resolve. The second result reports
// whether the span names something openable, not whether the text merely
// looks like a path.
func resolveFileLink(raw string, cwd string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.Trim(trimmed, `"'`+"`")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "", false
	}
	var abs string
	switch {
	case trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`):
		home, err := userHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", false
		}
		rest := strings.TrimPrefix(trimmed, "~")
		rest = strings.TrimLeft(rest, `/\`)
		abs = filepath.Join(home, rest)
	case filepath.IsAbs(trimmed):
		abs = filepath.Clean(trimmed)
	default:
		if strings.TrimSpace(cwd) == "" {
			return "", false
		}
		abs = filepath.Join(cwd, trimmed)
	}
	if !fileLinkExists(abs) {
		return "", false
	}
	return abs, true
}

// looksLikeFilePathToken reports whether a bare token is even worth stat-ing:
// it must carry a separator, a tilde/drive prefix, or a filename extension.
// This keeps ordinary prose ("ok", "foto", "relatório") out of the disk cache.
func looksLikeFilePathToken(token string) bool {
	if token == "" {
		return false
	}
	if strings.ContainsAny(token, `/\`) || strings.HasPrefix(token, "~") {
		return true
	}
	if len(token) >= 2 && token[1] == ':' && isASCIILetter(token[0]) {
		return true
	}
	if index := strings.LastIndex(token, "."); index > 0 && index < len(token)-1 {
		suffix := token[index+1:]
		if len(suffix) <= 6 && isFileExtensionChars(suffix) {
			return true
		}
	}
	return false
}

func isASCIILetter(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

func isFileExtensionChars(suffix string) bool {
	for i := 0; i < len(suffix); i++ {
		c := suffix[i]
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return len(suffix) > 0
}

// findFileLinks scans a plain transcript line for openable file paths: first
// markdown-link targets, backtick spans and quoted spans (whose boundaries the
// author already drew), then bare paths with left-expansion over spaces so
// "Gravação de Tela 2026-11-18 às 17.00.29.mov" links whole instead of just
// its last token. Returned spans never overlap; only existing paths resolve.
func findFileLinks(line string, cwd string) []fileLinkSpan {
	var spans []fileLinkSpan
	claimed := func(start, end int) bool {
		for _, span := range spans {
			if start < span.end && end > span.start {
				return true
			}
		}
		return false
	}
	claim := func(start, end int, raw string) {
		if start >= end || claimed(start, end) {
			return
		}
		abs, ok := resolveFileLink(raw, cwd)
		if !ok {
			return
		}
		spans = append(spans, fileLinkSpan{start: start, end: end, raw: raw, abs: abs})
	}

	// Markdown links: [label](target). The span covers the whole match because
	// the renderer shows the raw syntax.
	for index := 0; index < len(line); {
		open := strings.IndexByte(line[index:], '[')
		if open < 0 {
			break
		}
		open += index
		mid := strings.Index(line[open:], "](")
		if mid < 0 {
			break
		}
		mid += open
		close := strings.IndexByte(line[mid+2:], ')')
		if close < 0 {
			break
		}
		close += mid + 2
		target := strings.TrimSpace(line[mid+2 : close])
		target = strings.Trim(target, "<>")
		if looksLikeFilePathToken(target) {
			claim(open, close+1, target)
		}
		index = close + 1
	}

	// Backtick and quoted spans.
	// Delimited spans link on existence alone: the author drew the boundary,
	// so even extensionless names ("README") resolve. Quoted prose that names
	// nothing on disk simply yields no span.
	scanDelimited := func(line string, quote byte) {
		for index := 0; index < len(line); {
			open := strings.IndexByte(line[index:], quote)
			if open < 0 {
				return
			}
			open += index
			close := strings.IndexByte(line[open+1:], quote)
			if close < 0 {
				// Unbalanced opener: take it literally and let the bare
				// scan below handle the rest instead of dropping the line.
				index = open + 1
				continue
			}
			close += open + 1
			content := strings.TrimSpace(line[open+1 : close])
			if content == "" {
				index = close + 1
				continue
			}
			claim(open, close+1, content)
			index = close + 1
		}
	}
	scanDelimited(line, '`')
	scanDelimited(line, '"')
	scanDelimited(line, '\'')

	// Bare paths: whitespace-split chunks, expanded left over spaces while the
	// joined candidate exists (longest match wins), with trailing sentence
	// punctuation trimmed. Chunks carrying an opening boundary char stop the
	// expansion so `[see` never glues onto a path.
	chunks := splitLinkChunks(line)
	// Try longest-first: prepend previous chunks (up to a cap) and keep the
	// longest candidate that exists. A failed probe never stops the search —
	// only the anchor itself must exist for the span to fall back to it — so
	// "nota: arquivo.txt" still links arquivo.txt while a spaced name links
	// whole. Expansion stops past a chunk with a leading boundary char:
	// prose on the other side of a bracket or quote is never part of the path.
	const maxLinkChunks = 12
	for end := range chunks {
		anchor := stripLinkPunctuation(chunks[end].text)
		if !looksLikeFilePathToken(anchor) {
			continue
		}
		anchorOffset := strings.Index(chunks[end].text, anchor)
		if anchorOffset < 0 {
			continue
		}
		anchorStart := chunks[end].start + anchorOffset
		anchorEnd := anchorStart + len(anchor)
		bestStart, bestEnd, bestRaw := anchorStart, anchorEnd, anchor
		if _, ok := resolveFileLink(anchor, cwd); !ok {
			bestRaw = ""
		}
		probe := anchor
		for begin := end - 1; begin >= 0 && end-begin <= maxLinkChunks; begin-- {
			raw := chunks[begin].text
			core := stripLinkPunctuation(raw)
			if core == "" {
				break
			}
			// A leading boundary char ("(foto", "[x") marks where prose ends:
			// the stripped core may still join this probe, but nothing
			// further left belongs to the path.
			stop := false
			if trimmed := strings.TrimLeft(raw, " \"'`([{<"); trimmed != raw {
				stop = true
			}
			core = stripLinkBoundaryPrefix(core)
			if core == "" {
				break
			}
			probe = core + " " + probe
			if len([]rune(probe)) > maxLinkRunes {
				break
			}
			if _, ok := resolveFileLink(probe, cwd); ok {
				bestStart = chunks[begin].start + strings.Index(raw, core)
				bestEnd = anchorEnd
				bestRaw = probe
			}
			if stop {
				break
			}
		}
		if bestRaw == "" {
			continue
		}
		claim(bestStart, bestEnd, bestRaw)
	}
	return spans
}

// maxLinkRunes caps a single link candidate: paths longer than this are not
// attempted, bounding the stat probes on hostile input.
const maxLinkRunes = 200

// linkChunk is one whitespace-delimited token with byte offsets into its line.
type linkChunk struct {
	start int
	end   int
	text  string
}

// splitLinkChunks splits a plain line into whitespace-delimited chunks,
// shared by the single-row scanner and the cross-row joiner.
func splitLinkChunks(line string) []linkChunk {
	var chunks []linkChunk
	for index := 0; index < len(line); {
		r, size := utf8.DecodeRuneInString(line[index:])
		if unicode.IsSpace(r) {
			index += size
			continue
		}
		start := index
		for index < len(line) {
			r, size := utf8.DecodeRuneInString(line[index:])
			if unicode.IsSpace(r) {
				break
			}
			index += size
		}
		chunks = append(chunks, linkChunk{start: start, end: index, text: line[start:index]})
	}
	return chunks
}

// stripGutterPrefix drops wrap continuation markers (indentation and the
// tool-body tree gutter) so a middle piece splices cleanly. Only leading
// whitespace and box-drawing bars go: real path text never starts with those.
func stripGutterPrefix(s string) string {
	for {
		trimmed := strings.TrimLeft(s, " \t")
		stripped := trimmed
		for _, bar := range []string{"\u2502", "\u2503", "|"} {
			if rest, found := strings.CutPrefix(stripped, bar); found {
				stripped = rest
				break
			}
		}
		if stripped == trimmed {
			return trimmed
		}
		s = stripped
	}
}

// stripLinkPunctuation trims sentence punctuation glued to a chunk's edges
// ("foto.png.", "(saída:" ...) without touching drive letters or extensions.
func stripLinkPunctuation(chunk string) string {
	trimmed := strings.Trim(chunk, "\"'`")
	trimmed = strings.TrimLeft(trimmed, "([{<")
	// A lone drive letter ("C:") is not sentence punctuation.
	if !(len(trimmed) == 2 && trimmed[1] == ':' && isASCIILetter(trimmed[0])) {
		trimmed = strings.TrimRight(trimmed, ".,;:!?")
	}
	trimmed = strings.TrimRight(trimmed, ")]}>")
	return trimmed
}

// stripLinkBoundaryPrefix drops a leading boundary char ("(foto", "[x") that
// marks where prose ends and the path begins.
func stripLinkBoundaryPrefix(chunk string) string {
	return strings.TrimLeft(chunk, "\"'`([{<")
}

// linkifyStyledLine wraps existing-file paths in an already-styled line with
// OSC 8 hyperlinks. Escape sequences are skipped via ansiSequenceEnd, and
// text already inside an OSC 8 link is never wrapped twice — the same state
// tracking truncateStyledLine uses, so truncation after linkifying stays
// balanced too.
func linkifyStyledLine(styled string, cwd string) string {
	plain, byteOffsets, inLink := scanStyledRow(styled)
	return spliceLinkSpans(styled, plain, byteOffsets, inLink, findFileLinks(string(plain), cwd))
}

// scanStyledRow walks one styled row, returning its printable runes, the
// styled byte offset of each rune, and whether the rune sits inside an
// existing OSC 8 link.
func scanStyledRow(styled string) (plain []rune, byteOffsets []int, inLink []bool) {
	link := false
	for index := 0; index < len(styled); {
		if styled[index] == '\x1b' {
			end := ansiSequenceEnd(styled, index)
			if end > index {
				sequence := styled[index:end]
				if strings.HasPrefix(sequence, "\x1b]8;") {
					link = sequence != "\x1b]8;;\x1b\\" && sequence != "\x1b]8;;\a"
				}
				index = end
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(styled[index:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		plain = append(plain, r)
		byteOffsets = append(byteOffsets, index)
		inLink = append(inLink, link)
		index += size
	}
	return plain, byteOffsets, inLink
}

// spliceLinkSpans wraps spans with OSC 8 hyperlinks. Span offsets are byte
// offsets into string(plain). Spans touching already-linked text or with
// non-rune-boundary offsets are skipped, so nested or corrupt links are
// impossible; the rest splice from the end so offsets stay valid.
func spliceLinkSpans(styled string, plain []rune, byteOffsets []int, inLink []bool, spans []fileLinkSpan) string {
	type splice struct {
		at  int
		url string
		end bool
	}
	// Plain byte offset of each rune.
	plainBytes := make([]int, len(plain)+1)
	for i, r := range plain {
		plainBytes[i+1] = plainBytes[i] + utf8.RuneLen(r)
	}
	type positioned struct {
		byteStart int
		byteEnd   int
		url       string
	}
	var positionedSpans []positioned
	for _, span := range spans {
		rs, re, ok := runeRangeForByteRange(plainBytes, span.start, span.end)
		if !ok {
			continue
		}
		blocked := false
		for _, linked := range inLink[rs:re] {
			if linked {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		abs, ok := spanAbs(span)
		if !ok {
			continue
		}
		lastStart := byteOffsets[re-1]
		_, size := utf8.DecodeRuneInString(styled[lastStart:])
		positionedSpans = append(positionedSpans, positioned{
			byteStart: byteOffsets[rs],
			byteEnd:   lastStart + size,
			url:       fileLinkURL(abs),
		})
	}
	// Descending by start; overlapping spans cannot occur (the detector emits
	// disjoint ranges and multis suppress covered singles), but later, wider
	// spans win on contact to keep the output well-formed.
	sort.Slice(positionedSpans, func(i, j int) bool { return positionedSpans[i].byteStart > positionedSpans[j].byteStart })
	accepted := positionedSpans[:0]
	for _, span := range positionedSpans {
		overlap := false
		for _, kept := range accepted {
			if span.byteStart < kept.byteEnd && kept.byteStart < span.byteEnd {
				overlap = true
				break
			}
		}
		if !overlap {
			accepted = append(accepted, span)
		}
	}
	out := styled
	for _, span := range accepted {
		out = out[:span.byteEnd] + "\x1b]8;;\x1b\\" + out[span.byteEnd:]
		out = out[:span.byteStart] + "\x1b]8;;" + span.url + "\x1b\\" + out[span.byteStart:]
	}
	return out
}

// runeRangeForByteRange maps plain-byte offsets back to rune indices with
// exact boundary matches; anything else is rejected rather than sliced
// mid-rune.
func runeRangeForByteRange(plainBytes []int, start, end int) (int, int, bool) {
	rs, re := -1, -1
	for i, off := range plainBytes {
		if off == start {
			rs = i
		}
		if off == end {
			re = i
			break
		}
	}
	if rs < 0 || re < 0 || re <= rs {
		return 0, 0, false
	}
	return rs, re, true
}

// spanAbs returns the link target carried by a span.
func spanAbs(span fileLinkSpan) (string, bool) {
	return span.abs, strings.TrimSpace(span.abs) != ""
}

// linkifyStyledLineSpans wraps precomputed spans (byte offsets into the row's
// plain text, e.g. pieces of a joined multi-line link) in one styled row.
func linkifyStyledLineSpans(styled string, spans []fileLinkSpan) string {
	plain, byteOffsets, inLink := scanStyledRow(styled)
	return spliceLinkSpans(styled, plain, byteOffsets, inLink, spans)
}

// linkifyTranscriptLines wraps existing-file paths in every line of a
// rendered multi-line block (assistant answer or tool output), rejoining
// paths the width wrap split across rows. OSC 8 is zero-width, so fitted
// widths are preserved.
func linkifyTranscriptLines(text string, cwd string) string {
	lines := strings.Split(text, "\n")
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = string(stripStyledText(line))
	}
	singles, multis := planFileLinks(plain, cwd)
	byLine := map[int][]fileLinkSpan{}
	for line, spans := range singles {
		byLine[line] = append(byLine[line], spans...)
	}
	for _, multi := range multis {
		for _, part := range multi.parts {
			byLine[part.line] = append(byLine[part.line], fileLinkSpan{start: part.start, end: part.end, abs: multi.abs})
		}
	}
	for i, line := range lines {
		lines[i] = linkifyStyledLineSpans(line, byLine[i])
	}

	return strings.Join(lines, "\n")
}

// stripStyledText returns the printable text of a styled row, dropping ANSI
// escape sequences (including OSC 8) the same way the selectable-line builder
// does, so render-time spans and click-time spans agree.
func stripStyledText(styled string) []rune {
	plain, _, _ := scanStyledRow(styled)
	return plain
}

// planFileLinks finds single-row links plus links broken across wrapped rows.
// lines are consecutive rendered rows of one block; blank rows always end a
// chain, so paragraph breaks never glue. Single-row spans fully covered by a
// multi-row span are dropped in favour of the longer match.
func planFileLinks(lines []string, cwd string) (map[int][]fileLinkSpan, []fileLinkMultiSpan) {
	singles := map[int][]fileLinkSpan{}
	for i, line := range lines {
		if spans := findFileLinks(line, cwd); len(spans) > 0 {
			singles[i] = spans
		}
	}
	var multis []fileLinkMultiSpan
	for start := 0; start < len(lines); start++ {
		if strings.TrimSpace(lines[start]) == "" {
			continue
		}
		// Longest chain first: more pieces means a more specific path, and
		// the existence check arbitrates every candidate.
		for length := maxLinkChainLines; length >= 2; length-- {
			end := start + length - 1
			if end >= len(lines) {
				continue
			}
			if badLinkChainGap(lines, start, end) {
				continue
			}
			abs, parts, ok := joinLinkChain(lines, start, end, cwd)
			if !ok {
				continue
			}
			multis = append(multis, fileLinkMultiSpan{abs: abs, parts: parts})
			start = end
			break
		}
	}
	covered := map[int][]fileLinkSpan{}
	for _, multi := range multis {
		for _, part := range multi.parts {
			covered[part.line] = append(covered[part.line], fileLinkSpan{start: part.start, end: part.end})
		}
	}
	for line := range covered {
		kept := singles[line][:0]
		for _, span := range singles[line] {
			drop := false
			for _, cover := range covered[line] {
				if span.start >= cover.start && span.end <= cover.end {
					drop = true
					break
				}
			}
			if !drop {
				kept = append(kept, span)
			}
		}
		if len(kept) == 0 {
			delete(singles, line)
		} else {
			singles[line] = kept
		}
	}
	return singles, multis
}

// badLinkChainGap reports whether rows start..end contain a blank row: a
// paragraph break is never a wrap continuation.
func badLinkChainGap(lines []string, start, end int) bool {
	for _, line := range lines[start : end+1] {
		if strings.TrimSpace(line) == "" {
			return true
		}
	}
	return false
}

// fileLinkPiece is one visible piece of a wrapped link: which rendered row
// it is on plus byte offsets into that row's plain text.
type fileLinkPiece struct {
	line  int
	start int
	end   int
	raw   string
}

// fileLinkMultiSpan is one file path broken across wrapped rows. Every piece
// carries the same URL, so Ctrl+click on any piece opens the whole file.
type fileLinkMultiSpan struct {
	abs   string
	parts []fileLinkPiece
}

// maxLinkChainLines caps how many wrapped rows one path may span. Four covers
// the realistic cases (a ~200-char path at 60 columns); longer chains fall
// back to per-row detection.
const maxLinkChainLines = 4

// linkChainFragment is one row's contribution to a joined path, with byte
// offsets into the row's plain text.
type linkChainFragment struct {
	text       string
	start, end int
}

// maxLinkTailChunks caps how many trailing chunks a tail candidate may span;
// maxLinkHeads caps how many head candidates one row may offer. Together with
// maxLinkRunes they bound the stat probes on hostile input.
const (
	maxLinkTailChunks = 8
	maxLinkHeads      = 8
)

// joinLinkChain tries to join rows start..end (inclusive, consecutive wrapped
// rows) into one existing path. The tail expands left over the first row
// (a hard wrap can sever mid-word, leaving a bare fragment behind), middle
// rows contribute their whole stripped text, and the head must reach the
// last row's start. Separators are enumerated per junction — all-space first
// (word-wrap splits), then direct (hard splits), then mixed — and the first
// existing candidate wins. It returns the absolute path plus one piece per
// row for rendering and click hit-testing.
func joinLinkChain(lines []string, start, end int, cwd string) (string, []fileLinkPiece, bool) {
	if end-start+1 > maxLinkChainLines {
		return "", nil, false
	}
	middle := make([]linkChainFragment, 0, end-start-1)
	for i := start + 1; i < end; i++ {
		frag, ok := linkMiddleFragment(lines[i])
		if !ok {
			return "", nil, false
		}
		middle = append(middle, frag)
	}
	heads := linkHeadFragments(lines[end])
	if len(heads) == 0 {
		return "", nil, false
	}
	tails := linkTailFragments(lines[start])
	if len(tails) == 0 {
		return "", nil, false
	}
	for _, tail := range tails {
		for _, head := range heads {
			frags := append([]linkChainFragment{tail}, middle...)
			frags = append(frags, head)
			texts := make([]string, 0, len(frags))
			for _, frag := range frags {
				texts = append(texts, frag.text)
			}
			for _, seps := range linkSeparatorCombos(len(texts) - 1) {
				candidate := texts[0]
				for j, text := range texts[1:] {
					candidate += seps[j] + text
					if len([]rune(candidate)) > maxLinkRunes {
						break
					}
				}
				if len([]rune(candidate)) > maxLinkRunes {
					continue
				}
				if abs, ok := resolveFileLink(candidate, cwd); ok {
					parts := make([]fileLinkPiece, 0, len(frags))
					for j, frag := range frags {
						parts = append(parts, fileLinkPiece{line: start + j, start: frag.start, end: frag.end, raw: frag.text})
					}
					return abs, parts, true
				}
			}
		}
	}
	return "", nil, false
}

// linkTailFragments lists the start row's tail pieces, longest first: every
// suffix of the row (by chunk starts, plus a boundary-stripped variant of the
// first), because a hard wrap can sever mid-word and leave a bare fragment
// behind. The existence check on the joined candidate arbitrates.
func linkTailFragments(line string) []linkChainFragment {
	trimmed := strings.TrimRight(line, " \t")
	// One trailing punctuation run belongs to prose, not to the path.
	if stripped := strings.TrimRight(trimmed, ".,;:!?)]}>\"'"); stripped != trimmed {
		trimmed = stripped
	}
	if trimmed == "" {
		return nil
	}
	chunks := splitLinkChunks(trimmed)
	if len(chunks) == 0 {
		return nil
	}
	begin := 0
	if len(chunks) > maxLinkTailChunks {
		begin = len(chunks) - maxLinkTailChunks
	}
	var out []linkChainFragment
	seen := map[int]bool{}
	push := func(text string, off int) {
		if text == "" || len([]rune(text)) > maxLinkRunes || seen[off] {
			return
		}
		seen[off] = true
		out = append(out, linkChainFragment{text: text, start: off, end: off + len(text)})
	}
	for i := begin; i < len(chunks); i++ {
		// Candidate text runs to the trimmed line end, preserving the
		// original spacing; offsets stay in original-line coordinates
		// because only trailing characters were removed.
		raw := trimmed[chunks[i].start:]
		core := strings.TrimLeft(raw, " \"'`([{<")
		if core == "" {
			continue
		}
		push(core, chunks[i].start+(len(raw)-len(core)))
	}
	return out
}

// linkMiddleFragment takes a middle row whole (gutter- and whitespace-free):
// wrapped continuation rows carry path text only. A blank row is not a
// middle: badLinkChainGap already refused chains across paragraphs.
func linkMiddleFragment(line string) (linkChainFragment, bool) {
	stripped := stripGutterPrefix(strings.TrimSpace(line))
	if stripped == "" {
		return linkChainFragment{}, false
	}
	start := strings.Index(line, stripped)
	if start < 0 {
		return linkChainFragment{}, false
	}
	if before := line[:start]; strings.Trim(before, " \t\u2502\u2503|") != "" {
		return linkChainFragment{}, false
	}
	return linkChainFragment{text: stripped, start: start, end: start + len(stripped)}, true
}

// linkHeadFragments lists every path head that starts a wrapped row, longest
// first. A row may anchor on more than one chunk — "…/work/Gravação de Tela
// teste.mov" carries both a separator-bearing chunk and a dotted extension —
// and when the row above was cut mid-token only the expansion that swallows
// the whole row completes the path. Every head must start at the row start
// (modulo indentation and gutter); the existence check arbitrates the rest.
func linkHeadFragments(line string) []linkChainFragment {
	stripped := stripGutterPrefix(strings.TrimSpace(line))
	if stripped == "" {
		return nil
	}
	lineOff := strings.Index(line, stripped)
	if lineOff < 0 {
		return nil
	}
	chunks := splitLinkChunks(stripped)
	if len(chunks) == 0 {
		return nil
	}
	var heads []linkChainFragment
	seen := map[string]bool{}
	for i := range chunks {
		anchor := stripLinkPunctuation(chunks[i].text)
		if !looksLikeFilePathToken(anchor) {
			continue
		}
		head, rel, ok := expandLinkHeadLeft(chunks, i)
		if !ok || head == "" || seen[head] {
			continue
		}
		start := lineOff + rel
		// Anything before the head must be filler: whitespace, gutter bars
		// or one opening boundary char.
		if before := line[:start]; strings.Trim(before, " \t\u2502\u2503|([{<\"'") != "" {
			continue
		}
		seen[head] = true
		heads = append(heads, linkChainFragment{text: head, start: start, end: start + len(head)})
	}
	// Longest first: the most complete path gets the first stat probes.
	sort.SliceStable(heads, func(i, j int) bool { return len(heads[i].text) > len(heads[j].text) })
	if len(heads) > maxLinkHeads {
		heads = heads[:maxLinkHeads]
	}
	return heads
}

// expandLinkHeadLeft grows the anchor chunk leftwards over whole chunks,
// mirroring the single-row scanner: a punctuation-only chunk ends the walk,
// and a chunk with stripped leading boundary chars contributes its remainder
// and then ends it. It reports the head text plus its byte offset within the
// stripped row.
func expandLinkHeadLeft(chunks []linkChunk, anchorIndex int) (string, int, bool) {
	raw := chunks[anchorIndex].text
	anchor := stripLinkPunctuation(raw)
	if anchor == "" {
		return "", 0, false
	}
	head := anchor
	// The head starts after any leading boundary run of its own chunk.
	start := chunks[anchorIndex].start
	if lead := len(chunks[anchorIndex].text) - len(strings.TrimLeft(chunks[anchorIndex].text, " \"'`([{<")); lead > 0 {
		start += lead
	}
	for i := anchorIndex - 1; i >= 0; i-- {
		raw := chunks[i].text
		prev := stripLinkPunctuation(raw)
		if prev == "" {
			return "", 0, false
		}
		trimmed := strings.TrimLeft(raw, " \"'`([{<")
		if trimmed != raw {
			// Opening boundary: the head may start right after it, but
			// nothing further left belongs to the path.
			start = chunks[i].start + (len(raw) - len(trimmed))
			head = trimmed + " " + head
			if len([]rune(head)) > maxLinkRunes {
				return "", 0, false
			}
			return head, start, true
		}
		start = chunks[i].start
		head = prev + " " + head
		if len([]rune(head)) > maxLinkRunes {
			return "", 0, false
		}
	}
	return head, start, true
}

// linkSeparatorCombos enumerates join separators for n junctions: the
// all-space join first (word-wrap splits), then the direct join (hard
// splits), then mixed.
func linkSeparatorCombos(n int) [][]string {
	if n <= 0 {
		return [][]string{{}}
	}
	var combos [][]string
	spaces := make([]string, n)
	for i := range spaces {
		spaces[i] = " "
	}
	combos = append(combos, append([]string(nil), spaces...))
	direct := make([]string, n)
	combos = append(combos, direct)
	var rec func(prefix []string, depth int)
	rec = func(prefix []string, depth int) {
		if depth == n {
			allSpace, allDirect := true, true
			for _, sep := range prefix {
				if sep != " " {
					allSpace = false
				} else {
					allDirect = false
				}
			}
			if !allSpace && !allDirect {
				combos = append(combos, append([]string(nil), prefix...))
			}
			return
		}
		for _, sep := range []string{" ", ""} {
			rec(append(prefix, sep), depth+1)
		}
	}
	rec(nil, 0)
	return combos
}

// fileLinkURL builds the file:// target for an absolute path, percent-encoded
// (spaces and accents included) so terminals never truncate the sequence.
func fileLinkURL(absPath string) string {
	return fileURL("", absPath)
}
