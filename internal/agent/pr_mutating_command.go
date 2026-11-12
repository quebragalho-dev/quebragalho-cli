package agent

import (
	"regexp"
)

// PrIdentityStaleMeta is the result-Meta key executeToolCall stamps when the
// executed shell command may have changed the PR/MR graph (gh pr
// create/close/merge/reopen/ready/edit, glab mr create/close/merge/reopen,
// git push). The TUI drops its cached PR identity on sight, so a PR the
// operator just opened shows up on the next header refresh instead of
// lingering behind the identity TTL. Carried in Meta like the load_tools and
// escalate_to_model signals — never sent to the provider.
const PrIdentityStaleMeta = "qg.pr_identity_stale"

var prMutatingCommandPattern = regexp.MustCompile(`(?i)\b(gh\s+pr\s+(create|close|merge|reopen|ready|edit)|glab\s+mr\s+(create|close|merge|reopen)|git\s+push)\b`)

// prMutatingShellCommand reports whether a shell tool call runs a command
// that may change the PR/MR graph. Matching is deliberately loose (a quoted
// occurrence also matches): a false positive only forces one extra header
// refresh, while a miss would hide a freshly opened PR behind the cache.
func prMutatingShellCommand(toolName string, args map[string]any) bool {
	if !isShellCommandTool(toolName) {
		return false
	}
	command, ok := firstStringArg(args, "command", "cmd", "script", "shell")
	if !ok {
		return false
	}
	return prMutatingCommandPattern.MatchString(command)
}
