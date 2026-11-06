package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// Narrow terminals must not wrap the pixel wordmark: below its width the
// version banner falls back to the plain name and tagline.
func TestWordmarkForWidthFallsBackOnNarrowTerminals(t *testing.T) {
	for _, width := range []int{40, 60} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			got := WordmarkForWidth(width)
			if strings.Contains(got, "█") || strings.Contains(got, "▀") || strings.Contains(got, "▄") {
				t.Fatalf("width %d must not draw pixel art: %q", width, got)
			}
			if !strings.Contains(got, "quebragalho") {
				t.Fatalf("width %d must keep the plain name: %q", width, got)
			}
			if !strings.Contains(got, emptyStateTagline+" "+emptyStateHeart) {
				t.Fatalf("width %d must keep the tagline: %q", width, got)
			}
			for _, line := range strings.Split(got, "\n") {
				if w := lipgloss.Width(line); w > width {
					t.Fatalf("width %d: line %q is %d cells", width, line, w)
				}
			}
		})
	}
}

func TestWordmarkForWidthKeepsArtOnWideTerminals(t *testing.T) {
	for _, width := range []int{61, 80} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			if got, want := WordmarkForWidth(width), Wordmark(); got != want {
				t.Fatalf("width %d:\n got %q\nwant %q", width, got, want)
			}
		})
	}
}

func TestWordmarkForWidthUnknownKeepsArt(t *testing.T) {
	for _, width := range []int{0, -1} {
		if got, want := WordmarkForWidth(width), Wordmark(); got != want {
			t.Fatalf("width %d must keep current behavior:\n got %q\nwant %q", width, got, want)
		}
	}
}
