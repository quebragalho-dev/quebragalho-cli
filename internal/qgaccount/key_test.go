package qgaccount

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestKeyParsesProfileAndTreatsNullModelsAsAll(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/key" {
			t.Fatalf("path = %q, want /key", r.URL.Path)
		}
		// Real shape, including the extra limits object the client must ignore.
		_, _ = w.Write([]byte(`{"id":"e1645173","name":"teste cli","key_prefix":"qg-sk-Rhcii8","created_at":"2026-10-09T03:08:07Z","allowed_models":null,"plan":{"code":"prepaid","type":"prepaid"},"limits":{"rpm":0,"max_concurrency":20}}`))
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	info, err := client.Key(context.Background())
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if info.ID != "e1645173" || info.Name != "teste cli" || info.KeyPrefix != "qg-sk-Rhcii8" || info.CreatedAt != "2026-10-09T03:08:07Z" {
		t.Fatalf("key info mismatch: %+v", info)
	}
	if info.AllowedModels != nil {
		t.Fatalf("AllowedModels = %v, want nil (null = every model)", info.AllowedModels)
	}
	if info.Plan.Code != "prepaid" || info.Plan.Type != "prepaid" {
		t.Fatalf("plan mismatch: %+v", info.Plan)
	}
}

func TestKeyKeepsAllowedModelsList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "x", "name": "n", "key_prefix": "p", "created_at": "2026-01-01T00:00:00Z",
			"allowed_models": []string{"muse-spark-1.3-contributor"},
			"plan":           map[string]string{"code": "prepaid", "type": "prepaid"},
		})
	}))
	defer server.Close()

	client := NewClient(testKey)
	client.BaseURL = server.URL
	info, err := client.Key(context.Background())
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if !reflect.DeepEqual(info.AllowedModels, []string{"muse-spark-1.3-contributor"}) {
		t.Fatalf("AllowedModels = %v", info.AllowedModels)
	}
}
