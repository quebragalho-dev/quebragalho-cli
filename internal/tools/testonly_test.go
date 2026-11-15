package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/localcontrol"
)

// Test-only helpers: constructors and validators only test code uses, kept
// out of the production binary. Production registers the Scoped constructors
// and reads normalized capabilities through CapabilitiesOf.

// NewGlobTool builds an unscoped glob tool. Test-only: production uses
// NewScopedGlobTool with an explicit path scope.
func NewGlobTool(workspaceRoot string) Tool {
	return NewScopedGlobTool(workspaceRoot, nil)
}

// NewGrepTool builds an unscoped grep tool. Test-only: production uses
// NewScopedGrepTool with an explicit path scope.
func NewGrepTool(workspaceRoot string) Tool {
	return NewScopedGrepTool(workspaceRoot, nil)
}

// NewListDirectoryTool builds an unscoped list_directory tool. Test-only:
// production uses NewScopedListDirectoryTool with an explicit path scope.
func NewListDirectoryTool(workspaceRoot string) Tool {
	return NewScopedListDirectoryTool(workspaceRoot, nil)
}

// NewReadMinifiedFileTool builds an unscoped read_minified_file tool.
// Test-only: production uses NewScopedReadMinifiedFileTool with an explicit
// path scope.
func NewReadMinifiedFileTool(workspaceRoot string) Tool {
	return NewScopedReadMinifiedFileTool(workspaceRoot, nil)
}

// NewViewImageTool builds the workspace-scoped image viewer. Test-only:
// production uses NewScopedViewImageTool with an explicit path scope.
func NewViewImageTool(workspaceRoot string) Tool {
	return NewScopedViewImageTool(workspaceRoot, nil)
}

// BuiltinCatalog returns every built-in tool that QG can expose to the model
// from this package (core + control + optional local-control helpers).
// Test-only: it is the source of truth for metadata coverage tests; every
// tool returned here must declare an explicit Effect other than EffectUnknown.
//
// Not included (fail-closed EffectUnknown by design):
//   - MCP tools (internal/mcp registryTool)
//   - Plugin tools (internal/plugins pluginTool)
//   - Specialist tools registered outside this package (Task, Generate, …)
//
// workspaceRoot is used only to construct path-scoped tools; it need not exist.
func BuiltinCatalog(workspaceRoot string) []Tool {
	if workspaceRoot == "" {
		workspaceRoot = "."
	}
	var all []Tool
	all = append(all, CoreReadOnlyToolsScoped(workspaceRoot, nil)...)
	all = append(all, CoreWriteToolsScoped(workspaceRoot, nil)...)
	all = append(all, CoreShellToolsScoped(workspaceRoot, nil)...)
	// Network tools: always include web_fetch; always include web_search for
	// classification even when no backend is configured at runtime (registration
	// remains conditional in CoreNetworkTools).
	all = append(all, NewWebFetchTool(), NewWebSearchTool())
	all = append(all, NewEscalateModelTool())

	// tool_search needs a live registry of the tools above.
	reg := NewRegistry()
	for _, tool := range all {
		reg.Register(tool)
	}
	search := NewToolSearchTool(reg)
	reg.Register(search)
	all = append(all, search)

	// Local-control tools are feature-gated at registration time but always
	// exist as built-in implementations with fixed classifications.
	all = append(all, NewLocalBrowserTools(localcontrol.BrowserOptions{})...)
	all = append(all, NewLocalDesktopTools(localcontrol.DesktopOptions{})...)
	all = append(all, NewLocalTerminalTools(localcontrol.TerminalOptions{})...)
	all = append(all, NewLocalControlArtifactTools(LocalControlArtifactOptions{})...)
	return all
}

// ValidateBuiltinCatalog returns diagnostics for every catalog tool that is
// missing or inconsistently classified. Empty means full coverage.
// Test-only: no production path validates the catalog at runtime.
//
// Duplicate builtin names are always rejected: ResourceKeys is a function and
// cannot be compared, so partial metadata comparison would miss conflict-scope
// drift between two same-name tools.
//
// Validation uses constructor-declared capabilities (before ThreadSafe
// normalization) so a mutator wired with ThreadSafe=true is still reported.
// Runtime callers continue to use CapabilitiesOf, which normalizes.
func ValidateBuiltinCatalog(workspaceRoot string) []string {
	var problems []string
	seen := map[string]struct{}{}
	for _, tool := range BuiltinCatalog(workspaceRoot) {
		name := tool.Name()
		if _, ok := seen[name]; ok {
			problems = append(problems, name+": duplicate builtin name in catalog")
			continue
		}
		seen[name] = struct{}{}

		// Runtime view: must not remain Unknown after normalization.
		runtimeCaps := CapabilitiesOf(tool)
		if runtimeCaps.Effect == EffectUnknown {
			problems = append(problems, name+": built-in tool remains EffectUnknown (metadata required)")
		}

		// Declaration view: catch invalid ThreadSafe on mutators that
		// CapabilitiesOf would silently clear.
		declared := declaredCapabilitiesOf(tool)
		problems = append(problems, ValidateCapabilities(name, declared)...)
	}
	return problems
}

