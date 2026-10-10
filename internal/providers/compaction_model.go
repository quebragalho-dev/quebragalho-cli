package providers

import (
	"context"
	"os"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// Compaction summarization model selection.
//
// Compaction summaries used to run on the session's main model — near a 200k
// window one summarization is a ~100k-token input call, the single most
// expensive recurring event in a long run on pricier endpoints. On QG the
// default coding model (muse-spark-1.3-contributor) is already among the
// cheapest models on the single catalog, and the source carries no price table
// for the rest, so there is NO curated default summarizer: compaction runs on
// the main model unless the operator opts into another one.
//
// Resolution order: QG_COMPACTION_MODEL env > preferences.compactionModel
// config > "" (use the main model). The literal value "main" at any level
// forces the main model.

// CompactionModelEnv overrides the compaction summarization model.
const CompactionModelEnv = "QG_COMPACTION_MODEL"

// compactionMainSentinel disables the dedicated summarizer explicitly.
const compactionMainSentinel = "main"

// CompactionModelID returns the model that compaction summarization calls
// should use, or "" to use the session's main model.
// preference is the resolved config value (preferences.compactionModel).
func CompactionModelID(preference string) string {
	value := strings.TrimSpace(os.Getenv(CompactionModelEnv))
	if value == "" {
		value = strings.TrimSpace(preference)
	}
	if value == "" || strings.EqualFold(value, compactionMainSentinel) {
		return ""
	}
	return value
}

// CompactionSummarizerFactory returns the lazy constructor the agent loop
// calls for its dedicated compaction summarizer (agent.Options.Summarizer).
// The compactor passes the main model in force at that moment — on the first
// paid compaction and again after a mid-run model switch. Returning (nil, nil)
// means "summarize on the main provider", which is the default when no
// preference is set, when the preference is "main", and when the main model
// already equals the preferred one. The factory itself is nil only when no
// provider builder exists. Shared by the exec and TUI front ends so the
// selection rules cannot drift between them.
func CompactionSummarizerFactory(base config.ProviderProfile, preference string, newProvider func(config.ProviderProfile) (qgruntime.Provider, error)) func(context.Context, string) (qgruntime.Provider, error) {
	if newProvider == nil {
		return nil
	}
	return func(_ context.Context, mainModelID string) (qgruntime.Provider, error) {
		profile := base
		if strings.TrimSpace(mainModelID) != "" {
			profile.Model = mainModelID
		}
		modelID := CompactionModelID(preference)
		if modelID == "" || strings.EqualFold(modelID, strings.TrimSpace(profile.Model)) {
			return nil, nil
		}
		profile.Model = modelID
		return newProvider(profile)
	}
}
