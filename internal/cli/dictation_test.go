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
	factory := newDictationTranscriberFactory(resolved, "", nil)

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

func TestFactoryServerForStreamingModelDir(t *testing.T) {
	// A streaming model directory keeps the server-based transcriber, as today.
	modelDir := namedModelDir(t, "model-nemo-streaming-fast-conformer-transducer-en-80ms-int8",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt")
	stt := config.STTConfig{
		Provider:          config.STTProviderLocal,
		LocalModelPath:    modelDir,
		LocalServerBinary: filepath.Join(t.TempDir(), "no-such-server"),
	}
	resolved := config.ResolvedConfig{STT: stt}
	factory := newDictationTranscriberFactory(resolved, "", newDictationServerManager(stt))

	tr, streaming, err := factory(stt, true)
	if err != nil {
		t.Fatalf("factory with a streaming model: %v", err)
	}
	if !streaming {
		t.Fatal("a streaming model must use the server pipeline")
	}
	// The server backend fails here (no server binary), which proves this is
	// not the rolling transcriber (empty stream → "", nil) nor the batch one
	// (ErrStreamingUnsupported).
	chunks := make(chan []byte)
	close(chunks)
	_, err = tr.StreamTranscribe(context.Background(), chunks, func(string, bool) {})
	if err == nil {
		t.Fatal("expected the missing server binary to fail the stream")
	}
	if errors.Is(err, dictation.ErrStreamingUnsupported) {
		t.Fatalf("a streaming model must not report ErrStreamingUnsupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "no-such-server") {
		t.Fatalf("the stream should fail on the configured server binary, got %v", err)
	}
}

func TestFactoryBatchWhenStreamingNotPreferred(t *testing.T) {
	// preferStreaming false keeps the batch transcriber, even for a local model.
	modelDir := namedModelDir(t, "model-nemo-parakeet-tdt-0.6b-v3-int8",
		"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt")
	stt := config.STTConfig{Provider: config.STTProviderLocal, LocalModelPath: modelDir}
	resolved := config.ResolvedConfig{STT: stt}
	factory := newDictationTranscriberFactory(resolved, "", nil)

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
	factory := newDictationTranscriberFactory(resolved, "", newDictationServerManager(resolved.STT))

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
	// the streaming factory must NOT fire up the sherpa server on the leftover
	// path — it should fall back to the cloud batch pipeline the user chose.
	t.Setenv("GROQ_API_KEY", "gsk-test")
	stt := config.STTConfig{
		Provider:       config.STTProviderGroq, // cloud batch, chosen now
		LocalModelPath: "/leftover/model/dir",  // set by an earlier local selection
	}
	resolved := config.ResolvedConfig{STT: stt}
	factory := newDictationTranscriberFactory(resolved, "", newDictationServerManager(stt))

	tr, streaming, err := factory(stt, true) // prefer streaming
	if err != nil {
		t.Fatalf("expected cloud batch fallback, got error: %v", err)
	}
	if streaming {
		t.Error("a cloud provider must not stream through the local sherpa server")
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
	factory := newDictationTranscriberFactory(resolved, "", nil)

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
	factory := newDictationTranscriberFactory(resolved, "", newDictationServerManager(resolved.STT))

	_, _, err := factory(resolved.STT, true)
	var setupErr *dictation.SetupError
	if !errors.As(err, &setupErr) {
		t.Fatalf("want *SetupError guiding local model setup, got %v", err)
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
