package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/quebragalho-dev/quebragalho-cli/internal/media"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// newVisionBTWTestModel is newBTWTestModel on a model that accepts images.
func newVisionBTWTestModel(t *testing.T) model {
	t.Helper()
	m := newBTWTestModel(t)
	m.modelName = "muse-spark-1.3-contributor"
	return m
}

// A preview decoded before /btw opened belongs to the parent, which keeps the
// staged image. Its result arrives on the side surface and must still settle the
// parent's marker, or the image would wait for a preview that never comes.
func TestBTWParentPreviewSettlesWhenResultArrivesInSide(t *testing.T) {
	parent := newVisionBTWTestModel(t)
	parent, pending := parent.attachClipboardImage(previewImageBlock(t).Data, "image/png")
	key, ok := attachmentKeyOf(parent.pendingImages[0])
	if !ok {
		t.Fatal("setup: pasted image has no preview key")
	}
	side, _ := parent.handleBTWCommand("")
	for _, msg := range collectCmdMessages(pending) {
		next, _ := side.Update(msg)
		side = next.(model)
	}
	returned, _ := side.leaveBTW()
	if returned.pendingThumbnail(0) == nil {
		t.Fatalf("parent preview was not settled: entry=%+v", returned.attachmentThumbs[key])
	}
}

// A frame decode for a video extracted by the parent is started by the parent's
// completion. The command must reach the program, or the frame stays in flight.
func TestBTWVideoFramesDecodeThumbnailsForParent(t *testing.T) {
	parent := newVisionBTWTestModel(t)
	parent.pendingVideoExtractions = map[uint64]pendingVideoExtraction{
		7: {label: "clip.mp4", cancel: func() {}},
	}
	side, _ := parent.handleBTWCommand("")
	frame := previewImageBlock(t).Data
	next, cmd := side.Update(videoFramesMsg{
		id:       7,
		label:    "clip.mp4",
		frames:   []media.Frame{{Data: frame, TimestampSec: 0}},
		duration: 1,
	})
	side = next.(model)
	if cmd == nil {
		t.Fatal("completing the parent's video frames returned no decode command")
	}
	side = settleThumbnails(t, side, cmd)
	returned, _ := side.leaveBTW()
	if len(returned.pendingImages) != 1 {
		t.Fatalf("setup: parent should hold the frame, got %d images", len(returned.pendingImages))
	}
	if returned.pendingThumbnail(0) == nil {
		t.Fatal("parent frame preview was never decoded after /btw")
	}
}

// The side conversation starts with the parent's preview map. Storing a result in
// the side copies the map, so the parent keeps its own entries.
func TestBTWSideStoreLeavesParentPreviewMapAlone(t *testing.T) {
	parent := newVisionBTWTestModel(t)
	parent, pending := parent.attachClipboardImage(previewImageBlock(t).Data, "image/png")
	key, ok := attachmentKeyOf(parent.pendingImages[0])
	if !ok {
		t.Fatal("setup: pasted image has no preview key")
	}
	var decoded attachmentThumbnailMsg
	for _, msg := range collectCmdMessages(pending) {
		if typed, ok := msg.(attachmentThumbnailMsg); ok {
			decoded = typed
		}
	}
	// A side conversation with a question keeps the staged image and shares the map.
	snapshot := parent
	side := parent
	side.btw = btwState{active: true, parent: &snapshot}
	side = side.storeAttachmentThumbnail(decoded)
	if !side.attachmentThumbs[key].ready {
		t.Fatal("side did not store its own preview")
	}
	if snapshot.attachmentThumbs[key].ready {
		t.Fatal("side store wrote into the parent's preview map")
	}
}

// pumpBTWCmd runs a command and feeds every message it produces to m, the way
// the program would, until the hidden parent's turn has settled. Messages go
// through the side model, so routing to the parent is what is under test.
func pumpBTWCmd(t *testing.T, m model, cmd tea.Cmd) model {
	t.Helper()
	for step := 0; cmd != nil; step++ {
		if step > 200 {
			t.Fatal("parent turn did not settle")
		}
		var next []tea.Cmd
		for _, msg := range collectCmdMessages(cmd) {
			updated, c := m.Update(msg)
			m = updated.(model)
			next = append(next, c)
		}
		if m.btw.parent != nil && !m.btw.parent.pending {
			return m
		}
		cmd = tea.Batch(next...)
	}
	return m
}

// A queued message waits for a video's frames. When the frames arrive during
// /btw, the queued turn must start on the parent and run to completion there:
// its command has to be returned, or the parent stays pending forever.
func TestBTWVideoFramesStartQueuedTurnOnParent(t *testing.T) {
	parent := newVisionBTWTestModel(t)
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "ok"},
		{Type: qgruntime.StreamEventDone},
	}}
	parent.provider = provider
	parent.pendingVideoExtractions = map[uint64]pendingVideoExtraction{
		7: {label: "clip.mp4", cancel: func() {}},
	}
	parent.queuedMessages = []string{"descreva o video"}
	side, _ := parent.handleBTWCommand("")
	sideTranscript := len(side.transcript)

	next, cmd := side.Update(videoFramesMsg{
		id:       7,
		label:    "clip.mp4",
		frames:   []media.Frame{{Data: previewImageBlock(t).Data}},
		duration: 1,
	})
	side = pumpBTWCmd(t, next.(model), cmd)

	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want 1 for the queued turn", len(provider.requests))
	}
	var text string
	var images int
	for _, msg := range provider.requests[0].Messages {
		if strings.Contains(msg.Content, "descreva o video") {
			text = msg.Content
		}
		images += len(msg.Images)
	}
	if text == "" || images != 1 {
		t.Fatalf("request lacks the queued text or the frame: text=%q images=%d", text, images)
	}
	if side.btw.parent == nil || side.btw.parent.pending {
		t.Fatalf("parent turn did not finish: %#v", side.btw.parent)
	}
	if len(side.transcript) != sideTranscript || side.pending {
		t.Fatal("the parent's turn leaked into the side conversation")
	}
}
