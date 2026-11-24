package tui

import (
	"bytes"
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// Enter right after a paste: the preview decode has not run yet. The turn must
// carry the pasted bytes unchanged, and the preview that finishes later must be
// dropped rather than stored for an image that is no longer staged.
func TestAttachmentSendRightAfterPasteKeepsImageBytes(t *testing.T) {
	provider := &fakeProvider{events: []qgruntime.StreamEvent{{Type: qgruntime.StreamEventDone}}}
	m := newModel(context.Background(), Options{
		ModelName:    "muse-spark-1.3-contributor",
		ProviderName: "openai",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.width, m.height = 100, 30
	block := previewImageBlock(t)
	m, pending := m.attachClipboardImage(block.Data, "image/png")

	m.input.SetValue("describe this")
	updated, cmd := m.handleSubmit()
	sent := updated.(model)
	if cmd == nil {
		t.Fatal("submit should start a run")
	}
	if len(sent.pendingImages) != 0 {
		t.Fatalf("send kept %d staged images", len(sent.pendingImages))
	}
	if len(sent.lastImages) != 1 {
		t.Fatalf("turn carries %d images, want 1", len(sent.lastImages))
	}
	if got := sent.lastImages[0]; got.MediaType != "image/png" || !bytes.Equal(got.Data, block.Data) {
		t.Fatalf("turn image changed: media=%q bytes equal=%v", got.MediaType, bytes.Equal(got.Data, block.Data))
	}
	settled := settleThumbnails(t, sent, pending)
	if settled.pendingThumbnail(0) != nil || len(settled.attachmentThumbs) != 0 {
		t.Fatalf("late preview for a sent image was stored: %d entries", len(settled.attachmentThumbs))
	}
}
