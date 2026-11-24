package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/quebragalho-dev/quebragalho-cli/internal/termimage"
)

const (
	attachmentImageID             = uint32(0xC0E0)
	attachmentPreviewCols         = 10
	attachmentPreviewRows         = 4
	attachmentPreviewHeightPixels = 64
	attachmentPreviewMaxImages    = 4
	// attachmentThumbnailMaxWidth/Height bound the stored preview in device pixels.
	// A 10x4 cell slot is about 180x144 px on a Retina terminal (cells near 18x36
	// px), so the preview is sharp there and the Kitty PNG stays small. Pixels
	// beyond that are wasted: the terminal scales the image to the slot anyway.
	attachmentThumbnailMaxWidth  = 180
	attachmentThumbnailMaxHeight = 144
)

// attachmentThumbnailVisible deliberately has a conservative layout gate. The
// image is an enhancement to the existing attachment chips, not a reason to
// crowd a narrow or modal surface.
func (m model) attachmentThumbnailVisible(width int) bool {
	if len(m.attachmentRenderers) == 0 || !m.altScreen ||
		m.height < attachmentPreviewRows+10 || width < attachmentPreviewCols+28 {
		return false
	}
	if m.pendingPermission != nil || m.pendingAskUser != nil || m.pendingSpecReview != nil ||
		m.mcpAddWizard != nil || m.mcpManager != nil || m.picker != nil ||
		m.sttKeyPrompt != nil || m.autoCompactPrompt != nil || m.renamePrompt != nil || m.terminalAttach != nil || m.setup.visible || m.helpOverlay || m.leaderHelpOverlay {
		return false
	}
	for index := 0; index < m.attachmentThumbnailSlots(width); index++ {
		if m.pendingThumbnail(index) != nil &&
			index < len(m.attachmentRenderers) && m.attachmentRenderers[index].Support().Supported() {
			return true
		}
	}
	return false
}

func (m model) attachmentThumbnailSlots(width int) int {
	innerWidth := maxInt(1, width-4)
	slots := maxInt(1, innerWidth/(attachmentPreviewCols+1))
	return min(attachmentPreviewMaxImages, min(slots, len(m.attachmentRenderers)))
}

func (m model) attachmentThumbnailLines(width int) []string {
	left := strings.Repeat(" ", attachmentPreviewCols+1)
	lines := make([]string, attachmentPreviewRows)
	for index := range lines {
		lines[index] = left
	}
	for index, line := range lines {
		lines[index] = fitStyledLine(line, width)
	}
	return lines
}

func (m model) attachmentThumbnailSupplementalChips() string {
	if len(m.pendingVideoInfos) == 0 && len(m.pendingImageLabels) <= 1 && len(m.pendingDocuments) == 0 {
		return ""
	}
	return renderAttachmentChips(m.pendingImageLabels, m.pendingDocuments, m.pendingVideoInfos)
}

func (m model) attachmentComposerPrefixRows(width int) int {
	if m.attachmentThumbnailVisible(width) {
		rows := attachmentPreviewRows
		if m.attachmentThumbnailSupplementalChips() != "" {
			rows++
		}
		return rows
	}
	if renderAttachmentChips(m.pendingImageLabels, m.pendingDocuments, m.pendingVideoInfos) != "" {
		return 1
	}
	return 0
}

// attachmentImageDraw places one gallery thumbnail inside the reserved top rows
// of the composer. The position comes from the same footer layout used for mouse
// input, so it follows sidebar changes and terminal resizes instead of relying on
// a brittle absolute bottom-right calculation.
func (m model) attachmentImageDraw(index int) *termimage.ImageDraw {
	width := m.chatColumnWidth()
	if !m.attachmentThumbnailVisible(width) || index < 0 || index >= m.attachmentThumbnailSlots(width) ||
		m.pendingThumbnail(index) == nil {
		return nil
	}
	frame := m.scrollableTranscriptFrame(m.pinnedTitleBar(width), m.footerView(width))
	if frame.composerRect.height < attachmentPreviewRows+2 {
		return nil
	}
	return &termimage.ImageDraw{
		ID:           attachmentImageID + uint32(index),
		Animation:    m.pendingThumbnail(index),
		State:        termimage.Idle,
		X:            frame.composerRect.x + 2 + index*(attachmentPreviewCols+1),
		Y:            frame.composerRect.y + 1,
		Columns:      attachmentPreviewCols,
		Rows:         attachmentPreviewRows,
		HeightPixels: attachmentPreviewHeightPixels,
	}
}

// attachmentPreviewSignature is the decoded preview each composer slot shows.
// Two signatures that differ mean the terminal needs an image write, which the
// text frame alone does not bring.
type attachmentPreviewSignature [attachmentPreviewMaxImages]*termimage.Animation

func (m model) attachmentPreviewSignature() attachmentPreviewSignature {
	var signature attachmentPreviewSignature
	for index := range signature {
		signature[index] = m.pendingThumbnail(index)
	}
	return signature
}

// attachmentImageFlushCmd asks Bubble Tea for one more write when a slot's
// preview changed during this Update. The image update rides on the next write
// Bubble Tea makes, and it makes one only when the text changes. A preview that
// arrives on an unchanged frame would otherwise wait for the cursor blink. The
// request is an empty synchronized frame (imageFlushFrame): it draws nothing,
// no terminal answers it, and it is sent only when some slot can draw an image.
func (m model) attachmentImageFlushCmd(before attachmentPreviewSignature) tea.Cmd {
	if m.attachmentPreviewSignature() == before || !m.attachmentImagesSupported() {
		return nil
	}
	return tea.Raw(imageFlushFrame)
}

func (m model) attachmentImagesSupported() bool {
	for _, renderer := range m.attachmentRenderers {
		if renderer.Support().Supported() {
			return true
		}
	}
	return false
}

func attachmentPreviewImageIDs() []uint32 {
	ids := make([]uint32, attachmentPreviewMaxImages)
	for index := range ids {
		ids[index] = attachmentImageID + uint32(index)
	}
	return ids
}
