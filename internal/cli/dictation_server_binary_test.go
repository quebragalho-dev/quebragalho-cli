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

// TestFactoryRefreshesStreamingServerBinaryMidSession reproduces the fresh-install
// failure: the engine download sets stt.localServerBinary in the same session,
// but the session-long server manager keeps looking up the default binary name
// on PATH instead of the newly configured executable.
func TestFactoryRefreshesStreamingServerBinaryMidSession(t *testing.T) {
	modelDir := t.TempDir()
	for _, f := range []string{"encoder.onnx", "decoder.onnx", "joiner.onnx", "tokens.txt"} {
		if err := os.WriteFile(filepath.Join(modelDir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Absolute path of the just-downloaded engine; nothing exists there, so a
	// start attempt surfaces which binary the manager actually tried.
	configured := filepath.Join(t.TempDir(), "sherpa-onnx-online-websocket-server")

	mgr := newDictationServerManager(config.STTConfig{})
	resolved := config.ResolvedConfig{STT: config.STTConfig{Provider: config.STTProviderLocal}}
	factory := newDictationTranscriberFactory(resolved, "", mgr)

	stt := config.STTConfig{
		Provider:          config.STTProviderLocal,
		LocalModelPath:    modelDir,
		LocalServerBinary: configured,
	}
	if _, _, err := factory(stt, true); err != nil {
		t.Fatalf("factory with a local streaming setup: %v", err)
	}

	_, err := mgr.EnsureRunning(context.Background())
	if err == nil {
		t.Fatal("expected EnsureRunning to fail for a nonexistent server binary")
	}
	var setupErr *dictation.SetupError
	if errors.As(err, &setupErr) && setupErr.Tool == "sherpa-onnx-online-websocket-server" {
		t.Fatalf("manager still uses the default binary %q instead of the configured %q: %v",
			setupErr.Tool, configured, err)
	}
	if !strings.Contains(err.Error(), configured) {
		t.Fatalf("error should name the configured binary %q, got: %v", configured, err)
	}
}
