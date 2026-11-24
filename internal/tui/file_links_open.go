package tui

import (
	"os/exec"
	"runtime"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// file_links_open.go handles the click half of transcript file links: mapping
// a modifier+click to the link under the cursor and opening it with the OS
// default viewer. Rendering (scanning + OSC 8) lives in file_links.go.

// fileLinkAtClick resolves the link under a click: spans are recomputed from
// the plain row text (the same scanner the renderer uses), and col is the
// zero-based cell column within the row text.
func fileLinkAtClick(lineText string, col int, cwd string) (string, bool) {
	if col < 0 {
		return "", false
	}
	for _, span := range findFileLinks(lineText, cwd) {
		startCol := lipgloss.Width(lineText[:span.start])
		endCol := lipgloss.Width(lineText[:span.end])
		if col >= startCol && col < endCol {
			return span.abs, true
		}
	}
	return "", false
}

// fileLinkClickModifier reports whether a mouse modifier means "open the link
// under the cursor" rather than select text. Ctrl works everywhere the xterm
// protocol reaches the app; Alt covers terminals that remap Command onto the
// Alt bit. Shift is deliberately excluded: it is the universal bypass that
// hands selection back to the terminal.
func fileLinkClickModifier(mod tea.KeyMod) bool {
	return mod.Contains(tea.ModCtrl) || mod.Contains(tea.ModAlt) || mod.Contains(tea.ModMeta)
}

// fileLinkClickTarget returns the absolute path of the file link under a
// modified click, or false when the click opens nothing. rows holds the plain
// text of consecutive selectable rows and index positions the clicked one; a
// single-row hit wins first, so unbroken links resolve exactly as before. Then
// up to two rows on each side are considered, and only chains of rows with
// consecutive bodyYs join (paragraph breaks never glue). Plain and
// shift-modified clicks return false so the normal selection flow proceeds.
func fileLinkClickTarget(rows []transcriptSelectableLine, index int, col int, cwd string, mod tea.KeyMod) (string, bool) {
	if !fileLinkClickModifier(mod) || index < 0 || index >= len(rows) {
		return "", false
	}
	if abs, ok := fileLinkAtClick(rows[index].text, col, cwd); ok {
		return abs, true
	}
	// Gather the largest consecutive window around the click (at most two
	// rows each side): wrapped pieces only ever sit on adjacent body rows.
	lo, hi := index, index
	for lo-1 >= 0 && lo-1 >= index-2 && rows[lo-1].bodyY+1 == rows[lo].bodyY {
		lo--
	}
	for hi+1 < len(rows) && hi+1 <= index+2 && rows[hi].bodyY+1 == rows[hi+1].bodyY {
		hi++
	}
	if hi == lo {
		return "", false
	}
	windowTexts := make([]string, 0, hi-lo+1)
	for _, line := range rows[lo : hi+1] {
		windowTexts = append(windowTexts, line.text)
	}
	_, multis := planFileLinks(windowTexts, cwd)
	for _, multi := range multis {
		for _, part := range multi.parts {
			if part.line != index-lo {
				continue
			}
			startCol := lipgloss.Width(rows[index].text[:part.start])
			endCol := lipgloss.Width(rows[index].text[:part.end])
			if col >= startCol && col < endCol {
				return multi.abs, true
			}
		}
	}
	return "", false
}

// fileLinkOpenCmd opens a file link in a tea.Cmd, so Update never blocks on the
// OS viewer (rundll32 can take a long time when the target app is busy). A
// failure comes back as fileLinkOpenFailedMsg.
func fileLinkOpenCmd(path string) tea.Cmd {
	return openWithViewerCmd(path, func(err error) tea.Msg {
		return fileLinkOpenFailedMsg{err: err}
	})
}

// fileLinkOpenFailedMsg reports a file link whose viewer could not be started.
type fileLinkOpenFailedMsg struct {
	err error
}

// openWithViewerCmd runs fileLinkOpener off the Update loop. It returns nil on
// success and failed(err) otherwise.
func openWithViewerCmd(path string, failed func(error) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if err := fileLinkOpener(path); err != nil {
			return failed(err)
		}
		return nil
	}
}

// fileLinkOpener opens a verified-existing path with the OS default viewer.
// Replaced by a fake in tests; never receives a shell string.
var fileLinkOpener = func(path string) error {
	name, args := fileLinkOpenCommand(runtime.GOOS, path)
	return runFileLinkOpener(name, args...)
}

// runFileLinkOpener executes the viewer without a shell: the path always
// travels as argv, never interpolated into a command line.
var runFileLinkOpener = func(name string, args ...string) error {
	return exec.Command(name, args...).Run()
}

// fileLinkOpenCommand maps the platform to its default file viewer. Windows
// gets the percent-encoded file:// URL (rundll32 splits raw spaces itself),
// macOS and Linux get the raw path as a single argv — never through a shell.
func fileLinkOpenCommand(goos string, absPath string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{absPath}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", fileLinkURL(absPath)}
	default:
		return "xdg-open", []string{absPath}
	}
}
