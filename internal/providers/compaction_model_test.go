package providers

import (
	"context"
	"errors"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestCompactionModelIDResolutionOrder(t *testing.T) {
	t.Setenv(CompactionModelEnv, "my-env-model")
	if got := CompactionModelID("my-config-model"); got != "my-env-model" {
		t.Fatalf("env must win, got %q", got)
	}

	t.Setenv(CompactionModelEnv, "main")
	if got := CompactionModelID("my-config-model"); got != "" {
		t.Fatalf("env 'main' must force the main model, got %q", got)
	}

	t.Setenv(CompactionModelEnv, "")
	if got := CompactionModelID("my-config-model"); got != "my-config-model" {
		t.Fatalf("config preference must apply when env unset, got %q", got)
	}
	if got := CompactionModelID("main"); got != "" {
		t.Fatalf("config 'main' must force the main model, got %q", got)
	}
}

// TestCompactionModelIDHasNoCuratedDefault pins the single-provider contract:
// QG exposes one catalog whose prices are not knowable from source and the
// default coding model is already among the cheapest, so no opt-in means no
// dedicated summarizer (compaction runs on the main model).
func TestCompactionModelIDHasNoCuratedDefault(t *testing.T) {
	t.Setenv(CompactionModelEnv, "")
	if got := CompactionModelID(""); got != "" {
		t.Fatalf("default compaction model = %q, want \"\" (main model)", got)
	}
}

func TestCompactionSummarizerFactoryHonorsPreference(t *testing.T) {
	t.Setenv(CompactionModelEnv, "")
	base := config.ProviderProfile{Name: "qg", CatalogID: "qg", ProviderKind: config.ProviderKindOpenAICompatible, Model: "muse-spark-1.3-contributor"}
	var built []string
	newProvider := func(profile config.ProviderProfile) (qgruntime.Provider, error) {
		built = append(built, profile.Model)
		return nil, errors.New("not needed")
	}

	// No preference: no dedicated summarizer, nothing built.
	factory := CompactionSummarizerFactory(base, "", newProvider)
	if factory == nil {
		t.Fatal("factory must exist when a provider builder exists")
	}
	if provider, err := factory(context.Background(), "muse-spark-1.3-contributor"); provider != nil || err != nil || len(built) != 0 {
		t.Fatalf("no preference must yield (nil, nil) without building, got %v %v built=%v", provider, err, built)
	}

	// Explicit preference builds the chosen model on the main provider.
	factory = CompactionSummarizerFactory(base, "deepseek-v4.1-flash", newProvider)
	if _, err := factory(context.Background(), "muse-spark-1.3-contributor"); err == nil || len(built) != 1 || built[0] != "deepseek-v4.1-flash" {
		t.Fatalf("preferred model must be built, built=%v err=%v", built, err)
	}

	// Main model already IS the preferred model: no second instance.
	built = nil
	if provider, err := factory(context.Background(), "deepseek-v4.1-flash"); provider != nil || err != nil || len(built) != 0 {
		t.Fatalf("main already equal to preference must yield (nil, nil), got %v %v built=%v", provider, err, built)
	}

	// The explicit "main" preference disables the dedicated summarizer.
	if provider, err := CompactionSummarizerFactory(base, "main", newProvider)(context.Background(), "claude-sonnet-4-5"); provider != nil || err != nil {
		t.Fatalf("preference main must disable the summarizer, got %v %v", provider, err)
	}
	if CompactionSummarizerFactory(base, "", nil) != nil {
		t.Fatal("no provider builder means no factory")
	}
}
