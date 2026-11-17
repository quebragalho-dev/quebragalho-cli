package dictation

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMicBufferRoundTrip(t *testing.T) {
	b := newMicBuffer(micBufferCapBytes(DefaultSampleRate, micBufferCapHold))
	want := []byte{1, 2, 3, 4, 5}
	b.push(want)
	got := make([]byte, len(want))
	n, err := b.read(got)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != len(want) || string(got[:n]) != string(want) {
		t.Errorf("round trip = %v, want %v", got[:n], want)
	}
}

func TestMicBufferDropsOldestWhenReaderLags(t *testing.T) {
	b := newMicBuffer(100)
	b.push(bytesOf(1, 60))
	b.push(bytesOf(2, 60))
	b.push(bytesOf(3, 60))
	got := make([]byte, 100)
	n, err := b.read(got)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if n != 100 {
		t.Fatalf("read %d bytes, want the newest 100", n)
	}
	// The two oldest pushes (120 bytes) are gone; the newest 100 survive:
	// 20 bytes of the second push and all 60 of the third... precisely, the
	// tail after dropping 180-100=80 bytes: 40 of fill 2, then 60 of fill 3.
	tail := got[:n]
	for i, v := range tail[:40] {
		if v != 2 {
			t.Fatalf("tail[%d] = %d, want fill 2", i, v)
		}
	}
	for i, v := range tail[40:] {
		if v != 3 {
			t.Fatalf("tail[%d] = %d, want fill 3", 40+i, v)
		}
	}
}

func TestMicBufferCloseUnblocksReader(t *testing.T) {
	b := newMicBuffer(1 << 20)
	done := make(chan error, 1)
	go func() {
		_, err := b.read(make([]byte, 8))
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("read returned before any data or close")
	case <-time.After(100 * time.Millisecond):
	}
	b.close()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Errorf("read after close = %v, want io.EOF", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked reader was not woken by close")
	}
}

func TestMicBufferReadDrainsThenEOF(t *testing.T) {
	b := newMicBuffer(1 << 20)
	b.push([]byte{9, 9})
	b.close()
	got := make([]byte, 8)
	n, err := b.read(got)
	if err != nil || n != 2 {
		t.Fatalf("first read = %d, %v; want buffered data", n, err)
	}
	if _, err := b.read(got); !errors.Is(err, io.EOF) {
		t.Errorf("second read = %v, want io.EOF", err)
	}
}

func TestMicBufferPushAfterCloseDropped(t *testing.T) {
	b := newMicBuffer(1 << 20)
	b.close()
	b.push([]byte{1})
	b.close() // second close is fine
	if _, err := b.read(make([]byte, 8)); !errors.Is(err, io.EOF) {
		t.Errorf("read = %v, want io.EOF", err)
	}
}

func bytesOf(v byte, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = v
	}
	return b
}

func TestMicBufferConcurrentPushRead(t *testing.T) {
	b := newMicBuffer(1 << 20)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				b.push(bytesOf(byte(w), 64))
			}
		}()
	}
	go func() {
		wg.Wait()
		b.close()
	}()
	var total int
	buf := make([]byte, 256)
	for {
		n, err := b.read(buf)
		total += n
		if err != nil {
			break
		}
	}
	if total == 0 {
		t.Error("concurrent push/read delivered nothing")
	}
}

func TestEncodeWAVRoundTrip(t *testing.T) {
	for _, rate := range []int{16000, 24000} {
		pcm := loudPCM(t, 2)
		wav := encodeWAV(pcm, rate)
		if SniffFormat(wav) != FormatWAV {
			t.Fatalf("rate %d: SniffFormat = %q, want wav", rate, SniffFormat(wav))
		}
		backRate, data, err := decodeWAVHeader(wav)
		if err != nil {
			t.Fatalf("rate %d: %v", rate, err)
		}
		if backRate != rate {
			t.Errorf("rate %d: header says %d", rate, backRate)
		}
		if string(data) != string(pcm) {
			t.Errorf("rate %d: data mismatch", rate)
		}
	}
}

