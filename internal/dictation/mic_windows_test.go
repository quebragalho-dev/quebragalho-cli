//go:build windows

package dictation

import (
	"runtime"
	"testing"
	"unsafe"
)

// TestWaveHeaderSize pins the winmm WAVEHDR layout: the driver reads and
// writes these 48 bytes between calls, so any drift breaks capture.
func TestWaveHeaderSize(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skipf("WAVEHDR size pinned on amd64, running on %s", runtime.GOARCH)
	}
	if got := unsafe.Sizeof(waveHeader{}); got != 48 {
		t.Errorf("unsafe.Sizeof(waveHeader{}) = %d, want 48", got)
	}
}
