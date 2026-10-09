package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestNewCreatesOpenAIProviderWithFactoryOptions(t *testing.T) {
	transport := &captureTransport{
		responseBody: "data: [DONE]\n\n",
	}
	client := &http.Client{Transport: transport}

	provider, err := New(config.ProviderProfile{
		Name:         "custom",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      "https://provider.example/v1/",
		APIKey:       "sk-factory",
		Model:        "factory-model",
	}, Options{
		HTTPClient: client,
		UserAgent:  "qg-factory-test",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	stream, err := provider.StreamCompletion(context.Background(), qgruntime.CompletionRequest{
		Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("StreamCompletion() error = %v", err)
	}
	for range stream {
	}

	if transport.request == nil {
		t.Fatal("HTTP client was not used")
	}
	if transport.request.URL.String() != "https://provider.example/v1/chat/completions" {
		t.Fatalf("request URL = %q, want provider base URL", transport.request.URL.String())
	}
	if transport.request.Header.Get("Authorization") != "Bearer sk-factory" {
		t.Fatalf("Authorization = %q, want bearer token", transport.request.Header.Get("Authorization"))
	}
	if transport.request.Header.Get("User-Agent") != "qg-factory-test" {
		t.Fatalf("User-Agent = %q, want factory user agent", transport.request.Header.Get("User-Agent"))
	}
}

func TestNewPassesQGModelThrough(t *testing.T) {
	transport := &captureTransport{
		responseBody: "data: [DONE]\n\n",
	}
	client := &http.Client{Transport: transport}

	provider, err := New(config.ProviderProfile{
		Name:         "qg",
		CatalogID:    "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		APIKey:       "sk-qg-test",
		Model:        "muse-spark-1.3-contributor",
	}, Options{HTTPClient: client})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stream, err := provider.StreamCompletion(context.Background(), qgruntime.CompletionRequest{
		Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("StreamCompletion() error = %v", err)
	}
	for range stream {
	}

	if transport.request.URL.String() != "https://api.quebragalho.dev/v1/chat/completions" {
		t.Fatalf("request URL = %q, want QG chat completions", transport.request.URL.String())
	}
	var body map[string]any
	if err := json.NewDecoder(transport.body()).Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if body["model"] != "muse-spark-1.3-contributor" {
		t.Fatalf("model = %q, want muse-spark-1.3-contributor passthrough", body["model"])
	}

	metadata, err := ResolveRuntimeMetadata(config.ProviderProfile{
		Name:         "qg",
		CatalogID:    "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		Model:        "muse-spark-1.3-contributor",
	}, Options{})
	if err != nil {
		t.Fatalf("ResolveRuntimeMetadata() error = %v", err)
	}
	if metadata.ProviderKind != config.ProviderKindOpenAICompatible || metadata.APIModel != "muse-spark-1.3-contributor" {
		t.Fatalf("runtime metadata = %#v, want OpenAI-compatible muse-spark-1.3-contributor", metadata)
	}
}

func TestNewThreadsCustomProviderHeaders(t *testing.T) {
	transport := &captureTransport{
		responseBody: "data: [DONE]\n\n",
	}
	client := &http.Client{Transport: transport}

	provider, err := New(config.ProviderProfile{
		Name:          "gateway",
		ProviderKind:  config.ProviderKindOpenAICompatible,
		BaseURL:       "https://gateway.example/v1",
		APIKey:        "sk-gateway",
		AuthHeader:    "X-API-Key",
		AuthScheme:    "Token",
		CustomHeaders: map[string]string{"HTTP-Referer": "https://qg.dev"},
		Model:         "gateway-model",
	}, Options{HTTPClient: client})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stream, err := provider.StreamCompletion(context.Background(), qgruntime.CompletionRequest{
		Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("StreamCompletion() error = %v", err)
	}
	for range stream {
	}

	if transport.request.Header.Get("Authorization") != "" {
		t.Fatalf("Authorization = %q, want empty when custom auth header is set", transport.request.Header.Get("Authorization"))
	}
	if transport.request.Header.Get("X-API-Key") != "Token sk-gateway" {
		t.Fatalf("X-API-Key = %q, want custom auth header", transport.request.Header.Get("X-API-Key"))
	}
	if transport.request.Header.Get("HTTP-Referer") != "https://qg.dev" {
		t.Fatalf("HTTP-Referer = %q, want custom provider header", transport.request.Header.Get("HTTP-Referer"))
	}
}

func TestNewQGProviderSendsEndpointAndAuth(t *testing.T) {
	transport := &captureTransport{responseBody: "data: [DONE]\n\n"}
	provider, err := New(config.ProviderProfile{
		Name:          "qg",
		CatalogID:     "qg",
		ProviderKind:  config.ProviderKindOpenAICompatible,
		APIKey:        "sk-qg-test",
		Model:         "muse-spark-1.3-contributor",
		CustomHeaders: map[string]string{"X-Trace": "test"},
	}, Options{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stream, err := provider.StreamCompletion(context.Background(), qgruntime.CompletionRequest{
		Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("StreamCompletion() error = %v", err)
	}
	for range stream {
	}

	if transport.request == nil {
		t.Fatal("HTTP client was not used")
	}
	if got := transport.request.URL.String(); got != "https://api.quebragalho.dev/v1/chat/completions" {
		t.Fatalf("request URL = %q, want QG endpoint", got)
	}
	for header, want := range map[string]string{
		"Authorization": "Bearer sk-qg-test",
		"X-Trace":       "test",
	} {
		if got := transport.request.Header.Get(header); got != want {
			t.Fatalf("%s = %q, want %q", header, got, want)
		}
	}
}

func TestNewSupportsOpenAIProviderKind(t *testing.T) {
	provider, err := New(config.ProviderProfile{
		Name:         "openai",
		ProviderKind: config.ProviderKindOpenAI,
		Model:        "gpt-test",
	}, Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if provider == nil {
		t.Fatal("New() returned nil provider")
	}
}

// TestPromptCacheKeyOnlyOnOfficialOpenAI locks in #624: session-backed TUI
// turns always carry a PromptCacheKey, but openai-compatible gateways (NVIDIA
// NIM, strict local proxies) reject the OpenAI-only prompt_cache_key field.
// The factory must omit it for openai-compatible profiles while still
// forwarding it for official OpenAI so multi-turn cache routing stays intact.
func TestPromptCacheKeyOnlyOnOfficialOpenAI(t *testing.T) {
	requestWithSession := qgruntime.CompletionRequest{
		Messages:       []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "hello"}},
		PromptCacheKey: "sess_tui_123",
	}

	for _, tc := range []struct {
		name         string
		kind         config.ProviderKind
		wantCacheKey bool
	}{
		{name: "openai", kind: config.ProviderKindOpenAI, wantCacheKey: true},
		{name: "openai-compatible", kind: config.ProviderKindOpenAICompatible, wantCacheKey: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := &captureTransport{responseBody: "data: [DONE]\n\n"}
			provider, err := New(config.ProviderProfile{
				Name:         "test",
				ProviderKind: tc.kind,
				BaseURL:      "https://provider.example/v1",
				APIKey:       "sk-test",
				Model:        "test-model",
			}, Options{HTTPClient: &http.Client{Transport: transport}})
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			stream, err := provider.StreamCompletion(context.Background(), requestWithSession)
			if err != nil {
				t.Fatalf("StreamCompletion() error = %v", err)
			}
			for range stream {
			}
			var body map[string]any
			if err := json.NewDecoder(transport.body()).Decode(&body); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			_, hasKey := body["prompt_cache_key"]
			if hasKey != tc.wantCacheKey {
				t.Fatalf("prompt_cache_key present = %v, want %v; body = %#v", hasKey, tc.wantCacheKey, body)
			}
			if tc.wantCacheKey && body["prompt_cache_key"] != "sess_tui_123" {
				t.Fatalf("prompt_cache_key = %#v, want sess_tui_123", body["prompt_cache_key"])
			}
		})
	}
}

func TestParseThinkTagsForProfileUsesConservativeDefaultsAndOverride(t *testing.T) {
	openAICompatible := resolvedProfile{providerKind: config.ProviderKindOpenAICompatible, apiModel: "qwen3-coder:480b"}
	if !parseThinkTagsForProfile(config.ProviderProfile{}, openAICompatible) {
		t.Fatal("qwen3 OpenAI-compatible model should parse inline think tags by default")
	}
	minimaxM27 := resolvedProfile{providerKind: config.ProviderKindOpenAICompatible, apiModel: "MiniMax-M2.7"}
	if !parseThinkTagsForProfile(config.ProviderProfile{}, minimaxM27) {
		t.Fatal("MiniMax-M2.7 OpenAI-compatible model should parse inline think tags by default")
	}

	generic := resolvedProfile{providerKind: config.ProviderKindOpenAICompatible, apiModel: "factory-model"}
	if parseThinkTagsForProfile(config.ProviderProfile{}, generic) {
		t.Fatal("generic OpenAI-compatible model should preserve literal think tags by default")
	}

	official := resolvedProfile{providerKind: config.ProviderKindOpenAI, apiModel: "gpt-4.1"}
	if parseThinkTagsForProfile(config.ProviderProfile{}, official) {
		t.Fatal("official OpenAI model should preserve literal think tags by default")
	}

	enabled := true
	if !parseThinkTagsForProfile(config.ProviderProfile{ParseThinkTags: &enabled}, generic) {
		t.Fatal("explicit parseThinkTags=true should enable inline think parsing")
	}

	disabled := false
	if parseThinkTagsForProfile(config.ProviderProfile{ParseThinkTags: &disabled}, openAICompatible) {
		t.Fatal("explicit parseThinkTags=false should disable inline think parsing")
	}
}

func TestNewRejectsMismatchedOfficialProviderAndKnownModel(t *testing.T) {
	_, err := New(config.ProviderProfile{
		Name:         "openai",
		ProviderKind: config.ProviderKindOpenAI,
		Model:        "muse-spark-1.3-contributor",
	}, Options{})
	if err == nil {
		t.Fatal("New() error = nil, want provider/model mismatch")
	}
	if !strings.Contains(err.Error(), "belongs to qg, not openai") {
		t.Fatalf("error = %q, want model/provider mismatch", err.Error())
	}
}

func TestNewRejectsUnsupportedProviderKind(t *testing.T) {
	_, err := New(config.ProviderProfile{
		Name:         "bad",
		ProviderKind: "bedrock",
		Model:        "model",
	}, Options{})
	if err == nil {
		t.Fatal("New() error = nil, want unsupported kind error")
	}
	if !strings.Contains(err.Error(), "single provider") {
		t.Fatalf("error = %q, want single-provider rejection", err.Error())
	}
}

type captureTransport struct {
	request      *http.Request
	requestBody  string
	responseBody string
}

func (transport *captureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.request = request
	if request.Body != nil {
		body, _ := io.ReadAll(request.Body)
		transport.requestBody = string(body)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(transport.responseBody)),
		Request:    request,
	}, nil
}

func (transport *captureTransport) body() io.Reader {
	return strings.NewReader(transport.requestBody)
}

func drainStream(t *testing.T, stream <-chan qgruntime.StreamEvent) {
	t.Helper()
	for event := range stream {
		if event.Type == qgruntime.StreamEventError {
			t.Fatalf("stream error: %s", event.Error)
		}
	}
}
