package dictation

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var errFakeMic = errors.New("fake microphone failure")

// bytesMicStream delivers data then EOF, like a microphone that stops.
type bytesMicStream struct {
	r      *bytes.Reader
	closed atomic.Bool
	closes atomic.Int32
}

func newBytesMicStream(data []byte) *bytesMicStream {
	return &bytesMicStream{r: bytes.NewReader(data)}
}

func (m *bytesMicStream) Read(p []byte) (int, error) { return m.r.Read(p) }

func (m *bytesMicStream) Close() error {
	m.closed.Store(true)
	m.closes.Add(1)
	return nil
}

// infiniteMicStream delivers non-zero audio forever until closed.
type infiniteMicStream struct {
	closed atomic.Bool
	closes atomic.Int32
}

func (m *infiniteMicStream) Read(p []byte) (int, error) {
	if m.closed.Load() {
		return 0, io.EOF
	}
	for i := range p {
		p[i] = 1
	}
	return len(p), nil
}

func (m *infiniteMicStream) Close() error {
	m.closed.Store(true)
	m.closes.Add(1)
	return nil
}

// loudThenSilentMicStream delivers one loud chunk, then digital silence
// forever. It keeps delivering after Close so silence tests isolate the
// auto-stop logic from the close race; Close only records that it ran.
type loudThenSilentMicStream struct {
	chunk  int
	loud   []byte
	closes atomic.Int32
}

func (m *loudThenSilentMicStream) Read(p []byte) (int, error) {
	if m.chunk == 0 {
		m.chunk++
		copy(p, m.loud)
		return len(p), nil
	}
	clear(p)
	return len(p), nil
}

func (m *loudThenSilentMicStream) Close() error {
	m.closes.Add(1)
	return nil
}

func failingOpenMic() func(int) (micStream, error) {
	return func(int) (micStream, error) { return nil, errFakeMic }
}

func refuseOpenMic(t *testing.T) func(int) (micStream, error) {
	t.Helper()
	return func(int) (micStream, error) {
		t.Error("openMic must not be called on this path")
		return nil, errFakeMic
	}
}

func okLookPath(string) (string, error) { return "/usr/bin/fake-tool", nil }

func missingLookPath(string) (string, error) { return "", exec.ErrNotFound }

func loudPCM(t *testing.T, chunks int) []byte {
	t.Helper()
	chunk := chunkSizeBytes(DefaultSampleRate)
	pcm := make([]byte, chunk*chunks)
	for i := 0; i+1 < len(pcm); i += 2 {
		pcm[i], pcm[i+1] = 0x00, 0x40 // +16384, well above the silence threshold
	}
	return pcm
}

func TestNativeStreamingChunksAndStop(t *testing.T) {
	rate := DefaultSampleRate
	pcm := make([]byte, chunkSizeBytes(rate)*6)
	for i := range pcm {
		pcm[i] = byte(i)
	}
	stream := newBytesMicStream(pcm)
	var calls atomic.Int32
	r := NewRecorder(RecorderOptions{
		Platform: PlatformDarwin,
		openMic: func(sampleRate int) (micStream, error) {
			calls.Add(1)
			if sampleRate != rate {
				t.Errorf("openMic rate = %d, want %d", sampleRate, rate)
			}
			return stream, nil
		},
		tempDir: testTempDir(t),
	})
	chunks, stop, err := r.StartStreaming()
	if err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	var got []byte
	for c := range chunks {
		if len(c) != chunkSizeBytes(rate) {
			t.Errorf("chunk size = %d, want %d", len(c), chunkSizeBytes(rate))
		}
		got = append(got, c...)
	}
	if !bytes.Equal(got, pcm) {
		t.Errorf("streamed %d bytes, want %d with identical content", len(got), len(pcm))
	}
	if err := stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := stop(); err != nil {
		t.Fatalf("second stop should be a no-op, got %v", err)
	}
	if !stream.closed.Load() {
		t.Error("stop did not close the microphone stream")
	}
	if calls.Load() != 1 {
		t.Errorf("openMic called %d times, want 1", calls.Load())
	}
}

