package tui

import (
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/media"
)

// attachment_open.go turns a plain left click on the composer's attachment
// chips or thumbnails into "open that attachment". The original file opens in
// the OS viewer through fileLinkOpener, the same opener file links use: the
// path travels as its own argv element, never through a shell. A clipboard
// image has no original, so it opens its private temp copy; if a file image's
// original is gone, or the temp copy was removed, the staged bytes are written
// to a fresh private temp copy first.

// attachmentChipAtMouse resolves a plain left press on the chip row or the
// thumbnail strip to the attachment under it. Every other press reports false,
// so cursor placement, selection and drag keep their current behaviour.
func (m model) attachmentChipAtMouse(msg tea.MouseMsg) (attachmentChip, bool) {
	if m.attachmentClickBlocked() {
		return attachmentChip{}, false
	}
	width := m.chatColumnWidth()
	if width < 8 {
		return attachmentChip{}, false
	}
	frame := m.scrollableTranscriptFrame(m.pinnedTitleBar(width), m.footerView(width))
	localX, localY, ok := frame.composerRect.local(mouseX(msg), mouseY(msg))
	if !ok {
		return attachmentChip{}, false
	}
	chips := attachmentChipList(m.pendingImageLabels, m.pendingDocuments, m.pendingVideoInfos)
	chipRow := 1
	if m.attachmentThumbnailVisible(width) {
		if localY >= 1 && localY <= attachmentPreviewRows {
			return m.attachmentThumbnailChipAt(localX, width, chips)
		}
		// Thumbnail mode draws the chip row only when it has something to add
		// beyond the thumbnails; a lone thumbnail has no row under it.
		if m.attachmentThumbnailSupplementalChips() == "" {
			return attachmentChip{}, false
		}
		chipRow = attachmentPreviewRows + 1
	}
	if localY != chipRow {
		return attachmentChip{}, false
	}
	return chipAtColumn(chips, localX-2, width-4)
}

// attachmentThumbnailChipAt maps a click on a thumbnail slot to the chip that
// covers the image in that slot. Slots without a decoded thumbnail are blank
// space, not an attachment.
func (m model) attachmentThumbnailChipAt(localX, width int, chips []attachmentChip) (attachmentChip, bool) {
	slotOffset := localX - 2
	if slotOffset < 0 {
		return attachmentChip{}, false
	}
	slot := slotOffset / (attachmentPreviewCols + 1)
	if slotOffset%(attachmentPreviewCols+1) >= attachmentPreviewCols ||
		slot >= m.attachmentThumbnailSlots(width) || m.pendingThumbnail(slot) == nil {
		return attachmentChip{}, false
	}
	return chipCoveringImage(chips, slot)
}

// chipAtColumn returns the chip under column (0 = first content cell). Chips are
// joined by one space, and the row is cut at innerWidth, so a chip that is only
// partly visible still answers for its visible cells.
func chipAtColumn(chips []attachmentChip, column, innerWidth int) (attachmentChip, bool) {
	if column < 0 {
		return attachmentChip{}, false
	}
	pos := 0
	for _, chip := range chips {
		if pos >= innerWidth {
			break
		}
		w := lipgloss.Width(chip.text)
		end := min(pos+w, innerWidth)
		if column >= pos && column < end {
			return chip, true
		}
		pos += w + 1
	}
	return attachmentChip{}, false
}

// chipCoveringImage returns the chip that holds pendingImages[index].
func chipCoveringImage(chips []attachmentChip, index int) (attachmentChip, bool) {
	for _, chip := range chips {
		if chip.images > 0 && index >= chip.first && index < chip.first+chip.images {
			return chip, true
		}
	}
	return attachmentChip{}, false
}

// attachmentClickBlocked reports whether a surface sits above the composer, so a
// click there belongs to that surface and never opens an attachment.
func (m model) attachmentClickBlocked() bool {
	return !m.altScreen || m.height <= 0 || m.composerMouseSelectionBlocked() ||
		m.pendingPermission != nil || m.pendingAskUser != nil || m.pendingSpecReview != nil ||
		m.sttKeyPrompt != nil || m.autoCompactPrompt != nil || m.terminalAttach != nil ||
		m.helpOverlay || m.leaderHelpOverlay
}

