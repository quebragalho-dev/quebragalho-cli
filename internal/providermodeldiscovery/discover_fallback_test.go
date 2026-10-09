package providermodeldiscovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

func TestDiscoverCatalogFallsBackWhenLiveIDsUnusable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected request path %q", r.URL.Path)
		}
		// 200, but only known non-coding ids: the filtered live list is empty,
		// so the result must fall back to the fixed catalog (M11), not collapse
		// to an empty list.
		_, _ = w.Write([]byte(`{"data":[{"id":"whisper-1"},{"id":"text-embedding-3-large"}]}`))
	}))
	defer server.Close()

	provider, ok := providercatalog.Get("qg")
	if !ok {
		t.Fatal("qg missing from catalog")
	}
	models, err := DiscoverCatalog(context.Background(), provider, config.ProviderProfile{
		CatalogID:    "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      server.URL + "/v1",
		APIKey:       "sk-live",
	}, Options{HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("DiscoverCatalog returned error: %v", err)
	}
	if len(models) != 19 || models[0].ID != "muse-spark-1.3-contributor" {
		t.Fatalf("models = %#v, want the 19-id fixed catalog fallback", modelIDs(models))
	}
}
