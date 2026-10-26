package cli

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

func TestProviderProfileIsLocal(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{"loopback name", "http://localhost:11434/v1", true},
		{"loopback v4", "http://127.0.0.1:8080", true},
		{"loopback v6", "http://[::1]:10531/v1", true},
		{"remote", "https://api.moonshot.ai/v1", false},
		{"contains-localhost-substring", "https://notlocalhost.com/v1", false},
		{"host-with-127-substring", "https://api127.0.0.1.example.com", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := providerProfileIsLocal(config.ProviderProfile{BaseURL: tc.baseURL})
			if got != tc.want {
				t.Fatalf("providerProfileIsLocal(%q) = %v, want %v", tc.baseURL, got, tc.want)
			}
		})
	}
}

// TestFirstUsableProviderAcceptsOAuthLoginProfile: a keyless catalog profile
// whose credential is a stored OAuth login (the shape the login flows persist)
// must be usable during active-pointer recovery — the same rule setupRequired
// and usableSavedProviders apply — instead of falling through to onboarding.
