package cli

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestHelpLinesFit80Cells(t *testing.T) {
	helps := map[string]func(*bytes.Buffer) error{
		"help":                 func(b *bytes.Buffer) error { return writeHelp(b) },
		"exec":                 func(b *bytes.Buffer) error { return writeExecHelp(b) },
		"setup":                func(b *bytes.Buffer) error { return writeSetupHelp(b) },
		"login":                func(b *bytes.Buffer) error { return writeLoginHelp(b) },
		"logout":               func(b *bytes.Buffer) error { return writeLogoutHelp(b) },
		"auth":                 func(b *bytes.Buffer) error { return writeAuthHelp(b) },
		"providers":            func(b *bytes.Buffer) error { return writeProvidersHelp(b) },
		"repo-map":             func(b *bytes.Buffer) error { return writeRepoMapHelp(b) },
		"sessions":             func(b *bytes.Buffer) error { return writeSessionsHelp(b) },
		"spec":                 func(b *bytes.Buffer) error { return writeSpecHelp(b) },
		"skills":               func(b *bytes.Buffer) error { return writeSkillsHelp(b) },
		"skills-list":          func(b *bytes.Buffer) error { return writeSkillsListHelp(b) },
		"skill-add":            func(b *bytes.Buffer) error { return writeSkillAddHelp(b) },
		"skill-info":           func(b *bytes.Buffer) error { return writeSkillInfoHelp(b) },
		"skill-remove":         func(b *bytes.Buffer) error { return writeSkillRemoveHelp(b) },
		"plugins":              func(b *bytes.Buffer) error { return writePluginsHelp(b) },
		"plugins-list":         func(b *bytes.Buffer) error { return writePluginsListHelp(b) },
		"plugin-add":           func(b *bytes.Buffer) error { return writePluginAddHelp(b) },
		"plugin-info":          func(b *bytes.Buffer) error { return writePluginInfoHelp(b) },
		"plugin-remove":        func(b *bytes.Buffer) error { return writePluginRemoveHelp(b) },
		"tools":                func(b *bytes.Buffer) error { return writeToolsHelp(b) },
		"tools-make":           func(b *bytes.Buffer) error { return writeToolsMakeHelp(b) },
		"tools-list":           func(b *bytes.Buffer) error { return writeToolsListHelp(b) },
		"doctor":               func(b *bytes.Buffer) error { return writeDoctorHelp(b) },
		"search":               func(b *bytes.Buffer) error { return writeSearchHelp(b) },
		"serve":                func(b *bytes.Buffer) error { return writeServeHelp(b) },
		"hooks":                func(b *bytes.Buffer) error { return writeHooksHelp(b) },
		"hooks-list":           func(b *bytes.Buffer) error { return writeHooksListHelp(b) },
		"hooks-add":            func(b *bytes.Buffer) error { return writeHooksAddHelp(b) },
		"cron":                 func(b *bytes.Buffer) error { writeCronHelp(b); return nil },
		"mcp":                  func(b *bytes.Buffer) error { return writeMCPHelp(b) },
		"mcp-add":              func(b *bytes.Buffer) error { return writeMCPAddHelp(b) },
		"mcp-remove":           func(b *bytes.Buffer) error { return writeMCPRemoveHelp(b) },
		"mcp-check":            func(b *bytes.Buffer) error { return writeMCPCheckHelp(b) },
		"mcp-tools":            func(b *bytes.Buffer) error { return writeMCPToolsHelp(b) },
		"mcp-permissions":      func(b *bytes.Buffer) error { return writeMCPPermissionsHelp(b) },
		"mcp-oauth":            func(b *bytes.Buffer) error { return writeMCPOAuthHelp(b) },
		"mcp-legacy-list":      func(b *bytes.Buffer) error { return writeMCPLegacyListHelp(b) },
		"worktrees":            func(b *bytes.Buffer) error { return writeWorktreesHelp(b) },
		"verify":               func(b *bytes.Buffer) error { return writeVerifyHelp(b) },
		"changes":              func(b *bytes.Buffer) error { return writeChangesHelp(b) },
		"config":               func(b *bytes.Buffer) error { return writeConfigHelp(b) },
		"models":               func(b *bytes.Buffer) error { return writeModelsHelp(b) },
		"context":              func(b *bytes.Buffer) error { return writeContextHelp(b) },
		"backends":             func(b *bytes.Buffer) error { return writeBackendsHelp(b) },
		"backends-doctor":      func(b *bytes.Buffer) error { return writeBackendsDoctorHelp(b) },
		"sandbox":              func(b *bytes.Buffer) error { return writeSandboxHelp(b) },
		"sandbox-policy":       func(b *bytes.Buffer) error { return writeSandboxPolicyHelp(b) },
		"sandbox-setup":        func(b *bytes.Buffer) error { return writeSandboxSetupHelp(b) },
		"sandbox-grants":       func(b *bytes.Buffer) error { return writeSandboxGrantsHelp(b) },
		"sandbox-grant-set":    func(b *bytes.Buffer) error { return writeSandboxGrantSetHelp(b) },
		"sandbox-grant-revoke": func(b *bytes.Buffer) error { return writeSandboxGrantRevokeHelp(b) },
		"sandbox-grant-clear":  func(b *bytes.Buffer) error { return writeSandboxGrantClearHelp(b) },
		"sandbox-check":        func(b *bytes.Buffer) error { return writeSandboxCheckHelp(b) },
		"repo-info":            func(b *bytes.Buffer) error { writeRepoInfoHelp(b); return nil },
		"completions":          func(b *bytes.Buffer) error { return writeCompletionsHelp(b) },
		"usage":                func(b *bytes.Buffer) error { return writeUsageHelp(b) },
		"specialist":           func(b *bytes.Buffer) error { return writeSpecialistHelp(b) },
		"agent-eval":           func(b *bytes.Buffer) error { return writeAgentEvalHelp(b) },
		"update":               func(b *bytes.Buffer) error { return writeUpdateHelp(b) },
		"daemon":               func(b *bytes.Buffer) error { writeDaemonUsage(b, 0); return nil },
	}
	for name, fn := range helps {
		var buf bytes.Buffer
		if err := fn(&buf); err != nil {
			t.Fatalf("%s help failed: %v", name, err)
		}
		for _, line := range strings.Split(buf.String(), "\n") {
			if w := lipgloss.Width(line); w > 80 {
				t.Errorf("%s help exceeds 80 cells (%d): %q", name, w, line)
			}
		}
	}
}
