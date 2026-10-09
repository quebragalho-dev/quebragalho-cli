package providermodeldiscovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

func TestDiscoverOpenAICompatibleModelsFetchesModelsEndpoint(t *testing.T) {
	const apiKey = "sk-live-secret"
	var gotPath string
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "model-b", "object": "model"},
				{"id": "model-a", "object": "model"},
				{"id": "model-a", "object": "model"},
				{"object": "model"}
			]
		}`))
	}))
	defer server.Close()

	models, err := Discover(context.Background(), config.ProviderProfile{
		Name:         "test",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      server.URL + "/v1",
		APIKey:       apiKey,
	}, Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("requested path = %q, want /v1/models", gotPath)
	}
	if gotAuth != "Bearer "+apiKey {
		t.Fatalf("Authorization = %q, want bearer API key", gotAuth)
	}
	if got, want := modelIDs(models), []string{"model-a", "model-b"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
}

func TestParseModelsResponseCapturesContextAndFree(t *testing.T) {
	models, err := parseModelsResponse([]byte(`{
		"data": [
			{"id": "xiaomi/mimo-v2.5-pro", "name": "MiMo V2.5-Pro", "context_window": 262144},
			{"id": "nvidia/nemotron-3-ultra:free", "name": "Nemotron", "context_length": 128000, "is_free": true}
		]
	}`))
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %#v, want 2", models)
	}
	if got, want := modelIDs(models), []string{"nvidia/nemotron-3-ultra:free", "xiaomi/mimo-v2.5-pro"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("models = %#v, want %#v", got, want)
	}
	byID := map[string]Model{}
	for _, model := range models {
		byID[model.ID] = model
	}
	if byID["xiaomi/mimo-v2.5-pro"].ContextWindow != 262144 {
		t.Fatalf("mimo context = %d", byID["xiaomi/mimo-v2.5-pro"].ContextWindow)
	}
	if byID["nvidia/nemotron-3-ultra:free"].Description != "Nemotron (free)" {
		t.Fatalf("free model description = %q, want annotated free label", byID["nvidia/nemotron-3-ultra:free"].Description)
	}
}

func TestParseModelsResponseSupportsChatGPTCatalog(t *testing.T) {
	models, err := parseModelsResponse([]byte(`{
		"models": [
			{"slug":"gpt-5.6-sol","display_name":"GPT-5.6 Sol","description":"Frontier coding model","visibility":"list","context_window":400000,"default_reasoning_level":"high","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"}],"service_tiers":[{"id":"priority"}],"default_service_tier":"standard"},
			{"slug":"gpt-5.6-terra","display_name":"GPT-5.6 Terra","visibility":"list"},
			{"slug":"internal-router","display_name":"Internal","visibility":"hide"},
			{"slug":"server-only","display_name":"Server only","visibility":"none"}
		]
	}`))
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if got, want := modelIDs(models), []string{"gpt-5.6-sol", "gpt-5.6-terra"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("models = %#v, want visible ChatGPT models %#v", got, want)
	}
	if models[0].Description != "GPT-5.6 Sol" || models[0].ContextWindow != 400000 {
		t.Fatalf("ChatGPT metadata = %#v", models[0])
	}
	if got := strings.Join(models[0].ReasoningEfforts, ","); got != "low,medium,high,xhigh" {
		t.Fatalf("reasoning efforts = %q", got)
	}
	if models[0].DefaultReasoningEffort != "high" || !models[0].Reasoning {
		t.Fatalf("reasoning metadata = %#v", models[0])
	}
	if got := strings.Join(models[0].ServiceTiers, ","); got != "priority" || models[0].DefaultServiceTier != "standard" {
		t.Fatalf("service tier metadata = %#v", models[0])
	}
}

func TestParseModelsResponseNormalizesLegacyFastTier(t *testing.T) {
	models, err := parseModelsResponse([]byte(`{"data":[{"id":"gpt-test","additional_speed_tiers":["fast","priority"]}]}`))
	if err != nil {
		t.Fatalf("parseModelsResponse: %v", err)
	}
	if got := strings.Join(models[0].ServiceTiers, ","); got != "priority" {
		t.Fatalf("service tiers = %q, want priority", got)
	}
}

func TestDiscoverSendsAuthAndCustomHeadersWithoutAttribution(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for header, want := range map[string]string{
			"Authorization": "Bearer test-key",
			"X-Trace":       "test",
		} {
			if got := r.Header.Get(header); got != want {
				t.Errorf("%s = %q, want %q", header, got, want)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"muse-spark-1.3-contributor"}]}`))
	}))
	defer server.Close()

	_, err := Discover(context.Background(), config.ProviderProfile{
		CatalogID:     "qg",
		ProviderKind:  config.ProviderKindOpenAICompatible,
		BaseURL:       server.URL + "/v1",
		APIKey:        "test-key",
		CustomHeaders: map[string]string{"X-Trace": "test"},
	}, Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
}

