package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fileLinkTestDir creates a temp dir with the given files (parents created)
// and returns it. Names may contain spaces and non-ASCII text.
func fileLinkTestDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestResolveFileLinkAbsoluteExists(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	abs, ok := resolveFileLink(filepath.Join(dir, "a.png"), t.TempDir())
	if !ok {
		t.Fatal("existing absolute path did not resolve")
	}
	if abs != filepath.Join(dir, "a.png") {
		t.Fatalf("abs = %q", abs)
	}
}

func TestResolveFileLinkMissingIsNotALink(t *testing.T) {
	dir := t.TempDir()
	if _, ok := resolveFileLink(filepath.Join(dir, "nope.png"), dir); ok {
		t.Fatal("missing path must not resolve to a link")
	}
}

func TestResolveFileLinkRelativeToCWD(t *testing.T) {
	dir := fileLinkTestDir(t, "sub/pic.gif")
	abs, ok := resolveFileLink(filepath.Join("sub", "pic.gif"), dir)
	if !ok {
		t.Fatal("cwd-relative path did not resolve")
	}
	if abs != filepath.Join(dir, "sub", "pic.gif") {
		t.Fatalf("abs = %q", abs)
	}
}

func TestResolveFileLinkHomeExpansion(t *testing.T) {
	home := fileLinkTestDir(t, "shots/cap.mov")
	restore := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	defer func() { userHomeDir = restore }()

	abs, ok := resolveFileLink(filepath.Join("~", "shots", "cap.mov"), t.TempDir())
	if !ok {
		t.Fatal("~/ path did not resolve against the home directory")
	}
	if abs != filepath.Join(home, "shots", "cap.mov") {
		t.Fatalf("abs = %q", abs)
	}
}

func TestResolveFileLinkSpacesAndAccents(t *testing.T) {
	name := "Gravação de Tela 2026-11-18 às 17.00.29.mov"
	dir := fileLinkTestDir(t, name)
	abs, ok := resolveFileLink(name, dir)
	if !ok {
		t.Fatalf("name with spaces and accents did not resolve: %q", name)
	}
	if abs != filepath.Join(dir, name) {
		t.Fatalf("abs = %q", abs)
	}
}

func TestFindFileLinksIgnoresInventedURLs(t *testing.T) {
	dir := t.TempDir()
	for _, line := range []string{
		"see https://example.com/a.png for details",
		"open http://localhost:3000/x.png now",
		"file report.txt is missing",
		"see [the docs](https://example.com/a.png) instead",
	} {
		if spans := findFileLinks(line, dir); len(spans) != 0 {
			t.Fatalf("line %q must not link, got %#v", line, spans)
		}
	}
}

func TestFindFileLinksMarkdownLinkTarget(t *testing.T) {
	dir := fileLinkTestDir(t, "img/photo 1.png")
	line := "see [the photo](img/photo 1.png) above"
	spans := findFileLinks(line, dir)
	if len(spans) != 1 {
		t.Fatalf("markdown link spans = %#v, want one", spans)
	}
	if spans[0].abs != filepath.Join(dir, "img", "photo 1.png") {
		t.Fatalf("abs = %q", spans[0].abs)
	}
	if !strings.Contains(line[spans[0].start:spans[0].end], "img/photo 1.png") {
		t.Fatalf("span %v does not cover the target in %q", spans[0], line)
	}
}

func TestFindFileLinksBacktickAndQuotedSpans(t *testing.T) {
	dir := fileLinkTestDir(t, "a b/c.txt")
	for _, line := range []string{
		"run `a b/c.txt` next",
		`open "a b/c.txt" now`,
	} {
		spans := findFileLinks(line, dir)
		if len(spans) != 1 {
			t.Fatalf("line %q spans = %#v, want one", line, spans)
		}
		if spans[0].abs != filepath.Join(dir, "a b", "c.txt") {
			t.Fatalf("line %q abs = %q", line, spans[0].abs)
		}
	}
}

func TestFindFileLinksBareNameWithSpaces(t *testing.T) {
	name := "Gravação de Tela 2026-11-18 às 17.00.29.mov"
	dir := fileLinkTestDir(t, name)
	line := "assista " + name + " quando puder"
	spans := findFileLinks(line, dir)
	if len(spans) != 1 {
		t.Fatalf("spaced name spans = %#v, want one", spans)
	}
	if got := line[spans[0].start:spans[0].end]; got != name {
		t.Fatalf("span text = %q, want %q", got, name)
	}
	if spans[0].abs != filepath.Join(dir, name) {
		t.Fatalf("abs = %q", spans[0].abs)
	}
}

