package dictation

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

func TestSherpaCAPIOnlineConfigLayout(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("64-bit layout pin")
	}
	if got := unsafe.Sizeof(cOnlineModelConfig{}); got != 136 {
		t.Fatalf("sizeof(cOnlineModelConfig) = %d, want 136", got)
	}
	if got := unsafe.Sizeof(cOnlineRecognizerConfig{}); got != 272 {
		t.Fatalf("sizeof(cOnlineRecognizerConfig) = %d, want 272", got)
	}
	checks := []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{"featConfig", unsafe.Offsetof(cOnlineRecognizerConfig{}.featConfig), 0},
		{"modelConfig", unsafe.Offsetof(cOnlineRecognizerConfig{}.modelConfig), 8},
		{"decodingMethod", unsafe.Offsetof(cOnlineRecognizerConfig{}.decodingMethod), 144},
		{"maxActivePaths", unsafe.Offsetof(cOnlineRecognizerConfig{}.maxActivePaths), 152},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("offsetof(%s) = %d, want %d", c.name, c.got, c.want)
		}
	}
}

func TestFindSherpaCAPILibrary(t *testing.T) {
	libName := sherpaCAPILibName()
	if libName == "" {
		t.Skip("platform has no sherpa C API library")
	}

	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	libDir := filepath.Join(root, "lib")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(binDir, "sherpa-onnx")
	if err := os.WriteFile(binPath, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := FindSherpaCAPILibrary(binPath); got != "" {
		t.Fatalf("expected empty when library is absent, got %q", got)
	}

	// Unix/macOS layout: ../lib/libsherpa-onnx-c-api.{so,dylib}.
	libInLib := filepath.Join(libDir, libName)
	if err := os.WriteFile(libInLib, []byte("lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindSherpaCAPILibrary(binPath); got != libInLib {
		t.Fatalf("FindSherpaCAPILibrary(../lib) = %q, want %q", got, libInLib)
	}

	// Sibling in bin/ takes precedence (Windows layout).
	libInBin := filepath.Join(binDir, libName)
	if err := os.WriteFile(libInBin, []byte("lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindSherpaCAPILibrary(binPath); got != libInBin {
		t.Fatalf("FindSherpaCAPILibrary(bin) = %q, want %q", got, libInBin)
	}
}

func TestDetectOnlineModelSpec(t *testing.T) {
	krokoDir := filepath.Join(t.TempDir(), "sherpa-onnx-streaming-zipformer-en-kroko-2025-08-06")
	if err := os.MkdirAll(krokoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(krokoDir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec, ok, err := detectOnlineModelSpec(krokoDir)
	if err != nil || !ok {
		t.Fatalf("kroko spec = (%+v, %v, %v), want ok=true", spec, ok, err)
	}
	if spec.modelType != "zipformer2" {
		t.Errorf("kroko modelType = %q, want zipformer2", spec.modelType)
	}
	if spec.encoder == "" || spec.decoder == "" || spec.joiner == "" || spec.tokens == "" {
		t.Errorf("incomplete transducer spec: %+v", spec)
	}

	// WeNet online models are CLI-only (unsupported by C API struct).
	wenetDir := filepath.Join(t.TempDir(), "sherpa-onnx-streaming-wenet-en")
	if err := os.MkdirAll(wenetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"model.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(wenetDir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, ok, err = detectOnlineModelSpec(wenetDir)
	if err != nil {
		t.Fatalf("wenet spec error: %v", err)
	}
	if ok {
		t.Fatal("wenet should return ok=false so it falls back to rolling CLI")
	}

	// Missing tokens.txt yields a SetupError.
	badDir := writeModelDir(t, "encoder.onnx", "decoder.onnx", "joiner.onnx")
	_, _, err = detectOnlineModelSpec(badDir)
	var setupErr *SetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("missing tokens.txt err = %v, want *SetupError", err)
	}
}

type fakeOnlineRecognizer struct {
	mu       sync.Mutex
	released bool
	streams  []*fakeOnlineStream
}

func (f *fakeOnlineRecognizer) NewStream() onlineStream {
	s := &fakeOnlineStream{}
	f.mu.Lock()
	f.streams = append(f.streams, s)
	f.mu.Unlock()
	return s
}

func (f *fakeOnlineRecognizer) Release() {
	f.mu.Lock()
	f.released = true
	f.mu.Unlock()
}

type fakeOnlineStream struct {
	mu            sync.Mutex
	samplesFed    int
	samplesPrePad int
	decodeCalls   int
	inputFinished bool
	closed        bool
	pendingReady  bool
	text          string
}

func (s *fakeOnlineStream) AcceptWaveform(_ int, samples []float32) {
	s.mu.Lock()
	s.samplesFed += len(samples)
	if !s.inputFinished && len(samples) != DefaultSampleRate/2 {
		s.samplesPrePad += len(samples)
	}
	s.pendingReady = true
	s.mu.Unlock()
}

func (s *fakeOnlineStream) InputFinished() {
	s.mu.Lock()
	s.inputFinished = true
	s.pendingReady = true
	s.mu.Unlock()
}

func (s *fakeOnlineStream) IsReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingReady
}

func (s *fakeOnlineStream) Decode() {
	s.mu.Lock()
	s.decodeCalls++
	s.pendingReady = false
	if s.samplesFed >= 1600 && !s.inputFinished {
		s.text = "Please open the read me file and write a"
	}
	if s.inputFinished {
		s.text = "Please open the read me file and write a short summary of the project."
	}
	s.mu.Unlock()
}

func (s *fakeOnlineStream) ResultText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return " " + s.text + " "
}

func (s *fakeOnlineStream) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func TestLocalStreamingTranscriberFlushesTailWithoutReDecode(t *testing.T) {
	rec := &fakeOnlineRecognizer{}
	tr := &localStreamingTranscriber{
		sampleRate: DefaultSampleRate,
		acquire:    func() (onlineRecognizer, error) { return rec, nil },
	}

	chunks := make(chan []byte)
	var partials []string
	done := make(chan string, 1)
	go func() {
		text, err := tr.StreamTranscribe(context.Background(), chunks, func(txt string, final bool) {
			if final {
				t.Errorf("unexpected final=true in partial callback")
			}
			partials = append(partials, txt)
		})
		if err != nil {
			t.Errorf("StreamTranscribe: %v", err)
		}
		done <- text
	}()

	// Send 3200 bytes (1600 samples) split across an odd byte boundary to verify
	// carry-byte handling too.
	pcm := make([]byte, 3200)
	chunks <- pcm[:1599]
	chunks <- pcm[1599:]
	close(chunks)

	select {
	case final := <-done:
		want := "Please open the read me file and write a short summary of the project."
		if final != want {
			t.Fatalf("final = %q, want %q", final, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamTranscribe did not finish promptly after close")
	}

	if len(rec.streams) != 1 || !rec.streams[0].closed || !rec.streams[0].inputFinished {
		t.Fatalf("stream not finalized/closed properly: %+v", rec.streams)
	}
	if !rec.released {
		t.Fatal("recognizer reference was not released")
	}
	if rec.streams[0].samplesPrePad != 1600 {
		t.Fatalf("samplesPrePad = %d, want 1600", rec.streams[0].samplesPrePad)
	}
	if rec.streams[0].samplesFed != 1600+DefaultSampleRate/2 {
		t.Fatalf("samplesFed = %d, want %d (with 0.5s tail padding)", rec.streams[0].samplesFed, 1600+DefaultSampleRate/2)
	}
	wantPartials := []string{"Please open the read me file and write a"}
	if !reflect.DeepEqual(partials, wantPartials) {
		t.Fatalf("partials = %v, want %v", partials, wantPartials)
	}
}

func TestLocalStreamingTranscriberDrainsChunksWhileLoading(t *testing.T) {
	rec := &fakeOnlineRecognizer{}
	gate := make(chan struct{})
	tr := &localStreamingTranscriber{
		sampleRate: DefaultSampleRate,
		acquire: func() (onlineRecognizer, error) {
			<-gate
			return rec, nil
		},
	}

	// Unbuffered channel: if StreamTranscribe blocked waiting for acquire(),
	// sending chunks would deadlock!
	chunks := make(chan []byte)
	done := make(chan string, 1)
	go func() {
		text, err := tr.StreamTranscribe(context.Background(), chunks, nil)
		if err != nil {
			t.Errorf("StreamTranscribe: %v", err)
		}
		done <- text
	}()

	chunks <- make([]byte, 1600)
	chunks <- make([]byte, 1600)
	close(chunks)
	close(gate)

	select {
	case final := <-done:
		if final == "" {
			t.Fatal("expected non-empty transcript after buffered chunks flushed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamTranscribe stalled while loading recognizer")
	}
	if rec.streams[0].samplesPrePad != 1600 {
		t.Fatalf("buffered samplesPrePad = %d, want 1600", rec.streams[0].samplesPrePad)
	}
}

func TestLocalStreamingTranscriberCancelReleasesLateAcquire(t *testing.T) {
	rec := &fakeOnlineRecognizer{}
	started := make(chan struct{})
	gate := make(chan struct{})
	tr := &localStreamingTranscriber{
		sampleRate: DefaultSampleRate,
		acquire: func() (onlineRecognizer, error) {
			close(started)
			<-gate
			return rec, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	chunks := make(chan []byte)
	errCh := make(chan error, 1)
	go func() {
		_, err := tr.StreamTranscribe(ctx, chunks, nil)
		errCh <- err
	}()

	<-started
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StreamTranscribe did not exit promptly on cancel")
	}

	// Once acquire finishes later, the defer cleanup must release its ref.
	close(gate)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		released := rec.released
		rec.mu.Unlock()
		if released {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("recognizer was not released after late acquire completed")
}

func TestLocalStreamingTranscriberEmptyStream(t *testing.T) {
	rec := &fakeOnlineRecognizer{}
	tr := &localStreamingTranscriber{
		sampleRate: DefaultSampleRate,
		acquire:    func() (onlineRecognizer, error) { return rec, nil },
	}
	chunks := make(chan []byte)
	close(chunks)
	text, err := tr.StreamTranscribe(context.Background(), chunks, nil)
	if err != nil || text != "" {
		t.Fatalf("empty stream = (%q, %v), want (\"\", nil)", text, err)
	}
}

func TestSherpaCAPIVersionCheck(t *testing.T) {
	wantVer := strings.TrimPrefix(DefaultSherpaVersion, "v")
	cases := []struct {
		name       string
		version    string
		missingSym bool
		wantOK     bool
	}{
		{name: "matching version", version: wantVer, wantOK: true},
		{name: "different version", version: "1.14.0", wantOK: false},
		{name: "empty version", version: "", wantOK: false},
		{name: "missing version symbol", missingSym: true, wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			libPath := filepath.Join(t.TempDir(), "libsherpa-onnx-c-api.so")
			t.Cleanup(func() {
				sherpaLibsMu.Lock()
				delete(sherpaLibs, libPath)
				delete(failedCAPILibs, libPath)
				sherpaLibsMu.Unlock()
			})

			closed := false
			ldr := sherpaRawLoader{
				open: func(string) (sherpaLibHandle, error) { return sherpaLibHandle(1), nil },
				sym: func(_ sherpaLibHandle, name string) (uintptr, error) {
					if tc.missingSym && name == "SherpaOnnxGetVersionStr" {
						return 0, errors.New("missing symbol")
					}
					return 1, nil
				},
				close:       func(sherpaLibHandle) { closed = true },
				register:    func(any, uintptr) {},
				readVersion: func(*sherpaCAPI) string { return tc.version },
			}

			api, err := openAndVerifySherpaCAPI(libPath, ldr)
			if tc.wantOK {
				if err != nil || api == nil {
					t.Fatalf("openAndVerifySherpaCAPI(%q) = (%v, %v), want success", tc.version, api, err)
				}
				if closed {
					t.Error("matching version should keep library handle open")
				}
				if isSherpaCAPIFailed(libPath, "") {
					t.Error("matching version must not mark library as failed")
				}
				return
			}
			if err == nil || api != nil {
				t.Fatalf("openAndVerifySherpaCAPI(%q) = (%v, %v), want rejection", tc.version, api, err)
			}
			if !closed {
				t.Error("rejected library handle should be closed")
			}
			if !isSherpaCAPIFailed(libPath, "") {
				t.Error("rejected library should be remembered as failed in-process")
			}
		})
	}
}

func TestLocalStreamingTranscriberFallbackOnAcquireFailure(t *testing.T) {
	libPath := filepath.Join(t.TempDir(), "libsherpa-onnx-c-api.so")
	modelDir := filepath.Join(t.TempDir(), "sherpa-onnx-streaming-zipformer-en-kroko-2025-08-06")
	t.Cleanup(func() {
		recCacheMu.Lock()
		delete(failedCAPIKeys, failedCAPIKey(libPath, modelDir))
		recCacheMu.Unlock()
	})

	fileRec := newScripted(newStubClock(), func(n int, _ context.Context) (string, error) {
		return "full fallback transcript", nil
	})
	started := make(chan struct{})
	gate := make(chan struct{})
	tr := &localStreamingTranscriber{
		cfg:             LocalConfig{ModelPath: modelDir, Online: true},
		libPath:         libPath,
		sampleRate:      DefaultSampleRate,
		fallback:        fileRec,
		rollingFallback: NewRollingTranscriber(fileRec, RollingOptions{SampleRate: DefaultSampleRate, MinAudio: 10 * time.Second}),
		acquire: func() (onlineRecognizer, error) {
			close(started)
			<-gate
			return nil, errors.New("c api init failed")
		},
	}

	chunks := make(chan []byte)
	type outcome struct {
		text string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		txt, err := tr.StreamTranscribe(context.Background(), chunks, nil)
		done <- outcome{txt, err}
	}()

	<-started
	// Send 1600 bytes BEFORE acquire fails (buffered in pending).
	chunks <- make([]byte, 1600)
	// Let acquire fail while chunks channel is still open.
	close(gate)
	// Send 1600 more bytes AFTER acquire failed.
	chunks <- make([]byte, 1600)
	close(chunks)

	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("StreamTranscribe fallback err = %v", out.err)
		}
		if out.text != "full fallback transcript" {
			t.Fatalf("StreamTranscribe fallback text = %q, want %q", out.text, "full fallback transcript")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("StreamTranscribe fallback timed out")
	}

	if fileRec.count() != 1 {
		t.Fatalf("fallback file Transcribe calls = %d, want 1", fileRec.count())
	}
	if gotBytes := fileRec.dataLen(t, 1); gotBytes != 3200 {
		t.Fatalf("fallback received %d PCM bytes, want 3200 (must not drop buffered audio)", gotBytes)
	}
	if !isSherpaCAPIFailed(libPath, modelDir) {
		t.Fatal("failed C API acquire should be remembered for future recordings")
	}
}

// TestLocalStreamingRealCAPI runs StreamTranscribe against a REAL
// sherpa-onnx-c-api shared library and streaming model when opt-in env vars are
// set.
func TestLocalStreamingRealCAPI(t *testing.T) {
	bin := os.Getenv("QG_STT_STREAM_BINARY")
	model := os.Getenv("QG_STT_STREAM_MODEL")
	wav := os.Getenv("QG_STT_STREAM_WAV")
	if bin == "" || model == "" || wav == "" {
		t.Skip("set QG_STT_STREAM_BINARY, QG_STT_STREAM_MODEL, QG_STT_STREAM_WAV to run the real C API test")
	}
	libPath := CanUseSherpaCAPI(model, bin)
	if libPath == "" {
		t.Fatalf("CanUseSherpaCAPI(%q, %q) returned empty", model, bin)
	}
	raw, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	sampleRate, pcm := parseWavPCM(raw)
	tr, err := NewLocalStreamingTranscriber(LocalConfig{Binary: bin, ModelPath: model, Online: true}, libPath)
	if err != nil {
		t.Fatalf("NewLocalStreamingTranscriber: %v", err)
	}
	chunks := make(chan []byte)
	go func() {
		defer close(chunks)
		size := chunkSizeBytes(sampleRate)
		for off := 0; off < len(pcm); off += size {
			end := off + size
			if end > len(pcm) {
				end = len(pcm)
			}
			chunks <- pcm[off:end]
		}
	}()
	var partials int
	final, err := tr.StreamTranscribe(context.Background(), chunks, func(string, bool) { partials++ })
	if err != nil {
		t.Fatalf("StreamTranscribe: %v", err)
	}
	if final == "" {
		t.Fatal("real C API returned empty transcript")
	}
	if !strings.Contains(final, "summary") || !strings.Contains(final, "project") {
		t.Fatalf("real C API lost tail: %q", final)
	}
	t.Logf("C API transcript (%d partials): %q", partials, final)
}
