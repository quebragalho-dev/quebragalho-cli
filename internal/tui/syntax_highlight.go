package tui

import (
	"image/color"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

var (
	lexerCacheMu sync.RWMutex
	lexerCache   = map[string]chroma.Lexer{}

	// extWinners maps an extension to the lexer lexers.Match returns for
	// every basename with that extension unless a competitor pattern
	// matches. It is built once from the registry's "*.<ext>" globs, which
	// is a single pass over a few hundred entries.
	extWinnersOnce sync.Once
	extWinners     map[string]chroma.Lexer
	// extPatterns holds every registry pattern once for competitor checks.
	extPatterns []string

	// extCompetitors memoizes, per extension, the non-"*.<ext>" patterns
	// that could match a basename with that extension. It is filled lazily
	// so a process only pays for the extensions it actually sees.
	extCompetitorsMu sync.RWMutex
	extCompetitors   = map[string][]string{}
)

// cachedLexer resolves a language name to a chroma lexer once, memoizing the
// result — including a nil "no lexer" result — so chroma's per-language registry
// Match scan (which its own docs call slow) runs at most once per language. An
// empty language short-circuits before any lookup, which is the common case for
// a bare ``` fence on every streaming frame.
func cachedLexer(lang string) chroma.Lexer {
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang == "" {
		return nil
	}
	lexerCacheMu.RLock()
	lexer, ok := lexerCache[lang]
	lexerCacheMu.RUnlock()
	if ok {
		return lexer
	}
	lexer = lexers.Get(lang)
	lexerCacheMu.Lock()
	lexerCache[lang] = lexer
	lexerCacheMu.Unlock()
	return lexer
}

func cachedLexerForPath(path string) chroma.Lexer {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	base := filepath.Base(path)
	if entry, ok := extLexerForBase(base); ok {
		return entry
	}
	key := "path:" + base
	lexerCacheMu.RLock()
	lexer, ok := lexerCache[key]
	lexerCacheMu.RUnlock()
	if ok {
		return lexer
	}
	lexer = lexers.Match(path)
	lexerCacheMu.Lock()
	lexerCache[key] = lexer
	lexerCacheMu.Unlock()
	return lexer
}

// extLexerForBase resolves a basename through the extension index. ok is false
// when the extension is not indexed or a competitor pattern matches, and the
// caller must fall back to lexers.Match.
func extLexerForBase(base string) (chroma.Lexer, bool) {
	extWinnersOnce.Do(buildExtWinners)
	ext, ok := baseExtension(base)
	if !ok {
		return nil, false
	}
	winner, ok := extWinners[ext]
	if !ok || winner == nil {
		return nil, false
	}
	extCompetitorsMu.RLock()
	competitors, ok := extCompetitors[ext]
	extCompetitorsMu.RUnlock()
	if !ok {
		competitors = extCompetitorPatterns(ext)
		extCompetitorsMu.Lock()
		extCompetitors[ext] = competitors
		extCompetitorsMu.Unlock()
	}
	for _, glob := range competitors {
		matched, err := filepath.Match(glob, base)
		if err != nil || matched {
			return nil, false
		}
	}
	return winner, true
}

// baseExtension returns the text after the last dot of a basename. A leading
// dot is not an extension (".bashrc"), and the match is case-sensitive like
// filepath.Match itself: "foo.C" (C++) and "foo.c" (C) differ.
func baseExtension(base string) (string, bool) {
	dot := strings.LastIndexByte(base, '.')
	if dot <= 0 || dot == len(base)-1 {
		return "", false
	}
	return base[dot+1:], true
}

// chromaIgnoredSuffixes mirrors chroma's LexerRegistry.Match: a glob that
// fails directly is retried with each of these suffixes appended.
var chromaIgnoredSuffixes = []string{
	"~", ".bak", ".old", ".orig",
	".dpkg-dist", ".dpkg-old", ".ucf-dist", ".ucf-new", ".ucf-old",
	".rpmnew", ".rpmorig", ".rpmsave",
	".in",
}

func buildExtWinners() {
	registry := lexers.GlobalLexerRegistry
	claimants := map[string][]chroma.Lexer{}
	for _, lexer := range registry.Lexers {
		for _, glob := range lexer.Config().Filenames {
			rest, ok := simpleExtensionGlob(glob)
			if !ok {
				continue
			}
			claimants[rest] = append(claimants[rest], lexer)
		}
	}
	// Every primary pattern, alias pattern, and ignored-suffix expansion that
	// could match a basename with an indexed extension. The per-file check is
	// exact, so over-approximating here only costs a few extra failed matches.
	var patterns []string
	for _, lexer := range registry.Lexers {
		config := lexer.Config()
		patterns = append(patterns, config.Filenames...)
		patterns = append(patterns, config.AliasFilenames...)
		for _, glob := range config.Filenames {
			for _, suffix := range chromaIgnoredSuffixes {
				patterns = append(patterns, glob+suffix)
			}
		}
	}
	extPatterns = patterns
	winners := make(map[string]chroma.Lexer, len(claimants))
	for ext, lexers := range claimants {
		sorted := append([]chroma.Lexer(nil), lexers...)
		sort.Sort(chroma.PrioritisedLexers(sorted))
		winners[ext] = sorted[0]
	}
	extWinners = winners
}

// extCompetitorPatterns returns the deduplicated non-"*.<ext>" registry
// patterns that could match a basename with the given extension. When none of
// them matches a basename, lexers.Match sees exactly the "*.<ext>" claimants,
// so the precomputed winner is what Match would return.
func extCompetitorPatterns(ext string) []string {
	seen := map[string]bool{}
	var competitors []string
	for _, pattern := range extPatterns {
		if pattern == "*."+ext || seen[pattern] {
			continue
		}
		seen[pattern] = true
		if globCanMatchExtension(pattern, ext) {
			competitors = append(competitors, pattern)
		}
	}
	sort.Strings(competitors)
	return competitors
}

// simpleExtensionGlob reports whether glob has the exact form "*.<ext>" where
// ext carries no wildcard and no separator: "*.go" qualifies, and so does
// "*.go.tmpl" (under "go.tmpl"); "*.php[345]", "Dockerfile.*", and exact names
// like "Makefile" do not.
func simpleExtensionGlob(glob string) (string, bool) {
	if !strings.HasPrefix(glob, "*.") {
		return "", false
	}
	rest := glob[2:]
	if rest == "" || strings.ContainsRune(rest, '/') || globHasWildcard(rest) {
		return "", false
	}
	return rest, true
}

// globHasWildcard reports whether s contains an unescaped wildcard construct.
// On any parse doubt it returns true: callers treat that as "could match".
func globHasWildcard(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '*', '?':
			return true
		case '[':
			return true
		}
	}
	return false
}