// openAttachment opens the attachment behind a chip. The viewer runs in a
// tea.Cmd so Update never blocks on it; when nothing can be opened, the user
// gets a short Portuguese notice instead.
func (m model) openAttachment(chip attachmentChip) (model, tea.Cmd) {
	switch chip.kind {
	case attachmentChipImage:
		return m.openPendingImage(chip.index)
	case attachmentChipVideo:
		if chip.index >= len(m.pendingVideoInfos) {
			return m, nil
		}
		info := m.pendingVideoInfos[chip.index]
		if info.gif {
			return m.openOriginalFile("o GIF", info.label, info.source, gifOriginalExt)
		}
		return m.openOriginalFile("o vídeo", info.label, info.source, videoOriginalExt)
	case attachmentChipDocument:
		if chip.index >= len(m.pendingDocuments) {
			return m, nil
		}
		doc := m.pendingDocuments[chip.index]
		return m.openOriginalFile("o PDF", doc.label, doc.path, pdfOriginalExt)
	}
	return m, nil
}

// openPendingImage opens an image's original file when it still exists, then
// its temp copy, and otherwise writes the staged bytes to a new private temp
// copy and registers it so the existing discard paths delete it. The original
// user file never enters pendingImageTemps.
func (m model) openPendingImage(index int) (model, tea.Cmd) {
	if index < 0 || index >= len(m.pendingImages) {
		return m, nil
	}
	if index < len(m.pendingImageOrigins) {
		// Only an original whose resolved name carries an image extension goes to the
		// viewer. Attach-time detection is by content, so "x.hta" holding PNG bytes must
		// never reach open/xdg-open/rundll32; it falls through to the byte copy.
		if resolved, ok := resolveAttachmentOriginal(m.pendingImageOrigins[index]); ok && imageOriginalExt(attachmentExt(resolved)) {
			return m, openAttachmentCmd(resolved)
		}
	}
	if index < len(m.pendingImageTemps) && attachmentFileExists(m.pendingImageTemps[index]) {
		return m, openAttachmentCmd(m.pendingImageTemps[index])
	}
	if len(m.pendingImageTemps) != len(m.pendingImages) {
		return m.appendImageNotice("Não foi possível abrir a imagem: o anexo está fora de sincronia."), nil
	}
	block := m.pendingImages[index]
	copyPath, err := saveClipboardTempFile(block.Data, clipboardMediaTypeExt(block.MediaType))
	if err != nil {
		return m.appendImageNotice("Não foi possível abrir a imagem: falha ao gravar uma cópia temporária."), nil
	}
	m.pendingImageTemps[index] = copyPath
	return m, openAttachmentCmd(copyPath)
}

// openOriginalFile opens a PDF, video or GIF from the path it was attached from,
// provided the resolved file name carries an extension of that kind. A missing
// file or a name of another kind gets a short notice and nothing is opened.
func (m model) openOriginalFile(noun, label, path string, allowed func(ext string) bool) (model, tea.Cmd) {
	resolved, ok := resolveAttachmentOriginal(path)
	if !ok {
		return m.appendImageNotice("Não foi possível abrir " + noun + " " + label + ": o arquivo original não existe mais."), nil
	}
	if !allowed(attachmentExt(resolved)) {
		return m.appendImageNotice("Não foi possível abrir " + noun + " " + label + ": a extensão do arquivo original não é aceita para abrir."), nil
	}
	return m, openAttachmentCmd(resolved)
}

// resolveAttachmentOriginal follows symlinks and reports the real file a path
// names. The viewer is always given this resolved path, so the extension check
// and the opened file are the same file.
func resolveAttachmentOriginal(path string) (string, bool) {
	if path == "" {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !attachmentFileExists(resolved) {
		return "", false
	}
	return resolved, true
}

// attachmentExt is the lower-cased extension used to decide what kind of file a
// name is; matching ignores case, so ".PNG" and ".png" are the same kind.
func attachmentExt(path string) string {
	return strings.ToLower(filepath.Ext(path))
}

// The per-kind extension rules reuse the lists the attach paths already use.
// Images: the drag-and-drop image set. Videos: the video container set. A
// GIF is an animated image, so it opens by its .gif name. A PDF is only ever
// opened by its .pdf name.
func imageOriginalExt(ext string) bool { return droppableImageExts[ext] }
func gifOriginalExt(ext string) bool   { return ext == ".gif" }
func pdfOriginalExt(ext string) bool   { return ext == ".pdf" }
func videoOriginalExt(ext string) bool { return media.VideoExtensions[ext] }

// attachmentOpenFailedMsg reports a viewer that could not be started, so the
// Update loop can show the reason in the transcript.
type attachmentOpenFailedMsg struct {
	err error
}

// openAttachmentCmd runs the OS viewer off the Update loop. It returns nil on
// success and a failure message otherwise.
func openAttachmentCmd(path string) tea.Cmd {
	return func() tea.Msg {
		if err := fileLinkOpener(path); err != nil {
			return attachmentOpenFailedMsg{err: err}
		}
		return nil
	}
}

// attachmentFileExists reports whether path names an existing regular file.
func attachmentFileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
