package tui

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/atotto/clipboard"
	"github.com/quebragalho-dev/quebragalho-cli/internal/imageinput"
)

// clipboardReadMsg carries the result of an async OS-clipboard read back to
// Update. It drives the right-click "paste" path.
type clipboardReadMsg struct {
	content string
	err     error
}

// clipboardImageMsg carries clipboard content read from the OS: image bytes
// (a screenshot) and/or copied file paths (a Finder/Explorer/file-manager
// copy). Sent when the text clipboard is empty — the user pasted an image or
// files, not text. Both data and files are empty when there is nothing to
// attach.
type clipboardImageMsg struct {
	data      []byte
	mediaType string
	files     []string
	err       error
}

var (
	// readClipboardContent is the OS clipboard reader (image + copied files).
	// A var so tests substitute a stub instead of depending on the developer's
	// real clipboard.
	readClipboardContent = imageinput.ReadClipboardContent
	// saveClipboardTempFile stages clipboard image bytes in the private qg
	// dir. Stubbed in tests to keep them hermetic (no ~/.config writes).
	saveClipboardTempFile = imageinput.WriteClipboardTemp
)

// clipboardNoticeText monta o aviso de falha ao ler o clipboard. Erros que já
// são uma frase acionável em português (ferramenta ausente, ferramenta que não
// respondeu) aparecem sozinhos — prefixá-los repetia "não foi possível ler a
// área de transferência" duas vezes. Os demais mantêm o prefixo, que diz qual
// leitura falhou.
func clipboardNoticeText(err error) string {
	if imageinput.IsClipboardTimeout(err) || errors.Is(err, imageinput.ErrClipboardToolMissing) {
		return err.Error()
	}
	return "Falha ao ler a imagem da área de transferência: " + err.Error()
}

// readClipboardImageCmd reads the OS clipboard for image and file content off
// the Update goroutine. Returns a clipboardImageMsg with whatever was found,
// or nil (no command) when the clipboard holds neither — the caller treats nil
// as a silent no-op. A file lookup failure stays silent unless there is no
// image either, so a stray Ctrl+V with text on the clipboard never nags; only
// an actionable error (e.g. the Linux clipboard tool missing) surfaces.
func readClipboardImageCmd() tea.Cmd {
	return func() tea.Msg {
		content, err := readClipboardContent()
		if err != nil {
			return clipboardImageMsg{err: err}
		}
		if content.Image == nil && len(content.Files) == 0 {
			return nil // nothing to attach — silent no-op
		}
		return clipboardImageMsg{data: content.Image, mediaType: content.MediaType, files: content.Files}
	}
}

// pasteFromClipboardCmd reads the OS clipboard off the Update goroutine (it
// shells out to pbpaste/xclip/etc.) and delivers the text as a clipboardReadMsg.
// A right-click pastes straight from here — no menu.
func pasteFromClipboardCmd() tea.Cmd {
	return func() tea.Msg {
		content, err := clipboard.ReadAll()
		return clipboardReadMsg{content: content, err: err}
	}
}

// routePaste inserts pasted text into whichever input surface is focused. It is
// shared by the terminal bracketed-paste handler (tea.PasteMsg) and the
// right-click paste (clipboardReadMsg) so a bracketed paste and a right-click
// paste behave identically. Surfaces with no editable text field (a permission/
// spec prompt, the MCP manager, or the detailed transcript) swallow the paste.
// Searchable pickers consume pasted text through the same filtering path as
// typed characters. The session rename editor accepts text only; empty content
// is a no-op there rather than an image probe.
func (m model) routePaste(content string) (tea.Model, tea.Cmd) {
	// A paste is a deliberate action, same as a keypress or click — it means
	// the user moved on to something else, so it disarms a stale Esc
	// cancel-confirmation. tea.PasteMsg/clipboardReadMsg aren't
	// tea.KeyPressMsg, so they don't go through that generic keypress hook;
	// do it once here, before any early return, so both the bracketed-paste
	// and right-click-paste paths are covered uniformly.
	m = m.disarmCancelConfirmation()
	// Same immediate-input transition as the keypress hook: text entering the
	// composer must land the caret in its solid-while-typing state right away
	// and refresh the typing timestamp. Without this, a paste right after the
	// blink phase hid the caret leaves the newly populated composer caret-less
	// until the next 530ms tick, and the stale timestamp makes that tick
	// toggle instead of going solid.
	m.lastCharTime = m.now()
	m.composerCursorVisible = true
	// The /autocompact panel owns the paste while open: it edits the panel
	// input, never the composer behind it.
	if m.autoCompactPrompt != nil {
		if content != "" {
			m.autoCompactPrompt.appendInput(strings.TrimSpace(content))
		}
		return m, nil
	}
	if m.renamePrompt != nil {
		if content == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.PasteMsg{Content: sanitizeComposerInput(content)})
		return m, cmd
	}
	if content == "" {
		// Empty text clipboard — the user may have pasted a screenshot.
		// Probe the OS clipboard for image content asynchronously.
		return m, readClipboardImageCmd()
	}
	// Setup and the ask_user questionnaire share the main text input.
	if m.setup.visible {
		return m.handleSetupPaste(tea.PasteMsg{Content: content})
	}
	if m.pendingAskUser != nil {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.PasteMsg{Content: content})
		return m, cmd
	}
	if m.picker != nil {
		if m.picker.kind == pickerEffort || m.picker.kind == pickerUsage {
			return m, nil
		}
		if m.modelPickerIsLoading() {
			return m, nil
		}
		m.picker.appendQuery([]rune(sanitizeComposerInput(content)))
		return m, nil
	}
	if m.transcriptDetailed || m.pendingSpecReview != nil || m.pendingPermission != nil || m.mcpAddWizard != nil || m.mcpManager != nil {
		return m, nil
	}
	// A drag-dropped image/PDF arrives as a (backslash-escaped) file path. Attach
	// it as an image instead of inserting the raw path — otherwise a "/Users/…"
	// path would be submitted as an unknown slash-command.
	if path, ok := droppedAttachmentPath(content, m.cwd); ok {
		return m.handleImageCommand(path)
	}
	state := m.currentComposerState()
	m = m.applyComposerText(state, content, true)
	m.recomputeSuggestions()
	return m, nil
}
