package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// Without customization the status line must render byte for byte like the
// built-in one, at every width tier. This pins the default; the custom path
// below must never alter it.
func TestStatusLineDefaultIdentical(t *testing.T) {
	for _, width := range []int{40, 58, 80, 110, 200} {
		plain := limeTestModel()
		custom := limeTestModel()
		custom.statuslineCfg = config.StatusLineConfig{}
		if got, want := plainRender(t, custom.statusLine(width)), plainRender(t, plain.statusLine(width)); got != want {
			t.Fatalf("width %d: custom-empty status line differs:\n got: %q\nwant: %q", width, got, want)
		}
	}
}

// A custom layout renders the configured ids left and right with the
// configured separator. Unknown ids are skipped (validation reports them).
func TestStatusLineCustomLayout(t *testing.T) {
	m := limeTestModel()
	m.gitBranch = "main"
	m.statuslineCfg = config.StatusLineConfig{
		Lines:     []config.StatusLineLine{{Left: []string{"mode", "bogus"}, Right: []string{"branch", "cwd"}}},
		Separator: " | ",
	}
	got := plainRender(t, m.statusLine(200))
	for _, want := range []string{"automático", "main", " | "} {
		if !strings.Contains(got, want) {
			t.Fatalf("custom status line = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "bogus") {
		t.Fatalf("custom status line = %q, unknown id leaked into output", got)
	}
}

// The external command's cached output appends as its own footer lines after
// the field lines, three lines total. A failure mark shows only when there is
// no cached output to keep.
func TestStatusLineCustomAppendsCommandOutput(t *testing.T) {
	m := limeTestModel()
	m.statuslineCfg = config.StatusLineConfig{
		Lines:   []config.StatusLineLine{{Left: []string{"mode"}}},
		Command: &config.StatusLineCommand{Command: "echo hi"},
	}
	m.statuslineCmdOutput = "custom one\ncustom two"
	lines := strings.Split(plainRender(t, m.statusLine(200)), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (1 field + 2 command):\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[1], "custom one") || !strings.Contains(lines[2], "custom two") {
		t.Fatalf("command lines missing:\n%s", strings.Join(lines, "\n"))
	}
	m.statuslineCmdOutput = ""
	m.statuslineCmdErr = "statusline: comando falhou (saída 1)"
	got := plainRender(t, m.statusLine(200))
	if !strings.Contains(got, "falhou") {
		t.Fatalf("failure mark missing:\n%s", got)
	}
}
func TestStatusLineCustomMultiline(t *testing.T) {
	m := limeTestModel()
	m.statuslineCfg = config.StatusLineConfig{
		Lines: []config.StatusLineLine{
			{Left: []string{"mode"}},
			{Left: []string{"effort"}},
			{Left: []string{"context"}},
			{Left: []string{"branch"}},
		},
	}
	lines := strings.Split(plainRender(t, m.statusLine(200)), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3 (capped):\n%s", len(lines), strings.Join(lines, "\n"))
	}
}

// Only registered ids validate; the reporter names every unknown id so
// /statusline can show one actionable warning.
func TestStatusLineFieldValidation(t *testing.T) {
	for _, id := range []string{"mode", "effort", "voice", "background", "goal", "context", "branch", "update", "cwd", "cache", "model", "speed", "cost", "time"} {
		if !statuslineFieldKnown(id) {
			t.Fatalf("catalog id %q unknown", id)
		}
	}
	if statuslineFieldKnown("command") {
		t.Fatal("command is not a field id: the external command appends its own footer lines")
	}
	if statuslineFieldKnown("bogus") {
		t.Fatal("bogus validates, want unknown")
	}
	unknown := unknownStatusLineFields(config.StatusLineConfig{Lines: []config.StatusLineLine{{Left: []string{"mode", "nope"}, Right: []string{"nah"}}}})
	if len(unknown) != 2 || unknown[0] != "nope" || unknown[1] != "nah" {
		t.Fatalf("unknown = %#v", unknown)
	}
}

// The cost segment sums priced BRL usage events across the session (resume
// included); USD and malformed amounts never inflate it.
func TestSessionCostBRL(t *testing.T) {
	m := limeTestModel()
	m.sessionEvents = []sessions.Event{
		{Type: sessions.EventUsage, Payload: json.RawMessage(`{"cost_exact": "1/20", "cost_currency": "BRL"}`)},
		{Type: sessions.EventUsage, Payload: json.RawMessage(`{"cost_exact": "0.07", "cost_currency": "BRL"}`)},
		{Type: sessions.EventUsage, Payload: json.RawMessage(`{"cost_exact": "9.99", "cost_currency": "USD"}`)},
		{Type: sessions.EventUsage, Payload: json.RawMessage(`{"cost_exact": "junk", "cost_currency": "BRL"}`)},
		{Type: sessions.EventMessage, Payload: json.RawMessage(`{"content": "hi"}`)},
	}
	if total, ok := m.sessionCostBRLRat(); !ok {
		t.Fatal("expected priced BRL usage")
	} else if got := formatBRLRat(total); got != "R$ 0,12" {
		t.Fatalf("sessionCostBRLRat = %q, want %q", got, "R$ 0,12")
	}
	empty := limeTestModel()
	if _, ok := empty.sessionCostBRLRat(); ok {
		t.Fatal("empty sessionCostBRLRat reported costs")
	}
}
