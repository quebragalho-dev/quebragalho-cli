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

// handleFileLinkClick opens the file link under a modified click. Plain and
// shift-modified clicks return false so the normal selection flow proceeds.
func handleFileLinkClick(mod tea.KeyMod, lineText string, col int, cwd string) (bool, error) {
	if !fileLinkClickModifier(mod) {
		return false, nil
	}
	abs, ok := fileLinkAtClick(lineText, col, cwd)
	if !ok {
		return false, nil
	}
	if err := fileLinkOpener(abs); err != nil {
		return true, err
	}
	return true, nil
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