func TestFindFileLinksFallsBackToAnchor(t *testing.T) {
	dir := fileLinkTestDir(t, "real.txt")
	line := "nota: real.txt, ok?"
	spans := findFileLinks(line, dir)
	if len(spans) != 1 {
		t.Fatalf("spans = %#v, want one", spans)
	}
	if got := line[spans[0].start:spans[0].end]; got != "real.txt" {
		t.Fatalf("span text = %q, want the anchor without prose", got)
	}
}

func TestFileLinkURLEncoding(t *testing.T) {
	url := fileLinkURL("/tmp/Gravação de Tela/às 17.png")
	if strings.ContainsAny(url, " \n\r") {
		t.Fatalf("file URL must not contain raw spaces or newlines: %q", url)
	}
	if !strings.HasPrefix(url, "file://") {
		t.Fatalf("file URL must use the file scheme: %q", url)
	}
	if !strings.Contains(url, "%20") || !strings.Contains(url, "%C3%A3") {
		t.Fatalf("file URL must percent-encode spaces and ã: %q", url)
	}
}

func TestLinkifyStyledLineSkipsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	styled := "see " + filepath.Join(dir, "ghost.png") + " here"
	if got := linkifyStyledLine(styled, dir); got != styled {
		t.Fatalf("missing file must stay unlinked, got %q", got)
	}
}

func TestLinkifyStyledLineEmitsWellFormedOSC8(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	styled := "see a.png here"
	got := linkifyStyledLine(styled, dir)
	open := "\x1b]8;;" + fileLinkURL(filepath.Join(dir, "a.png"))
	if count := strings.Count(got, open); count != 1 {
		t.Fatalf("OSC 8 opener = %d occurrences in %q", count, got)
	}
	if !strings.HasSuffix(got, "\x1b]8;;\x1b\\") && !strings.Contains(got, "\x1b]8;;\x1b\\") {
		t.Fatalf("OSC 8 terminator missing in %q", got)
	}
}

func TestLinkifyStyledLineSkipsNestedLinks(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	inner := hyperlink(fileLinkURL(filepath.Join(dir, "a.png")), "a.png")
	got := linkifyStyledLine("see "+inner+" here", dir)
	if strings.Count(got, "\x1b]8;;") != 2 {
		t.Fatalf("already-linked text must not be wrapped again: %q", got)
	}
}

func TestFindFileLinksTrimsTrailingPunctuation(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	for _, line := range []string{
		"veja a.png.",
		"veja a.png, ok?",
		"(a.png)",
		"[a.png]",
	} {
		spans := findFileLinks(line, dir)
		if len(spans) != 1 {
			t.Fatalf("line %q spans = %#v, want one", line, spans)
		}
		if got := line[spans[0].start:spans[0].end]; got != "a.png" {
			t.Fatalf("line %q span = %q, want the bare name", line, got)
		}
	}
}

func TestFindFileLinksQuotedNameWithoutExtension(t *testing.T) {
	dir := fileLinkTestDir(t, "README")
	spans := findFileLinks(`leia o "README" com calma`, dir)
	if len(spans) != 1 {
		t.Fatalf("spans = %#v, want one", spans)
	}
	if spans[0].abs != filepath.Join(dir, "README") {
		t.Fatalf("abs = %q", spans[0].abs)
	}
}

func TestFindFileLinksDirectory(t *testing.T) {
	dir := fileLinkTestDir(t, "shots/a.png")
	spans := findFileLinks("abra shots/ para ver", dir)
	if len(spans) != 1 {
		t.Fatalf("spans = %#v, want the directory", spans)
	}
	if spans[0].abs != filepath.Join(dir, "shots") {
		t.Fatalf("abs = %q", spans[0].abs)
	}
}

func TestLinkifyStyledLineKeepsSurroundingStyle(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	styled := "\x1b[1msee a.png now\x1b[0m"
	got := linkifyStyledLine(styled, dir)
	if !strings.Contains(got, "\x1b]8;;") {
		t.Fatalf("link missing in %q", got)
	}
	if !strings.HasPrefix(got, "\x1b[1msee ") {
		t.Fatalf("leading style/text disturbed: %q", got)
	}
}

func TestFileLinkCacheExpires(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "novo.png")
	restore := fileLinkCacheNow
	base := fileLinkCacheNow()
	fileLinkCacheNow = func() time.Time { return base }
	defer func() { fileLinkCacheNow = restore }()

	if _, ok := resolveFileLink(path, dir); ok {
		t.Fatal("arquivo ainda não existe")
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, ok := resolveFileLink(path, dir); ok {
		t.Fatal("cache ainda válido não deve ver o arquivo novo")
	}
	fileLinkCacheNow = func() time.Time { return base.Add(fileLinkCacheTTL + time.Second) }
	abs, ok := resolveFileLink(path, dir)
	if !ok || abs != path {
		t.Fatalf("após expirar, resolve = %q, %v", abs, ok)
	}
}

