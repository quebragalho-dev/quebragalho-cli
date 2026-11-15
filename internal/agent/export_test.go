// Test seams: helpers only test code uses, kept out of the production binary.
package agent

import (
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func equalStringSlices(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// parsePreservedState recovers the plan + skills from a prior summary's preserved
// block. JSON escaping makes this lossless even when a skill body contains
// markdown headings, code fences, or quotes. Returns ("", nil) when absent or
// malformed.
func parsePreservedState(summaryContent string) (string, []skillEntry) {
	state := parsePreservedStateBlock(summaryContent)
	return state.Plan, preservedSkillsToEntries(state.Skills)
}

// partitionTools builds the per-turn advertised tool list and optional
// tool_search discovery text. INACTIVE (DeferThreshold <= 0 or the eligible count is
// below it): every visible tool is exposed with its full schema EXCEPT tool_search
// (dropped so it is never advertised when it cannot help), and the discovery text is
// empty — byte-identical to the pre-deferral output. ACTIVE: a deferred-eligible
// tool is exposed only when loaded[name]; otherwise it is hidden and searchable
// through tool_search. Non-deferred tools (including tool_search) are always
// exposed. The exposed slice is alpha-sorted by name, matching the legacy order
// so the inactive path is stable.
func partitionTools(registry *tools.Registry, permissionMode PermissionMode, options Options, loaded map[string]bool) ([]qgruntime.ToolDefinition, string) {
	return partitionToolsCached(registry, permissionMode, options, loaded, nil)
}

// buildPromptSubstrings builds the prompt and retains both its exact joined
// form and narrower diagnostic components. Test-only: production builds the
// parts once and calls buildPromptSubstringsFromParts.
func buildPromptSubstrings(options Options, exposed []qgruntime.ToolDefinition) promptSubstrings {
	return buildPromptSubstringsFromParts(buildSystemPromptParts(options), exposed)
}

// ComputePrefixFingerprint builds and fingerprints a prompt for callers that do
// not already have one. Test-only: the agent loop uses computePrefixFingerprint
// with the prompt parts it built once at run start.
func ComputePrefixFingerprint(options Options, exposed []qgruntime.ToolDefinition) prefixFingerprint {
	return computePrefixFingerprint(buildPromptSubstrings(options, exposed))
}
