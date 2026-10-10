package provideronboarding

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestLocalRuntimeCandidatesEmptyForSingleProviderCatalog: the candidates are
// derived from catalog descriptors marked Local, and the single-provider
// catalog has none — so `providers detect` reports nothing by default (explicit
// candidates remain testable through LocalDetectOptions).
func TestLocalRuntimeCandidatesEmptyForSingleProviderCatalog(t *testing.T) {
	if candidates := LocalRuntimeCandidates(); len(candidates) != 0 {
		t.Fatalf("LocalRuntimeCandidates() = %#v, want none for the single-provider catalog", candidates)
	}
}

func TestDetectLocalRuntimesReportsReachableRuntime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"llama3.1"}]}`))
	}))
	defer server.Close()

	detected := DetectLocalRuntimes(context.Background(), LocalDetectOptions{
		HTTPClient: server.Client(),
		Candidates: []LocalRuntime{{
			CatalogID: "ollama",
			Name:      "Ollama Local",
			BaseURL:   server.URL + "/v1",
		}},
	})
	if len(detected) != 1 {
		t.Fatalf("DetectLocalRuntimes() = %#v, want one reachable runtime", detected)
	}
	if !detected[0].Reachable {
		t.Fatalf("runtime should be reachable: %#v", detected[0])
	}
	if len(detected[0].Models) == 0 || detected[0].Models[0] != "llama3.1" {
		t.Fatalf("expected discovered model list, got %#v", detected[0].Models)
	}
}

// A local runtime serves whichever model the user loaded, so the adopt command
// must pin the id the probe saw — never a catalog placeholder DefaultModel.
func TestSetupActionPinsProbedModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF"}]}`))
	}))
	defer server.Close()

	detected := DetectLocalRuntimes(context.Background(), LocalDetectOptions{
		HTTPClient: server.Client(),
		Candidates: []LocalRuntime{{
			CatalogID:    "atomic-chat-local",
			Name:         "Atomic Chat Local",
			BaseURL:      server.URL + "/v1",
			DefaultModel: "local-model",
		}},
	})
	if len(detected) != 1 {
		t.Fatalf("DetectLocalRuntimes() = %#v, want one reachable runtime", detected)
	}
	if got := detected[0].AdoptModel(); got != "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF" {
		t.Fatalf("AdoptModel() = %q, want the probed id, not the catalog placeholder", got)
	}
	action := detected[0].SetupAction()
	if !strings.Contains(action.Command, "--model unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF") {
		t.Fatalf("SetupAction command must pin the probed model, got %q", action.Command)
	}
	if strings.Contains(action.Command, "local-model") {
		t.Fatalf("SetupAction command must not persist the catalog placeholder, got %q", action.Command)
	}
}

func TestSetupActionPreservesModelsWithSpaces(t *testing.T) {
	runtime := DetectedLocalRuntime{
		LocalRuntime: LocalRuntime{CatalogID: "atomic-chat-local", Name: "Atomic Chat Local"},
		Models:       []string{"my loaded model"},
	}
	if got := runtime.AdoptModel(); got != "my loaded model" {
		t.Fatalf("AdoptModel() = %q", got)
	}
	if got := runtime.SetupAction().Command; !strings.Contains(got, `--model "my loaded model"`) {
		t.Fatalf("model with spaces not preserved: %q", got)
	}
}

func TestSetupActionOmitsUnsafeCommands(t *testing.T) {
	for _, id := range []string{"$(touch pwned)", "`id`", "x&calc", "a \" & calc & \"b", "a;b", "a|b", "a'b", "a>b", "%PATH%", "!PATH!", "@args", "line\ncommand", "a\u201db"} {
		t.Run(id, func(t *testing.T) {
			runtime := DetectedLocalRuntime{LocalRuntime: LocalRuntime{CatalogID: "atomic-chat-local", Name: "Atomic Chat Local"}, Models: []string{id, "safe-model"}}
			if runtime.AdoptModel() != id {
				t.Fatal("discovered model must not be silently changed")
			}
			action := runtime.SetupAction()
			if action.Command != "" || !strings.Contains(action.Detail, "qg setup") {
				t.Fatalf("unsafe command must be replaced with interactive guidance: %#v", action)
			}
		})
	}
}

func TestAdoptModelPrefersCatalogDefaultWhenServed(t *testing.T) {
	runtime := DetectedLocalRuntime{
		LocalRuntime: LocalRuntime{CatalogID: "ollama", Name: "Ollama Local", DefaultModel: "llama3.1"},
		Reachable:    true,
		Models:       []string{"qwen3:8b", "llama3.1"},
	}
	if got := runtime.AdoptModel(); got != "llama3.1" {
		t.Fatalf("AdoptModel() = %q, want the catalog default when the server serves it", got)
	}
}

