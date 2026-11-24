package tui

import (
	"bytes"
	"image"
	"image/color"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/termimage"
)

// syncTerminal is the program's output in these tests. Bubble Tea writes from
// its own goroutines, so the buffer is guarded.
type syncTerminal struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (t *syncTerminal) Write(value []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.Write(value)
}

func (t *syncTerminal) Read([]byte) (int, error) { return 0, io.EOF }
func (t *syncTerminal) Close() error             { return nil }
func (t *syncTerminal) Fd() uintptr              { return 0 }

func (t *syncTerminal) snapshot() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.buf.String()
}

// messageLog records every message the program delivers to the model.
type messageLog struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (l *messageLog) add(msg tea.Msg) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, msg)
}

func (l *messageLog) len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.msgs)
}

func (l *messageLog) since(mark int) []tea.Msg {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]tea.Msg(nil), l.msgs[mark:]...)
}

// quietModel leaves out Init's timers (cursor blink, idle ticks), so the only
// messages in the program are the ones the test sends and the attachment path
// causes.
type quietModel struct {
	model
	log *messageLog
}

func (m quietModel) Init() tea.Cmd { return nil }

func (m quietModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	m.log.add(msg)
	next, cmd := m.model.Update(msg)
	return quietModel{model: next.(model), log: m.log}, cmd
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAttachmentPreviewReachesTerminalWithoutCursorBlink runs a real program
// with the image output in front of it. The second preview lands on a text frame
// that did not change, so nothing but the preview's own message can make
// Bubble Tea write; the transmission must still reach the terminal then.
func TestAttachmentPreviewReachesTerminalWithoutCursorBlink(t *testing.T) {
	release := make(chan struct{}, 2)
	original := decodeAttachmentThumbnail
	decodeAttachmentThumbnail = func(block qgruntime.ImageBlock) (*termimage.Animation, error) {
		<-release
		return original(block)
	}
	t.Cleanup(func() { decodeAttachmentThumbnail = original })

	m := thumbnailTestModel(t)
	terminal := &syncTerminal{}
	output := newImageOutput(terminal, m.attachmentRenderers...)
	log := &messageLog{}
	p := tea.NewProgram(quietModel{model: m, log: log}, tea.WithInput(nil), tea.WithOutput(output), tea.WithWindowSize(100, 30))
	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()
	t.Cleanup(p.Kill)

	first := previewImageBlock(t)
	second := solidPNGBlock(t, 9, 7)
	// transmissions counts the Kitty transmissions of one composer slot. The
	// ids are per slot, so a retransmission of the first preview cannot stand
	// in for the second one.
	transmissions := func(index int) int {
		pattern := regexp.MustCompile(`\x1b_Ga=T,[^;]*\bi=` + strconv.Itoa(int(attachmentImageID)+index) + `,`)
		return len(pattern.FindAllStringIndex(terminal.snapshot(), -1))
	}

	p.Send(clipboardImageMsg{data: first.Data, mediaType: "image/png"})
	release <- struct{}{}
	waitUntil(t, "the first preview transmission", func() bool { return transmissions(0) >= 1 })

	before := len(terminal.snapshot())
	mark := log.len()
	p.Send(clipboardImageMsg{data: second.Data, mediaType: "image/png"})
	waitUntil(t, "the chip frame for the second paste", func() bool { return len(terminal.snapshot()) > before })
	release <- struct{}{}
	waitUntil(t, "the second preview transmission", func() bool { return transmissions(1) >= 1 })

	for _, msg := range log.since(mark) {
		switch typed := msg.(type) {
		case clipboardImageMsg, attachmentThumbnailMsg:
		case tea.RawMsg:
			if typed.Msg != imageFlushFrame {
				t.Fatalf("flush request carries %q, want the empty synchronized frame", typed.Msg)
			}
		default:
			t.Fatalf("second preview needed an extra message %T, want only the paste, the decode and the flush request", msg)
		}
	}
	p.Quit()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("program: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("program did not quit")
	}
}

// Only a terminal that draws images gets the flush request. Any other terminal
// receives no extra byte for a preview.
func TestAttachmentPreviewFlushRequestOnlyForImageTerminals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		protocol termimage.ImageProtocol
		want     bool
	}{
		{name: "kitty", protocol: termimage.ImageProtocolKitty, want: true},
		{name: "none", protocol: termimage.ImageProtocolNone, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := thumbnailTestModel(t)
			for i := range m.attachmentRenderers {
				m.attachmentRenderers[i] = termimage.NewImageRenderer(termimage.ImageSupport{Protocol: tc.protocol})
			}
			m, pending := m.attachClipboardImage(previewImageBlock(t).Data, "image/png")
			ready := collectCmdMessages(pending)
			if len(ready) == 0 {
				t.Fatal("setup: paste should start a decode")
			}
			_, cmd := m.Update(ready[0])
			hasFlush := false
			for _, msg := range collectCmdMessages(cmd) {
				if _, ok := msg.(tea.RawMsg); ok {
					hasFlush = true
				}
			}
			if hasFlush != tc.want {
				t.Fatalf("flush request = %v, want %v", hasFlush, tc.want)
			}
		})
	}
}

// The flush request is written like any other frame: a Sixel preview goes
// between the start and the end of that synchronized frame, so the flush never
// lands the image in front of text.
func TestAttachmentFlushDrawsSixelInsideItsFrame(t *testing.T) {
	frame := image.NewNRGBA(image.Rect(0, 0, 8, 6))
	frame.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	anim, err := termimage.ThumbnailAnimation(frame)
	if err != nil {
		t.Fatal(err)
	}
	renderer := termimage.NewImageRenderer(termimage.ImageSupport{Protocol: termimage.ImageProtocolSixel})
	terminal := &syncTerminal{}
	output := newImageOutput(terminal, renderer)
	renderer.Set(&termimage.ImageDraw{
		ID: attachmentImageID, Animation: anim, State: termimage.Idle,
		Columns: attachmentPreviewCols, Rows: attachmentPreviewRows, HeightPixels: attachmentPreviewHeightPixels,
	})
	if _, err := output.Write([]byte(imageFlushFrame)); err != nil {
		t.Fatal(err)
	}
	got := terminal.snapshot()
	frameStart := strings.Index(got, terminalSyncStart)
	sixelAt := strings.Index(got, "\x1bP")
	frameEnd := strings.LastIndex(got, terminalSyncEnd)
	if frameStart < 0 || sixelAt < frameStart || frameEnd < sixelAt {
		t.Fatalf("sixel must sit inside the flushed synchronized frame: %q", got)
	}
}