func TestPlanFileLinksJoinsTwoLineWrap(t *testing.T) {
	dir := fileLinkTestDir(t, filepath.Join("e2e-links", "work", "Gravação de Tela teste.mov"))
	plain := []string{
		"veja " + filepath.Join(dir, "e2e-links", "work", "Gravação"),
		"de Tela teste.mov aqui",
	}
	singles, multis := planFileLinks(plain, dir)
	if len(multis) != 1 {
		t.Fatalf("multis = %#v, want one joined span", multis)
	}
	multi := multis[0]
	if multi.abs != filepath.Join(dir, "e2e-links", "work", "Gravação de Tela teste.mov") {
		t.Fatalf("joined abs = %q", multi.abs)
	}
	if len(multi.parts) != 2 || multi.parts[0].line != 0 || multi.parts[1].line != 1 {
		t.Fatalf("parts = %#v, want one piece per row", multi.parts)
	}
	if len(singles) != 0 {
		t.Fatalf("singles = %#v, want none (pieces alone do not exist)", singles)
	}
}

func TestPlanFileLinksJoinsThreeLineWrap(t *testing.T) {
	dir := fileLinkTestDir(t, filepath.Join("e2e-links", "work", "Gravação de Tela teste.mov"))
	plain := []string{
		"arquivo " + filepath.Join(dir, "e2e-links", "wo"),
		"rk/Gravação de",
		"Tela teste.mov ok",
	}
	_, multis := planFileLinks(plain, dir)
	if len(multis) != 1 {
		t.Fatalf("multis = %#v, want one three-piece span", multis)
	}
	if got := multis[0].abs; got != filepath.Join(dir, "e2e-links", "work", "Gravação de Tela teste.mov") {
		t.Fatalf("joined abs = %q", got)
	}
	if len(multis[0].parts) != 3 {
		t.Fatalf("parts = %#v, want three", multis[0].parts)
	}
}

func TestPlanFileLinksNoJoinAcrossBlankLine(t *testing.T) {
	dir := fileLinkTestDir(t, filepath.Join("w", "Gravação de Tela teste.mov"))
	plain := []string{
		"veja " + filepath.Join(dir, "w", "Gravação"),
		"",
		"de Tela teste.mov aqui",
	}
	_, multis := planFileLinks(plain, dir)
	if len(multis) != 0 {
		t.Fatalf("multis = %#v, want none across a paragraph break", multis)
	}
}

func TestPlanFileLinksMissingStaysUnlinked(t *testing.T) {
	dir := t.TempDir()
	plain := []string{
		"veja " + filepath.Join(dir, "que", "não", "existe", "a.png"),
		"cortado",
	}
	singles, multis := planFileLinks(plain, dir)
	if len(singles) != 0 || len(multis) != 0 {
		t.Fatalf("singles = %#v multis = %#v, want none for a missing path", singles, multis)
	}
}

func TestLinkifyTranscriptLinesLinksEveryPiece(t *testing.T) {
	dir := fileLinkTestDir(t, filepath.Join("e2e-links", "work", "Gravação de Tela teste.mov"))
	tail := filepath.Join("e2e-links", "work", "Gravação")
	block := "veja " + filepath.Join(dir, tail) + "\nde Tela teste.mov aqui"
	got := linkifyTranscriptLines(block, dir)
	url := fileLinkURL(filepath.Join(dir, "e2e-links", "work", "Gravação de Tela teste.mov"))
	if count := strings.Count(got, "\x1b]8;;"+url+"\x1b\\"); count != 2 {
		t.Fatalf("both pieces must carry the same URL, got %d opens in %q", count, got)
	}
}

func TestFileLinkClickJoinedOpensWholeFile(t *testing.T) {
	dir := fileLinkTestDir(t, filepath.Join("e2e-links", "work", "Gravação de Tela teste.mov"))
	plain := []string{
		"veja " + filepath.Join(dir, "e2e-links", "work", "Gravação"),
		"de Tela teste.mov aqui",
	}
	rows := []transcriptSelectableLine{
		{bodyY: 10, text: plain[0]},
		{bodyY: 11, text: plain[1]},
	}
	var opened []string
	restore := fileLinkOpener
	fileLinkOpener = func(path string) error {
		opened = append(opened, path)
		return nil
	}
	defer func() { fileLinkOpener = restore }()

	// Click inside the second piece ("de Tela..."): the whole joined path
	// must open.
	col := strings.Index(rows[1].text, "Tela") + 1
	handled, err := handleFileLinkClickWindow(rows, 1, col, dir, tea.ModCtrl)
	if err != nil {
		t.Fatalf("opener error: %v", err)
	}
	if !handled {
		t.Fatal("click on a wrapped piece must be handled")
	}
	want := filepath.Join(dir, "e2e-links", "work", "Gravação de Tela teste.mov")
	if len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened = %#v, want [%q]", opened, want)
	}
}

