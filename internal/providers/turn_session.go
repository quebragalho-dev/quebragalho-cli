package providers

import (
	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// OptimizedTurnSessions keeps the single-provider contract: QG ships only the
// default adapter. The legacy optimized Codex/OpenAI turn sessions are removed
// in Etapa 2, so this always reports ineligible and callers keep the default
// wrap byte-identical.
func OptimizedTurnSessions(profile config.ProviderProfile, provider qgruntime.Provider, options Options) (qgruntime.TurnSessionProvider, bool) {
	if _, err := resolveProfile(profile, options); err != nil {
		return nil, false
	}
	return nil, false
}

// DefaultTurnSessions wraps an already-built provider in the DEFAULT (no-op)
// turn-session adapter with the profile's resolved capability projection. It is
// the non-optimized sibling of OptimizedTurnSessions for callers that still
// want capabilities populated — e.g. the mid-run model-switch fallback when the
// switched model is not eligible for the optimized session. Capability
// resolution failure degrades to an unknown (zero) projection rather than
// failing the wrap: the default session has no behavior that depends on
// capabilities, so a swap must never be blocked by a projection error.
func DefaultTurnSessions(profile config.ProviderProfile, provider qgruntime.Provider, options Options) qgruntime.TurnSessionProvider {
	caps, err := resolveCapabilities(profile, options)
	if err != nil {
		caps = qgruntime.ProviderCapabilities{}
	}
	return qgruntime.NewProviderTurnSessionProvider(provider, caps)
}
