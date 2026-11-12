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

// Normalized returns the trimmed canonical form (see normalize). The TUI
// proposal path normalizes before persisting so " context " and "context"
// agree; Resolve and SetStatusLine normalize stored values the same way.
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

// validate rejects shapes that would silently misbehave. It runs at Resolve
// time so a bad value fails loudly with a statusline-prefixed error.
func (c StatusLineConfig) validate() error {
	if c.TimeoutMs < 0 {
		return fmt.Errorf("invalid statusline.timeoutMs %d: must be >= 0 (0 uses the default %d)", c.TimeoutMs, DefaultStatusLineTimeoutMs)
	}
	if c.RefreshMs < 0 || (c.RefreshMs > 0 && c.RefreshMs < MinStatusLineRefreshMs) {
		return fmt.Errorf("invalid statusline.refreshMs %d: must be 0 or >= %d", c.RefreshMs, MinStatusLineRefreshMs)
	}
	if len(c.Lines) > MaxStatusLineLines {
		return fmt.Errorf("invalid statusline.lines: got %d lines, want at most %d", len(c.Lines), MaxStatusLineLines)
	}
	return nil
}
