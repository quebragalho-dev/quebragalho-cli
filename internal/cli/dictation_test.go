package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/dictation"
)

// namedModelDir creates a model directory whose base name carries the family
// signature (how sherpa-onnx distributes models), holding the given files.
func namedModelDir(t *testing.T, base string, files ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), base)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestFactoryRollingForBatchModelDir(t *testing.T) {
	// A batch model directory (no "streaming" in its installed name) with
	// preferStreaming gets live text through the rolling transcriber — even
	// with no server manager (the rolling path needs no server).
	modelDir := namedModelDir(t, "model-nemo-parakeet-tdt-0.6b-v3-int8",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt")
	stt := config.STTConfig{Provider: config.STTProviderLocal, LocalModelPath: modelDir}
	resolved := config.ResolvedConfig{STT: stt}
	factory := newDictationTranscriberFactory(resolved, "")

	tr, streaming, err := factory(stt, true)
	if err != nil {
		t.Fatalf("factory with a batch model: %v", err)
	}
	if !streaming {
		t.Fatal("a batch model with preferStreaming must use the live (rolling) pipeline")
	}
	if tr == nil {
		t.Fatal("expected a rolling transcriber")
	}
	// The rolling transcriber streams (an empty stream yields no text and no
	// error), unlike the batch transcriber's ErrStreamingUnsupported.
	chunks := make(chan []byte)
	close(chunks)
	text, err := tr.StreamTranscribe(context.Background(), chunks, func(string, bool) {})
	if err != nil {
		t.Fatalf("rolling StreamTranscribe: %v", err)
	}
	if errors.Is(err, dictation.ErrStreamingUnsupported) {
		t.Fatal("a batch model must not report ErrStreamingUnsupported on the live pipeline")
	}
	if text != "" {
		t.Errorf("empty stream text = %q, want \"\"", text)
	}
}

func TestFactoryRollingFileForStreamingModelDir(t *testing.T) {
	// A streaming model directory must use the rolling file transcriber
	// (wrapping sherpa-onnx), never a websocket server.
	modelDir := namedModelDir(t, "model-nemo-streaming-fast-conformer-transducer-en-80ms-int8",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt")
	missingBin := filepath.Join(t.TempDir(), "no-such-sherpa-onnx")
	stt := config.STTConfig{
		Provider:          config.STTProviderLocal,
		LocalModelPath:    modelDir,
		LocalOnlineBinary: missingBin,
	}
	resolved := config.ResolvedConfig{STT: stt}
	factory := newDictationTranscriberFactory(resolved, "")

	tr, streaming, err := factory(stt, true)
	if err != nil {
		t.Fatalf("factory with a streaming model: %v", err)
	}
	if !streaming {
		t.Fatal("a streaming model must use the live (rolling) pipeline")
	}
	if tr == nil {
		t.Fatal("expected a rolling transcriber")
	}
	// An empty stream proves this is rollingTranscriber ("", nil), NOT a
	// websocket server transcriber (which dials the server even on empty chunks).
	chunks := make(chan []byte)
	close(chunks)
	text, err := tr.StreamTranscribe(context.Background(), chunks, func(string, bool) {})
	if err != nil {
		t.Fatalf("rolling StreamTranscribe on streaming model: %v", err)
	}
	if text != "" {
		t.Errorf("empty stream text = %q, want \"\"", text)
	}
	// Transcribing real audio bytes exercises the underlying sherpa-onnx file
	// binary (and must fail naming LocalOnlineBinary, not online-websocket-server).
	_, err = tr.Transcribe(context.Background(), []byte("RIFF\x00\x00\x00\x00WAVE"))
	if err == nil {
		t.Fatal("expected missing sherpa-onnx binary to fail Transcribe")
	}
	var setupErr *dictation.SetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("want *SetupError, got %v", err)
	}
	if setupErr.Tool != missingBin {
		t.Errorf("SetupError.Tool = %q, want %q", setupErr.Tool, missingBin)
	}
	if strings.Contains(setupErr.Hint, "online-websocket-server") {
		t.Errorf("Hint must not mention online-websocket-server: %q", setupErr.Hint)
	}
}

func TestFactoryBatchWhenStreamingNotPreferred(t *testing.T) {
	// preferStreaming false keeps the batch transcriber, even for a local model.
	modelDir := namedModelDir(t, "model-nemo-parakeet-tdt-0.6b-v3-int8",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt")
	stt := config.STTConfig{Provider: config.STTProviderLocal, LocalModelPath: modelDir}
	resolved := config.ResolvedConfig{STT: stt}
	factory := newDictationTranscriberFactory(resolved, "")

	tr, streaming, err := factory(stt, false)
	if err != nil {
		t.Fatalf("factory batch: %v", err)
	}
	if streaming {
		t.Error("preferStreaming false must use the batch pipeline")
	}
	chunks := make(chan []byte)
	close(chunks)
	if _, err := tr.StreamTranscribe(context.Background(), chunks, func(string, bool) {}); !errors.Is(err, dictation.ErrStreamingUnsupported) {
		t.Errorf("batch transcriber StreamTranscribe = %v, want ErrStreamingUnsupported", err)
	}
}

func TestFactoryFallsBackToBatchWhenLocalStreamingNotConfigured(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "gsk-test")
	resolved := config.ResolvedConfig{STT: config.STTConfig{
		Provider: config.STTProviderGroq, // cloud batch
		// StreamProvider unset → defaults to local, which has no model configured.
	}}
	factory := newDictationTranscriberFactory(resolved, "")

	tr, streaming, err := factory(resolved.STT, true) // prefer streaming
	if err != nil {
		t.Fatalf("expected batch fallback, got error: %v", err)
	}
	if streaming {
		t.Error("should have fallen back to batch (streaming=false)")
	}
	if tr == nil {
		t.Error("expected a Groq batch transcriber")
	}
}

func TestFactoryCloudProviderIgnoresLeftoverLocalModel(t *testing.T) {
	// Regression: pick local once (LocalModelPath gets set), then switch to a
	// cloud batch provider. streamProvider stays unset (defaults to "local"), so
	// the streaming factory must NOT fire up local rolling on the leftover
	// path — it should fall back to the cloud batch pipeline the user chose.
	t.Setenv("GROQ_API_KEY", "gsk-test")
	stt := config.STTConfig{
		Provider:       config.STTProviderGroq, // cloud batch, chosen now
		LocalModelPath: "/leftover/model/dir",  // set by an earlier local selection
	}
	resolved := config.ResolvedConfig{STT: stt}
	factory := newDictationTranscriberFactory(resolved, "")

	tr, streaming, err := factory(stt, true) // prefer streaming
	if err != nil {
		t.Fatalf("expected cloud batch fallback, got error: %v", err)
	}
	if streaming {
		t.Error("a cloud provider must not stream through the local engine")
	}
	if tr == nil {
		t.Error("expected a Groq batch transcriber")
	}
}

func TestFactorySurfacesExplicitCloudStreamingError(t *testing.T) {
	// Deepgram chosen explicitly but no key resolvable → surface the setup error.
	resolved := config.ResolvedConfig{STT: config.STTConfig{
		StreamProvider: config.STTProviderDeepgram,
	}}
	factory := newDictationTranscriberFactory(resolved, "")

	_, streaming, err := factory(resolved.STT, true)
	if err == nil {
		t.Fatal("expected a setup error for missing Deepgram key")
	}
	if !streaming {
		t.Error("an explicit cloud streaming choice should report streaming=true even on error")
	}
	var setupErr *dictation.SetupError
	if !errors.As(err, &setupErr) {
		t.Errorf("want *SetupError, got %v", err)
	}
}

func TestFactoryLocalBatchSetupErrorWhenNoModel(t *testing.T) {
	resolved := config.ResolvedConfig{STT: config.STTConfig{Provider: config.STTProviderLocal}}
	factory := newDictationTranscriberFactory(resolved, "")

	_, _, err := factory(resolved.STT, true)
	var setupErr *dictation.SetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("want *SetupError guiding local model setup, got %v", err)
	}
}

