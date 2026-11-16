package tui

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/dictation"
)

// buildFails returns a controller whose build always fails with a setup error,
// as if the local engine/model were missing.
func setupErrController(downloadRoot string) dictationController {
	return dictationController{
		cfg:          config.STTConfig{Provider: config.STTProviderLocal},
		platform:     dictation.DetectPlatform(),
		downloadRoot: downloadRoot,
		build: func(config.STTConfig, bool) (Transcriber, bool, error) {
			return nil, false, &dictation.SetupError{Tool: "local STT model", Hint: "set stt.localModelPath"}
		},
	}
}

func TestF9PointsToSTTModelWhenLocalMissing(t *testing.T) {
	if !dictation.AutoDownloadSupported() {
		t.Skip("no prebuilt engine for this platform")
	}
	m := model{dictation: setupErrController("/tmp/qg-stt")}
	next, _ := m.toggleDictation()
	if !transcriptHasText(next, "/stt-model") {
		t.Error("F9 with a missing local engine should point at /stt-model to download")
	}
}

func TestF9PlainSetupErrorWithoutDownloadRoot(t *testing.T) {
	m := model{dictation: setupErrController("")} // no download root
	next, _ := m.toggleDictation()
	if !transcriptHasText(next, "set stt.localModelPath") {
		t.Error("without a download root, expected the plain setup error")
	}
}

func TestDictationNoticeDedupe(t *testing.T) {
	m := model{}
	m = m.appendDictationNotice("k", "same message")
	m = m.appendDictationNotice("k", "same message")
	m = m.appendDictationNotice("k", "same message")
	n := 0
	for _, row := range m.transcript {
		if row.text == "same message" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("consecutive identical notices should dedupe to 1, got %d", n)
	}
	// A different key breaks the dedupe.
	m = m.appendDictationNotice("other", "another")
	if !transcriptHasText(m, "another") {
		t.Error("a different-keyed notice should still show")
	}
}

func TestDownloadedBatchModelAppliedToConfig(t *testing.T) {
	m := model{dictation: dictationController{cfg: config.STTConfig{Provider: config.STTProviderLocal}}}
	m.dictation.downloading = true
	comp := dictation.EngineComponents{
		BinaryPath: "/x/bin/sherpa-onnx-offline",
		ServerPath: "/x/bin/sherpa-onnx-online-websocket-server",
		ModelPath:  "/x/model",
	}
	got, _ := m.handleDictationDownloaded(dictationDownloadedMsg{components: comp})
	if got.dictation.downloading {
		t.Error("downloading flag should clear")
	}
	if got.dictation.cfg.LocalBinary != comp.BinaryPath || got.dictation.cfg.LocalModelPath != comp.ModelPath {
		t.Errorf("engine paths not applied: %+v", got.dictation.cfg)
	}
	// Every installed model gets live text (true streaming or rolling), so a
	// batch install switches the live pipeline on too.
	if !got.dictation.cfg.StreamingEnabled() {
		t.Error("a batch variant should set streaming on (rolling live text)")
	}
	if !strings.Contains(got.transientNotice.text, "pronto") {
		t.Error("expected a ready notice")
	}
}

func TestDownloadedStreamingModelSetsStreamingOn(t *testing.T) {
	m := model{dictation: dictationController{cfg: config.STTConfig{Provider: config.STTProviderLocal}}}
	comp := dictation.EngineComponents{BinaryPath: "/x/bin/sherpa-onnx-offline", ModelPath: "/x/model"}
	got, _ := m.handleDictationDownloaded(dictationDownloadedMsg{components: comp})
	if !got.dictation.cfg.StreamingEnabled() {
		t.Error("a streaming variant should set streaming on")
	}
}

// firstVariantWithStreaming returns the first curated download variant with
// the given pipeline flag.
func firstVariantWithStreaming(t *testing.T, streaming bool) dictation.ModelVariant {
	t.Helper()
	for _, v := range dictation.ModelVariants() {
		if v.Streaming == streaming {
			return v
		}
	}
	t.Fatalf("no curated variant with streaming=%v", streaming)
	return dictation.ModelVariant{}
}

// savedSTTStreaming reads the persisted config back and reports stt.streaming.
func savedSTTStreaming(t *testing.T, path string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.FileConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg.STT.Streaming != nil && *cfg.STT.Streaming
}

// TestInstallEnablesStreamingDownloadPath: installing any variant through the
// download pipeline switches live text on in memory and persists
// stt.streaming:true.
func TestInstallEnablesStreamingDownloadPath(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		variant := firstVariantWithStreaming(t, streaming)
		cfgPath := filepath.Join(t.TempDir(), "config.json")
		m := model{dictation: dictationController{
			cfg:            config.STTConfig{Provider: config.STTProviderLocal},
			userConfigPath: cfgPath,
		}}
		comp := dictation.EngineComponents{
			BinaryPath: "/x/bin/sherpa-onnx-offline",
			ServerPath: "/x/bin/sherpa-onnx-online-websocket-server",
			ModelPath:  "/x/" + variant.DirName,
		}
		got, _ := m.handleDictationDownloaded(dictationDownloadedMsg{components: comp})
		if !got.dictation.cfg.StreamingEnabled() {
			t.Errorf("streaming=%v download: streaming must be on in memory", streaming)
		}
		if !savedSTTStreaming(t, cfgPath) {
			t.Errorf("streaming=%v download: stt.streaming must persist as true", streaming)
		}
	}
}

