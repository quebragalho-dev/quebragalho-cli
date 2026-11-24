package termimage

import (
	"image"
	"image/color"
	"testing"
)

func TestShrinkToFitKeepsAspectWithinBox(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3024, 1964))
	got := ShrinkToFit(src, 180, 144).Bounds()
	if got.Dx() > 180 || got.Dy() > 144 {
		t.Fatalf("shrunk bounds = %v, want within 180x144", got)
	}
	if got.Dx() != 180 && got.Dy() != 144 {
		t.Fatalf("shrunk bounds = %v, want one side touching the box", got)
	}
	wantRatio := 3024.0 / 1964.0
	gotRatio := float64(got.Dx()) / float64(got.Dy())
	if d := gotRatio - wantRatio; d > 0.02 || d < -0.02 {
		t.Fatalf("aspect ratio = %.3f, want %.3f", gotRatio, wantRatio)
	}
}

func TestShrinkToFitExtremeAspectNeverCollapsesToZero(t *testing.T) {
	for _, tc := range []struct{ w, h, wantW, wantH int }{
		{20000, 1, 180, 1},
		{1, 20000, 1, 144},
	} {
		src := image.NewNRGBA(image.Rect(0, 0, tc.w, tc.h))
		got := ShrinkToFit(src, 180, 144).Bounds()
		if got.Dx() != tc.wantW || got.Dy() != tc.wantH {
			t.Fatalf("%dx%d shrunk to %v, want %dx%d", tc.w, tc.h, got, tc.wantW, tc.wantH)
		}
	}
}

func TestShrinkToFitNeverEnlarges(t *testing.T) {
	for _, size := range []image.Rectangle{image.Rect(0, 0, 40, 30), image.Rect(0, 0, 180, 144)} {
		src := image.NewNRGBA(size)
		if got := ShrinkToFit(src, 180, 144); got != image.Image(src) {
			t.Fatalf("%v was replaced by %v; images that already fit must stay untouched", size, got.Bounds())
		}
	}
}

func TestShrinkToFitPreservesTransparency(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 400, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 400; x++ {
			c := color.NRGBA{R: 200, G: 40, B: 40, A: 255}
			if x < 200 {
				c.A = 0
			}
			src.SetNRGBA(x, y, c)
		}
	}
	got := ShrinkToFit(src, 180, 144).(*image.NRGBA)
	if a := got.NRGBAAt(10, got.Bounds().Dy()/2).A; a != 0 {
		t.Fatalf("transparent area alpha = %d, want 0", a)
	}
	if a := got.NRGBAAt(got.Bounds().Dx()-10, got.Bounds().Dy()/2).A; a != 255 {
		t.Fatalf("opaque area alpha = %d, want 255", a)
	}
}
