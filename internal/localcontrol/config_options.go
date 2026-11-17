package localcontrol

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// BrowserOptionsFromConfig builds BrowserOptions from resolved LocalControlConfig.
func BrowserOptionsFromConfig(cfg config.LocalControlConfig) BrowserOptions {
	return BrowserOptions{
		Enabled:    cfg.BrowserEnabled(),
		Driver:     cfg.Browser.Driver,
		HelperPath: cfg.Browser.HelperPath,
	}
}

// DesktopOptionsFromConfig builds DesktopOptions from resolved LocalControlConfig
// using the supplied effective enabled state (so callers like the TUI toggle
// can override desktop availability without mutating global config fields).
func DesktopOptionsFromConfig(cfg config.LocalControlConfig, enabled bool) DesktopOptions {
	options := DesktopOptions{
		Enabled:    enabled && !cfg.Disabled(),
		Driver:     cfg.Desktop.Driver,
		HelperPath: cfg.Desktop.HelperPath,
	}
	if driver := strings.TrimSpace(cfg.Desktop.Driver); driver == "" || driver == DefaultDesktopDriver {
		options.Ensure = func(ctx context.Context) (string, error) {
			return EnsureAgentDesktop(ctx, AgentDesktopInstallOptions{})
		}
	}
	return options
}

// TerminalOptionsFromConfig builds TerminalOptions from resolved LocalControlConfig.
func TerminalOptionsFromConfig(cfg config.LocalControlConfig) TerminalOptions {
	return TerminalOptions{
		Enabled:    cfg.TerminalEnabled(),
		Driver:     cfg.Terminal.Driver,
		HelperPath: cfg.Terminal.HelperPath,
	}
}

// ArtifactsDirFromConfig resolves the local-control artifact directory against workspaceRoot.
func ArtifactsDirFromConfig(workspaceRoot string, cfg config.LocalControlConfig) string {
	dir := strings.TrimSpace(cfg.ArtifactsDir)
	if dir == "" {
		dir = filepath.Join(".qg", "artifacts")
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(workspaceRoot, dir)
}