func TestAdoptModelChatEligibilityAndOllamaDefault(t *testing.T) {
	tests := []struct {
		name, provider, defaultModel string
		models                       []string
		want                         string
	}{
		{"skip embedding", "atomic-chat-local", "local-model", []string{"text-embedding-local", "qwen3-coder-30b"}, "qwen3-coder-30b"},
		{"reject nonchat default", "lmstudio", "text-embedding-local", []string{"text-embedding-local", "chat-model"}, "chat-model"},
		{"latest alias", "ollama", "llama3.1", []string{"qwen3:8b", "llama3.1:latest"}, "llama3.1:latest"},
		{"exact before alias", "ollama", "llama3.1", []string{"llama3.1:latest", "llama3.1"}, "llama3.1"},
		{"absent default", "ollama", "llama3.1", []string{"text-embedding-local", "qwen3:8b", "other-chat"}, "qwen3:8b"},
		{"different tag", "ollama", "llama3.1", []string{"qwen3:8b", "llama3.1:custom"}, "qwen3:8b"},
		{"explicit default tag", "ollama", "llama3.1:custom", []string{"qwen3:8b", "llama3.1:latest"}, "qwen3:8b"},
		{"other runtime", "lmstudio", "llama3.1", []string{"qwen3:8b", "llama3.1:latest"}, "qwen3:8b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected := DetectedLocalRuntime{LocalRuntime: LocalRuntime{CatalogID: tt.provider, DefaultModel: tt.defaultModel}, Models: tt.models}
			if got := detected.AdoptModel(); got != tt.want {
				t.Fatalf("AdoptModel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetupActionOmitsKnownNonChatOnlyModels(t *testing.T) {
	for _, provider := range []string{"atomic-chat-local", "lmstudio", "ollama"} {
		for _, defaultModel := range []string{"local-model", "text-embedding-local"} {
			detected := DetectedLocalRuntime{LocalRuntime: LocalRuntime{CatalogID: provider, DefaultModel: defaultModel}, Models: []string{"text-embedding-local", "whisper-1"}}
			action := detected.SetupAction()
			if detected.AdoptModel() != "" || action.Command != "" || !strings.Contains(action.Detail, "qg providers detect") {
				t.Fatalf("%s must offer guidance without adopting non-chat models: %+v", provider, action)
			}
		}
	}
}

func TestDetectLocalRuntimesSkipsUnreachableRuntime(t *testing.T) {
	// A client whose transport always fails simulates a closed local port.
	failing := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}

	detected := DetectLocalRuntimes(context.Background(), LocalDetectOptions{
		HTTPClient: failing,
		Candidates: []LocalRuntime{{
			CatalogID: "ollama",
			Name:      "Ollama Local",
			BaseURL:   "http://127.0.0.1:11434/v1",
		}},
	})
	if len(detected) != 0 {
		t.Fatalf("DetectLocalRuntimes() = %#v, want no reachable runtimes", detected)
	}
}

func TestDetectLocalRuntimesIgnoresServerErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	detected := DetectLocalRuntimes(context.Background(), LocalDetectOptions{
		HTTPClient: server.Client(),
		Candidates: []LocalRuntime{{
			CatalogID: "lmstudio",
			Name:      "LM Studio",
			BaseURL:   server.URL + "/v1",
		}},
	})
	if len(detected) != 0 {
		t.Fatalf("a 5xx local response must not count as a reachable runtime: %#v", detected)
	}
}

func TestLocalRuntimeActionOffersNoKeySetup(t *testing.T) {
	runtime := DetectedLocalRuntime{LocalRuntime: LocalRuntime{
		CatalogID: "ollama",
		Name:      "Ollama Local",
		BaseURL:   "http://localhost:11434/v1",
	}, Reachable: true}

	action := runtime.SetupAction()
	if !strings.Contains(action.Command, "qg providers add ollama") {
		t.Fatalf("setup command should add the ollama provider, got %q", action.Command)
	}
	if strings.Contains(action.Command, "--api-key-env") {
		t.Fatalf("local runtime setup must not require an API key env, got %q", action.Command)
	}
	if !strings.Contains(strings.ToLower(action.Detail), "no api key") {
		t.Fatalf("setup detail should advertise the no-key path, got %q", action.Detail)
	}
}

func TestDetectLocalRuntimesAppliesDefaultTimeout(t *testing.T) {
	// A handler that blocks past the configured timeout must be treated as
	// unreachable rather than hanging the wizard.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	detected := DetectLocalRuntimes(context.Background(), LocalDetectOptions{
		HTTPClient: server.Client(),
		Timeout:    20 * time.Millisecond,
		Candidates: []LocalRuntime{{
			CatalogID: "ollama",
			Name:      "Ollama Local",
			BaseURL:   server.URL + "/v1",
		}},
	})
	if len(detected) != 0 {
		t.Fatalf("a runtime slower than the timeout must be skipped: %#v", detected)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