// globCanMatchExtension is a sound over-approximation of "glob can match some
// basename whose extension is ext". False means impossible, so the glob is
// skipped; true only adds an exact per-file check.
func globCanMatchExtension(glob, ext string) bool {
	if strings.ContainsRune(glob, '/') {
		return false
	}
	if strings.ContainsRune(glob, '\\') {
		return true
	}
	end, found, ok := lastWildcardEnd(glob)
	if !ok {
		return true
	}
	if !found {
		dot := strings.LastIndexByte(glob, '.')
		if dot <= 0 {
			return false
		}
		return glob[dot+1:] == ext
	}
	tail := glob[end:]
	if dot := strings.LastIndexByte(tail, '.'); dot >= 0 {
		return tail[dot+1:] == ext
	}
	// The literal tail has no dot, so every match ends with it; it must be
	// suffix-compatible with ".<ext>".
	tail, dotted := tail, "."+ext
	return strings.HasSuffix(tail, dotted) || strings.HasSuffix(dotted, tail)
}

// lastWildcardEnd returns the offset just past the last wildcard construct in
// glob. found is false when glob has no wildcard; ok is false on parse doubt
// (unterminated class), which callers treat as "could match".
func lastWildcardEnd(glob string) (end int, found, ok bool) {
	for i := 0; i < len(glob); i++ {
		switch glob[i] {
		case '\\':
			i++
		case '*', '?':
			end, found = i+1, true
		case '[':
			j := i + 1
			if j < len(glob) && glob[j] == ']' {
				j++
			}
			for j < len(glob) && glob[j] != ']' {
				if glob[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(glob) {
				return 0, true, false
			}
			end, found = j+1, true
			i = j
		}
	}
	return end, found, true
}

// tokenStyle maps a chroma token type onto QG's existing, contrast-audited
// palette rather than a chroma color scheme — so highlighted code stays on-brand
// and degrades through the same lipgloss profile path as the rest of the UI
// (truecolor → 256 → 16 → plain on no-TTY).
type codeSyntaxStyle struct {
	foreground string
	bold       bool
	italic     bool
}

type codeSyntaxTheme struct {
	text         codeSyntaxStyle
	keyword      codeSyntaxStyle
	typeName     codeSyntaxStyle
	function     codeSyntaxStyle
	name         codeSyntaxStyle
	string       codeSyntaxStyle
	number       codeSyntaxStyle
	comment      codeSyntaxStyle
	preprocessor codeSyntaxStyle
	operator     codeSyntaxStyle
	punctuation  codeSyntaxStyle
}

func codeStyle(foreground string) codeSyntaxStyle { return codeSyntaxStyle{foreground: foreground} }

func boldCodeStyle(foreground string) codeSyntaxStyle {
	return codeSyntaxStyle{foreground: foreground, bold: true}
}

func italicCodeStyle(foreground string) codeSyntaxStyle {
	return codeSyntaxStyle{foreground: foreground, italic: true}
}

func codeThemeForMode(mode themeMode) *codeSyntaxTheme {
	theme, ok := codeThemes[mode]
	if !ok {
		return nil
	}
	return &theme
}

func (theme codeSyntaxTheme) styleFor(tt chroma.TokenType) (codeSyntaxStyle, bool) {
	var style codeSyntaxStyle
	switch {
	case tt == chroma.CommentPreproc:
		style = theme.preprocessor
	case tt.InCategory(chroma.Comment):
		style = theme.comment
	case tt == chroma.KeywordType:
		style = theme.typeName
	case tt.InCategory(chroma.Keyword):
		style = theme.keyword
	case tt == chroma.NameFunction:
		style = theme.function
	case tt == chroma.NameClass || tt == chroma.NameBuiltin || tt == chroma.NameNamespace || tt == chroma.NameDecorator || tt == chroma.NameAttribute || tt == chroma.NameTag:
		style = theme.name
	case tt.InSubCategory(chroma.LiteralString):
		style = theme.string
	case tt.InSubCategory(chroma.LiteralNumber):
		style = theme.number
	case tt.InCategory(chroma.Operator):
		style = theme.operator
	case tt.InCategory(chroma.Punctuation):
		style = theme.punctuation
	default:
		style = theme.text
	}
	return style, style.foreground != ""
}

// tokenStyle maps a lexical token through the active palette's complete syntax
// style when one is available. Palette-local fallbacks keep System, inverted, and
// unmatched themes readable without forcing an unrelated code scheme on them.
func tokenStyle(tt chroma.TokenType) lipgloss.Style {
	return tokenStyleForTheme(qgTheme, tt)
}

func tokenStyleForTheme(theme tuiTheme, tt chroma.TokenType) lipgloss.Style {
	if syntax := theme.codeTheme; syntax != nil {
		if entry, ok := syntax.styleFor(tt); ok {
			return codeSyntaxLipglossStyle(entry)
		}
	}
	return fallbackTokenStyle(theme, tt)
}

func codeSyntaxLipglossStyle(entry codeSyntaxStyle) lipgloss.Style {
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(entry.foreground))
	if entry.bold {
		style = style.Bold(true)
	}
	if entry.italic {
		style = style.Italic(true)
	}
	return style
}

// inlineCodeStyle gives short code spans the same palette-aware treatment as
// code blocks without turning ordinary assistant prose into a code block.
func inlineCodeStyle() lipgloss.Style {
	if syntax := qgTheme.codeTheme; syntax != nil && syntax.function.foreground != "" {
		return codeSyntaxLipglossStyle(syntax.function)
	}
	return qgTheme.blue
}

// fallbackTokenStyle is the contrast-audited semantic mapping used by System,
// inverted palettes, and palettes without a corresponding bundled syntax style.
func fallbackTokenStyle(theme tuiTheme, tt chroma.TokenType) lipgloss.Style {
	switch {
	case tt.InCategory(chroma.Keyword):
		return theme.accent
	case tt.InCategory(chroma.Comment):
		return theme.faint
	case tt.InSubCategory(chroma.LiteralString):
		return theme.green
	case tt.InSubCategory(chroma.LiteralNumber):
		return theme.amber
	case tt == chroma.NameFunction || tt == chroma.NameClass || tt == chroma.NameBuiltin || tt == chroma.NameNamespace || tt == chroma.NameDecorator:
		return theme.blue
	case tt.InCategory(chroma.Operator), tt.InCategory(chroma.Punctuation):
		return theme.muted
	default:
		return theme.ink
	}
}

// highlightCode syntax-highlights a fenced code block, wrapping at measure cells
// while carrying each token's color across wrap boundaries. ok is false when no
// lexer matches the language (the caller then renders the block plain), so an
// unknown or missing language is never worse than today. Wrapping is done at the
// token level so colors never split an ANSI escape.
func highlightCode(code []string, lang string, measure int) ([]string, bool) {
	return highlightCodeWithLexer(cachedLexer(lang), code, measure, nil)
}

func highlightCodeAuto(code []string, lang string, measure int) ([]string, bool) {
	if strings.TrimSpace(lang) == "" {
		lang = inferCodeLanguage(code)
	}
	return highlightCode(code, lang, measure)
}

func highlightCodeForPath(code []string, path string, measure int, bg color.Color) ([]string, bool) {
	return highlightCodeWithLexer(cachedLexerForPath(path), code, measure, bg)
}

// highlightShellCommand styles a one-line command for a tool-card heading.
// Command output is deliberately left untouched: it may be structured data,
// prose, or terminal control text rather than shell source.
func highlightShellCommand(command string) (string, bool) {
	if command == "" {
		return "", false
	}
	lexer := cachedLexer("bash")
	if lexer == nil {
		return "", false
	}
	iterator, err := lexer.Tokenise(nil, command)
	if err != nil {
		return "", false
	}
	var builder strings.Builder
	expectingCommand := true
	for _, token := range iterator.Tokens() {
		if token.Type == chroma.Text || token.Type == chroma.TextWhitespace {
			builder.WriteString(highlightShellText(token.Value, &expectingCommand))
			continue
		}
		builder.WriteString(tokenStyle(token.Type).Render(token.Value))
		if token.Type.InCategory(chroma.Operator) || (token.Type.InCategory(chroma.Punctuation) && strings.ContainsAny(token.Value, ";|")) {
			expectingCommand = true
		}
	}
	return builder.String(), true
}

func highlightShellText(text string, expectingCommand *bool) string {
	var builder strings.Builder
	for start := 0; start < len(text); {
		end := start
		space := text[start] == ' ' || text[start] == '\t'
		for end < len(text) {
			isSpace := text[end] == ' ' || text[end] == '\t'
			if isSpace != space {
				break
			}
			end++
		}
		part := text[start:end]
		if space {
			builder.WriteString(part)
			start = end
			continue
		}

		clean := strings.Trim(part, "\"'")
		style := tokenStyle(chroma.Text)
		switch {
		case isShellControlOperator(clean):
			style = tokenStyle(chroma.Operator)
			*expectingCommand = true
		case *expectingCommand:
			style = tokenStyle(chroma.NameFunction)
			*expectingCommand = isShellCommandWrapper(clean)
		case strings.HasPrefix(clean, "-"):
			style = tokenStyle(chroma.NameAttribute)
		case isShellNumber(clean):
			style = tokenStyle(chroma.LiteralNumber)
		case looksLikeShellPath(clean):
			style = tokenStyle(chroma.LiteralString)
		}
		builder.WriteString(style.Render(part))
		start = end
	}
	return builder.String()
}

func isShellControlOperator(value string) bool {
	switch value {
	case "&&", "||", "|", ";":
		return true
	default:
		return false
	}
}

func isShellCommandWrapper(value string) bool {
	switch value {
	case "sudo", "env", "command", "time", "xargs":
		return true
	default:
		return false
	}
}

func isShellNumber(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func looksLikeShellPath(value string) bool {
	return strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~/") || strings.Contains(value, "/")
}

// highlightSpan overlays a background on a rune range in one source line while
// retaining the lexer-selected foreground. Diff rendering uses it to keep the
// precise changed word visible inside a syntax-highlighted add/delete row.
type highlightSpan struct {
	line       int
	start, end int
	background color.Color
}

func highlightCodeForPathWithSpans(code []string, path string, measure int, bg color.Color, spans []highlightSpan) ([]string, bool) {
	return highlightCodeWithLexerAndSpans(cachedLexerForPath(path), code, measure, bg, spans)
}

// highlightCodeForPathWithLineBackgrounds applies syntax highlighting to a
// single source unit while preserving a distinct background for every source
// line. The diff viewer uses one lexer pass per hunk so multiline syntax keeps
// its state across changed and unchanged lines.
func highlightCodeForPathWithLineBackgrounds(code []string, path string, measure int, backgrounds []color.Color, spans []highlightSpan) ([]string, bool) {
	return highlightCodeWithLexerAndLineBackgrounds(cachedLexerForPath(path), code, measure, backgrounds, spans)
}

func inferCodeLanguage(code []string) string {
	for _, line := range code {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "from ") && strings.Contains(trimmed, " import "):
			return "python"
		case strings.HasPrefix(trimmed, "import ") && !strings.Contains(trimmed, " from "):
			return "python"
		case strings.HasPrefix(trimmed, "def ") && strings.HasSuffix(trimmed, ":"):
			return "python"
		case strings.HasPrefix(trimmed, "class ") && strings.HasSuffix(trimmed, ":"):
			return "python"
		case strings.HasPrefix(trimmed, "if ") && strings.HasSuffix(trimmed, ":"):
			return "python"
		case strings.HasPrefix(trimmed, "elif ") && strings.HasSuffix(trimmed, ":"):
			return "python"
		case trimmed == "else:" || trimmed == "try:" || trimmed == "finally:":
			return "python"
		case strings.HasPrefix(trimmed, "for ") && strings.HasSuffix(trimmed, ":"):
			return "python"
		case strings.HasPrefix(trimmed, "while ") && strings.HasSuffix(trimmed, ":"):
			return "python"
		case strings.HasPrefix(trimmed, "with ") && strings.HasSuffix(trimmed, ":"):
			return "python"
		case strings.HasPrefix(trimmed, "except") && strings.HasSuffix(trimmed, ":"):
			return "python"
		case strings.HasPrefix(trimmed, "return "):
			return "python"
		case strings.HasPrefix(trimmed, "print("):
			return "python"
		case strings.HasPrefix(trimmed, "package "):
			return "go"
		case strings.HasPrefix(trimmed, "func ") && strings.Contains(trimmed, "{"):
			return "go"
		case strings.HasPrefix(trimmed, "const "), strings.HasPrefix(trimmed, "let "), strings.HasPrefix(trimmed, "var "):
			return "javascript"
		case strings.HasPrefix(trimmed, "function ") && strings.Contains(trimmed, "{"):
			return "javascript"
		case strings.HasPrefix(trimmed, "<!DOCTYPE "), strings.HasPrefix(trimmed, "<html"), strings.HasPrefix(trimmed, "<div"), strings.HasPrefix(trimmed, "<span"):
			return "html"
		}
		break
	}
	return ""
}

