package providers

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func qgGateProfile() config.ProviderProfile {
	return config.ProviderProfile{
		Name:         "qg",
		ProviderKind: config.ProviderKindOpenAICompatible,
		BaseURL:      "https://api.quebragalho.dev/v1",
		APIKey:       "sk-qg-gate-test",
		Model:        "muse-spark-1.3-contributor",
	}
}

func buildGateProvider(t *testing.T, profile config.ProviderProfile) qgruntime.Provider {
	t.Helper()
	provider, err := New(profile, Options{UserAgent: "qg-gate-test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return provider
}

type fakeGateProvider struct{}

func (fakeGateProvider) StreamCompletion(context.Context, qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	return nil, nil
}

func TestOptimizedTurnSessionsAlwaysOffSingleProvider(t *testing.T) {
	profile := qgGateProfile()
	provider := buildGateProvider(t, profile)
	if tsp, ok := OptimizedTurnSessions(profile, provider, Options{}); ok || tsp != nil {
		t.Fatal("OptimizedTurnSessions must stay off for the single QG provider")
	}
	if _, ok := OptimizedTurnSessions(profile, fakeGateProvider{}, Options{}); ok {
		t.Fatal("OptimizedTurnSessions must stay off for foreign provider values")
	}
	if _, ok := OptimizedTurnSessions(profile, nil, Options{}); ok {
		t.Fatal("OptimizedTurnSessions must stay off for nil provider")
	}
}

func TestDefaultTurnSessionsPreservesResolvedCapabilities(t *testing.T) {
	profile := qgGateProfile()
	tsp := DefaultTurnSessions(profile, buildGateProvider(t, profile), Options{})
	if tsp == nil {
		t.Fatal("DefaultTurnSessions returned nil")
	}
	caps := tsp.Capabilities()
	if caps.Model != "muse-spark-1.3-contributor" {
		t.Fatalf("Capabilities().Model = %q, want the resolved api model", caps.Model)
	}
	session, err := tsp.OpenTurnSession(context.Background())
	if err != nil {
		t.Fatalf("OpenTurnSession: %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
