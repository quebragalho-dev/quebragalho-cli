package dictation

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// curatedVariantByID returns the curated variant with the given ID, failing
// the test when it is absent.
func curatedVariantByID(t *testing.T, id string) ModelVariant {
	t.Helper()
	for _, v := range ModelVariants() {
		if v.ID == id {
			return v
		}
	}
	t.Fatalf("curated variant %q not found", id)
	return ModelVariant{}
}

func TestParakeetCuratedVariantInvariants(t *testing.T) {
	vs := ModelVariants()
	p := curatedVariantByID(t, defaultDictationModelID)

	// Parakeet v3 is the first batch variant (streaming options come first).
	firstBatch := ModelVariant{}
	for _, v := range vs {
		if !v.Streaming {
			firstBatch = v
			break
		}
	}
	if firstBatch.ID != p.ID {
		t.Errorf("first batch variant = %q, want Parakeet v3 %q", firstBatch.ID, p.ID)
	}
	if !p.Recommended {
		t.Error("Parakeet v3 should be Recommended")
	}
	if p.Streaming {
		t.Error("Parakeet v3 is a batch model, Streaming should be false")
	}
	if p.Language != "" {
		t.Errorf("Parakeet v3 Language = %q, want empty (multilingual)", p.Language)
	}
	for _, want := range []string{"pt", "en"} {
		found := false
		for _, l := range p.Languages {
			if l == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Parakeet v3 Languages %q should contain %q", p.Languages, want)
		}
	}
	if len(p.Languages) != 25 {
		t.Errorf("Parakeet v3 has %d Languages, want 25", len(p.Languages))
	}
	seen := map[string]bool{}
	for _, l := range p.Languages {
		if len(l) != 2 || strings.ToLower(l) != l {
			t.Errorf("Parakeet v3 language %q should be two lowercase letters", l)
		}
		for _, c := range l {
			if c < 'a' || c > 'z' {
				t.Errorf("Parakeet v3 language %q should be two lowercase letters", l)
			}
		}
		if seen[l] {
			t.Errorf("Parakeet v3 Languages has duplicate %q", l)
		}
		seen[l] = true
	}
	// DirName follows the "model-"+ID rule from the ModelVariants comment.
	id := strings.TrimSuffix(strings.TrimPrefix(p.AssetName, "sherpa-onnx-"), ".tar.bz2")
	if want := "model-" + id; p.DirName != want {
		t.Errorf("Parakeet v3 DirName = %q, want %q", p.DirName, want)
	}
	if p.Bytes <= 0 {
		t.Errorf("Parakeet v3 Bytes = %d, want > 0", p.Bytes)
	}
	if len(p.Digest) != 64 {
		t.Errorf("Parakeet v3 digest has length %d, want 64 hex characters", len(p.Digest))
	} else {
		for _, c := range p.Digest {
			valid := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
			if !valid {
				t.Errorf("Parakeet v3 digest %q is not lowercase hex", p.Digest)
				break
			}
		}
	}
}

func TestLanguageRankMultilingual(t *testing.T) {
	parakeet := curatedVariantByID(t, defaultDictationModelID)
	senseVoice := curatedVariantByID(t, "sense-voice-zh-en-ja-ko-yue-int8-2025-09-09")
	whisper := curatedVariantByID(t, "whisper-large-v3")
	enOnly := ModelVariant{Language: "en"}
	cases := []struct {
		v    ModelVariant
		lang string
		want int
	}{
		{parakeet, "pt", 1},
		{parakeet, "ja", 2},
		{parakeet, "", 1},
		{senseVoice, "ja", 1},
		{senseVoice, "pt", 2},
		{whisper, "pt", 1},
		{whisper, "xx", 1},
		{enOnly, "en", 0},
		{enOnly, "pt", 2},
	}
	for _, tc := range cases {
		if got := LanguageRank(tc.v, tc.lang); got != tc.want {
			t.Errorf("LanguageRank(%q, %q) = %d, want %d", tc.v.ID, tc.lang, got, tc.want)
		}
	}
}

