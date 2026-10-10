package providers

import (
	"context"
	"net/http"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestNewQGDefaultsToQuebragalhoEndpoint(t *testing.T) {
	transport := &captureTransport{responseBody: "data: [DONE]\n\n"}
	client := &http.Client{Transport: transport}
	provider, err := New(config.ProviderProfile{
		Name:         "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		APIKey:       "sk-qg",
		Model:        "muse-spark-1.3-contributor",
	}, Options{HTTPClient: client})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	stream, err := provider.StreamCompletion(context.Background(), qgruntime.CompletionRequest{
		Messages: []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("StreamCompletion() error = %v", err)
	}
	for range stream {
	}
	if transport.request == nil {
		t.Fatal("HTTP client was not used")
	}
	want := "https://api.quebragalho.dev/v1/chat/completions"
	if transport.request.URL.String() != want {
		t.Fatalf("request URL = %q, want %q", transport.request.URL.String(), want)
	}
}

func TestNewQGRejectsAnthropicKind(t *testing.T) {
	_, err := New(config.ProviderProfile{
		Name:         "qg",
		ProviderKind: config.ProviderKindAnthropic,
		APIKey:       "sk-qg",
		Model:        "muse-spark-1.3-contributor",
	}, Options{})
	if err == nil {
		t.Fatal("New() error = nil, want single-provider rejection")
	}
}
