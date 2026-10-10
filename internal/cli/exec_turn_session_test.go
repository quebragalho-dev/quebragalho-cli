package cli

import (
	"bytes"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// The optimized OpenAI turn session (and its prewarm probe) was removed in
// Etapa 2: OptimizedTurnSessions always reports ineligible, which
// TestOptimizedTurnSessionsAlwaysOffSingleProvider pins at the unit level.
// The end-to-end prewarm test went with the feature.

// TestRunExecGateOnFallsBackForFakeProvider proves fallback safety: with the
// gate on but a provider that is not the concrete *openai.Provider, the run
// proceeds on the default path exactly as today.
func TestRunExecGateOnFallsBackForFakeProvider(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("QG_OPENAI_TURN_SESSION", "1")
	cwd := t.TempDir()

	var builds int
	var stdout, stderr bytes.Buffer
	exitCode := runWithDeps([]string{"exec", "--model", "claude-haiku-4.5", "hello"}, &stdout, &stderr, appDeps{
		getwd: func() (string, error) { return cwd, nil },
		resolveConfig: func(_ string, overrides config.Overrides) (config.ResolvedConfig, error) {
			model := "claude-haiku-4.5"
			if overrides.Provider.Model != "" {
				model = overrides.Provider.Model
			}
			cfg := execResolvedConfig()
			cfg.Provider.ProviderKind = config.ProviderKindAnthropic
			cfg.Provider.Model = model
			cfg.MaxTurns = 3
			return cfg, nil
		},
		newProvider: func(config.ProviderProfile) (qgruntime.Provider, error) {
			builds++
			return &escalatingExecProvider{}, nil
		},
	})
	if exitCode != exitSuccess {
		t.Fatalf("exitCode = %d stdout=%s stderr=%s", exitCode, stdout.String(), stderr.String())
	}
	if builds != 1 {
		t.Fatalf("newProvider called %d times, want 1 (default path, no optimized session)", builds)
	}
}
