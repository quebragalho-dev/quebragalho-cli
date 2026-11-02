package tui

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
)

func prefixOptionLabels(request agent.PermissionRequest) []string {
	labels := []string{}
	for _, option := range permissionOptions(request) {
		if option.choice == permissionDecisionAllowPrefix || option.choice == permissionDecisionAlwaysAllowPrefix {
			labels = append(labels, option.label)
		}
	}
	return labels
}

func prefixRequest(escalates bool) agent.PermissionRequest {
	return agent.PermissionRequest{
		ToolName: "bash",
		AvailableDecisions: []agent.PermissionDecisionAction{
			agent.PermissionDecisionAllow,
			agent.PermissionDecisionAllowPrefix,
			agent.PermissionDecisionAlwaysAllowPrefix,
			agent.PermissionDecisionDeny,
		},
		PrefixApprovalEscalates: escalates,
	}
}

// Approving a prefix also runs the command outside the sandbox, and the label
// is the only place the person deciding could learn that. Both prefix options
// escalate, so both must say so; covering only one would leave the other as a
// silent path to the same outcome.
func TestPrefixOptionsDiscloseLeavingTheSandbox(t *testing.T) {
	labels := prefixOptionLabels(prefixRequest(true))
	if len(labels) != 2 {
		t.Fatalf("expected both prefix options, got %#v", labels)
	}
	for _, label := range labels {
		if !strings.Contains(label, "fora do ambiente isolado") {
			t.Errorf("prefix option %q does not say the command leaves the sandbox, so approving it is uninformed consent", label)
		}
	}
}

// The disclosure must survive rendering: the card shows override labels,
// not the base option text, so a warning that only exists on the base label
// would never reach the person deciding.
func TestRenderedPrefixOptionsDiscloseLeavingTheSandbox(t *testing.T) {
	request := prefixRequest(true)
	request.CommandPrefix = []string{"git", "status"}
	card, _ := renderFocusedPermissionPrompt(request, 1, false, "", 100)
	got := plainRender(t, card)
	for _, want := range []string{
		"Sim, fora do ambiente isolado: permitir `git status` nesta sessão",
		"Sim, fora do ambiente isolado: sempre permitir `git status`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered prefix card = %q, missing %q", got, want)
		}
	}
}

// The rendered card must stay silent without the flag: the same prefixes
// without escalation carry no warning, so the card must not cry wolf.
func TestRenderedPrefixOptionsStaySilentWithoutFlag(t *testing.T) {
	request := prefixRequest(false)
	request.CommandPrefix = []string{"git", "status"}
	card, _ := renderFocusedPermissionPrompt(request, 1, false, "", 100)
	got := plainRender(t, card)
	if strings.Contains(got, "fora do ambiente isolado") {
		t.Fatalf("rendered prefix card without escalation must not warn, got %q", got)
	}
	for _, want := range []string{
		"Sim, e permitir `git status` nesta sessão",
		"Sim, e sempre permitir `git status`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered prefix card = %q, missing %q", got, want)
		}
	}
}

// The disclosure has to be conditional. When escalation will not happen, saying
// it will is a false warning, and a prompt that cries wolf on every prefix is
// one the user learns to ignore, which costs the disclosure its whole value.
func TestPrefixOptionsStaySilentWhenNoEscalationHappens(t *testing.T) {
	for _, label := range prefixOptionLabels(prefixRequest(false)) {
		if strings.Contains(label, "fora do ambiente isolado") {
			t.Errorf("prefix option %q warns about the sandbox when approving would not leave it", label)
		}
	}
}

// The warning leads so a narrow card truncates the variable command instead
// of the disclosure. At 50 and 80 columns with a long prefix, the full warning
// and both hotkeys must survive; only the command may carry the ellipsis.
func TestRenderedEscalatingPrefixKeepsWarningAndHotkeyWhenTruncated(t *testing.T) {
	long := []string{"docker", "compose", "up", "--build"}
	short := []string{"git", "status"}
	for _, width := range []int{50, 80} {
		for _, prefix := range [][][]string{{short}, {long}} {
			cmd := prefix[0]
			request := prefixRequest(true)
			request.CommandPrefix = cmd
			for _, cursor := range []int{1, 2} {
				card, _ := renderFocusedPermissionPrompt(request, cursor, false, "", width)
				got := plainRender(t, card)
				if !strings.Contains(got, "Sim, fora do ambiente isolado:") {
					t.Fatalf("width=%d cmd=%q cursor=%d: warning truncated, got %q", width, cmd, cursor, got)
				}
				if !strings.Contains(got, "[p]") {
					t.Fatalf("width=%d cmd=%q: [p] truncated, got %q", width, cmd, got)
				}
				if !strings.Contains(got, "[y]") {
					t.Fatalf("width=%d cmd=%q: [y] truncated, got %q", width, cmd, got)
				}
			}
		}
	}
}

// The non-prefix options do not escalate, so the flag must not bleed into them.
func TestNonPrefixOptionsNeverCarryTheDisclosure(t *testing.T) {
	for _, option := range permissionOptions(prefixRequest(true)) {
		if option.choice == permissionDecisionAllowPrefix || option.choice == permissionDecisionAlwaysAllowPrefix {
			continue
		}
		if strings.Contains(option.label, "fora do ambiente isolado") {
			t.Errorf("option %q carries the prefix escalation disclosure but does not escalate", option.label)
		}
	}
}
