package tui

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/termimage"
)

func solidPNGBlock(t *testing.T, width, height int) qgruntime.ImageBlock {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 60, G: 170, B: 220, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	return qgruntime.ImageBlock{MediaType: "image/png", Data: encoded.Bytes()}
}

// transmittedBytes is what the terminal receives to show a thumbnail in its
// composer slot: the Kitty placement carrying the PNG.
func transmittedBytes(t *testing.T, thumb *termimage.Animation) int {
	t.Helper()
	renderer := termimage.NewImageRenderer(termimage.ImageSupport{Protocol: termimage.ImageProtocolKitty})
	renderer.Set(&termimage.ImageDraw{
		ID:           attachmentImageID,
		Animation:    thumb,
		State:        termimage.Idle,
		Columns:      attachmentPreviewCols,
		Rows:         attachmentPreviewRows,
		HeightPixels: attachmentPreviewHeightPixels,
	})
	var out bytes.Buffer
	if err := renderer.Render(&out); err != nil {
		t.Fatal(err)
	}
	return out.Len()
}

func TestAttachmentThumbnailShrinksScreenshotBeforeStoring(t *testing.T) {
	thumb, err := attachmentThumbnail(screenshotBlock(t, 3024, 1964))
	if err != nil {
		t.Fatal(err)
	}
	if got := thumb.Frame(termimage.Idle, 0).Bounds(); got.Dx() > attachmentThumbnailMaxWidth || got.Dy() > attachmentThumbnailMaxHeight {
		t.Fatalf("stored thumbnail is %v, want within %dx%d", got, attachmentThumbnailMaxWidth, attachmentThumbnailMaxHeight)
	}
	if n := transmittedBytes(t, thumb); n >= 60*1024 {
		t.Fatalf("thumbnail transmits %d bytes, want under 60 KiB", n)
	}
}

func TestAttachmentThumbnailNeverEnlargesSmallImages(t *testing.T) {
	thumb, err := attachmentThumbnail(solidPNGBlock(t, 40, 30))
	if err != nil {
		t.Fatal(err)
	}
	if got := thumb.Frame(termimage.Idle, 0).Bounds(); got.Dx() != 40 || got.Dy() != 30 {
		t.Fatalf("small image became %v, want 40x30", got)
	}
}

func TestAttachmentThumbnailExtremeAspectKeepsOnePixel(t *testing.T) {
	thumb, err := attachmentThumbnail(solidPNGBlock(t, 20000, 1))
	if err != nil {
		t.Fatal(err)
	}
	if got := thumb.Frame(termimage.Idle, 0).Bounds(); got.Dx() < 1 || got.Dy() != 1 {
		t.Fatalf("20000x1 thumbnail is %v, want height 1 and non-zero width", got)
	}
}

// settleThumbnails runs cmd and feeds every message it produces back through
// Update, the way the program loop delivers batches. Nested batches are flattened
// because Update batches the thumbnail commands inside other batches.
func settleThumbnails(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for _, msg := range collectCmdMessages(cmd) {
		updated, _ := m.Update(msg)
		m = updated.(model)
	}
	return m
}

func collectCmdMessages(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, sub := range batch {
		out = append(out, collectCmdMessages(sub)...)
	}
	return out
}

// attachClipboardSettled attaches clipboard bytes and waits for the preview
// decode, so tests see the state the user has once the thumbnail arrives.
func attachClipboardSettled(t *testing.T, m model, data []byte) model {
	t.Helper()
	next, cmd := m.attachClipboardImage(data, "image/png")
	return settleThumbnails(t, next, cmd)
}

// thumbnailTestModel is a vision model on a graphics terminal with room for the
// whole gallery, so every staged image can show its preview.
func thumbnailTestModel(t *testing.T) model {
	t.Helper()
	m := newModel(context.Background(), Options{ModelName: "muse-spark-1.3-contributor"})
	m.width, m.height = 100, 30
	m.altScreen = true
	m.headerPrinted = true
	m.attachmentRenderers = make([]*termimage.ImageRenderer, attachmentPreviewMaxImages)
	for i := range m.attachmentRenderers {
		m.attachmentRenderers[i] = termimage.NewImageRenderer(termimage.ImageSupport{Protocol: termimage.ImageProtocolKitty})
	}
	return m
}

// countThumbnailDecodes swaps the decode seam for the test and returns the
// counter it bumps.
func countThumbnailDecodes(t *testing.T) *int {
	t.Helper()
	count := new(int)
	original := decodeAttachmentThumbnail
	decodeAttachmentThumbnail = func(block qgruntime.ImageBlock) (*termimage.Animation, error) {
		*count++
		return original(block)
	}
	t.Cleanup(func() { decodeAttachmentThumbnail = original })
	return count
}