func TestNativeStreamingMaxDuration(t *testing.T) {
	r := NewRecorder(RecorderOptions{
		Platform:    PlatformDarwin,
		MaxDuration: 150 * time.Millisecond,
		openMic:     func(int) (micStream, error) { return &infiniteMicStream{}, nil },
		tempDir:     testTempDir(t),
	})
	chunks, stop, err := r.StartStreaming()
	if err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	defer stop()
	drained := make(chan int)
	go func() {
		total := 0
		for c := range chunks {
			total += len(c)
		}
		drained <- total
	}()
	select {
	case total := <-drained:
		if total == 0 {
			t.Error("max-duration capture delivered no audio")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("max-duration cap did not end the native capture")
	}
}

func TestNativeStreamingSilenceAutoStop(t *testing.T) {
	rate := DefaultSampleRate
	loud := loudPCM(t, 1)
	silence := make([]byte, chunkSizeBytes(rate))
	var buf []byte
	buf = append(buf, loud...)
	// Far more silence than the 2s hold: auto-stop must end the capture
	// early instead of draining everything.
	const totalSilent = 200
	for i := 0; i < totalSilent; i++ {
		buf = append(buf, silence...)
	}
	r := NewRecorder(RecorderOptions{
		Platform:        PlatformDarwin,
		SampleRate:      rate,
		SilenceAutoStop: true,
		openMic:         func(int) (micStream, error) { return newBytesMicStream(buf), nil },
		tempDir:         testTempDir(t),
	})
	chunks, stop, err := r.StartStreaming()
	if err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	defer stop()
	var total int
	drained := make(chan struct{})
	go func() {
		for c := range chunks {
			total += len(c)
		}
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatal("silence auto-stop did not end the native stream")
	}
	// Speech + the 2s hold, plus a few chunks of scheduling slack — but far
	// from the whole buffer. Without auto-stop every chunk would arrive.
	limit := (1 + int(silenceHold/chunkInterval) + 20) * chunkSizeBytes(rate)
	if total > limit {
		t.Errorf("auto-stop delivered %d bytes, want under %d (early close)", total, limit)
	}
}

func TestNativeStreamingSilenceKeptWithoutAutoStop(t *testing.T) {
	rate := DefaultSampleRate
	loud := loudPCM(t, 1)
	silence := make([]byte, chunkSizeBytes(rate))
	var buf []byte
	buf = append(buf, loud...)
	for i := 0; i < 50; i++ {
		buf = append(buf, silence...)
	}
	r := NewRecorder(RecorderOptions{
		Platform:   PlatformWindows,
		SampleRate: rate,
		openMic:    func(int) (micStream, error) { return newBytesMicStream(buf), nil },
		tempDir:    testTempDir(t),
	})
	chunks, stop, err := r.StartStreaming()
	if err != nil {
		t.Fatalf("StartStreaming: %v", err)
	}
	defer stop()
	var total int
	for c := range chunks {
		total += len(c)
	}
	if total != len(buf) {
		t.Errorf("streamed %d bytes, want all %d", total, len(buf))
	}
}

func TestNativeBatchWAVRoundTrip(t *testing.T) {
	rate := DefaultSampleRate
	pcm := loudPCM(t, 3)
	r := NewRecorder(RecorderOptions{
		Platform: PlatformDarwin,
		openMic:  func(int) (micStream, error) { return newBytesMicStream(pcm), nil },
		tempDir:  testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := r.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if SniffFormat(got) != FormatWAV {
		t.Fatalf("native batch should be WAV, got format %q", SniffFormat(got))
	}
	backRate, data, err := decodeWAVHeader(got)
	if err != nil {
		t.Fatalf("decodeWAVHeader: %v", err)
	}
	if backRate != rate {
		t.Errorf("WAV rate = %d, want %d", backRate, rate)
	}
	if !bytes.Equal(data, pcm) {
		t.Errorf("WAV data is %d bytes, want the %d delivered bytes", len(data), len(pcm))
	}
}

func TestNativeBatchEmptyRecording(t *testing.T) {
	r := NewRecorder(RecorderOptions{
		Platform: PlatformWindows,
		openMic:  func(int) (micStream, error) { return newBytesMicStream(nil), nil },
		tempDir:  testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	_, err := r.Stop()
	if err == nil || err.Error() != "no audio was captured (is the microphone available?)" {
		t.Errorf("empty native take should report no audio, got %v", err)
	}
}

func TestNativeBatchDoubleStart(t *testing.T) {
	r := NewRecorder(RecorderOptions{
		Platform: PlatformDarwin,
		openMic:  func(int) (micStream, error) { return newBytesMicStream(loudPCM(t, 1)), nil },
		tempDir:  testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Start(); !errors.Is(err, errAlreadyRecording) {
		t.Errorf("second Start should be rejected, got %v", err)
	}
	if _, err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := r.Start(); err != nil {
		t.Fatalf("Start after Stop should work, got %v", err)
	}
	if _, err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestNativeBatchMaxDurationBound(t *testing.T) {
	// More audio than the 1s cap: the bound, not EOF, must end the take.
	over := loudPCM(t, 3*DefaultSampleRate*2/chunkSizeBytes(DefaultSampleRate))
	r := NewRecorder(RecorderOptions{
		Platform:    PlatformWindows,
		MaxDuration: time.Second,
		openMic:     func(int) (micStream, error) { return newBytesMicStream(over), nil },
		tempDir:     testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := r.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_, data, err := decodeWAVHeader(got)
	if err != nil {
		t.Fatalf("decodeWAVHeader: %v", err)
	}
	want := DefaultSampleRate * 2 * 1
	if len(data) != want {
		t.Errorf("batch capped at %d bytes, want %d (1s of PCM)", len(data), want)
	}
}

func TestNativeBatchSilenceAutoStop(t *testing.T) {
	rate := DefaultSampleRate
	loud := loudPCM(t, 1)
	r := NewRecorder(RecorderOptions{
		Platform:        PlatformDarwin,
		SampleRate:      rate,
		SilenceAutoStop: true,
		openMic: func(int) (micStream, error) {
			return &loudThenSilentMicStream{loud: loud}, nil
		},
		tempDir: testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := r.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	_, data, err := decodeWAVHeader(got)
	if err != nil {
		t.Fatalf("decodeWAVHeader: %v", err)
	}
	want := (1 + int(silenceHold/chunkInterval)) * chunkSizeBytes(rate)
	if len(data) != want {
		t.Errorf("auto-stopped take is %d bytes, want %d (speech + 2s silence)", len(data), want)
	}
}

func TestNativeBatchStopUnblocksAccumulation(t *testing.T) {
	stream := &infiniteMicStream{}
	r := NewRecorder(RecorderOptions{
		Platform: PlatformDarwin,
		openMic:  func(int) (micStream, error) { return stream, nil },
		tempDir:  testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := r.Stop(); done <- err }()
	select {
	case err := <-done:
		// Either some audio arrived or none did yet — both are honest.
		// Anything else (or a hang) means Close did not release the reader.
		if err != nil && !strings.Contains(err.Error(), "no audio was captured") {
			t.Errorf("Stop: %v", err)
		}
		if stream.closes.Load() != 1 {
			t.Errorf("Stop closed the stream %d times, want 1", stream.closes.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop hung: closing the stream did not unblock accumulation")
	}
}

func TestNativeFallbackToProgram(t *testing.T) {
	cap := &captured{}
	r := NewRecorder(RecorderOptions{
		Platform: PlatformDarwin,
		openMic:  failingOpenMic(),
		lookPath: okLookPath,
		starter:  fileWritingStarter(t, cap, wavFixture()),
		tempDir:  testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start should fall back to the program, got %v", err)
	}
	if _, err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if cap.spec.name != "sox" {
		t.Errorf("fallback tool = %q, want sox", cap.spec.name)
	}
}

func TestNativeFallbackToProgramStreaming(t *testing.T) {
	pcm := loudPCM(t, 2)
	var launched bool
	starter := func(spec commandSpec) (processHandle, io.ReadCloser, error) {
		launched = true
		if spec.name != "sox" {
			t.Errorf("fallback tool = %q, want sox", spec.name)
		}
		return newFakeProcess(), io.NopCloser(bytes.NewReader(pcm)), nil
	}
	r := NewRecorder(RecorderOptions{
		Platform: PlatformDarwin,
		openMic:  failingOpenMic(),
		lookPath: okLookPath,
		starter:  starter,
		tempDir:  testTempDir(t),
	})
	chunks, stop, err := r.StartStreaming()
	if err != nil {
		t.Fatalf("StartStreaming should fall back to the program, got %v", err)
	}
	defer stop()
	var total int
	for c := range chunks {
		total += len(c)
	}
	if !launched {
		t.Error("fallback never launched the program")
	}
	if total != len(pcm) {
		t.Errorf("streamed %d bytes, want %d", total, len(pcm))
	}
}

func TestNativeNoProgramSetupError(t *testing.T) {
	for _, platform := range []Platform{PlatformDarwin, PlatformWindows} {
		r := NewRecorder(RecorderOptions{
			Platform: platform,
			openMic:  failingOpenMic(),
			lookPath: missingLookPath,
			starter: func(commandSpec) (processHandle, io.ReadCloser, error) {
				t.Error("no program means no launch")
				return nil, nil, nil
			},
			tempDir: testTempDir(t),
		})
		err := r.Start()
		var setupErr *SetupError
		if !errors.As(err, &setupErr) {
			t.Errorf("%s Start: want *SetupError, got %v", platform, err)
		} else {
			if setupErr.Tool != "microfone" {
				t.Errorf("%s SetupError.Tool = %q, want microfone", platform, setupErr.Tool)
			}
			if !strings.Contains(setupErr.Hint, "não foi possível abrir o microfone") {
				t.Errorf("%s hint missing the open failure: %q", platform, setupErr.Hint)
			}
		}
		_, _, err = r.StartStreaming()
		if !errors.As(err, &setupErr) {
			t.Errorf("%s StartStreaming: want *SetupError, got %v", platform, err)
		}
	}
}

func TestWindowsAudioDeviceKeepsProgramPath(t *testing.T) {
	cap := &captured{}
	r := NewRecorder(RecorderOptions{
		Platform:           PlatformWindows,
		WindowsAudioDevice: "Microphone (Realtek)",
		openMic:            refuseOpenMic(t),
		starter:            fileWritingStarter(t, cap, wavFixture()),
		tempDir:            testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if cap.spec.name != "ffmpeg" {
		t.Errorf("tool = %q, want ffmpeg", cap.spec.name)
	}
}

func TestProgramPlatformsNeverOpenMic(t *testing.T) {
	// Linux batch + streaming.
	linux := NewRecorder(RecorderOptions{
		Platform: PlatformLinux,
		openMic:  refuseOpenMic(t),
		starter:  fileWritingStarter(t, &captured{}, wavFixture()),
		tempDir:  testTempDir(t),
	})
	if err := linux.Start(); err != nil {
		t.Fatalf("linux Start: %v", err)
	}
	if _, err := linux.Stop(); err != nil {
		t.Fatalf("linux Stop: %v", err)
	}
	streamer := NewRecorder(RecorderOptions{
		Platform: PlatformLinux,
		openMic:  refuseOpenMic(t),
		starter: func(commandSpec) (processHandle, io.ReadCloser, error) {
			return newFakeProcess(), io.NopCloser(bytes.NewReader(loudPCM(t, 1))), nil
		},
		tempDir: testTempDir(t),
	})
	chunks, stop, err := streamer.StartStreaming()
	if err != nil {
		t.Fatalf("linux StartStreaming: %v", err)
	}
	for range chunks {
	}
	_ = stop()

	// Termux batch.
	dir := t.TempDir()
	termuxRun := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "termux-microphone-record" {
			t.Errorf("termux tool = %q", name)
		}
		if len(args) > 0 && args[0] == "-l" {
			_ = os.WriteFile(args[len(args)-1], m4aFixture(), 0o600)
		}
		return nil, nil
	}
	termux := NewRecorder(RecorderOptions{
		Platform:  PlatformTermux,
		openMic:   refuseOpenMic(t),
		runOutput: termuxRun,
		tempDir:   func() (string, error) { return dir, nil },
	})
	if err := termux.Start(); err != nil {
		t.Fatalf("termux Start: %v", err)
	}
	if _, err := termux.Stop(); err != nil {
		t.Fatalf("termux Stop: %v", err)
	}
}

func TestProgramOverrideEnvForcesProgramPath(t *testing.T) {
	t.Setenv("QG_STT_CAPTURE", "program")
	var micCalls atomic.Int32
	cap := &captured{}
	r := NewRecorder(RecorderOptions{
		Platform: PlatformDarwin,
		lookPath: missingLookPath,
		openMic: func(int) (micStream, error) {
			micCalls.Add(1)
			return nil, errFakeMic
		},
		starter: fileWritingStarter(t, cap, wavFixture()),
		tempDir: testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start with QG_STT_CAPTURE=program should use the program, got %v", err)
	}
	if _, err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if cap.spec.name != "sox" {
		t.Errorf("tool = %q, want sox", cap.spec.name)
	}
	if micCalls.Load() != 0 {
		t.Errorf("openMic called %d times with the override set, want 0", micCalls.Load())
	}
}

func TestProgramOverrideForcesProgramPath(t *testing.T) {
	cap := &captured{}
	r := NewRecorder(RecorderOptions{
		Platform:     PlatformDarwin,
		forceProgram: true,
		lookPath:     okLookPath,
		openMic:      refuseOpenMic(t),
		starter:      fileWritingStarter(t, cap, wavFixture()),
		tempDir:      testTempDir(t),
	})
	if err := r.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if cap.spec.name != "sox" {
		t.Errorf("tool = %q, want sox", cap.spec.name)
	}
}
