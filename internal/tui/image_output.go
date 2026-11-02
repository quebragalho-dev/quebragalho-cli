package tui

import (
	"bytes"
	"io"
	"sync"

	"github.com/charmbracelet/x/ansi"

	"github.com/quebragalho-dev/quebragalho-cli/internal/termimage"
)

const (
	terminalSyncStart = "\x1b[?2026h"
	terminalSyncEnd   = "\x1b[?2026l"
)

type terminalOutputFile interface {
	io.ReadWriteCloser
	Fd() uintptr
}

type imageOutput struct {
	output         terminalOutputFile
	renderers      []*termimage.ImageRenderer
	renderDisabled []bool
	mu             sync.Mutex
}

func newImageOutput(output terminalOutputFile, renderers ...*termimage.ImageRenderer) *imageOutput {
	active := make([]*termimage.ImageRenderer, 0, len(renderers))
	for _, renderer := range renderers {
		if renderer != nil {
			active = append(active, renderer)
		}
	}
	return &imageOutput{output: output, renderers: active, renderDisabled: make([]bool, len(active))}
}

func (o *imageOutput) Read(value []byte) (int, error) {
	return o.output.Read(value)
}

func (o *imageOutput) Write(value []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	leavingAltScreen := bytes.Contains(value, []byte("\x1b[?1049l"))
	if leavingAltScreen {
		for _, renderer := range o.renderers {
			if !rendererUsesKitty(renderer) {
				if err := o.writeImageUpdate(renderer.Clear); err != nil {
					return 0, err
				}
			}
		}
	}
	if leavingAltScreen {
		written, err := o.output.Write(value)
		if err != nil {
			return written, err
		}
		for _, renderer := range o.renderers {
			if rendererUsesKitty(renderer) {
				if err := o.writeImageUpdate(renderer.Clear); err != nil {
					return written, err
				}
			}
		}
		return written, nil
	}
	if bytes.Contains(value, []byte(ansi.EraseEntireScreen)) ||
		bytes.Contains(value, []byte(ansi.SetModeAltScreenSaveCursor)) {
		for _, renderer := range o.renderers {
			renderer.Invalidate()
		}
	}

	var imageErase, imageUpdate bytes.Buffer
	for index, renderer := range o.renderers {
		if o.renderDisabled[index] {
			continue
		}
		var rendererErase, rendererUpdate bytes.Buffer
		if err := renderer.RenderFrame(&rendererErase, &rendererUpdate); err != nil {
			// Terminal images are decorative. Disable only the failing renderer for
			// this output session so one bad attachment cannot hide the others
			// or terminate the interactive shell.
			o.renderDisabled[index] = true
			continue
		}
		imageErase.Write(rendererErase.Bytes())
		imageUpdate.Write(rendererUpdate.Bytes())
	}
	if imageErase.Len() == 0 && imageUpdate.Len() == 0 {
		return writeChecked(o.output, value)
	}

	// Bubble Tea encloses supported terminal frames in synchronized-output
	// markers. Keep the image update inside that same transaction so the
	// terminal never presents the text frame and image movement separately.
	syncStart := bytes.LastIndex(value, []byte(terminalSyncStart))
	syncEnd := bytes.LastIndex(value, []byte(terminalSyncEnd))
	if syncEnd >= 0 && syncEnd > syncStart || syncStart > syncEnd {
		beforeAt := 0
		if syncStart >= 0 {
			beforeAt = syncStart + len(terminalSyncStart)
		}
		afterAt := syncEnd
		if afterAt < beforeAt {
			afterAt = len(value)
		}
		var frame bytes.Buffer
		frame.Grow(len(value) + imageErase.Len() + imageUpdate.Len())
		frame.Write(value[:beforeAt])
		frame.Write(imageErase.Bytes())
		frame.Write(value[beforeAt:afterAt])
		frame.Write(imageUpdate.Bytes())
		frame.Write(value[afterAt:])
		written, err := o.output.Write(frame.Bytes())
		consumed := originalBytesWritten(written, afterAt+imageErase.Len(), imageUpdate.Len(), len(value)+imageErase.Len())
		consumed = originalBytesWritten(consumed, beforeAt, imageErase.Len(), len(value))
		if err == nil && written != frame.Len() {
			err = io.ErrShortWrite
		}
		return consumed, err
	}

	if _, err := io.WriteString(o.output, terminalSyncStart); err != nil {
		return 0, err
	}
	_, writeErr := writeChecked(o.output, imageErase.Bytes())
	written := 0
	if writeErr == nil {
		written, writeErr = writeChecked(o.output, value)
	}
	if writeErr == nil {
		_, writeErr = writeChecked(o.output, imageUpdate.Bytes())
	}
	_, endErr := io.WriteString(o.output, terminalSyncEnd)
	if writeErr != nil {
		return written, writeErr
	}
	return written, endErr
}

func rendererUsesKitty(renderer *termimage.ImageRenderer) bool {
	switch renderer.Support().Protocol {
	case termimage.ImageProtocolKitty, termimage.ImageProtocolKittyLocalFile:
		return true
	default:
		return false
	}
}

func writeChecked(writer io.Writer, value []byte) (int, error) {
	written, err := writer.Write(value)
	if err == nil && written != len(value) {
		err = io.ErrShortWrite
	}
	return written, err
}

// originalBytesWritten maps progress through an expanded synchronized frame
// back to bytes consumed from the caller's original buffer. Bytes belonging
// to the injected image update must never be reported as caller bytes.
func originalBytesWritten(written, prefixLength, injectedLength, originalLength int) int {
	if written <= 0 {
		return 0
	}
	if written <= prefixLength {
		return written
	}
	consumed := prefixLength
	suffixWritten := written - prefixLength - injectedLength
	if suffixWritten > 0 {
		consumed += min(suffixWritten, originalLength-prefixLength)
	}
	return min(consumed, originalLength)
}

func (o *imageOutput) writeImageUpdate(render func(io.Writer) error) error {
	var update bytes.Buffer
	if err := render(&update); err != nil {
		return err
	}
	if update.Len() == 0 {
		return nil
	}
	if _, err := io.WriteString(o.output, terminalSyncStart); err != nil {
		return err
	}
	_, writeErr := o.output.Write(update.Bytes())
	_, endErr := io.WriteString(o.output, terminalSyncEnd)
	if writeErr != nil {
		return writeErr
	}
	return endErr
}

func (o *imageOutput) Close() error {
	return nil
}

func (o *imageOutput) Fd() uintptr {
	return o.output.Fd()
}

func (o *imageOutput) clearImage() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.writeImageUpdate(func(writer io.Writer) error {
		if _, err := io.WriteString(writer, ansi.ResetModeMouseExtSgrPixel); err != nil {
			return err
		}
		for _, renderer := range o.renderers {
			if err := renderer.Clear(writer); err != nil {
				return err
			}
		}
		if len(o.renderers) == 0 {
			return nil
		}
		ids := attachmentPreviewImageIDs()
		return o.renderers[0].DeleteImages(writer, ids...)
	})
}
