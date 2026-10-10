package tui

import (
	"os"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providercatalog"
)

func providerWizardDiscoveryProfile(provider providercatalog.Descriptor, apiKey string, baseURL string) config.ProviderProfile {
	profile := providerWizardProfile(provider, provider.DefaultModel, apiKey, baseURL, "")
	if strings.TrimSpace(profile.APIKey) == "" && strings.TrimSpace(profile.APIKeyEnv) != "" {
		profile.APIKey = strings.TrimSpace(os.Getenv(profile.APIKeyEnv))
	}
	return profile
}
