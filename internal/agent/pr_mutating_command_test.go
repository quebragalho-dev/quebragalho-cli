package agent

import (
	"testing"
)

func TestPrMutatingShellCommand(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want bool
	}{
		{"gh pr create", "exec_command", map[string]any{"cmd": "gh pr create --title x --body y"}, true},
		{"gh pr merge", "exec_command", map[string]any{"cmd": "gh pr merge 121 --squash"}, true},
		{"gh pr close", "bash", map[string]any{"command": "gh pr close 120"}, true},
		{"gh pr reopen", "bash", map[string]any{"command": "gh pr reopen 120"}, true},
		{"gh pr ready", "exec_command", map[string]any{"cmd": "gh pr ready 121"}, true},
		{"gh pr edit", "exec_command", map[string]any{"cmd": "gh pr edit 121 --add-label demo"}, true},
		{"glab mr create", "exec_command", map[string]any{"cmd": "glab mr create --title x"}, true},
		{"glab mr merge", "bash", map[string]any{"command": "glab mr merge 55"}, true},
		{"glab mr close", "exec_command", map[string]any{"cmd": "glab mr close 55"}, true},
		{"glab mr reopen", "exec_command", map[string]any{"cmd": "glab mr reopen 55"}, true},
		{"git push", "exec_command", map[string]any{"cmd": "git push origin feat/x"}, true},
		{"git push chained", "bash", map[string]any{"command": "git push && gh pr create --fill"}, true},
		{"gh pr view is read-only", "exec_command", map[string]any{"cmd": "gh pr view 121"}, false},
		{"gh pr list is read-only", "bash", map[string]any{"command": "gh pr list --head feat/x --state open"}, false},
		{"git pull is not push", "exec_command", map[string]any{"cmd": "git pull --rebase"}, false},
		{"git status", "exec_command", map[string]any{"cmd": "git status --short"}, false},
		{"ls", "exec_command", map[string]any{"cmd": "ls -la"}, false},
		{"non-shell tool", "grep", map[string]any{"pattern": "gh pr create"}, false},
		{"empty args", "exec_command", map[string]any{}, false},
		{"non-string cmd", "exec_command", map[string]any{"cmd": 42}, false},
	}
	for _, tc := range cases {
		if got := prMutatingShellCommand(tc.tool, tc.args); got != tc.want {
			t.Errorf("%s: prMutatingShellCommand(%q, %v) = %v, want %v", tc.name, tc.tool, tc.args, got, tc.want)
		}
	}
}
