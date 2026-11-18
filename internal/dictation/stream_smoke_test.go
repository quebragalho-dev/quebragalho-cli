package dictation

import (
	"context"
	"encoding/binary"
	"os"
	"testing"
	"time"
)

// TestLocalStreamingRealBinary drives the full local live path against a REAL
// sherpa-onnx binary + streaming model: wrap the online file decoder in
// rollingTranscriber, feed a wav's PCM in 50ms chunks, and check the transcript.
// Opt-in:
//
//	QG_STT_STREAM_BINARY=/path/to/sherpa-onnx \
//	QG_STT_STREAM_MODEL=/path/to/streaming-model-dir \
//	QG_STT_STREAM_WAV=/path/to/mono-pcm16.wav \
//	go test ./internal/dictation/ -run RealBinary -v -timeout 5m
func TestLocalStreamingRealBinary(t *testing.T) {
	bin := os.Getenv("QG_STT_STREAM_BINARY")
	model := os.Getenv("QG_STT_STREAM_MODEL")
	wav := os.Getenv("QG_STT_STREAM_WAV")
	if bin == "" || model == "" || wav == "" {
		t.Skip("set QG_STT_STREAM_BINARY, QG_STT_STREAM_MODEL, QG_STT_STREAM_WAV to run the streaming smoke test")
	}
	raw, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	sampleRate, pcm := parseWavPCM(raw)

	inner, err := NewLocalTranscriber(LocalConfig{Binary: bin, ModelPath: model, Online: true})
	if err != nil {
		t.Fatalf("NewLocalTranscriber: %v", err)
	}
	tr := NewRollingTranscriber(inner, RollingOptions{SampleRate: sampleRate})

	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		size := chunkSizeBytes(sampleRate) // 50ms
		for off := 0; off < len(pcm); off += size {
			end := off + size
			if end > len(pcm) {
				end = len(pcm)
			}
			chunks <- pcm[off:end]
			time.Sleep(10 * time.Millisecond) // pace it a little, like a mic
		}
	}()

	var partials int
	final, err := tr.StreamTranscribe(context.Background(), chunks, func(string, bool) { partials++ })
	if err != nil {
		t.Fatalf("StreamTranscribe: %v", err)
	}
	if final == "" {
		t.Fatal("streaming returned empty transcript")
	}
	t.Logf("streaming transcript (%d partials): %q", partials, final)
}

func parseWavPCM(b []byte) (int, []byte) {
	if len(b) > 44 && string(b[:4]) == "RIFF" {
		rate := int(binary.LittleEndian.Uint32(b[24:28]))
		if rate <= 0 {
			rate = DefaultSampleRate
		}
		return rate, b[44:]
	}
	return DefaultSampleRate, b
}
