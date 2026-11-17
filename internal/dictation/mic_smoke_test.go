package dictation

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// TestNativeMicSmoke opens the real microphone for 2s through
// NewRecorder(...).StartStreaming(), like the Maestro does on macOS and
// Windows. Opt-in:
//
//	QG_MIC_SMOKE=1 go test ./internal/dictation/ -run NativeMicSmoke -v
func TestNativeMicSmoke(t *testing.T) {
	if os.Getenv("QG_MIC_SMOKE") != "1" {
		t.Skip("set QG_MIC_SMOKE=1 to open the real microphone")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("native capture only exists on darwin and windows")
	}
	r := NewRecorder(RecorderOptions{Platform: DetectPlatform(), SampleRate: DefaultSampleRate})
	chunks, stop, err := r.StartStreaming()
	if err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	defer stop()
	deadline := time.After(2 * time.Second)
	var total int
	var peak int16
	for {
		select {
		case <-deadline:
			_ = stop()
			goto done
		case c, ok := <-chunks:
			if !ok {
				goto done
			}
			total += len(c)
			if p := chunkPeak(c); p > peak {
				peak = p
			}
		}
	}
done:
	t.Logf("native mic: %d bytes in 2s (%.2fs of audio), peak %d", total, float64(total)/float64(DefaultSampleRate*2), peak)
	if total == 0 {
		t.Fatal("no audio bytes arrived from the native microphone")
	}
}
