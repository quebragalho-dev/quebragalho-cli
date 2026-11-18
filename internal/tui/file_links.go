package tui

import (
	"os"
	"path/filepath"
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
	type chunk struct {
		start int
		end   int
		text  string
	}
	var chunks []chunk
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
		chunks = append(chunks, chunk{start: start, end: index, text: line[start:index]})
	}
	// Try longest-first: prepend previous chunks (up to a cap) and keep the
	// longest candidate that exists. A failed probe never stops the search —
	// only the anchor itself must exist for the span to fall back to it — so
	// "nota: arquivo.txt" still links arquivo.txt while a spaced name links
	// whole. Expansion stops past a chunk with a leading boundary char:
	// prose on the other side of a bracket or quote is never part of the path.
	const maxLinkRunes = 200
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
	var plain []rune
	var byteOffsets []int
	inLink := false
	for index := 0; index < len(styled); {
		if styled[index] == '\x1b' {
			end := ansiSequenceEnd(styled, index)
			if end > index {
				sequence := styled[index:end]
				if strings.HasPrefix(sequence, "\x1b]8;") {
					inLink = sequence != "\x1b]8;;\x1b\\" && sequence != "\x1b]8;;\a"
				}
				index = end
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(styled[index:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		if !inLink {
			plain = append(plain, r)
			byteOffsets = append(byteOffsets, index)
		} else {
			// Inside an existing link: keep the rune out of linkable runs so
			// the visible text is never wrapped a second time. Track it with
			// a sentinel run boundary instead.
			plain = append(plain, '\x00')
			byteOffsets = append(byteOffsets, -1)
		}
		index += size
	}
	type plainRun struct{ start, end int }
	var runs []plainRun
	runStart := -1
	for i, r := range plain {
		if r == '\x00' {
			if runStart >= 0 {
				runs = append(runs, plainRun{runStart, i})
				runStart = -1
			}
			continue
		}
		if runStart < 0 {
			runStart = i
		}
	}
	if runStart >= 0 {
		runs = append(runs, plainRun{runStart, len(plain)})
	}
	type splice struct {
		at  int
		url string
		end bool
	}
	var splices []splice
	for _, run := range runs {
		runText := string(plain[run.start:run.end])
		for _, span := range findFileLinksInRun(runText, cwd) {
			byteStart := byteOffsets[run.start+span.start]
			byteEnd := byteOffsets[run.start+span.end-1]
			// End offset: byte offset just past the last rune of the span.
			lastRuneStart := byteEnd
			_, size := utf8.DecodeRuneInString(styled[lastRuneStart:])
			splices = append(splices,
				splice{at: byteStart, url: fileLinkURL(span.abs)},
				splice{at: lastRuneStart + size, end: true},
			)
		}
	}
	if len(splices) == 0 {
		return styled
	}
	// Splice from the end so earlier offsets stay valid.
	for i, j := 0, len(splices)-1; i < j; i, j = i+1, j-1 {
		splices[i], splices[j] = splices[j], splices[i]
	}
	out := styled
	for _, splice := range splices {
		if splice.end {
			out = out[:splice.at] + "\x1b]8;;\x1b\\" + out[splice.at:]
		} else {
			out = out[:splice.at] + "\x1b]8;;" + splice.url + "\x1b\\" + out[splice.at:]
		}
	}
	return out
}

// findFileLinksInRun runs findFileLinks over one style-uniform run with
// run-relative rune offsets. The caller maps them back to the styled string.
func findFileLinksInRun(runText, cwd string) []fileLinkSpanRune {
	var out []fileLinkSpanRune
	for _, span := range findFileLinks(runText, cwd) {
		start := len([]rune(runText[:span.start]))
		end := len([]rune(runText[:span.end]))
		out = append(out, fileLinkSpanRune{start: start, end: end, raw: span.raw, abs: span.abs})
	}
	return out
}

// fileLinkSpanRune mirrors fileLinkSpan with rune offsets into its run.
type fileLinkSpanRune struct {
	start int
	end   int
	raw   string
	abs   string
}

// linkifyTranscriptLines wraps existing-file paths in every line of a
// rendered multi-line block (assistant answer or tool output). OSC 8 is
// zero-width, so fitted widths are preserved.
func linkifyTranscriptLines(text string, cwd string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = linkifyStyledLine(line, cwd)
	}
	return strings.Join(lines, "\n")
}

// fileLinkURL builds the file:// target for an absolute path, percent-encoded
// (spaces and accents included) so terminals never truncate the sequence.
func fileLinkURL(absPath string) string {
	return fileURL("", absPath)
}