func TestDefaultVariantFor(t *testing.T) {
	for _, lang := range []string{"", "pt", "en", "de"} {
		v, ok := DefaultVariantFor(lang)
		if !ok {
			t.Errorf("DefaultVariantFor(%q) = false, want Parakeet v3", lang)
			continue
		}
		if v.ID != defaultDictationModelID {
			t.Errorf("DefaultVariantFor(%q) ID = %q, want %q", lang, v.ID, defaultDictationModelID)
		}
	}
	for _, lang := range []string{"ja", "zh"} {
		if v, ok := DefaultVariantFor(lang); ok {
			t.Errorf("DefaultVariantFor(%q) = (%q, true), want zero value, false", lang, v.ID)
		}
	}
}

func TestListModelsCarriesParakeetLanguages(t *testing.T) {
	assets := []map[string]any{
		{"name": "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2", "size": 487211443, "digest": "sha256:1495e91b59d7fc53ddca91f3431758ec99f8fa253fdb0e1e851be0f72b14b0f9"},
		{"name": "sherpa-onnx-whisper-small.tar.bz2", "size": 300 << 20},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "assets": assets})
	}))
	defer srv.Close()

	vs, err := ListModels(context.Background(), http.DefaultClient, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	byAsset := map[string]ModelVariant{}
	for _, v := range vs {
		byAsset[v.AssetName] = v
	}
	parakeet := byAsset["sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2"]
	if !parakeet.Recommended {
		t.Error("Parakeet asset should come back Recommended")
	}
	if parakeet.Label != "Parakeet v3" {
		t.Errorf("Parakeet asset Label = %q, want the curated label", parakeet.Label)
	}
	if parakeet.Language != "" {
		t.Errorf("Parakeet asset Language = %q, want empty", parakeet.Language)
	}
	if len(parakeet.Languages) != 25 {
		t.Errorf("Parakeet asset has %d Languages, want 25", len(parakeet.Languages))
	}
	if other := byAsset["sherpa-onnx-whisper-small.tar.bz2"]; other.Languages != nil {
		t.Errorf("non-curated asset Languages = %q, want nil", other.Languages)
	}
}

func TestLocalTranscribeDefaultThreads(t *testing.T) {
	old := localNumCPU
	defer func() { localNumCPU = old }()

	transcribeArgs := func(t *testing.T, numThreads int) []string {
		t.Helper()
		dir := writeModelDir(t,
			"preprocess.onnx", "encode.onnx", "uncached_decode.onnx", "cached_decode.onnx", "tokens.txt")
		var gotArgs []string
		runner := func(_ context.Context, _ string, args ...string) ([]byte, error) {
			gotArgs = args
			return []byte(`{"text":"hi"}`), nil
		}
		tr, err := NewLocalTranscriber(LocalConfig{
			ModelPath: dir, NumThreads: numThreads,
			runOutput: runner, tempDir: func() (string, error) { return t.TempDir(), nil },
		})
		if err != nil {
			t.Fatalf("NewLocalTranscriber: %v", err)
		}
		if _, err := tr.Transcribe(context.Background(), wavFixture()); err != nil {
			t.Fatalf("Transcribe: %v", err)
		}
		return gotArgs
	}
	countFlag := func(args []string) (string, int) {
		found := ""
		n := 0
		for _, a := range args {
			if strings.HasPrefix(a, "--num-threads=") {
				found, n = a, n+1
			}
		}
		return found, n
	}

	for _, tc := range []struct {
		cpu  int
		want string
	}{
		{8, "--num-threads=4"},
		{2, "--num-threads=2"},
		{1, "--num-threads=1"},
	} {
		localNumCPU = func() int { return tc.cpu }
		got, n := countFlag(transcribeArgs(t, 0))
		if n != 1 || got != tc.want {
			t.Errorf("CPU count %d: --num-threads flags = %q (x%d), want exactly [%q]", tc.cpu, got, n, tc.want)
		}
	}

	// An explicit stt.numThreads wins over the default exactly once.
	localNumCPU = func() int { return 1 }
	got, n := countFlag(transcribeArgs(t, 6))
	if n != 1 || got != "--num-threads=6" {
		t.Errorf("explicit NumThreads: flags = %q (x%d), want exactly [--num-threads=6]", got, n)
	}
}

