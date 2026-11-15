package config

import (
	"strings"
	"testing"
)

// Characterization of legacy multi-provider inputs. The resolver still accepts
// kind=anthropic/google (normalizing their official endpoints) and still builds
// profiles from ANTHROPIC_*/GEMINI_*/GOOGLE_* env vars; the single-provider
// factory rejects them later with its own error. Deleting dead transports must
// not change any behavior below — and the openai-compatible custom-endpoint
// path must keep working byte-identically.

func TestLegacyAnthropicKindNormalizesOfficialEndpoint(t *testing.T) {
	normalized, _, err := normalizeProviders([]ProviderProfile{{
		Name:         "legacy",
		ProviderKind: ProviderKindAnthropic,
		Model:        "m",
	}}, "legacy", map[string]string{})
	if err != nil {
		t.Fatalf("normalizeProviders = %v, want anthropic to normalize", err)
	}
	if len(normalized) != 1 {
		t.Fatalf("normalized = %#v, want one provider", normalized)
	}
	if normalized[0].BaseURL != AnthropicBaseURL {
		t.Fatalf("BaseURL = %q, want %q", normalized[0].BaseURL, AnthropicBaseURL)
	}
}

func TestLegacyAnthropicKindRejectsCustomBaseURL(t *testing.T) {
	_, _, err := normalizeProviders([]ProviderProfile{{
		Name:         "legacy",
		ProviderKind: ProviderKindAnthropic,
		BaseURL:      "https://proxy.example/v1",
	}}, "legacy", map[string]string{})
	if err == nil {
		t.Fatal("custom baseURL for kind=anthropic normalized, want official-endpoint error")
	}
	if !strings.Contains(err.Error(), "requires official baseURL") {
		t.Fatalf("error = %q, want official-baseURL complaint", err.Error())
	}
}

func TestLegacyGoogleKindNormalizesOfficialEndpoint(t *testing.T) {
	normalized, _, err := normalizeProviders([]ProviderProfile{{
		Name:         "legacy",
		ProviderKind: ProviderKindGoogle,
		Model:        "m",
	}}, "legacy", map[string]string{})
	if err != nil {
		t.Fatalf("normalizeProviders = %v, want google to normalize", err)
	}
	if normalized[0].BaseURL != GoogleBaseURL {
		t.Fatalf("BaseURL = %q, want %q", normalized[0].BaseURL, GoogleBaseURL)
	}
}

func TestLegacyAnthropicCompatKindRequiresBaseURL(t *testing.T) {
	_, _, err := normalizeProviders([]ProviderProfile{{
		Name:         "proxy",
		ProviderKind: ProviderKindAnthropicCompat,
	}}, "proxy", map[string]string{})
	if err == nil {
		t.Fatal("baseURL-less anthropic-compatible normalized, want requires-baseURL error")
	}
	if !strings.Contains(err.Error(), "requires baseURL") {
		t.Fatalf("error = %q, want requires-baseURL complaint", err.Error())
	}
}

func TestLegacyAnthropicEnvCreatesAnthropicProfile(t *testing.T) {
	cfg := &FileConfig{}
	applyProviderEnv(cfg, ProviderKindAnthropic, envProfile{Name: "anthropic", APIKey: "sk-ant-test"})
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %#v, want one anthropic profile", cfg.Providers)
	}
	profile := cfg.Providers[0]
	if profile.ProviderKind != ProviderKindAnthropic {
		t.Fatalf("kind = %q, want anthropic", profile.ProviderKind)
	}
	if profile.APIKey != "sk-ant-test" {
		t.Fatalf("APIKey = %q, want env key", profile.APIKey)
	}
}

func TestLegacyAnthropicCustomBaseURLPromotesToCompat(t *testing.T) {
	cfg := &FileConfig{}
	applyProviderEnv(cfg, ProviderKindAnthropic, envProfile{
		Name:    "anthropic",
		APIKey:  "sk-ant-test",
		BaseURL: "https://proxy.example/v1",
	})
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %#v, want one profile", cfg.Providers)
	}
	if cfg.Providers[0].ProviderKind != ProviderKindAnthropicCompat {
		t.Fatalf("kind = %q, want anthropic-compatible", cfg.Providers[0].ProviderKind)
	}
}

func TestLegacyGeminiEnvCreatesGoogleProfile(t *testing.T) {
	cfg := &FileConfig{}
	applyProviderEnv(cfg, ProviderKindGoogle, envProfile{Name: "google", APIKey: "gemini-test"})
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %#v, want one google profile", cfg.Providers)
	}
	profile := cfg.Providers[0]
	if profile.ProviderKind != ProviderKindGoogle {
		t.Fatalf("kind = %q, want google", profile.ProviderKind)
	}
	if profile.APIKey != "gemini-test" {
		t.Fatalf("APIKey = %q, want env key", profile.APIKey)
	}
}

func TestLegacyOpenAICompatCustomEndpointKeepsWorking(t *testing.T) {
	normalized, active, err := normalizeProviders([]ProviderProfile{{
		Name:         "proxy",
		ProviderKind: ProviderKindOpenAICompatible,
		BaseURL:      "https://proxy.example/v1",
		Model:        "m",
	}}, "proxy", map[string]string{})
	if err != nil {
		t.Fatalf("normalizeProviders = %v, want custom endpoint to normalize", err)
	}
	if active.BaseURL != "https://proxy.example/v1" {
		t.Fatalf("BaseURL = %q, want custom endpoint preserved", active.BaseURL)
	}
	if len(normalized) != 1 || normalized[0].ProviderKind != ProviderKindOpenAICompatible {
		t.Fatalf("providers = %#v, want one openai-compatible profile", normalized)
	}
}

func TestLegacyOpenAIKindWithCustomBaseURLPromotesToCompat(t *testing.T) {
	cfg := &FileConfig{}
	applyProviderEnv(cfg, ProviderKindOpenAI, envProfile{
		Name:    "openai",
		APIKey:  "sk-x",
		BaseURL: "https://proxy.example/v1",
	})
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %#v, want one profile", cfg.Providers)
	}
	if cfg.Providers[0].ProviderKind != ProviderKindOpenAICompatible {
		t.Fatalf("kind = %q, want openai-compatible promotion", cfg.Providers[0].ProviderKind)
	}
}

func TestLegacyEmptyTransportKindDefault(t *testing.T) {
	// Characterization: the default arm maps an empty transport to "" —
	// ad-hoc descriptors carry Transport "", and the mapping must stay put.
	if got := providerKindForCatalogTransport(""); got != "" {
		t.Fatalf("providerKindForCatalogTransport(\"\") = %q, want empty", got)
	}
}
