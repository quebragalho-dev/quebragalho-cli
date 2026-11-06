package tui

import (
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

func BenchmarkWorkingStatusLine(b *testing.B) {
	previous := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	defer func() { lipgloss.Writer.Profile = previous }()

	m := newModel(b.Context(), Options{})
	m.width = 100
	m.pending = true
	m.reducedMotion = false
	m.spinnerPhase = 7
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.workingStatusLine()
	}
}
