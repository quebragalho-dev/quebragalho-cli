package cli

import (
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

// providerProfileIsLocal reports whether a provider points at a local endpoint
// (a loopback URL or a catalog entry flagged Local), so the fallback prefers a
// remote keyed provider that is more likely reachable. Test-only: no production
// path calls it.
func providerProfileIsLocal(profile config.ProviderProfile) bool {
	if catalogID := strings.TrimSpace(profile.CatalogID); catalogID != "" {
		if descriptor, err := providercatalog.Require(catalogID); err == nil && descriptor.Local {
			return true
		}
	}
	base := strings.TrimSpace(profile.BaseURL)
	if base == "" {
		return false
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	// Also treat explicit private-network IPs (192.168.x.y, 10.x.y.z,
	// 172.16-31.x.y) as local — the user is pointing at their own LAN box.
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && ip.IsPrivate() {
		return true
	}
	return false
}

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