func highlightCodeWithLexer(lexer chroma.Lexer, code []string, measure int, bg color.Color) ([]string, bool) {
	return highlightCodeWithLexerAndSpans(lexer, code, measure, bg, nil)
}

func highlightCodeWithLexerAndSpans(lexer chroma.Lexer, code []string, measure int, bg color.Color, spans []highlightSpan) ([]string, bool) {
	backgrounds := make([]color.Color, len(code))
	for index := range backgrounds {
		backgrounds[index] = bg
	}
	return highlightCodeWithLexerAndLineBackgrounds(lexer, code, measure, backgrounds, spans)
}

func highlightCodeWithLexerAndLineBackgrounds(lexer chroma.Lexer, code []string, measure int, backgrounds []color.Color, spans []highlightSpan) ([]string, bool) {
	if measure < 4 {
		return nil, false
	}
	if lexer == nil {
		return nil, false
	}
	iterator, err := lexer.Tokenise(nil, strings.Join(code, "\n"))
	if err != nil {
		return nil, false
	}

	lines := []string{}
	spansByLine := make(map[int][]highlightSpan, len(spans))
	for _, span := range spans {
		spansByLine[span.line] = append(spansByLine[span.line], span)
	}
	var cur strings.Builder
	curWidth := 0
	flushLine := func() {
		lines = append(lines, cur.String())
		cur.Reset()
		curWidth = 0
	}
	emit := func(style lipgloss.Style, s string, line, column int) {
		var chunk strings.Builder
		var chunkStyle lipgloss.Style
		var chunkBackground color.Color
		var haveChunkStyle bool
		var styledBackground color.Color
		var styleWithBackground lipgloss.Style
		var haveStyledBackground bool
		flushChunk := func() {
			if chunk.Len() > 0 {
				cur.WriteString(chunkStyle.Render(chunk.String()))
				chunk.Reset()
				haveChunkStyle = false
				chunkBackground = nil
			}
		}
		for offset, r := range []rune(s) {
			var background color.Color
			if line < len(backgrounds) {
				background = backgrounds[line]
			}
			for _, span := range spansByLine[line] {
				if column+offset >= span.start && column+offset < span.end {
					background = span.background
					break
				}
			}
			if !haveStyledBackground || !sameHighlightColor(background, styledBackground) {
				styleWithBackground = style
				if background != nil {
					styleWithBackground = styleWithBackground.Background(background)
				}
				styledBackground = background
				haveStyledBackground = true
			}
			runeStyle := styleWithBackground
			if !haveChunkStyle {
				chunkStyle = runeStyle
				chunkBackground = background
				haveChunkStyle = true
			} else if !sameHighlightColor(background, chunkBackground) {
				flushChunk()
				chunkStyle = runeStyle
				chunkBackground = background
				haveChunkStyle = true
			}
			rw := lipgloss.Width(string(r))
			if curWidth+rw > measure {
				flushChunk()
				flushLine()
			}
			chunk.WriteString(string(r))
			curWidth += rw
		}
		flushChunk()
	}

	line, column := 0, 0
	for _, token := range iterator.Tokens() {
		style := tokenStyle(token.Type)
		for index, part := range strings.Split(token.Value, "\n") {
			if index > 0 {
				flushLine()
				line++
				column = 0
			}
			emit(style, part, line, column)
			column += len([]rune(part))
		}
	}
	flushLine()
	// chroma emits a trailing newline -> a final empty line; drop it.
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines, true
}

func sameHighlightColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