func TestAttachmentChipAppearsBeforeThumbnailDecodes(t *testing.T) {
	decodes := countThumbnailDecodes(t)
	m := thumbnailTestModel(t)
	updated, cmd := m.Update(clipboardImageMsg{data: previewImageBlock(t).Data, mediaType: "image/png"})
	next := updated.(model)
	if len(next.pendingImageLabels) != 1 || renderAttachmentChips(next.pendingImageLabels, nil, nil) == "" {
		t.Fatalf("chip missing right after the clipboard message: labels=%v", next.pendingImageLabels)
	}
	if cmd == nil {
		t.Fatal("expected a decode command for the new image")
	}
	if *decodes != 0 {
		t.Fatalf("decode ran inside Update (%d times), want it deferred to the command", *decodes)
	}
	if next.pendingThumbnail(0) != nil || next.attachmentThumbnailVisible(next.chatColumnWidth()) {
		t.Fatal("preview must not show before the decode command has run")
	}
	next = settleThumbnails(t, next, cmd)
	if next.pendingThumbnail(0) == nil || *decodes != 1 {
		t.Fatalf("preview not stored after settling: thumb=%v decodes=%d", next.pendingThumbnail(0) != nil, *decodes)
	}
}

func TestAttachmentSecondPasteDecodesOnlyTheNewImage(t *testing.T) {
	decodes := countThumbnailDecodes(t)
	m := attachClipboardSettled(t, thumbnailTestModel(t), previewImageBlock(t).Data)
	firstPreview := m.pendingThumbnail(0)
	if firstPreview == nil || *decodes != 1 {
		t.Fatalf("first paste: preview=%v decodes=%d, want preview and 1 decode", firstPreview != nil, *decodes)
	}
	_ = m.View()
	var before bytes.Buffer
	if err := m.attachmentRenderers[0].Render(&before); err != nil {
		t.Fatal(err)
	}

	second := solidPNGBlock(t, 9, 7)
	m = attachClipboardSettled(t, m, second.Data)
	if *decodes != 2 {
		t.Fatalf("decodes after second paste = %d, want 2 (only the new image)", *decodes)
	}
	if m.pendingThumbnail(0) != firstPreview {
		t.Fatal("first preview was replaced by a new animation; the terminal would retransmit it")
	}
	if m.pendingThumbnail(1) == nil {
		t.Fatal("second image has no preview")
	}
	_ = m.View()
	var after bytes.Buffer
	if err := m.attachmentRenderers[0].Render(&after); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after.String(), "a=T") {
		t.Fatalf("second paste retransmitted the first preview's PNG: %q", after.String())
	}
}

func TestAttachmentStaleDecodeAfterRemovalIsDropped(t *testing.T) {
	decodes := countThumbnailDecodes(t)
	m := thumbnailTestModel(t)
	block := previewImageBlock(t)
	next, pending := m.attachClipboardImage(block.Data, "image/png")
	m = next
	m, _, removed := m.removeLastAttachment()
	if !removed {
		t.Fatal("expected the staged image to be removed")
	}
	m = settleThumbnails(t, m, pending)
	if m.pendingThumbnail(0) != nil || len(m.attachmentThumbs) != 0 {
		t.Fatalf("result for a removed image was stored: entries=%d", len(m.attachmentThumbs))
	}
	if *decodes != 1 {
		t.Fatalf("decodes = %d, want the one already in flight", *decodes)
	}

	// Restoring the same bytes must decode them again instead of waiting on the
	// dropped marker forever.
	m.pendingImages = []qgruntime.ImageBlock{block}
	m.pendingImageLabels = []string{"clipboard"}
	m.pendingImageTemps = []string{""}
	m.pendingImageOrigins = []string{""}
	cmd := m.refreshPendingImageThumbnail()
	m = settleThumbnails(t, m, cmd)
	if m.pendingThumbnail(0) == nil || *decodes != 2 {
		t.Fatalf("restored image preview=%v decodes=%d, want preview and 2 decodes", m.pendingThumbnail(0) != nil, *decodes)
	}
}

