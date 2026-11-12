package config

import (
	"fmt"
	"strings"
)

// Statusline defaults: the external command gets 2s per run and re-runs at
// most every 5s on the idle timer (plus on state changes like a finished
// call), so a slow script can never make the TUI feel heavy.
const (
	// DefaultStatusLineTimeoutMs bounds one external statusline command run.
	DefaultStatusLineTimeoutMs = 2000
	// DefaultStatusLineRefreshMs is the idle re-run cadence for the command.
	DefaultStatusLineRefreshMs = 5000
	// MinStatusLineRefreshMs is the fastest allowed re-run cadence.
	MinStatusLineRefreshMs = 1000
	// MaxStatusLineLines caps a custom layout; the footer stays a status line.
	MaxStatusLineLines = 3
)

// StatusLineCommand runs a user command whose stdout becomes (part of) the
// status line. It is only honored from the user config: project config may
// carry one, but Resolve drops it and reports it via
// ResolvedConfig.ProjectStatusLineCommandIgnored.
type StatusLineCommand struct {
	Command string `json:"command"`
}

// StatusLineLine is one row of a custom layout: ids rendered on the left and
// on the right, joined by the configured separator. See the field registry in
// internal/tui for the stable id set ("mode", "effort", "context", ...).
type StatusLineLine struct {
	Left  []string `json:"left,omitempty"`
	Right []string `json:"right,omitempty"`
}

// StatusLineConfig customizes the TUI status line. The zero value means
// "render like today" (IsCustom false): the built-in single-line layout.
type StatusLineConfig struct {
	Lines     []StatusLineLine   `json:"lines,omitempty"`
	Separator string             `json:"separator,omitempty"`
	Command   *StatusLineCommand `json:"command,omitempty"`
	TimeoutMs int                `json:"timeoutMs,omitempty"`
	RefreshMs int                `json:"refreshMs,omitempty"`
}

// IsCustom reports whether any customization is configured. An empty object
// (all fields unset) renders the built-in layout byte for byte.
func (c StatusLineConfig) IsCustom() bool {
	return len(c.Lines) > 0 || (c.Command != nil && strings.TrimSpace(c.Command.Command) != "")
}

// EffectiveTimeoutMs returns the per-run bound, defaulted.
func (c StatusLineConfig) EffectiveTimeoutMs() int {
	if c.TimeoutMs <= 0 {
		return DefaultStatusLineTimeoutMs
	}
	return c.TimeoutMs
}

// EffectiveRefreshMs returns the idle re-run cadence, defaulted.
func (c StatusLineConfig) EffectiveRefreshMs() int {
	if c.RefreshMs <= 0 {
		return DefaultStatusLineRefreshMs
	}
	return c.RefreshMs
}

// Normalized returns the trimmed canonical form. Prefer adjusted(), which
// also clamps cosmetic values with warnings.
func (c StatusLineConfig) Normalized() StatusLineConfig {
	return c.normalize()
}
func (c StatusLineConfig) normalize() StatusLineConfig {
	out := StatusLineConfig{
		Separator: c.Separator,
		TimeoutMs: c.TimeoutMs,
		RefreshMs: c.RefreshMs,
	}
	for _, line := range c.Lines {
		trimmed := StatusLineLine{}
		for _, id := range line.Left {
			if id = strings.TrimSpace(id); id != "" {
				trimmed.Left = append(trimmed.Left, id)
			}
		}
		for _, id := range line.Right {
			if id = strings.TrimSpace(id); id != "" {
				trimmed.Right = append(trimmed.Right, id)
			}
		}
		out.Lines = append(out.Lines, trimmed)
	}
	if c.Command != nil && strings.TrimSpace(c.Command.Command) != "" {
		out.Command = &StatusLineCommand{Command: strings.TrimSpace(c.Command.Command)}
	}
	return out
}

// adjusted normalizes and clamps cosmetic values so they never prevent QG
// from opening: negative timeoutMs falls back to the default, refreshMs
// below the 1s floor rises to it, and layouts longer than three lines keep
// the head. Every touch is reported in pt-BR for the startup/status notice.
func (c StatusLineConfig) Adjusted() (StatusLineConfig, []string) {
	out := c.normalize()
	var warnings []string
	if out.TimeoutMs < 0 {
		warnings = append(warnings, fmt.Sprintf("statusline: timeoutMs %d inválido, usando o padrão %d", c.TimeoutMs, DefaultStatusLineTimeoutMs))
		out.TimeoutMs = 0
	}
	if out.RefreshMs < 0 {
		warnings = append(warnings, fmt.Sprintf("statusline: refreshMs %d inválido, usando o padrão %d", c.RefreshMs, DefaultStatusLineRefreshMs))
		out.RefreshMs = 0
	} else if out.RefreshMs > 0 && out.RefreshMs < MinStatusLineRefreshMs {
		warnings = append(warnings, fmt.Sprintf("statusline: refreshMs %d ajustado para o mínimo %d", c.RefreshMs, MinStatusLineRefreshMs))
		out.RefreshMs = MinStatusLineRefreshMs
	}
	if len(out.Lines) > MaxStatusLineLines {
		warnings = append(warnings, fmt.Sprintf("statusline: %d linhas, usando as %d primeiras", len(out.Lines), MaxStatusLineLines))
		out.Lines = out.Lines[:MaxStatusLineLines]
	}
	return out, warnings
}
