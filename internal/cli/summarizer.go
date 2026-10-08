package cli

import (
	"context"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/providers"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// summarizerFactory adapts the resolved profile and the authenticated provider
// builder into agent.Options.Summarizer; the selection rules live in
// providers.CompactionSummarizerFactory, shared with the TUI.
func summarizerFactory(resolved config.ResolvedConfig, newProvider func(config.ProviderProfile) (qgruntime.Provider, error)) func(context.Context, string) (agent.Provider, error) {
	return providers.CompactionSummarizerFactory(resolved.Provider, resolved.Preferences.CompactionModel, newProvider)
}
