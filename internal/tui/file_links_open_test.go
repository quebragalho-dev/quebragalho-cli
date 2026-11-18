package tui

import (
	"path/filepath"
	"strings"
	"testing"

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

func TestFileLinkClickModifierOpens(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	var opened []string
	restore := fileLinkOpener
	fileLinkOpener = func(path string) error {
		opened = append(opened, path)
		return nil
	}
	defer func() { fileLinkOpener = restore }()

	line := "see a.png here"
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModAlt} {
		opened = nil
		col := strings.Index(line, "a.png") + 1
		handled, err := handleFileLinkClick(mod, line, col, dir)
		if err != nil {
			t.Fatalf("mod %+v opener error: %v", mod, err)
		}
		if !handled {
			t.Fatalf("mod %+v click on a link must be handled", mod)
		}
		if len(opened) != 1 || opened[0] != filepath.Join(dir, "a.png") {
			t.Fatalf("mod %+v opened = %#v", mod, opened)
		}
	}
}

func TestFileLinkPlainClickDoesNotOpen(t *testing.T) {
	dir := fileLinkTestDir(t, "a.png")
	called := false
	restore := fileLinkOpener
	fileLinkOpener = func(path string) error {
		called = true
		return nil
	}
	defer func() { fileLinkOpener = restore }()

	line := "see a.png here"
	col := strings.Index(line, "a.png") + 1
	if handled, err := handleFileLinkClick(0, line, col, dir); handled || err != nil {
		t.Fatalf("plain click must not be handled as a link open (selection keeps working), handled=%v err=%v", handled, err)
	}
	if called {
		t.Fatal("plain click must not invoke the opener")
	}
	if handled, err := handleFileLinkClick(tea.ModShift, line, col, dir); handled || err != nil {
		t.Fatalf("shift+click must not open (native selection bypass), handled=%v err=%v", handled, err)
	}
	if called {
		t.Fatal("shift+click must not invoke the opener")
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

	var opened []string
	restore := fileLinkOpener
	fileLinkOpener = func(path string) error {
		opened = append(opened, path)
		return nil
	}
	defer func() { fileLinkOpener = restore }()

	x, y := transcriptLineMousePoint(t, m, "a.png", 1)
	click := tea.MouseClickMsg(tea.Mouse{Button: tea.MouseLeft, X: x, Y: y, Mod: tea.ModCtrl})
	updated, _ := m.Update(click)
	next := updated.(model)
	if len(opened) != 1 || opened[0] != filepath.Join(dir, "a.png") {
		t.Fatalf("ctrl+click opened = %#v, want [%q]", opened, filepath.Join(dir, "a.png"))
	}
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

	called := false
	restore := fileLinkOpener
	fileLinkOpener = func(path string) error {
		called = true
		return nil
	}
	defer func() { fileLinkOpener = restore }()

	x, y := transcriptLineMousePoint(t, m, "a.png", 1)
	updated, _ := m.Update(testMouseClick(tea.MouseLeft, x, y))
	next := updated.(model)
	if called {
		t.Fatal("plain click must not invoke the opener")
	}
	if !next.transcriptSelection.active {
		t.Fatal("plain click on link text must still start a selection")
	}
}
