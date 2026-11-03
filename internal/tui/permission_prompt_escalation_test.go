package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
)

func prefixDisplayedLabels(request agent.PermissionRequest) []string {
	labels := []string{}
	for _, option := range permissionOptions(request) {
		if option.choice == permissionDecisionAllowPrefix || option.choice == permissionDecisionAlwaysAllowPrefix {
			labels = append(labels, permissionOptionLabel(option, request))
		}
	}
	return labels
}

func prefixRequest(escalates bool) agent.PermissionRequest {
	return agent.PermissionRequest{
		ToolName: "bash",
		Action:   agent.PermissionActionPrompt,
		AvailableDecisions: []agent.PermissionDecisionAction{
			agent.PermissionDecisionAllow,
			agent.PermissionDecisionAllowPrefix,
			agent.PermissionDecisionAlwaysAllowPrefix,
			agent.PermissionDecisionDeny,
		},
		PrefixApprovalEscalates: escalates,
	}
}

// Approving a prefix also runs the command outside the sandbox, and the
// displayed label is the only place the person deciding could learn that.
// Both prefix options escalate, so both must say so; covering only one would
// leave the other as a silent path to the same outcome.
func TestPrefixOptionsDiscloseLeavingTheSandbox(t *testing.T) {
	labels := prefixDisplayedLabels(prefixRequest(true))
	if len(labels) != 2 {
		t.Fatalf("expected both prefix options, got %#v", labels)
	}
	for _, label := range labels {
		if !strings.Contains(label, "Fora do ambiente isolado") {
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
// without escalation carry no warning and show no command block, so the card
// must not cry wolf. The disclosure check is case-insensitive so a warning in
// any lettering still trips it.
func TestRenderedPrefixOptionsStaySilentWithoutFlag(t *testing.T) {
	request := prefixRequest(false)
	request.CommandPrefix = []string{"git", "status"}
	card, _ := renderFocusedPermissionPrompt(request, 1, false, "", 100)
	got := plainRender(t, card)
	if strings.Contains(strings.ToLower(got), "fora do ambiente isolado") {
		t.Fatalf("rendered prefix card without escalation must not warn, got %q", got)
	}
	if strings.Contains(got, "prefixo de comando:") {
		t.Fatalf("rendered prefix card without escalation must not show the command block, got %q", got)
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
	for _, label := range prefixDisplayedLabels(prefixRequest(false)) {
		if strings.Contains(strings.ToLower(label), "fora do ambiente isolado") {
			t.Errorf("prefix option %q warns about the sandbox when approving would not leave it", label)
		}
	}
}

// The warning leads and the command wraps instead of being cut, so a narrow
// card keeps the disclosure, the hotkeys, and the full command: no line of
// the command block may carry an ellipsis.
// The warning and the scope must survive narrow cards with the cursor on any
// option: the fixed short labels carry the disclosure, the hotkeys stay
// visible, and the full command rides on its own wrapped line(s) above the
// options.
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

// The non-prefix options do not escalate, so the flag must not bleed into
// their displayed labels either.
func TestNonPrefixOptionsNeverCarryTheDisclosure(t *testing.T) {
	request := prefixRequest(true)
	for _, option := range permissionOptions(request) {
		if option.choice == permissionDecisionAllowPrefix || option.choice == permissionDecisionAlwaysAllowPrefix {
			continue
		}
		if label := permissionOptionLabel(option, request); strings.Contains(strings.ToLower(label), "fora do ambiente isolado") {
			t.Errorf("option %q carries the prefix escalation disclosure but does not escalate", label)
		}
	}
}

// longPrefixCommands covers a command longer than the card: one that must wrap
// by word and one single token that must break by cell.
func longPrefixCommands() [][]string {
	return [][]string{
		{"docker", "compose", "-f", "docker-compose.production.yml", "up", "--build", "--remove-orphans"},
		{"./scripts/very-long-script-name-without-spaces-in-it-run-it"},
	}
}

// commandBlockLines returns the card lines carrying the wrapped command: the
// label line and every following line up to the first option (hotkey) line.
func commandBlockLines(card string) []string {
	lines := strings.Split(card, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, "prefixo de comando:") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	out := []string{lines[start]}
	for _, line := range lines[start+1:] {
		if strings.Contains(line, "[") {
			break
		}
		out = append(out, line)
	}
	return out
}

func spaceless(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "│", "")), "")
}

// TestRenderedLongPrefixWrapsCommandWithoutEllipsis pins the no-cut contract:
// with a prefix longer than the line, the full command rides above the
// options wrapped by word (or by cell for a single long token), and no line
// of the command block carries an ellipsis. Widths sweep tiny to wide with
// the cursor on every option.
func TestRenderedLongPrefixWrapsCommandWithoutEllipsis(t *testing.T) {
	for _, width := range []int{50, 58, 80, 120} {
		for _, cmd := range longPrefixCommands() {
			joined := strings.Join(cmd, " ")
			request := prefixRequest(true)
			request.CommandPrefix = cmd
			for cursor := 0; cursor < len(permissionOptions(request)); cursor++ {
				card, _ := renderFocusedPermissionPrompt(request, cursor, false, "", width)
				got := plainRender(t, card)
				if !strings.Contains(got, "Fora do ambiente isolado") {
					t.Fatalf("width=%d cmd=%q cursor=%d: disclosure missing, got %q", width, joined, cursor, got)
				}
				block := commandBlockLines(got)
				if len(block) == 0 {
					t.Fatalf("width=%d cmd=%q cursor=%d: command block missing, got %q", width, joined, cursor, got)
				}
				for _, line := range block {
					if strings.Contains(line, "…") {
						t.Fatalf("width=%d cmd=%q cursor=%d: command line cut %q, got %q", width, joined, cursor, line, got)
					}
					if lipgloss.Width(line) > width {
						t.Fatalf("width=%d cmd=%q cursor=%d: command line overflows %q, got %q", width, joined, cursor, line, got)
					}
				}
				var rendered string
				for _, line := range block {
					rendered += spaceless(line)
				}
				if want := spaceless("prefixo de comando: `" + joined + "`"); rendered != want {
					t.Fatalf("width=%d cmd=%q cursor=%d: command = %q, want %q", width, joined, cursor, rendered, want)
				}
			}
		}
	}
}

// TestPrefixCardClickResolvesEachOptionWithWrappedCommand pins the click map
// with a multi-line command block: clicking every option row must resolve
// exactly that option's choice, so the options shifting down can never
// misalign clicks.
func TestPrefixCardClickResolvesEachOptionWithWrappedCommand(t *testing.T) {
	for _, width := range []int{50, 80, 120} {
		for _, cmd := range longPrefixCommands() {
			joined := strings.Join(cmd, " ")
			request := prefixRequest(true)
			request.CommandPrefix = cmd
			for _, option := range permissionOptions(request) {
				var got []agent.PermissionDecision
				m := pendingPermissionModelWithRequest(t, request, func(d agent.PermissionDecision) {
					got = append(got, d)
				})
				m.width = width
				m.height = 30
				m.altScreen = true
				m.headerPrinted = true
				body, selectable := m.transcriptBody(m.chatColumnWidth(), "")
				start, _, top := m.transcriptViewportStart(body, m.chatColumnWidth())
				var target transcriptSelectableLine
				found := false
				for _, line := range selectable {
					if line.permOption && line.permChoice == option.choice {
						target, found = line, true
						break
					}
				}
				if !found {
					t.Fatalf("width=%d cmd=%q: no clickable row for option %q", width, joined, option.choice)
				}
				updated, _ := m.Update(testMouseClick(tea.MouseLeft, target.textStart, top+target.bodyY-start))
				next := updated.(model)
				if next.pendingPermission != nil {
					t.Fatalf("width=%d cmd=%q: clicking %q must resolve, prompt still pending", width, joined, option.choice)
				}
				if len(got) != 1 || got[0].Action != agent.PermissionDecisionAction(option.choice) {
					t.Fatalf("width=%d cmd=%q: clicking %q resolved %#v", width, joined, option.choice, got)
				}
			}
		}
	}
}