// declaredCapabilitiesOf returns constructor metadata when available; otherwise
// falls back to CapabilitiesOf (already normalized). Test-only: the production
// binary only reads normalized capabilities.
func declaredCapabilitiesOf(tool Tool) ToolCapabilities {
	if tool == nil {
		return UnknownCapabilities()
	}
	if d, ok := tool.(declaredCapabilityProvider); ok {
		return d.declaredCapabilities()
	}
	return CapabilitiesOf(tool)
}

// declaredCapabilities returns the constructor-set metadata without runtime
// normalization. Test-only: catalog validation uses this so a mis-declared
// ThreadSafe=true on a mutator is still reported even though CapabilitiesOf
// clears it for callers.
func (tool baseTool) declaredCapabilities() ToolCapabilities {
	return tool.capabilities
}

// declaredCapabilityProvider is implemented by tools that can report their
// raw constructor metadata (baseTool embedders). Test-only.
type declaredCapabilityProvider interface {
	declaredCapabilities() ToolCapabilities
}

// ValidateCapabilities checks a single capability record for consistency.
// It returns a non-empty slice of human-readable problems when invalid.
// Test-only: production relies on the fail-closed normalization in
// CapabilitiesOf instead of validating.
// Note: CapabilitiesOf already normalizes ThreadSafe away for non-ReadOnly
// effects; validation still rejects the raw combination so catalog tests
// catch mis-declared constructors.
func ValidateCapabilities(name string, caps ToolCapabilities) []string {
	var problems []string
	name = strings.TrimSpace(name)
	if name == "" {
		name = "<unnamed>"
	}
	if !caps.Effect.Valid() {
		problems = append(problems, name+": invalid effect value")
	}
	if caps.Effect == EffectUnknown && caps.ThreadSafe {
		problems = append(problems, name+": ThreadSafe=true is forbidden with EffectUnknown")
	}
	// Mutating / interactive / process-read effects are never concurrency-eligible.
	switch caps.Effect {
	case EffectWorkspaceWrite, EffectExternalWrite, EffectInteractive, EffectProcessRead:
		if caps.ThreadSafe {
			problems = append(problems, name+": ThreadSafe=true is not permitted for effect "+caps.Effect.String())
		}
	}
	return problems
}

// maybeFormatWrittenFile is the unscoped test-facing wrapper. Test-only:
// production callers use maybeFormatWrittenFileScoped so a formatter cannot
// redirect the final read or recovery write outside the configured write roots.
func maybeFormatWrittenFile(ctx context.Context, absolutePath string, writtenContent string) formatOnWriteResult {
	restoreMode := os.FileMode(0o644)
	if info, err := os.Stat(absolutePath); err == nil {
		restoreMode = info.Mode().Perm()
	}
	return maybeFormatWrittenFileScoped(ctx, filepath.Dir(absolutePath), nil, absolutePath, writtenContent, restoreMode)
}

// detectShellCommandIssue adapts a GOOS name to a shell runtime for tests.
// Test-only: production calls detectShellCommandIssueForRuntime directly.
func detectShellCommandIssue(command string, goos string) *shellIssue {
	shell := shellRuntime{GOOS: goos, Executable: "/bin/sh", Syntax: "/bin/sh", Kind: shellKindPOSIX}
	if goos == "windows" {
		shell = shellRuntime{GOOS: goos, Executable: "cmd.exe", Syntax: "Windows cmd.exe", Kind: shellKindCmd}
	}
	return detectShellCommandIssueForRuntime(command, shell)
}

// detectShellOutputIssue adapts a GOOS name to a shell runtime for tests.
// Test-only: production calls detectShellOutputIssueForRuntime directly.
func detectShellOutputIssue(output string, goos string) *shellIssue {
	shell := shellRuntime{GOOS: goos, Executable: "/bin/sh", Syntax: "/bin/sh", Kind: shellKindPOSIX}
	if goos == "windows" {
		shell = shellRuntime{GOOS: goos, Executable: "cmd.exe", Syntax: "Windows cmd.exe", Kind: shellKindCmd}
	}
	return detectShellOutputIssueForRuntime(output, shell)
}

// HostExecCommandShellGuidance returns the shell guidance appended to
// exec_command's description on this host, or "" where none is appended.
// Test-only: the per-turn token accounting that once consumed it no longer
// runs in production; tests compare it against the description text.
func HostExecCommandShellGuidance() string {
	return hostExecCommandShellGuidance(runtimeGOOS())
}

// hostExecCommandShellGuidance is the guidance a host running goos appends.
// Test-only: it is the pair of execCommandDescription, so the two can be
// checked against each other for every platform rather than only for the
// running one.
func hostExecCommandShellGuidance(goos string) string {
	if goos != "windows" {
		return ""
	}
	return shellGuidanceForGOOS(goos)
}