// stageInstalledModel fakes an extracted engine + model under root so
// applyInstalledModel resolves locally without network.
func stageInstalledModel(t *testing.T, root string, v dictation.ModelVariant) {
	t.Helper()
	binDir := filepath.Join(root, "engine-"+dictation.DefaultSherpaVersion+"-"+runtime.GOOS+"-"+runtime.GOARCH, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sherpa-onnx-offline"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	modelDir := filepath.Join(root, v.DirName)
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "tokens.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestInstallEnablesStreamingInstalledPath: picking an already-downloaded
// model switches live text on in memory and persists stt.streaming:true.
func TestInstallEnablesStreamingInstalledPath(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		variant := firstVariantWithStreaming(t, streaming)
		root := t.TempDir()
		stageInstalledModel(t, root, variant)
		cfgPath := filepath.Join(t.TempDir(), "config.json")
		m := model{dictation: dictationController{
			cfg:            config.STTConfig{Provider: config.STTProviderLocal},
			downloadRoot:   root,
			userConfigPath: cfgPath,
		}}
		got, _ := m.applyInstalledModel(variant)
		if !got.dictation.cfg.StreamingEnabled() {
			t.Errorf("streaming=%v installed model: streaming must be on in memory", streaming)
		}
		if !savedSTTStreaming(t, cfgPath) {
			t.Errorf("streaming=%v installed model: stt.streaming must persist as true", streaming)
		}
		if got.dictation.cfg.LocalModelPath == "" {
			t.Errorf("streaming=%v installed model: model path not applied", streaming)
		}
	}
}

func TestSTTDownloadLoadingPickerThenFilled(t *testing.T) {
	// The chooser opens in a single loading state (no curated-then-swap flicker),
	// then handleSTTModelsFetched fills it with the fetched list.
	loading := newSTTDownloadLoadingPicker()
	if loading.kind != pickerSTTDownload || !loading.loading || len(loading.items) != 0 {
		t.Fatalf("loading picker = %+v, want an empty, loading STT-download picker", loading)
	}

	m := model{dictation: dictationController{downloadRoot: "/tmp/x"}}
	m.picker = loading
	fetched := dictation.ModelVariants()
	m = m.handleSTTModelsFetched(sttModelsFetchedMsg{variants: fetched})
	if m.dictation.browseLoading {
		t.Error("browseLoading should clear once the list arrives")
	}
	if m.picker.loading {
		t.Error("the filled picker should no longer be loading")
	}
	if len(m.picker.items) != len(fetched) {
		t.Errorf("filled picker has %d items, want %d", len(m.picker.items), len(fetched))
	}
}

func TestSTTModelsFetchFailureFallsBackToCurated(t *testing.T) {
	// A failed fetch with nothing cached falls back to the curated shortlist so the
	// picker still works offline (never a permanently-empty loading box).
	m := model{dictation: dictationController{downloadRoot: "/tmp/x"}}
	m.picker = newSTTDownloadLoadingPicker()
	m = m.handleSTTModelsFetched(sttModelsFetchedMsg{err: errors.New("offline")})
	if m.picker.loading {
		t.Error("picker should stop loading even on fetch failure")
	}
	if len(m.picker.items) != len(dictation.ModelVariants()) {
		t.Errorf("fallback picker has %d items, want the %d curated variants",
			len(m.picker.items), len(dictation.ModelVariants()))
	}
}

func TestSTTDownloadPickerListsVariants(t *testing.T) {
	m := model{dictation: dictationController{downloadRoot: "/tmp/x"}}
	p := m.newSTTDownloadPicker()
	if p == nil || p.kind != pickerSTTDownload {
		t.Fatal("expected an STT download picker")
	}
	// Exactly the curated download variants — no dead-end "manual" row.
	if len(p.items) != len(dictation.ModelVariants()) {
		t.Errorf("picker has %d items, want %d variants", len(p.items), len(dictation.ModelVariants()))
	}
	for _, it := range p.items {
		if it.Value == "manual" {
			t.Error("the dead-end manual row should be gone")
		}
	}
}

func TestSTTDownloadSelectionStartsDownload(t *testing.T) {
	m := model{dictation: dictationController{downloadRoot: "/tmp/x", cfg: config.STTConfig{Provider: config.STTProviderLocal}}}
	variant := dictation.ModelVariants()[0]
	next, cmd := m.handleSTTDownloadSelection(variant.ID)
	if !next.dictation.downloading {
		t.Error("selecting a variant should start a download")
	}
	if cmd == nil {
		t.Error("expected a download command")
	}
	if !strings.Contains(next.transientNotice.text, variant.Label) {
		t.Error("expected the download-starting notice to name the variant")
	}
}

func TestDownloadFailureReported(t *testing.T) {
	m := model{dictation: dictationController{}}
	m.dictation.downloading = true
	next, _ := m.handleDictationDownloaded(dictationDownloadedMsg{err: errors.New("network down")})
	got := next
	if got.dictation.downloading {
		t.Error("downloading flag should clear on failure")
	}
	if !transcriptHasText(got, "network down") {
		t.Error("expected the failure reported")
	}
}
