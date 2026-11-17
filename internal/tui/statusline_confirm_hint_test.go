package tui

import (
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// The custom statusline must surface the same armed-confirmation hint the
// built-in line shows, replacing its first line in place so the footer
// height never jumps. Without an armed confirmation the custom output
// stays exactly as before.
func customConfirmHintTestModel() model {
	m := limeTestModel()
	m.gitBranch = "main"
	m.cwd = "/workspace/qg"
	m.modelName = "test-model"
	m.statuslineCfg = config.StatusLineConfig{
		Lines: []config.StatusLineLine{
			{Left: []string{"mode", "branch"}, Right: []string{"cwd"}},
			{Left: []string{"model"}},
		},
	}
	return m
}

func customConfirmHintTestModelWithCommand() model {
	m := customConfirmHintTestModel()
	m.statuslineCfg = config.StatusLineConfig{
		Lines: []config.StatusLineLine{
			{Left: []string{"mode"}},
		},
		Command: &config.StatusLineCommand{Command: "echo hi"},
	}
	m.statuslineCmdOutput = "custom one\ncustom two"
	return m
}

// Through the production statusLine path with fields only, an armed exit
// confirmation shows the same hint as the built-in line: first line carries
// the hint, remaining lines stay untouched, total height unchanged.
func TestStatusLineCustomShowsExitConfirmHint(t *testing.T) {
	m := customConfirmHintTestModel()
	base := strings.Split(plainRender(t, m.statusLine(100)), "\n")
	if len(base) != 2 {
		t.Fatalf("precondition: field layout must render 2 lines, got %d:\n%s", len(base), strings.Join(base, "\n"))
	}
	for _, line := range base {
		if strings.Contains(line, ctrlCExitConfirmText) || strings.Contains(line, escCancelConfirmText) {
			t.Fatalf("precondition: unarmed custom line must not carry a confirm hint:\n%s", strings.Join(base, "\n"))
		}
	}
	m.exitConfirmActive = true
	got := strings.Split(plainRender(t, m.statusLine(100)), "\n")
	if len(got) != len(base) {
		t.Fatalf("armed exit confirm changed footer height: got %d lines, want %d:\n%s", len(got), len(base), strings.Join(got, "\n"))
	}
	if !strings.Contains(got[0], ctrlCExitConfirmText) {
		t.Fatalf("custom status line with armed exit confirm must carry the exit hint in its first line, got:\n%s", strings.Join(got, "\n"))
	}
	if got[1] != base[1] {
		t.Fatalf("armed exit confirm must keep the remaining layout lines untouched:\n got %q\nwant %q", got[1], base[1])
	}
}

// Same as above for the Esc cancel confirmation while a turn runs.
func TestStatusLineCustomShowsCancelConfirmHint(t *testing.T) {
	m := customConfirmHintTestModel()
	base := strings.Split(plainRender(t, m.statusLine(100)), "\n")
	if len(base) != 2 {
		t.Fatalf("precondition: field layout must render 2 lines, got %d", len(base))
	}
	m.cancelConfirmActive = true
	got := strings.Split(plainRender(t, m.statusLine(100)), "\n")
	if len(got) != len(base) {
		t.Fatalf("armed cancel confirm changed footer height: got %d lines, want %d", len(got), len(base))
	}
	if !strings.Contains(got[0], escCancelConfirmText) {
		t.Fatalf("custom status line with armed cancel confirm must carry the cancel hint in its first line, got:\n%s", strings.Join(got, "\n"))
	}
	if got[1] != base[1] {
		t.Fatalf("armed cancel confirm must keep the remaining layout lines untouched:\n got %q\nwant %q", got[1], base[1])
	}
}

// With an external command holding cached output, the armed exit hint still
// takes over the first line only: the script lines stay and the height is
// unchanged, without running anything extra here.
func TestStatusLineCustomShowsExitConfirmHintWithCommand(t *testing.T) {
	m := customConfirmHintTestModelWithCommand()
	base := strings.Split(plainRender(t, m.statusLine(100)), "\n")
	if len(base) != 3 {
		t.Fatalf("precondition: 1 field line plus 2 command lines must render 3 lines, got %d:\n%s", len(base), strings.Join(base, "\n"))
	}
	m.exitConfirmActive = true
	got := strings.Split(plainRender(t, m.statusLine(100)), "\n")
	if len(got) != len(base) {
		t.Fatalf("armed exit confirm with command changed footer height: got %d, want %d", len(got), len(base))
	}
	if !strings.Contains(got[0], ctrlCExitConfirmText) {
		t.Fatalf("custom status line with command and armed exit confirm must carry the exit hint first, got:\n%s", strings.Join(got, "\n"))
	}
	for i := 1; i < len(base); i++ {
		if got[i] != base[i] {
			t.Fatalf("line %d must stay untouched during exit confirm:\n got %q\nwant %q", i, got[i], base[i])
		}
	}
}

// Same command-backed layout for the Esc cancel confirmation.
func TestStatusLineCustomShowsCancelConfirmHintWithCommand(t *testing.T) {
	m := customConfirmHintTestModelWithCommand()
	base := strings.Split(plainRender(t, m.statusLine(100)), "\n")
	if len(base) != 3 {
		t.Fatalf("precondition: expected 3 lines, got %d", len(base))
	}
	m.cancelConfirmActive = true
	got := strings.Split(plainRender(t, m.statusLine(100)), "\n")
	if len(got) != len(base) {
		t.Fatalf("armed cancel confirm with command changed footer height: got %d, want %d", len(got), len(base))
	}
	if !strings.Contains(got[0], escCancelConfirmText) {
		t.Fatalf("custom status line with command and armed cancel confirm must carry the cancel hint first, got:\n%s", strings.Join(got, "\n"))
	}
	for i := 1; i < len(base); i++ {
		if got[i] != base[i] {
			t.Fatalf("line %d must stay untouched during cancel confirm:\n got %q\nwant %q", i, got[i], base[i])
		}
	}
}

// The custom first line must match the built-in line byte-for-byte (styled):
// for each armed confirmation, BTW state and width, the custom layout's
// first line equals the built-in line of the same model with a zeroed config.
func TestStatusLineCustomMatchesBuiltinConfirmHintParity(t *testing.T) {
	for _, confirm := range []struct {
		name string
		arm  func(*model)
		hint string
	}{
		{"exit", func(m *model) { m.exitConfirmActive = true }, ctrlCExitConfirmText},
		{"cancel", func(m *model) { m.cancelConfirmActive = true }, escCancelConfirmText},
	} {
		for _, btwActive := range []bool{false, true} {
			for _, width := range []int{100, 30} {
				m := customConfirmHintTestModel()
				m.btw.active = btwActive
				confirm.arm(&m)
				customFirst := strings.Split(m.statusLine(width), "\n")[0]
				embedded := m
				embedded.statuslineCfg = config.StatusLineConfig{}
				want := embedded.statusLine(width)
				if strings.Contains(want, "\n") {
					t.Fatalf("%s btw=%v width=%d: built-in line must be single-line, got:\n%q", confirm.name, btwActive, width, want)
				}
				if customFirst != want {
					t.Fatalf("%s btw=%v width=%d: custom first line differs from built-in:\n got %q\nwant %q", confirm.name, btwActive, width, customFirst, want)
				}
				if width == 100 && !strings.Contains(customFirst, confirm.hint) {
					t.Fatalf("%s btw=%v width=%d: first line must carry %q, got:\n%q", confirm.name, btwActive, width, confirm.hint, customFirst)
				}
				if width != 100 && !strings.Contains(customFirst, "Pressione") {
					t.Fatalf("%s btw=%v width=%d: truncated first line must keep the hint prefix, got:\n%q", confirm.name, btwActive, width, customFirst)
				}
				if hasBTW := strings.Contains(customFirst, "BTW"); hasBTW != btwActive {
					t.Fatalf("%s btw=%v width=%d: BTW chip presence = %v, got:\n%q", confirm.name, btwActive, width, hasBTW, customFirst)
				}
			}
		}
	}
}

// Without any armed confirmation the custom layout renders exactly as
// before: no hint text leaks in and the field plus command lines keep
// their content.
func TestStatusLineCustomWithoutConfirmUnchanged(t *testing.T) {
	m := customConfirmHintTestModel()
	got := plainRender(t, m.statusLine(100))
	for _, hint := range []string{ctrlCExitConfirmText, escCancelConfirmText} {
		if strings.Contains(got, hint) {
			t.Fatalf("unarmed custom status line must not carry %q, got:\n%s", hint, got)
		}
	}
	for _, want := range []string{"automático", "main", "test-model"} {
		if !strings.Contains(got, want) {
			t.Fatalf("unarmed custom status line lost %q, got:\n%s", want, got)
		}
	}
	cmd := customConfirmHintTestModelWithCommand()
	cmdGot := plainRender(t, cmd.statusLine(100))
	for _, want := range []string{"automático", "custom one", "custom two"} {
		if !strings.Contains(cmdGot, want) {
			t.Fatalf("unarmed command layout lost %q, got:\n%s", want, cmdGot)
		}
	}
}
