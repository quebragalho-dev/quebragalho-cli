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

// The disclosure must survive rendering: the card shows the fixed short
// labels plus the command on its own line, so both the warning and the full
// command reach the person deciding.
func TestRenderedPrefixOptionsDiscloseLeavingTheSandbox(t *testing.T) {
	request := prefixRequest(true)
	request.CommandPrefix = []string{"git", "status"}
	card, _ := renderFocusedPermissionPrompt(request, 1, false, "", 100)
	got := plainRender(t, card)
	for _, want := range []string{
		"prefixo de comando: `git status`",
		"Fora do ambiente isolado, nesta sessão",
		"Fora do ambiente isolado, sempre",
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
// The warning and the scope must survive narrow cards with the cursor on any
// option: the fixed short labels carry the disclosure, the hotkeys stay
// visible, and the full command rides on its own line above the options.
func TestRenderedEscalatingPrefixKeepsWarningAndHotkeyWhenTruncated(t *testing.T) {
	prefixes := []struct {
		name string
		cmd  []string
	}{
		{"short", []string{"git", "status"}},
		{"long", []string{"docker", "compose", "up", "--build"}},
	}
	for _, width := range []int{50, 58, 80} {
		for _, prefix := range prefixes {
			cmd := strings.Join(prefix.cmd, " ")
			request := prefixRequest(true)
			request.CommandPrefix = prefix.cmd
			for cursor := 0; cursor < len(permissionOptions(request)); cursor++ {
				card, _ := renderFocusedPermissionPrompt(request, cursor, false, "", width)
				got := plainRender(t, card)
				if !strings.Contains(got, "Fora do ambiente isolado") {
					t.Fatalf("width=%d cmd=%q cursor=%d: disclosure missing, got %q", width, cmd, cursor, got)
				}
				pLine := cardLineWith(got, "[p]")
				if pLine == "" {
					t.Fatalf("width=%d cmd=%q cursor=%d: [p] truncated, got %q", width, cmd, cursor, got)
				}
				if !strings.Contains(pLine, "nesta sessão") {
					t.Fatalf("width=%d cmd=%q cursor=%d: scope cut from [p] line %q, got %q", width, cmd, cursor, pLine, got)
				}
				yLine := cardLineWith(got, "[y]")
				if yLine == "" {
					t.Fatalf("width=%d cmd=%q cursor=%d: [y] truncated, got %q", width, cmd, cursor, got)
				}
				if !strings.Contains(yLine, "sempre") {
					t.Fatalf("width=%d cmd=%q cursor=%d: scope cut from [y] line %q, got %q", width, cmd, cursor, yLine, got)
				}
				if !strings.Contains(got, cmd) {
					t.Fatalf("width=%d cmd=%q cursor=%d: full command missing, got %q", width, cmd, cursor, got)
				}
			}
		}
	}
}

// cardLineWith returns the first card line containing mark, or "" when the
// mark was truncated away.
func cardLineWith(card, mark string) string {
	for _, line := range strings.Split(card, "\n") {
		if strings.Contains(line, mark) {
			return line
		}
	}
	return ""
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