func TestFactoryBatchModeWithStreamingModelUsesOnlineBinary(t *testing.T) {
	// Even with preferStreaming=false (e.g. stt.streaming:false or Termux), a
	// streaming model directory must invoke the online binary, not offline.
	modelDir := namedModelDir(t, "model-streaming-zipformer-en-kroko-2025-08-06",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt")
	missingOnline := filepath.Join(t.TempDir(), "custom-sherpa-onnx")
	stt := config.STTConfig{
		Provider:          config.STTProviderLocal,
		LocalModelPath:    modelDir,
		LocalBinary:       filepath.Join(t.TempDir(), "custom-sherpa-onnx-offline"),
		LocalOnlineBinary: missingOnline,
	}
	factory := newDictationTranscriberFactory(config.ResolvedConfig{STT: stt}, "")
	tr, streaming, err := factory(stt, false)
	if err != nil {
		t.Fatalf("factory batch with streaming model: %v", err)
	}
	if streaming {
		t.Error("preferStreaming=false should return streaming=false")
	}
	_, err = tr.Transcribe(context.Background(), []byte("RIFF\x00\x00\x00\x00WAVE"))
	var setupErr *dictation.SetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("want *SetupError, got %v", err)
	}
	if setupErr.Tool != missingOnline {
		t.Errorf("SetupError.Tool = %q, want %q", setupErr.Tool, missingOnline)
	}
}