func TestCheckMicrophoneLoud(t *testing.T) {
	r := DefaultSampleRate
	chunk := chunkSizeBytes(r)
	pcm := make([]byte, chunk*4)
	for i := 0; i+1 < len(pcm); i += 2 {
		pcm[i], pcm[i+1] = 0x00, 0x40
	}
	m, err := CheckMicrophone(context.Background(), RecorderOptions{
		Platform: PlatformDarwin,
		openMic:  func(int) (micStream, error) { return newBytesMicStream(pcm), nil },
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("CheckMicrophone: %v", err)
	}
	if m.Peak <= 0 {
		t.Errorf("Peak = %f, want > 0", m.Peak)
	}
	if m.Silent {
		t.Error("loud signal must not be Silent")
	}
}

func TestCheckMicrophoneAllZeros(t *testing.T) {
	pcm := make([]byte, chunkSizeBytes(DefaultSampleRate)*4)
	m, err := CheckMicrophone(context.Background(), RecorderOptions{
		Platform: PlatformDarwin,
		openMic:  func(int) (micStream, error) { return newBytesMicStream(pcm), nil },
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("CheckMicrophone: %v", err)
	}
	if m.Peak != 0 {
		t.Errorf("Peak = %f, want 0", m.Peak)
	}
	if !m.Silent {
		t.Error("all-zero signal must be Silent")
	}
}

func TestCheckMicrophoneSingleNonZero(t *testing.T) {
	pcm := make([]byte, chunkSizeBytes(DefaultSampleRate)*2)
	pcm[100] = 1
	m, err := CheckMicrophone(context.Background(), RecorderOptions{
		Platform: PlatformWindows,
		openMic:  func(int) (micStream, error) { return newBytesMicStream(pcm), nil },
	}, 5*time.Second)
	if err != nil {
		t.Fatalf("CheckMicrophone: %v", err)
	}
	if m.Silent {
		t.Error("one non-zero sample must clear Silent")
	}
	if m.Peak <= 0 {
		t.Errorf("Peak = %f, want > 0", m.Peak)
	}
}

func TestCheckMicrophoneFailure(t *testing.T) {
	_, err := CheckMicrophone(context.Background(), RecorderOptions{
		Platform: PlatformDarwin,
		openMic:  failingOpenMic(),
		lookPath: missingLookPath,
	}, time.Second)
	var setupErr *SetupError
	if !errors.As(err, &setupErr) {
		t.Errorf("want *SetupError, got %v", err)
	}
}

func TestMicBufferCapBytes(t *testing.T) {
	// 16kHz mono PCM16 holds 2s in 64000 bytes.
	if got := micBufferCapBytes(DefaultSampleRate, micBufferCapHold); got != 64000 {
		t.Errorf("micBufferCapBytes = %d, want 64000", got)
	}
}

func TestUseNativeMic(t *testing.T) {
	cases := []struct {
		name string
		opts RecorderOptions
		want bool
	}{
		{"linux", RecorderOptions{Platform: PlatformLinux}, false},
		{"termux", RecorderOptions{Platform: PlatformTermux}, false},
		{"darwin", RecorderOptions{Platform: PlatformDarwin}, true},
		{"windows", RecorderOptions{Platform: PlatformWindows}, true},
		{"windows device", RecorderOptions{Platform: PlatformWindows, WindowsAudioDevice: "Mic"}, false},
		{"darwin forced program", RecorderOptions{Platform: PlatformDarwin, forceProgram: true}, false},
		{"windows forced program", RecorderOptions{Platform: PlatformWindows, forceProgram: true}, false},
	}
	for _, tc := range cases {
		if got := tc.opts.withDefaults().useNativeMic(); got != tc.want {
			t.Errorf("%s: useNativeMic = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCheckCaptureToolNative(t *testing.T) {
	if err := CheckCaptureTool(PlatformDarwin, missingLookPath); err != nil {
		t.Errorf("darwin with no program should be nil, got %v", err)
	}
	if err := CheckCaptureTool(PlatformWindows, missingLookPath); err != nil {
		t.Errorf("windows with no program should be nil, got %v", err)
	}
	for _, p := range []Platform{PlatformLinux, PlatformTermux} {
		var setupErr *SetupError
		if err := CheckCaptureTool(p, missingLookPath); !errors.As(err, &setupErr) {
			t.Errorf("%s with no program should be *SetupError, got %v", p, err)
		}
		if err := CheckCaptureTool(p, okLookPath); err != nil {
			t.Errorf("%s with the program should be nil, got %v", p, err)
		}
	}
	if err := CheckCaptureTool(PlatformUnsupported, okLookPath); err == nil ||
		!strings.Contains(err.Error(), "ditado não é suportado nesta plataforma") {
		t.Errorf("unsupported platform should say ditado não é suportado, got %v", err)
	}
}

func TestCheckCaptureToolProgramOverride(t *testing.T) {
	t.Setenv("QG_STT_CAPTURE", "program")
	for _, p := range []Platform{PlatformDarwin, PlatformWindows, PlatformLinux} {
		var setupErr *SetupError
		if err := CheckCaptureTool(p, missingLookPath); !errors.As(err, &setupErr) {
			t.Errorf("%s with override and no program should be *SetupError, got %v", p, err)
		}
		if err := CheckCaptureTool(p, okLookPath); err != nil {
			t.Errorf("%s with override and the program should be nil, got %v", p, err)
		}
	}
}