func TestDiscoverOpenAICompatibleModelsHonorsAuthHeaderValue(t *testing.T) {
	// A profile can authenticate via a raw auth-header value instead of APIKey;
	// discovery must send it rather than probe unauthenticated.
	const headerValue = "Bearer raw-header-secret"
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer server.Close()

	if _, err := Discover(context.Background(), config.ProviderProfile{
		Name:            "test",
		ProviderKind:    config.ProviderKindOpenAICompatible,
		BaseURL:         server.URL + "/v1",
		AuthHeaderValue: headerValue,
	}, Options{HTTPClient: server.Client()}); err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if gotAuth != headerValue {
		t.Fatalf("Authorization = %q, want raw auth-header value %q", gotAuth, headerValue)
	}
}

func TestDiscoveryHasCredential(t *testing.T) {
	cases := []struct {
		name    string
		profile config.ProviderProfile
		want    bool
	}{
		{"api key", config.ProviderProfile{APIKey: "sk-x"}, true},
		{"auth header only", config.ProviderProfile{AuthHeaderValue: "Bearer t"}, true},
		{"both", config.ProviderProfile{APIKey: "sk-x", AuthHeaderValue: "Bearer t"}, true},
		{"neither", config.ProviderProfile{}, false},
		{"whitespace only", config.ProviderProfile{APIKey: "  ", AuthHeaderValue: "\t"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := discoveryHasCredential(tc.profile); got != tc.want {
				t.Fatalf("discoveryHasCredential = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiscoverOpenAICompatibleModelsHandlesBaseURLWithoutVersion(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
	}))
	defer server.Close()

	models, err := Discover(context.Background(), config.ProviderProfile{
		Name:         "local",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      server.URL,
	}, Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("Discover returned error: %v", err)
	}
	if gotPath != "/models" {
		t.Fatalf("requested path = %q, want /models for provider base URLs without /v1", gotPath)
	}
	if len(models) != 1 || models[0].ID != "local-model" {
		t.Fatalf("models = %#v, want local-model", models)
	}
}

func TestDiscoverOpenAICompatibleModelsRejectsUnsupportedProviders(t *testing.T) {
	_, err := Discover(context.Background(), config.ProviderProfile{
		Name:         "google",
		ProviderKind: config.ProviderKindGoogle,
		BaseURL:      "https://generativelanguage.googleapis.com",
	}, Options{})
	if err == nil || !strings.Contains(err.Error(), "single provider") {
		t.Fatalf("Discover error = %v, want unsupported provider message", err)
	}
}

func TestDiscoverOpenAICompatibleModelsRedactsSecretsInErrors(t *testing.T) {
	const apiKey = "sk-live-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key "+apiKey, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := Discover(context.Background(), config.ProviderProfile{
		Name:         "test",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      server.URL + "/v1",
		APIKey:       apiKey,
	}, Options{HTTPClient: server.Client()})
	if err == nil {
		t.Fatal("Discover should return an error for non-2xx status")
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("error leaked API key: %v", err)
	}
	if !strings.Contains(err.Error(), "[REDACTED]") {
		t.Fatalf("error should contain redacted marker, got: %v", err)
	}
}

func TestDiscoverCatalogQGUsesLiveListAndDropsNonCoding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-live" {
			t.Fatalf("Authorization = %q, want bearer key", got)
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"muse-spark-1.3-contributor","name":"Muse Spark","context_window":200000},
			{"id":"hy4"},
			{"id":"whisper-1"}
		]}`))
	}))
	defer server.Close()

	provider, ok := providercatalog.Get("qg")
	if !ok {
		t.Fatal("qg missing from catalog")
	}
	models, err := DiscoverCatalog(context.Background(), provider, config.ProviderProfile{
		Name:         "qg",
		CatalogID:    "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      server.URL + "/v1",
		APIKey:       "sk-live",
	}, Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("DiscoverCatalog returned error: %v", err)
	}
	if got, want := modelIDs(models), []string{"hy4", "muse-spark-1.3-contributor"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("models = %#v, want live coding ids only (no whisper)", got)
	}
	if models[1].ContextWindow != 200000 {
		t.Fatalf("muse-spark metadata = %#v, want live context window", models[1])
	}
}

func modelIDs(models []Model) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}
