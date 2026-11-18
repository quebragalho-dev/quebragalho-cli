package tools

import (
	"context"
	"strings"
	"testing"
)

func TestSandboxedCredentialFailureHintsRequireEscalated(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{
			name:   "git push ssh publickey denied",
			output: "git@github.com: Permission denied (publickey).\nfatal: Could not read from remote repository.",
		},
		{
			name:   "ssh key read permission denied",
			output: "Load key \"/home/user/.ssh/id_ed25519\": Permission denied",
		},
		{
			name:   "npm publish ENEEDAUTH",
			output: "npm error code ENEEDAUTH\nnpm error need auth This command requires you to be logged in to https://registry.npmjs.org/",
		},
		{
			name:   "gh cli not logged in",
			output: "To get started with GitHub CLI, please run:  gh auth login",
		},
		{
			name:   "aws unable to locate credentials",
			output: "Unable to locate credentials. You can configure credentials by running \"aws configure\".",
		},
		{
			name:   "direct read of denied credential file",
			output: "cat: /home/user/.netrc: Permission denied",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bashMeta := map[string]string{}
			bashOut := formatBashOutputWithShellHintForRuntime("", tc.output, 1, true, shellRuntime{GOOS: "linux", Kind: shellKindPOSIX}, bashMeta)
			if !strings.Contains(bashOut, `require_escalated`) {
				t.Fatalf("bash output missing require_escalated hint:\n%s", bashOut)
			}
			if bashMeta["shell_issue"] == "" {
				t.Fatalf("bash meta missing shell_issue")
			}

			execRes := execToolResultWithBudgetForRuntime(execToolResultInput{
				commandText: "test",
				output:      tc.output,
				sessionID:   1,
				exitCode:    1,
				exited:      true,
				wrapped:     true,
			}, true, shellRuntime{GOOS: "linux", Kind: shellKindPOSIX})
			if !strings.Contains(execRes.Output, `require_escalated`) {
				t.Fatalf("exec_command output missing require_escalated hint:\n%s", execRes.Output)
			}
			if execRes.Meta["shell_issue"] == "" {
				t.Fatalf("exec_command meta missing shell_issue")
			}

			// When not wrapped (already running outside the sandbox) or exitCode == 0,
			// the hint must NOT appear.
			unwrappedOut := formatBashOutputWithShellHintForRuntime("", tc.output, 1, false, shellRuntime{GOOS: "linux", Kind: shellKindPOSIX}, map[string]string{})
			if strings.Contains(unwrappedOut, `require_escalated`) {
				t.Fatalf("unwrapped bash output should not hint require_escalated:\n%s", unwrappedOut)
			}
		})
	}
}

func TestUnsandboxedBuildBashCommandPreservesHostTokens(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_host_token")
	t.Setenv("NPM_TOKEN", "npm_host_token")
	t.Setenv("NODE_AUTH_TOKEN", "node_host_token")

	cmd, plan, err := buildBashCommand(context.Background(), "true", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("buildBashCommand: %v", err)
	}
	if plan.Wrapped {
		t.Fatalf("expected unwrapped plan for nil engine")
	}
	if cmd.Env != nil {
		t.Fatalf("expected nil cmd.Env (inherits full host env) for unsandboxed command, got %#v", cmd.Env)
	}
}
