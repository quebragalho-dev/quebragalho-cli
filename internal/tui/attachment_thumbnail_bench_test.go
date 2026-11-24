package tui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/termimage"
)

// screenshotBlock builds a deterministic screenshot-like PNG at the size of a
// Retina print (3024x1964), the case the paste latency was measured on: flat
// panels, text-like stripes and a little texture, so the encoded size is in the
// range a real capture produces.
func screenshotBlock(tb testing.TB, width, height int) qgruntime.ImageBlock {
	tb.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	state := uint32(2463534242)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			c := color.NRGBA{R: 30, G: 30, B: 34, A: 255}
			if y > height/8 && (y/23)%3 == 0 && x > width/10 && x < width*9/10 {
				c = color.NRGBA{R: 200, G: 200, B: 205, A: 255}
				if (x*7+y*13)%11 < 4 {
					c = color.NRGBA{R: 90, G: 120, B: 200, A: 255}
				}
			}
			if x < width/12 {
				c = color.NRGBA{R: 45, G: 45, B: 52, A: 255}
			}
			// Low-amplitude texture stands in for antialiasing and photo detail;
			// without it the capture compresses far below a real 1.5 MB print.
			state ^= state << 13
			state ^= state >> 17
			state ^= state << 5
			noise := uint8(state & 1)
			c.R += noise
			c.G += noise
			c.B += noise
			img.SetNRGBA(x, y, c)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		tb.Fatal(err)
	}
	return qgruntime.ImageBlock{MediaType: "image/png", Data: encoded.Bytes()}
}

// BenchmarkAttachmentThumbnailTransmit measures what one pasted screenshot costs
// before its chip can show a preview: decoding it, preparing the thumbnail, and
// writing the Kitty placement for the composer slot. transmit-bytes is the size
// of the escape sequence the terminal has to receive.
func BenchmarkAttachmentThumbnailTransmit(b *testing.B) {
	block := screenshotBlock(b, 3024, 1964)
	support := termimage.ImageSupport{Protocol: termimage.ImageProtocolKitty}
	var out bytes.Buffer
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		thumb, err := attachmentThumbnail(block)
		if err != nil {
			b.Fatal(err)
		}
		renderer := termimage.NewImageRenderer(support)
		renderer.Set(&termimage.ImageDraw{
			ID:           attachmentImageID,
			Animation:    thumb,
			State:        termimage.Idle,
			Columns:      attachmentPreviewCols,
			Rows:         attachmentPreviewRows,
			HeightPixels: attachmentPreviewHeightPixels,
		})
		out.Reset()
		if err := renderer.Render(&out); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(out.Len()), "transmit-bytes")
}
