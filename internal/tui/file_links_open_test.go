package tui

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// file_links_open_test.go covers the click half: modifier gating, hit
// testing, the OS opener mapping and the model-level click behavior.
func transcriptLineMousePoint(t *testing.T, m model, text string, offset int) (int, int) {
	t.Helper()
	line := transcriptSelectableLineContaining(t, m, text)
	width := m.chatColumnWidth()
	body, _ := m.transcriptBody(width, "")
	start, _, top := m.transcriptViewportStart(body, width)
	y := top + line.bodyY - start
	index := strings.Index(line.text, text)
	if index < 0 {
		t.Fatalf("line %q does not contain %q", line.text, text)
	}
	return line.textStart + index + offset, y
}

// gateFileLinkOpener swaps the opener for one that records each path and then
// parks until release runs, the way a busy viewer holds its caller. The wait is
// capped so a regression that runs the opener inside Update fails the test
// instead of hanging it.
func gateFileLinkOpener(t *testing.T) (*[]string, func()) {
	t.Helper()
	var opened []string
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	restore := fileLinkOpener
	fileLinkOpener = func(path string) error {
		opened = append(opened, path)
		select {
		case <-gate:
		case <-time.After(2 * time.Second):
		}
		return nil
	}
	t.Cleanup(func() {
		release()
		fileLinkOpener = restore
	})
	return &opened, release
}

// runFileLinkClick resolves a modified click on rows the way Update does, then
// runs the open command it returns. handled and err follow the old handler
// contract: err is the opener's failure, if any.
func runFileLinkClick(rows []transcriptSelectableLine, index int, col int, cwd string, mod tea.KeyMod) (bool, error) {
	abs, ok := fileLinkClickTarget(rows, index, col, cwd, mod)
	if !ok {
		return false, nil
	}
	if failed, ok := execCmd(fileLinkOpenCmd(abs)).(fileLinkOpenFailedMsg); ok {
		return true, failed.err
	}
	return true, nil
}

func TestFileLinkClickTargetModifierResolves(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	line := "see a.png here"
	rows := []transcriptSelectableLine{{bodyY: 0, text: line}}
	col := strings.Index(line, "a.png") + 1
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModAlt} {
		abs, ok := fileLinkClickTarget(rows, 0, col, dir, mod)
		if !ok || abs != filepath.Join(dir, "a.png") {
			t.Fatalf("mod %+v target = %q, %v", mod, abs, ok)
		}
	}
}

func TestFileLinkPlainClickDoesNotOpen(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	line := "see a.png here"
	rows := []transcriptSelectableLine{{bodyY: 0, text: line}}
	col := strings.Index(line, "a.png") + 1
	for _, mod := range []tea.KeyMod{0, tea.ModShift} {
		if abs, ok := fileLinkClickTarget(rows, 0, col, dir, mod); ok {
			t.Fatalf("mod %+v must not resolve a link (selection keeps working), got %q", mod, abs)
		}
	}
}

func TestFileLinkOpenCmdRunsOpenerOnce(t *testing.T) {
	opened, release := gateFileLinkOpener(t)
	cmd := fileLinkOpenCmd("/abs/a.png")
	if cmd == nil {
		t.Fatal("fileLinkOpenCmd must return a command")
	}
	if len(*opened) != 0 {
		t.Fatalf("building the command must not open the file, opened = %#v", *opened)
	}
	release()
	if msg := cmd(); msg != nil {
		t.Fatalf("successful open must return no message, got %#v", msg)
	}
	wantOpened(t, opened, "/abs/a.png")
}

func TestFileLinkOpenFailureNoticeFromCmd(t *testing.T) {
	restore := fileLinkOpener
	fileLinkOpener = func(string) error { return errors.New("xdg-open não encontrado") }
	t.Cleanup(func() { fileLinkOpener = restore })

	m := mouseTestModel()
	msg := execCmd(fileLinkOpenCmd("/abs/a.png"))
	next := updateModel(t, m, msg)
	text := lastTranscriptText(next)
	if !strings.Contains(text, "Não foi possível abrir o arquivo: xdg-open não encontrado") {
		t.Fatalf("aviso de arquivo ausente, último aviso: %q", text)
	}
	if strings.Contains(text, "o anexo") {
		t.Fatalf("aviso de link não pode usar o texto do anexo: %q", text)
	}
}

func TestFileLinkOpenFailureNoticeStaysInBTWSide(t *testing.T) {
	m := newBTWTestModel(t)
	side, _ := m.handleBTWCommand("")
	if !side.btw.active || side.btw.parent == nil {
		t.Fatal("setup: expected an active BTW side conversation")
	}
	parentRows := len(side.btw.parent.transcript)

	next := updateModel(t, side, fileLinkOpenFailedMsg{err: errors.New("boom")})
	if !next.btw.active || next.btw.parent == nil {
		t.Fatal("failed file open must not end the BTW side conversation")
	}
	if text := lastTranscriptText(next); !strings.Contains(text, "Não foi possível abrir o arquivo: boom") {
		t.Fatalf("aviso de arquivo ausente no /btw, último aviso: %q", text)
	}
	if got := len(next.btw.parent.transcript); got != parentRows {
		t.Fatalf("parent transcript changed from %d to %d rows", parentRows, got)
	}
}