func TestProgressBytesReportsDownloadBytes(t *testing.T) {
	srv := fakeReleaseServer(t, engineSHA, modelSHA)
	type byteCall struct {
		label string
		done  int64
		total int64
	}
	var byteCalls []byteCall
	var textCalls []string
	_, err := EnsureLocalEngine(context.Background(), DownloadOptions{
		DestRoot:      t.TempDir(),
		EngineVersion: "test",
		APIBase:       srv.URL,
		platformKey:   "linux-amd64",
		skipPinned:    true,
		Progress:      func(s string) { textCalls = append(textCalls, s) },
		ProgressBytes: func(label string, done, total int64) { byteCalls = append(byteCalls, byteCall{label, done, total}) },
	})
	if err != nil {
		t.Fatalf("EnsureLocalEngine: %v", err)
	}
	if len(byteCalls) == 0 {
		t.Fatal("expected ProgressBytes calls during engine+model download")
	}
	byLabel := map[string][]byteCall{}
	for _, c := range byteCalls {
		byLabel[c.label] = append(byLabel[c.label], c)
	}
	// One byte stream per downloaded file (engine + model), each monotonic and
	// finishing at done == total.
	if len(byLabel) != 2 {
		t.Errorf("ProgressBytes labels = %v, want one stream per downloaded file", byLabel)
	}
	// The byte label is the same label the text Progress uses.
	textLabels := map[string]bool{}
	for _, s := range textCalls {
		for label := range byLabel {
			if strings.Contains(s, label) {
				textLabels[label] = true
			}
		}
	}
	for label, calls := range byLabel {
		if !textLabels[label] {
			t.Errorf("byte label %q never appears in the text progress", label)
		}
		var lastDone int64 = -1
		var total int64 = -1
		for _, c := range calls {
			if total == -1 {
				total = c.total
			}
			if c.total != total {
				t.Errorf("label %q: total changed %d -> %d", label, total, c.total)
			}
			if c.done < lastDone {
				t.Errorf("label %q: byte calls not monotonic (%d after %d)", label, c.done, lastDone)
			}
			lastDone = c.done
		}
		if total <= 0 {
			t.Errorf("label %q: total = %d, want the known size", label, total)
		}
		if lastDone != total {
			t.Errorf("label %q: last done = %d, want done == total (%d)", label, lastDone, total)
		}
	}
}

func TestProgressReaderNilCallbacksDoNotPanic(t *testing.T) {
	// A download with neither callback set must not panic: the release server
	// reports known sizes, so the byte/text readers run with nil callbacks.
	srv := fakeReleaseServer(t, engineSHA, modelSHA)
	if _, err := EnsureLocalEngine(context.Background(), DownloadOptions{
		DestRoot: t.TempDir(), EngineVersion: "test", APIBase: srv.URL,
		platformKey: "linux-amd64", skipPinned: true,
	}); err != nil {
		t.Fatalf("EnsureLocalEngine with nil Progress/ProgressBytes: %v", err)
	}

	// Direct unit check: a progressReader with both callbacks nil.
	pr := &progressReader{r: bytesReader(make([]byte, 500)), total: 500, label: "Engine", lastPct: -1}
	buf := make([]byte, 100)
	for {
		if _, err := pr.Read(buf); err != nil {
			break
		}
	}
}

