package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestFactorySendsPromptCacheKeyForQGOnly(t *testing.T) {
	// QG endpoint gets the session id as prompt_cache_key.
	transport := &captureTransport{responseBody: "data: [DONE]\n\n"}
	provider, err := New(qgProfile(), Options{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.StreamCompletion(context.Background(), qgruntime.CompletionRequest{
		Messages:       []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "hi"}},
		PromptCacheKey: testConversationSession,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range stream {
	}
	var body map[string]any
	if err := json.NewDecoder(transport.body()).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["prompt_cache_key"] != testConversationSession {
		t.Fatalf("QG prompt_cache_key = %#v, want %q", body["prompt_cache_key"], testConversationSession)
	}

	// Custom base URL must not get the key.
	custom := qgProfile()
	custom.BaseURL = "https://gateway.example.com/v1/"
	custom.CatalogID = ""
	transport2 := &captureTransport{responseBody: "data: [DONE]\n\n"}
	provider2, err := New(custom, Options{HTTPClient: &http.Client{Transport: transport2}})
	if err != nil {
		t.Fatal(err)
	}
	stream2, err := provider2.StreamCompletion(context.Background(), qgruntime.CompletionRequest{
		Messages:       []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "hi"}},
		PromptCacheKey: testConversationSession,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range stream2 {
	}
	var body2 map[string]any
	if err := json.NewDecoder(transport2.body()).Decode(&body2); err != nil {
		t.Fatalf("decode body2: %v", err)
	}
	if _, ok := body2["prompt_cache_key"]; ok {
		t.Fatalf("custom endpoint must omit prompt_cache_key, got %#v", body2)
	}
}