func TestFactoryRefreshesOnlineBinaryMidSession(t *testing.T) {
	// Simulates a mid-session /stt-model download updating stt.localOnlineBinary:
	// the next factory call uses the newly configured binary.
	modelDir := namedModelDir(t, "model-streaming-zipformer-en-kroko-2025-08-06",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt")
	firstBin := filepath.Join(t.TempDir(), "first-sherpa-onnx")
	secondBin := filepath.Join(t.TempDir(), "second-sherpa-onnx")

	factory := newDictationTranscriberFactory(config.ResolvedConfig{STT: config.STTConfig{Provider: config.STTProviderLocal}}, "")
	tr1, _, err := factory(config.STTConfig{Provider: config.STTProviderLocal, LocalModelPath: modelDir, LocalOnlineBinary: firstBin}, true)
	if err != nil {
		t.Fatal(err)
	}
	tr2, _, err := factory(config.STTConfig{Provider: config.STTProviderLocal, LocalModelPath: modelDir, LocalOnlineBinary: secondBin}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err1 := tr1.Transcribe(context.Background(), []byte("RIFF\x00\x00\x00\x00WAVE"))
	_, err2 := tr2.Transcribe(context.Background(), []byte("RIFF\x00\x00\x00\x00WAVE"))
	if err1 == nil || !strings.Contains(err1.Error(), firstBin) {
		t.Errorf("first transcriber error = %v, want %q", err1, firstBin)
	}
	if err2 == nil || !strings.Contains(err2.Error(), secondBin) {
		t.Errorf("second transcriber error = %v, want %q", err2, secondBin)
	}
}

func TestResolveLocalOnlineBinaryCases(t *testing.T) {
	// Explicit localOnlineBinary wins.
	if got := resolveLocalOnlineBinary(config.STTConfig{LocalOnlineBinary: "/opt/sherpa-onnx", LocalBinary: "/other/sherpa-onnx-offline"}); got != "/opt/sherpa-onnx" {
		t.Errorf("explicit = %q, want /opt/sherpa-onnx", got)
	}
	// Sibling sherpa-onnx next to an installed sherpa-onnx-offline is discovered
	// automatically (handles configs written before localOnlineBinary existed).
	unixDir := t.TempDir()
	unixOnline := filepath.Join(unixDir, "sherpa-onnx")
	if err := os.WriteFile(unixOnline, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveLocalOnlineBinary(config.STTConfig{LocalBinary: filepath.Join(unixDir, "sherpa-onnx-offline")}); got != unixOnline {
		t.Errorf("unix sibling = %q, want %q", got, unixOnline)
	}
	// Windows .exe sibling case.
	winDir := t.TempDir()
	winOnline := filepath.Join(winDir, "sherpa-onnx.exe")
	if err := os.WriteFile(winOnline, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := resolveLocalOnlineBinary(config.STTConfig{LocalBinary: filepath.Join(winDir, "sherpa-onnx-offline.exe")}); got != winOnline {
		t.Errorf("windows sibling = %q, want %q", got, winOnline)
	}
	// Empty falls back to PATH default ("sherpa-onnx" via LocalConfig.Online).
	if got := resolveLocalOnlineBinary(config.STTConfig{}); got != "" {
		t.Errorf("empty = %q, want \"\"", got)
	}
}

func TestResolveSTTKeyPrefersConfiguredProvider(t *testing.T) {
	resolved := config.ResolvedConfig{
		Providers: []config.ProviderProfile{
			{Name: "groq", CatalogID: "groq", BaseURL: "https://api.groq.com/openai/v1", APIKey: "gsk-configured"},
		},
	}
	if got := resolveSTTKey(resolved, "", dictation.ProviderGroq, "GROQ_API_KEY"); got != "gsk-configured" {
		t.Errorf("key = %q, want the configured provider's key", got)
	}
}
