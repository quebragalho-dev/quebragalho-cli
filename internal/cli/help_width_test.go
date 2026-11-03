package cli

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestHelpTreeFits80Cells walks the real command tree — top-level help plus
// every subcommand listed under a "Comandos:" section — and requires each
// --help output to fit in 80 cells. A new command appears here automatically
// because the walk discovers it from its parent's "Comandos:" section.
func TestHelpTreeFits80Cells(t *testing.T) {
	seen := map[string]bool{}
	var walk func(path []string, parent string)
	walk = func(path []string, parent string) {
		key := strings.Join(path, " ")
		if seen[key] {
			return
		}
		seen[key] = true
		args := append(append([]string{}, path...), "--help")
		var stdout, stderr bytes.Buffer
		if code := runWithDeps(args, &stdout, &stderr, appDeps{}); code != exitSuccess {
			t.Errorf("qg %s --help exited %d: %s", key, code, stderr.String())
			return
		}
		out := stdout.String()
		for _, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > 80 {
				t.Errorf("qg %s --help exceeds 80 cells (%d): %q", key, w, line)
			}
		}
		if out == parent {
			return
		}
		for _, sub := range helpSubcommands(out) {
			walk(append(append([]string{}, path...), sub), out)
		}
	}
	walk(nil, "")
	// Helpers not reachable from the top-level command list.
	walk([]string{"trust"}, "")
	// `qg cron` without arguments prints usage to stderr and exits non-zero.
	var cronOut, cronErr bytes.Buffer
	if code := runWithDeps([]string{"cron"}, &cronOut, &cronErr, appDeps{}); code == exitSuccess {
		t.Fatalf("qg cron without args should print usage and fail")
	}
	for _, line := range strings.Split(cronErr.String(), "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("qg cron usage exceeds 80 cells (%d): %q", w, line)
		}
	}
	t.Logf("checked %d help outputs", len(seen))
}

// helpSubcommands lists the subcommands of a help text from its "Comandos:"
// section. Only rows starting with exactly two spaces count, so wrapped
// description continuations never parse as commands.
func helpSubcommands(out string) []string {
	var subs []string
	inSection := false
	for _, line := range strings.Split(out, "\n") {
		if line == "Comandos:" {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if strings.TrimSpace(line) == "" {
			inSection = false
			continue
		}
		if len(line) > 2 && line[0] == ' ' && line[1] == ' ' && line[2] != ' ' {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			name := fields[0]
			ok := name != ""
			for _, r := range name {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
					ok = false
					break
				}
			}
			if ok {
				subs = append(subs, name)
			}
			continue
		}
		if len(line) > 0 && line[0] != ' ' {
			break
		}
	}
	return subs
}
