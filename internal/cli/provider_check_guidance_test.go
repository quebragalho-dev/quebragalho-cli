package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providerhealth"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestRunProvidersCheckMissingKeyPrintsNextAction(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	deps := commandCenterDeps(t)
	deps.resolveConfig = func(string, config.Overrides) (config.ResolvedConfig, error) {
		profile := providerCheckGuidanceProfile()
		return config.ResolvedConfig{ActiveProvider: "qg", Provider: profile, Providers: []config.ProviderProfile{profile}, MaxTurns: 7}, nil
	}
	deps.newProvider = func(config.ProviderProfile) (qgruntime.Provider, error) {
		t.Fatal("newProvider should not run with a missing API key")
		return nil, nil
	}

	exitCode := runWithDeps([]string{"providers", "check", "qg"}, &stdout, &stderr, deps)

	if exitCode != exitProvider {
		t.Fatalf("exit code = %d, want %d", exitCode, exitProvider)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	output := stderr.String()
	if !strings.Contains(output, "provider qg requires API key; set QG_API_KEY (or QUEBRAGALHO_API_KEY)") {
		t.Fatalf("missing key error did not name env var: %q", output)
	}
	if !strings.Contains(output, "next: set QG_API_KEY (or QUEBRAGALHO_API_KEY) and rerun qg providers check qg") {
		t.Fatalf("missing next action: %q", output)
	}
}

func TestRunProvidersCheckMissingKeyDoesNotLeakSecrets(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	deps := commandCenterDeps(t)
	deps.resolveConfig = func(string, config.Overrides) (config.ResolvedConfig, error) {
		profile := providerCheckGuidanceProfile()
		profile.BaseURL = "https://user:base-secret@api.groq.com/openai/v1?api_key=query-secret"
		profile.AuthHeader = "Authorization"
		profile.CustomHeaders = map[string]string{"X-Token": "custom-secret"}
		return config.ResolvedConfig{ActiveProvider: "qg", Provider: profile, Providers: []config.ProviderProfile{profile}, MaxTurns: 7}, nil
	}

	exitCode := runWithDeps([]string{"providers", "check", "qg"}, &stdout, &stderr, deps)

	if exitCode != exitProvider {
		t.Fatalf("exit code = %d, want %d", exitCode, exitProvider)
	}
	output := stdout.String() + stderr.String()
	for _, leaked := range []string{"base-secret", "query-secret", "custom-secret", "user:"} {
		if strings.Contains(output, leaked) {
			t.Fatalf("providers check leaked %q: %q", leaked, output)
		}
	}
	if strings.Contains(output, "Authorization:") || strings.Contains(output, "Bearer ") {
		t.Fatalf("providers check leaked auth header material: %q", output)
	}
}

func TestRunProvidersCheckConnectivitySkippedPrintsNextAction(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	deps := commandCenterDeps(t)

	exitCode := runWithDeps([]string{"providers", "check", "work"}, &stdout, &stderr, deps)

	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, want %d: %s", exitCode, exitSuccess, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "next: run qg providers check work --connectivity") {
		t.Fatalf("missing connectivity next action: %q", output)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunProvidersCheckConnectivityFailurePrintsGuidance(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	deps := commandCenterDeps(t)
	deps.resolveConfig = func(string, config.Overrides) (config.ResolvedConfig, error) {
		profile := config.ProviderProfile{
			Name:         "local",
			ProviderKind: config.ProviderKindOpenAICompatible,
			BaseURL:      "https://api.example.com/v1",
			APIKey:       "sk-test-secret",
			Model:        "custom-model",
		}
		return config.ResolvedConfig{ActiveProvider: "local", Provider: profile, Providers: []config.ProviderProfile{profile}, MaxTurns: 7}, nil
	}
	deps.probeProviderHealth = func(context.Context, providerhealth.Options) providerhealth.Result {
		return providerhealth.Result{
			Status: providerhealth.StatusFail,
			Checks: []providerhealth.Check{
				{ID: "provider.connectivity", Status: providerhealth.StatusFail, Category: providerhealth.CategoryAuth, Message: "Provider endpoint returned 401: invalid API key"},
			},
		}
	}

	exitCode := runWithDeps([]string{"providers", "check", "local", "--connectivity"}, &stdout, &stderr, deps)

	if exitCode != exitProvider {
		t.Fatalf("exit code = %d, want %d", exitCode, exitProvider)
	}
	output := stdout.String()
	if !strings.Contains(output, "provider.connectivity: Provider endpoint returned 401: invalid API key") {
		t.Fatalf("missing primary health check message: %q", output)
	}
	if !strings.Contains(output, "next: verify the API key, base URL, and model, then rerun qg providers check local --connectivity") {
		t.Fatalf("missing connectivity failure next action: %q", output)
	}
	if strings.Contains(output, "sk-test-secret") {
		t.Fatalf("providers check leaked API key: %q", output)
	}
}

func TestRunProvidersCheckConnectivityOKPrintsExecNextAction(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	deps := commandCenterDeps(t)
	deps.probeProviderHealth = func(context.Context, providerhealth.Options) providerhealth.Result {
		return providerhealth.Result{
			Status: providerhealth.StatusPass,
			Checks: []providerhealth.Check{
				{ID: "provider.connectivity", Status: providerhealth.StatusPass, Message: "reachable"},
			},
		}
	}

	exitCode := runWithDeps([]string{"providers", "check", "work", "--connectivity"}, &stdout, &stderr, deps)

	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, want %d: %s", exitCode, exitSuccess, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, `next: run qg exec "hello" --model gpt-4.1`) {
		t.Fatalf("missing ready next action: %q", output)
	}
}

func providerCheckGuidanceProfile() config.ProviderProfile {
	return config.ProviderProfile{
		Name:         "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		CatalogID:    "qg",
		BaseURL:      "https://api.quebragalho.dev/v1",
		APIKeyEnv:    "QUEBRAGALHO_API_KEY",
		Model:        "muse-spark-1.3-contributor",
	}
}

// A keyless profile that still reaches the connectivity probe (e.g. right
// after `qg auth logout qg`) must get the credential guidance — both gateway
// env vars plus `qg login` — not the generic "verify the API key" advice.
func TestRunProvidersCheckConnectivityKeylessCitesCredentialEnv(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	deps := commandCenterDeps(t)
	deps.resolveConfig = func(string, config.Overrides) (config.ResolvedConfig, error) {
		profile := providerCheckGuidanceProfile()
		return config.ResolvedConfig{ActiveProvider: "qg", Provider: profile, Providers: []config.ProviderProfile{profile}, MaxTurns: 7}, nil
	}
	deps.probeProviderHealth = func(context.Context, providerhealth.Options) providerhealth.Result {
		return providerhealth.Result{
			Status: providerhealth.StatusFail,
			Checks: []providerhealth.Check{
				{ID: "provider.auth", Status: providerhealth.StatusFail, Category: providerhealth.CategoryAuth, Message: "Provider qg requires API credentials."},
			},
		}
	}

	exitCode := runWithDeps([]string{"providers", "check", "qg", "--connectivity"}, &stdout, &stderr, deps)
	if exitCode != exitProvider {
		t.Fatalf("exit code = %d, want %d: %s", exitCode, exitProvider, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "QG_API_KEY (or QUEBRAGALHO_API_KEY)") {
		t.Fatalf("keyless connectivity advice must cite both gateway env vars: %q", output)
	}
	if !strings.Contains(output, "qg login") {
		t.Fatalf("keyless connectivity advice must mention `qg login`: %q", output)
	}
}
