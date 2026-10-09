package cli

import (
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// `qg providers check` must demand the credential the profile declares: the
// qg catalog profile errors without its env key, while a stale-catalog profile
// that already carries a credential passes (the custom-catalog entries these
// shapes used to name were removed with the single-provider catalog).
func TestValidateProviderRuntimeReadyCredentialRules(t *testing.T) {
	cases := []struct {
		name    string
		profile config.ProviderProfile
		wantErr bool
	}{
		{
			name: "catalog provider missing key still errors",
			profile: config.ProviderProfile{
				Name:      "qg",
				CatalogID: "qg",
				Model:     "muse-spark-1.3-contributor",
			},
			wantErr: true,
		},
		{
			name: "stale catalog profile with a credential passes",
			profile: config.ProviderProfile{
				Name:         "legacy",
				ProviderKind: config.ProviderKindOpenAICompatible,
				BaseURL:      "http://192.168.1.50:8080/v1",
				APIKey:       "sk-legacy",
				Model:        "custom-model",
			},
			wantErr: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateProviderRuntimeReady(c.profile)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateProviderRuntimeReady() error = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}
