package providers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

const testConversationHeader = "X-QG-Conversation-ID"

const testConversationSession = "qg_20260101120000_1234567890_1"

func qgConversationTransport() *captureTransport {
	return &captureTransport{responseBody: "data: [DONE]\n\n"}
}

func qgProfile() config.ProviderProfile {
	return config.ProviderProfile{
		Name:         "qg",
		CatalogID:    "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		APIKey:       "sk-qg-test",
		Model:        "muse-spark-1.3-contributor",
	}
}

func requestConversationID(t *testing.T, provider qgruntime.Provider, transport *captureTransport, sessionID string) (string, string) {
	t.Helper()
	stream, err := provider.StreamCompletion(context.Background(), qgruntime.CompletionRequest{
		Messages:       []qgruntime.Message{{Role: qgruntime.MessageRoleUser, Content: "hi"}},
		PromptCacheKey: sessionID,
	})
	if err != nil {
		t.Fatalf("StreamCompletion() error = %v", err)
	}
	drainStream(t, stream)
	return transport.request.Header.Get(testConversationHeader), transport.requestBody
}

// The QG gateway gets the session id on every request: same session, same
// value across turns, and never a prompt_cache_key body field.
func TestFactorySendsConversationIDForQGEndpoint(t *testing.T) {
	for _, baseURL := range []string{"", "https://api.quebragalho.dev/v1", "https://api.quebragalho.dev/v1/", "HTTPS://API.QUEBRAGALHO.DEV/V1"} {
		profile := qgProfile()
		profile.BaseURL = baseURL
		transport := qgConversationTransport()
		provider, err := New(profile, Options{HTTPClient: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatalf("New(%q) error = %v", baseURL, err)
		}
		first, body := requestConversationID(t, provider, transport, testConversationSession)
		if first != testConversationSession {
			t.Fatalf("baseURL %q: header = %q, want the session id", baseURL, first)
		}
		second, _ := requestConversationID(t, provider, transport, testConversationSession)
		if second != testConversationSession {
			t.Fatalf("baseURL %q: second turn header = %q, want the session id", baseURL, second)
		}
		if strings.Contains(body, "prompt_cache_key") {
			t.Fatalf("baseURL %q: body carries prompt_cache_key for the qg endpoint: %s", baseURL, body)
		}
	}
}

// Two sessions never share a conversation id.
func TestFactorySendsDistinctConversationIDsPerSession(t *testing.T) {
	transport := qgConversationTransport()
	provider, err := New(qgProfile(), Options{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	first, _ := requestConversationID(t, provider, transport, testConversationSession)
	other := "qg_20260102120000_0987654321_2"
	second, _ := requestConversationID(t, provider, transport, other)
	if first != testConversationSession || second != other {
		t.Fatalf("headers = %q, %q, want the two session ids", first, second)
	}
	if first == second {
		t.Fatalf("two sessions shared conversation id %q", first)
	}
}

// Any other endpoint gets no conversation header, however close it looks.
func TestFactoryOmitsConversationIDForOtherEndpoints(t *testing.T) {
	for _, baseURL := range []string{
		"https://gateway.example.com/v1/",
		"https://api.quebragalho.dev/v1/extra",
		"https://api.quebragalho.dev.evil.com/v1",
	} {
		profile := qgProfile()
		profile.BaseURL = baseURL
		profile.CatalogID = ""
		transport := qgConversationTransport()
		provider, err := New(profile, Options{HTTPClient: &http.Client{Transport: transport}})
		if err != nil {
			t.Fatalf("New(%q) error = %v", baseURL, err)
		}
		got, _ := requestConversationID(t, provider, transport, testConversationSession)
		if got != "" {
			t.Fatalf("baseURL %q: header = %q, want no header", baseURL, got)
		}
	}
}

// A session id the contract rejects never reaches the wire either.
func TestFactoryOmitsInvalidConversationID(t *testing.T) {
	transport := qgConversationTransport()
	provider, err := New(qgProfile(), Options{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for _, sessionID := range []string{"", strings.Repeat("x", 200), " padded ", "with\ttab"} {
		got, _ := requestConversationID(t, provider, transport, sessionID)
		if got != "" {
			t.Fatalf("session id %q produced header %q, want no header", sessionID, got)
		}
	}
}
