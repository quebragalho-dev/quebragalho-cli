package tui

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// Regression for issue #555's follow-up: /providers must not warn that a
// no-auth custom endpoint is missing a credential, and must not resurrect
// that warning for a profile the pre-fix wizard already stamped with the
// catalog's own guessed default env var.
func TestProviderCredentialRequiredCustomEndpoint(t *testing.T) {
	cases := []struct {
		name    string
		profile config.ProviderProfile
		kind    string
		want    bool
	}{
		{
			name: "custom openai compatible with no credential configured",
			profile: config.ProviderProfile{
				Name:      "local-llama",
				CatalogID: "custom-openai-compatible",
				BaseURL:   "http://192.168.1.50:8080/v1",
			},
			want: false,
		},
		{
			name: "custom openai compatible with stale legacy default env",
			profile: config.ProviderProfile{
				Name:      "local-llama",
				CatalogID: "custom-openai-compatible",
				APIKeyEnv: "OPENAI_API_KEY",
			},
			want: false,
		},
		{
			name: "typed openai-compatible endpoint with explicit env expects a credential",
			profile: config.ProviderProfile{
				Name:         "local-llama",
				CatalogID:    "custom-openai-compatible",
				ProviderKind: config.ProviderKindOpenAICompatible,
				APIKeyEnv:    "LLAMA_CPP_API_KEY",
			},
			kind: string(config.ProviderKindOpenAICompatible),
			want: true,
		},
		{
			name: "catalog provider still requires auth",
			profile: config.ProviderProfile{
				Name:      "qg",
				CatalogID: "qg",
			},
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := providerCredentialRequired(c.profile, c.kind); got != c.want {
				t.Fatalf("providerCredentialRequired() = %v, want %v", got, c.want)
			}
		})
	}
}
