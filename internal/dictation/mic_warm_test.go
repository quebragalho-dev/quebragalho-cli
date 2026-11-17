package dictation

import (
	"errors"
	"io"
	"testing"
)

type fakeWarmStream struct{ closed bool }

func (f *fakeWarmStream) Read([]byte) (int, error) { return 0, io.EOF }
func (f *fakeWarmStream) Close() error             { f.closed = true; return nil }

func resetWarmMic(t *testing.T) {
	t.Helper()
	clear := func() {
		warmMic.mu.Lock()
		warmMic.stream = nil
		warmMic.mu.Unlock()
	}
	clear()
	t.Cleanup(clear)
}

// TestWarmMicrophoneIsTakenOver: the warmed stream is the one the next
// recording gets, it is opened once, and it is handed over only once.
func TestWarmMicrophoneIsTakenOver(t *testing.T) {
	resetWarmMic(t)
	fake := &fakeWarmStream{}
	opens := 0
	opts := RecorderOptions{Platform: PlatformDarwin, SampleRate: 16000, openMic: func(int) (micStream, error) {
		opens++
		return fake, nil
	}}
	WarmMicrophone(opts)
	WarmMicrophone(opts)
	if opens != 1 {
		t.Fatalf("microphone opened %d times, want 1", opens)
	}
	warmMic.mu.Lock()
	got := warmMic.stream
	warmMic.mu.Unlock()
	if got != micStream(fake) {
		t.Fatal("warmed stream was not kept for the next recording")
	}
	stream, err := openMicPreferWarm(16000)
	if err != nil || stream != micStream(fake) {
		t.Fatalf("openMicPreferWarm = (%v, %v), want the warmed stream", stream, err)
	}
	if fake.closed {
		t.Fatal("the stream handed to the recording must stay open")
	}
	warmMic.mu.Lock()
	left := warmMic.stream
	warmMic.mu.Unlock()
	if left != nil {
		t.Fatal("the warmed stream must be handed over only once")
	}
}

// TestWarmMicrophoneSkipsProgramCapture: platforms that record through an
// external program, and a failed open, leave nothing warmed.
func TestWarmMicrophoneSkipsProgramCapture(t *testing.T) {
	resetWarmMic(t)
	opens := 0
	open := func(int) (micStream, error) { opens++; return &fakeWarmStream{}, nil }
	WarmMicrophone(RecorderOptions{Platform: PlatformLinux, openMic: open})
	WarmMicrophone(RecorderOptions{Platform: PlatformWindows, WindowsAudioDevice: "Mic", openMic: open})
	if opens != 0 {
		t.Fatalf("microphone opened %d times where capture is a program, want 0", opens)
	}
	WarmMicrophone(RecorderOptions{Platform: PlatformDarwin, openMic: func(int) (micStream, error) { return nil, errors.New("no device") }})
	warmMic.mu.Lock()
	defer warmMic.mu.Unlock()
	if warmMic.stream != nil {
		t.Fatal("a failed open must leave nothing warmed")
	}
}
