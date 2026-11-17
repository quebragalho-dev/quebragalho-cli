package dictation

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// micStream is an open microphone delivering raw little-endian PCM16 mono at
// the sample rate it was opened with.
type micStream interface {
	// Read blocks until audio is available. It returns io.EOF after Close.
	Read(p []byte) (int, error)
	// Close stops the device and releases it. Idempotent, safe from any goroutine.
	Close() error
}

// errNoNativeMic: this build has no in-process capture (Linux, Termux).
var errNoNativeMic = errors.New("no native microphone capture on this platform")

// useNativeMic reports whether a recording with these options goes through
// the in-process microphone instead of the external capture program. A user
// who named a Windows FFmpeg device keeps the FFmpeg path, and
// QG_STT_CAPTURE=program forces the program path on every system.
func (o RecorderOptions) useNativeMic() bool {
	if o.forceProgram {
		return false
	}
	switch o.Platform {
	case PlatformDarwin:
		return true
	case PlatformWindows:
		return o.WindowsAudioDevice == ""
	}
	return false
}

// nativeMicSetupError maps a failed microphone open with no program to fall
// back to into setup guidance: the device may be unplugged or the terminal
// may lack the OS microphone permission.
func nativeMicSetupError(err error) *SetupError {
	return &SetupError{
		Tool: "microfone",
		Hint: fmt.Sprintf("não foi possível abrir o microfone: %v. Confira se há um microfone ligado e se o terminal tem permissão para usá-lo.", err),
	}
}

// micBufferCapHold is how far the reader may fall behind before the oldest
// audio is dropped: the callback/worker feeding the buffer never blocks.
const micBufferCapHold = 2 * time.Second

// micBufferCapBytes bounds one micBuffer to hold seconds of PCM16 mono.
func micBufferCapBytes(sampleRate int, hold time.Duration) int {
	return sampleRate * 2 * int(hold) / int(time.Second)
}

// micBuffer is a bounded FIFO of PCM bytes between the OS audio thread and
// the reader. push copies and never blocks; read blocks until audio is
// available and returns io.EOF after close.
type micBuffer struct {
	mu       sync.Mutex
	cond     *sync.Cond
	pending  []byte
	capBytes int
	closed   bool
}

func newMicBuffer(capBytes int) *micBuffer {
	b := &micBuffer{capBytes: capBytes}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *micBuffer) push(data []byte) {
	if len(data) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.pending = append(b.pending, data...)
	if len(b.pending) > b.capBytes {
		drop := len(b.pending) - b.capBytes
		b.pending = append([]byte(nil), b.pending[drop:]...)
	}
	b.cond.Broadcast()
}

func (b *micBuffer) read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.pending) == 0 {
		if b.closed {
			return 0, io.EOF
		}
		b.cond.Wait()
	}
	n := copy(p, b.pending)
	if rest := len(b.pending) - n; rest == 0 {
		b.pending = b.pending[:0]
	} else {
		copy(b.pending, b.pending[n:])
		b.pending = b.pending[:rest]
	}
	return n, nil
}

func (b *micBuffer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	b.cond.Broadcast()
}

// encodeWAV wraps raw PCM16 mono samples in a 44-byte RIFF header so
// SniffFormat and every transcriber keep working on native batch takes.
func encodeWAV(pcm []byte, sampleRate int) []byte {
	out := make([]byte, 0, 44+len(pcm))
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(36+len(pcm)))
	out = append(out, "WAVE"...)
	out = append(out, "fmt "...)
	out = binary.LittleEndian.AppendUint32(out, 16)
	out = binary.LittleEndian.AppendUint16(out, 1) // PCM
	out = binary.LittleEndian.AppendUint16(out, 1) // mono
	out = binary.LittleEndian.AppendUint32(out, uint32(sampleRate))
	out = binary.LittleEndian.AppendUint32(out, uint32(sampleRate*2)) // byte rate
	out = binary.LittleEndian.AppendUint16(out, 2)                    // block align
	out = binary.LittleEndian.AppendUint16(out, 16)                   // bits per sample
	out = append(out, "data"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(pcm)))
	out = append(out, pcm...)
	return out
}

// decodeWAVHeader reads the sample rate back from a WAV image encodeWAV wrote.
func decodeWAVHeader(wav []byte) (sampleRate int, data []byte, err error) {
	if len(wav) < 44 || string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return 0, nil, errors.New("not a WAV file")
	}
	return int(binary.LittleEndian.Uint32(wav[24:28])), wav[44:], nil
}

// MicCheck is the result of listening to the microphone for a moment.
type MicCheck struct {
	Peak   float64 // loudest sample, 0..1
	Silent bool    // every sample was exactly zero: the system is not delivering audio (no permission, or muted at the system level)
}

// CheckMicrophone records for d and reports what came in. It uses the same
// path a recording would use.
func CheckMicrophone(ctx context.Context, opts RecorderOptions, d time.Duration) (MicCheck, error) {
	r := NewRecorder(opts)
	chunks, stop, err := r.StartStreaming()
	if err != nil {
		return MicCheck{}, err
	}
	defer func() { _ = stop() }()

	var (
		peakAbs int32
		silent  = true
	)
	observe := func(chunk []byte) {
		for i := 0; i+1 < len(chunk); i += 2 {
			s := int16(uint16(chunk[i]) | uint16(chunk[i+1])<<8)
			if s != 0 {
				silent = false
			}
			a := int32(s)
			if a < 0 {
				a = -a
			}
			if a > peakAbs {
				peakAbs = a
			}
		}
	}
	result := func() (MicCheck, error) {
		m := MicCheck{Peak: float64(peakAbs) / 32768.0, Silent: silent}
		if m.Peak > 1 {
			m.Peak = 1
		}
		return m, nil
	}

	if d <= 0 {
		return result()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return MicCheck{}, ctx.Err()
		case <-timer.C:
			return result()
		case chunk, ok := <-chunks:
			if !ok {
				return result()
			}
			observe(chunk)
		}
	}
}

// nativeCaptureEnv reads the QG_STT_CAPTURE escape hatch once per
// withDefaults: "program" forces today's external-program path on every
// system. Tests set RecorderOptions.forceProgram directly, not the
// environment.
func nativeCaptureEnv() bool {
	return os.Getenv("QG_STT_CAPTURE") == "program"
}