func TestFileLinkOpenCommandPerPlatform(t *testing.T) {
	name, args := fileLinkOpenCommand("darwin", "/tmp/a b.png")
	if name != "open" || len(args) != 1 || args[0] != "/tmp/a b.png" {
		t.Fatalf("darwin = %s %#v", name, args)
	}
	name, args = fileLinkOpenCommand("linux", "/tmp/a b.png")
	if name != "xdg-open" || len(args) != 1 || args[0] != "/tmp/a b.png" {
		t.Fatalf("linux = %s %#v", name, args)
	}
	name, args = fileLinkOpenCommand("windows", `C:\a b\c.png`)
	if name != "rundll32" || len(args) != 2 || args[1] != "file:///C:/a%20b/c.png" {
		t.Fatalf("windows = %s %#v", name, args)
	}
}

// transcriptLineMousePoint returns the screen point over the first selectable
// line containing text, at the given byte offset into the line's plain text.
func TestFileLinkAtClickWideChars(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	// "👀" ocupa 2 células: "ver "(0-3) + "👀"(4-5) + " "(6) = a.png começa na coluna 7.
	line := "ver 👀 a.png fim"
	start := strings.Index(line, "a.png")
	if got := line[start:]; !strings.HasPrefix(got, "a.png") {
		t.Fatalf("setup: %q", got)
	}
	if _, ok := fileLinkAtClick(line, 5, dir); ok {
		t.Fatal("coluna 5 (dentro do emoji) não pode acertar o link")
	}
	if _, ok := fileLinkAtClick(line, 6, dir); ok {
		t.Fatal("coluna 6 (espaço antes do nome) não pode acertar o link")
	}
	abs, ok := fileLinkAtClick(line, 7, dir)
	if !ok || abs != filepath.Join(dir, "a.png") {
		t.Fatalf("coluna 6 = %q, %v", abs, ok)
	}
}

func TestFileLinkAtClickSelectsRightSpan(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png", "b.png")
	line := "compare a.png with b.png now"
	first := strings.Index(line, "a.png")
	second := strings.Index(line, "b.png")
	abs, ok := fileLinkAtClick(line, first+1, dir)
	if !ok || abs != filepath.Join(dir, "a.png") {
		t.Fatalf("first span = %q, %v", abs, ok)
	}
	abs, ok = fileLinkAtClick(line, second+3, dir)
	if !ok || abs != filepath.Join(dir, "b.png") {
		t.Fatalf("second span = %q, %v", abs, ok)
	}
	if _, ok := fileLinkAtClick(line, 0, dir); ok {
		t.Fatal("offset before any link must not hit")
	}
}

func TestFileLinkCtrlClickOpensFile(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	m := mouseTestModel()
	m.mouseCapture = true
	m.cwd = dir
	m.transcript = appendRow(m.transcript, rowAssistant, "veja a.png aqui")

	opened, release := gateFileLinkOpener(t)
	x, y := transcriptLineMousePoint(t, m, "a.png", 1)
	click := tea.MouseClickMsg(tea.Mouse{Button: tea.MouseLeft, X: x, Y: y, Mod: tea.ModCtrl})
	updated, cmd := m.Update(click)
	next := updated.(model)
	if cmd == nil {
		t.Fatal("ctrl+click on a link must return the open command")
	}
	if len(*opened) != 0 {
		t.Fatalf("ctrl+click must not run the opener inside Update, opened = %#v", *opened)
	}
	release()
	execCmd(cmd)
	wantOpened(t, opened, filepath.Join(dir, "a.png"))
	if next.transcriptSelection.active {
		t.Fatal("ctrl+click on a link must open it, not start a selection")
	}
}

func TestFileLinkPlainClickSelectsInstead(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	m := mouseTestModel()
	m.mouseCapture = true
	m.cwd = dir
	m.transcript = appendRow(m.transcript, rowAssistant, "veja a.png aqui")

	opened, release := gateFileLinkOpener(t)
	defer release()

	x, y := transcriptLineMousePoint(t, m, "a.png", 1)
	updated, cmd := m.Update(testMouseClick(tea.MouseLeft, x, y))
	next := updated.(model)
	if cmd != nil {
		execCmd(cmd)
	}
	wantNothingOpened(t, opened)
	if !next.transcriptSelection.active {
		t.Fatal("plain click on link text must still start a selection")
	}
}

func TestFileLinkCtrlClickOffLinkDoesNotOpen(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	m := mouseTestModel()
	m.mouseCapture = true
	m.cwd = dir
	m.transcript = appendRow(m.transcript, rowAssistant, "veja a.png aqui")

	opened, release := gateFileLinkOpener(t)
	defer release()

	x, y := transcriptLineMousePoint(t, m, "veja", 1)
	_, cmd := m.Update(tea.MouseClickMsg(tea.Mouse{Button: tea.MouseLeft, X: x, Y: y, Mod: tea.ModCtrl}))
	if cmd != nil {
		execCmd(cmd)
	}
	wantNothingOpened(t, opened)
}