func TestAttachmentRemovalKeepsRemainingPreviews(t *testing.T) {
	decodes := countThumbnailDecodes(t)
	m := thumbnailTestModel(t)
	m = attachClipboardSettled(t, m, solidPNGBlock(t, 8, 6).Data)
	m = attachClipboardSettled(t, m, solidPNGBlock(t, 9, 7).Data)
	m = attachClipboardSettled(t, m, solidPNGBlock(t, 10, 5).Data)
	first, second := m.pendingThumbnail(0), m.pendingThumbnail(1)
	if first == nil || second == nil || m.pendingThumbnail(2) == nil || *decodes != 3 {
		t.Fatalf("setup: previews missing or decodes=%d, want 3", *decodes)
	}
	m, _, _ = m.removeLastAttachment()
	if m.pendingThumbnail(0) != first || m.pendingThumbnail(1) != second || m.pendingThumbnail(2) != nil {
		t.Fatal("removing the last image shifted or dropped the remaining previews")
	}
	m, _, _ = m.removeLastAttachment()
	if m.pendingThumbnail(0) != first || m.pendingThumbnail(1) != nil {
		t.Fatal("removing again broke the first preview")
	}
	if *decodes != 3 {
		t.Fatalf("removals decoded again: decodes=%d, want 3", *decodes)
	}
}

func TestAttachmentClearDropsPreviewsWithoutDecoding(t *testing.T) {
	decodes := countThumbnailDecodes(t)
	m := attachClipboardSettled(t, thumbnailTestModel(t), previewImageBlock(t).Data)
	next, cmd := m.handleImageCommand("clear")
	if cmd != nil {
		t.Fatal("clear should not start a decode")
	}
	if next.pendingThumbnail(0) != nil || len(next.pendingImages) != 0 || *decodes != 1 {
		t.Fatalf("clear left state behind: decodes=%d", *decodes)
	}
}

func TestAttachmentRestoredImagesReuseTheirPreviews(t *testing.T) {
	decodes := countThumbnailDecodes(t)
	m := attachClipboardSettled(t, thumbnailTestModel(t), previewImageBlock(t).Data)
	preview := m.pendingThumbnail(0)

	// Submitting moves the staged images into lastImages; /retry and /edit put
	// them back. The ready preview must survive the round trip without a decode.
	m.lastImages = m.pendingImages
	m.lastImageLabels = m.pendingImageLabels
	m.lastImageOrigins = m.pendingImageOrigins
	m.pendingImages = nil
	m.pendingImageLabels = nil
	m.pendingImageOrigins = nil
	m.pendingImageTemps = nil
	_ = m.refreshPendingImageThumbnail()

	m.pendingImages = m.lastImages
	m.pendingImageLabels = m.lastImageLabels
	m.pendingImageOrigins = m.lastImageOrigins
	m.pendingImageTemps = make([]string, len(m.pendingImages))
	cmd := m.refreshPendingImageThumbnail()
	if cmd != nil || *decodes != 1 {
		t.Fatalf("restore decoded again: cmd=%v decodes=%d", cmd != nil, *decodes)
	}
	if m.pendingThumbnail(0) != preview {
		t.Fatal("restored image lost its preview")
	}
}

// Sending clears the pending list without a refresh, so the decode of a sent
// image can still be in flight when its result arrives. The result must be
// dropped even though nothing pruned its marker.
func TestAttachmentDecodeResultAfterSendIsDropped(t *testing.T) {
	decodes := countThumbnailDecodes(t)
	m := thumbnailTestModel(t)
	block := previewImageBlock(t)
	m, pending := m.attachClipboardImage(block.Data, "image/png")

	m.lastImages = m.pendingImages
	m.lastImageLabels = m.pendingImageLabels
	m.lastImageOrigins = m.pendingImageOrigins
	m.pendingImages = nil
	m.pendingImageLabels = nil
	m.pendingImageOrigins = nil
	m.pendingImageTemps = nil
	m = settleThumbnails(t, m, pending)
	if len(m.attachmentThumbs) != 0 || m.pendingThumbnail(0) != nil {
		t.Fatalf("result for a sent image was stored: entries=%d", len(m.attachmentThumbs))
	}

	// /retry restores the same bytes; the dropped marker must not block a decode.
	m.pendingImages = m.lastImages
	m.pendingImageLabels = m.lastImageLabels
	m.pendingImageOrigins = m.lastImageOrigins
	m.pendingImageTemps = make([]string, len(m.pendingImages))
	cmd := m.refreshPendingImageThumbnail()
	m = settleThumbnails(t, m, cmd)
	if m.pendingThumbnail(0) == nil || *decodes != 2 {
		t.Fatalf("restored image preview=%v decodes=%d, want preview and 2 decodes", m.pendingThumbnail(0) != nil, *decodes)
	}
}