// hardSplitLinkPath creates a real file of wantRunes characters: one long
// space-free directory run plus a basename with spaces, so the answer wrap has
// to sever the directory run mid-word — the shape a session scratchpad path
// takes.
func hardSplitLinkPath(t *testing.T, root string, wantRunes int) string {
	t.Helper()
	tail := filepath.Join("e2e-links", "work", "Gravação de Tela teste.mov")
	pad := wantRunes - len([]rune(filepath.Join(root, tail)))
	if pad < 24 {
		pad = 24
	}
	path := filepath.Join(root, strings.Repeat("a", pad), tail)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// TestAssistantWrapLinksHardSplitPath covers a backticked absolute path that the
// answer wrap splits mid-token: every wrapped piece must carry the OSC 8 target
// of the whole path, and Ctrl+click on either piece must open it.
func TestAssistantWrapLinksHardSplitPath(t *testing.T) {
	root := t.TempDir()
	path := hardSplitLinkPath(t, root, 160)
	// A cwd with nothing in it: the second row's text alone must not resolve as
	// a relative path, so only the cross-row join can produce the link.
	cwd := t.TempDir()

	rendered := renderAssistantRow(transcriptRow{kind: rowAssistant, text: "`" + path + "`", final: true}, 230)
	lines := strings.Split(rendered, "\n")
	if len(lines) != 2 {
		t.Fatalf("rendered rows = %d, want the path split over 2 rows: %q", len(lines), rendered)
	}
	plain := make([]string, len(lines))
	for i, line := range lines {
		plain[i] = string(stripStyledText(line))
	}
	// A hard split leaves no space at the break: the remainder of the
	// space-free run opens the second row.
	if strings.HasSuffix(plain[0], " ") || !strings.HasPrefix(plain[1], "a") {
		t.Fatalf("want a mid-token break, got rows %q", plain)
	}

	url := fileLinkURL(path)
	linked := strings.Split(linkifyTranscriptLines(rendered, cwd), "\n")
	for i, line := range linked {
		if !strings.Contains(line, "\x1b]8;;"+url+"\x1b\\") {
			t.Fatalf("row %d must carry the whole-path OSC 8 target, got %q", i, line)
		}
	}

	rows := []transcriptSelectableLine{
		{bodyY: 0, text: plain[0]},
		{bodyY: 1, text: plain[1]},
	}
	var opened []string
	restore := fileLinkOpener
	fileLinkOpener = func(p string) error {
		opened = append(opened, p)
		return nil
	}
	defer func() { fileLinkOpener = restore }()

	for _, click := range []struct {
		name     string
		row, col int
	}{
		{"first piece", 0, strings.Index(plain[0], "aaaa")},
		{"second piece", 1, strings.Index(plain[1], "Tela")},
	} {
		opened = nil
		handled, err := handleFileLinkClickWindow(rows, click.row, click.col, cwd, tea.ModCtrl)
		if err != nil {
			t.Fatalf("%s: opener error: %v", click.name, err)
		}
		if !handled {
			t.Fatalf("%s: click on a wrapped piece must be handled", click.name)
		}
		if len(opened) != 1 || opened[0] != path {
			t.Fatalf("%s: opened = %#v, want [%q]", click.name, opened, path)
		}
	}
}

// TestAssistantWrapLinksHardSplitMissingFile keeps the same wrap shape but
// names nothing on disk: no piece may become a link.
func TestAssistantWrapLinksHardSplitMissingFile(t *testing.T) {
	root := t.TempDir()
	path := hardSplitLinkPath(t, root, 160)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}
	cwd := t.TempDir()

	rendered := renderAssistantRow(transcriptRow{kind: rowAssistant, text: "`" + path + "`", final: true}, 230)
	if linked := linkifyTranscriptLines(rendered, cwd); strings.Contains(linked, "\x1b]8;;") {
		t.Fatalf("missing path must stay unlinked, got %q", linked)
	}
	plain := make([]string, 0, 2)
	for _, line := range strings.Split(rendered, "\n") {
		plain = append(plain, string(stripStyledText(line)))
	}
	if len(plain) != 2 {
		t.Fatalf("rendered rows = %d, want 2: %q", len(plain), rendered)
	}
	rows := []transcriptSelectableLine{
		{bodyY: 0, text: plain[0]},
		{bodyY: 1, text: plain[1]},
	}
	restore := fileLinkOpener
	fileLinkOpener = func(string) error {
		t.Fatal("opener must not run for a missing path")
		return nil
	}
	defer func() { fileLinkOpener = restore }()
	if handled, err := handleFileLinkClickWindow(rows, 1, strings.Index(plain[1], "Tela"), cwd, tea.ModCtrl); handled || err != nil {
		t.Fatalf("handled = %v, err = %v; want no link for a missing path", handled, err)
	}
}