func TestProgressBytesUnknownSizeNotCalled(t *testing.T) {
	// Without a known size (the release reports size 0 and the body arrives
	// chunked, so there is no Content-Length either), ProgressBytes is never
	// called: downloadVerifyExtract only wraps the body in a progressReader when
	// total > 0, since there are no whole-percent milestones to report.
	engineBytes, _ := base64.StdEncoding.DecodeString(engineTarB64)
	modelBytes, _ := base64.StdEncoding.DecodeString(modelTarB64)
	chunked := func(w http.ResponseWriter, data []byte) {
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush() // send headers first: chunked body, no Content-Length
		}
		w.Write(data)
	}
	mux := http.NewServeMux()
	base := ""
	var srv *httptest.Server
	mux.HandleFunc("/repos/k2-fsa/sherpa-onnx/releases/tags/", func(w http.ResponseWriter, r *http.Request) {
		name, dlPath, digest := modelAssetName, "/model.tar.bz2", modelSHA
		if !strings.HasSuffix(r.URL.Path, "/asr-models") {
			name, dlPath, digest = "sherpa-onnx-test-linux-x64-shared-no-tts.tar.bz2", "/engine.tar.bz2", engineSHA
		}
		json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "test",
			"assets": []map[string]any{{
				"name":                 name,
				"browser_download_url": base + dlPath,
				"digest":               "sha256:" + digest,
				"size":                 0,
			}},
		})
	})
	mux.HandleFunc("/engine.tar.bz2", func(w http.ResponseWriter, r *http.Request) { chunked(w, engineBytes) })
	mux.HandleFunc("/model.tar.bz2", func(w http.ResponseWriter, r *http.Request) { chunked(w, modelBytes) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base = srv.URL

	var calls int
	var texts []string
	if _, err := EnsureLocalEngine(context.Background(), DownloadOptions{
		DestRoot:      t.TempDir(),
		EngineVersion: "test",
		APIBase:       srv.URL,
		platformKey:   "linux-amd64",
		skipPinned:    true,
		Progress:      func(s string) { texts = append(texts, s) },
		ProgressBytes: func(string, int64, int64) { calls++ },
	}); err != nil {
		t.Fatalf("EnsureLocalEngine: %v", err)
	}
	if calls != 0 {
		t.Errorf("ProgressBytes called %d times with unknown size, want no calls", calls)
	}
}

func TestSetupErrorToolLiteralsPortuguese(t *testing.T) {
	// No Tool literal in the package's non-test files keeps the old English
	// fragments ("API key", "STT", "auto-download"): every Tool is either a
	// Portuguese name or a variable (binary path, recorder tool).
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "Tool:") {
				continue
			}
			for _, lit := range quotedLiterals(line) {
				for _, bad := range []string{"API key", "STT", "auto-download"} {
					if strings.Contains(lit, bad) {
						t.Errorf("%s:%d: Tool literal %q contains %q", name, i+1, lit, bad)
					}
				}
			}
		}
	}
}

// quotedLiterals returns the double-quoted string literals on a line.
func quotedLiterals(line string) []string {
	var out []string
	for i := 0; i < len(line); i++ {
		if line[i] != '"' {
			continue
		}
		j := i + 1
		for j < len(line) && line[j] != '"' {
			j++
		}
		if j < len(line) {
			out = append(out, line[i+1:j])
		}
		i = j
	}
	return out
}

func TestSetupErrorToolsRenamed(t *testing.T) {
	// Every SetupError Tool in scope reads as a Portuguese name; none keeps the
	// old English fragments.
	if _, err := NewLocalTranscriber(LocalConfig{}); err == nil ||
		!strings.Contains(err.Error(), "modelo de ditado local") {
		t.Errorf("missing model path should mention 'modelo de ditado local', got %v", err)
	}
	if _, err := NewCloudTranscriber(CloudConfig{Provider: ProviderGroq, Model: "m"}); err == nil ||
		!strings.Contains(err.Error(), "chave de API groq") {
		t.Errorf("missing cloud key should mention 'chave de API groq', got %v", err)
	}
	if _, err := NewDeepgramTranscriber(DeepgramConfig{}); err == nil ||
		!strings.Contains(err.Error(), "chave de API Deepgram") {
		t.Errorf("missing Deepgram key should mention 'chave de API Deepgram', got %v", err)
	}
	if _, err := NewOpenAIRealtimeTranscriber(OpenAIRealtimeConfig{}); err == nil ||
		!strings.Contains(err.Error(), "chave de API OpenAI") {
		t.Errorf("missing OpenAI key should mention 'chave de API OpenAI', got %v", err)
	}
	if _, err := EnsureLocalEngine(context.Background(),
		DownloadOptions{DestRoot: t.TempDir(), platformKey: "plan9-mips"}); err == nil ||
		!strings.Contains(err.Error(), "download do sherpa-onnx") {
		t.Errorf("unsupported platform should mention 'download do sherpa-onnx', got %v", err)
	}
	if _, err := onlineServerCommand(ServerConfig{}); err == nil ||
		!strings.Contains(err.Error(), "modelo de ditado ao vivo") {
		t.Errorf("missing streaming model should mention 'modelo de ditado ao vivo', got %v", err)
	}
}
