package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
	for _, id := range []string{"mode", "effort", "voice", "background", "goal", "loop", "context", "branch", "update", "cwd", "cache", "model", "speed", "cost", "time"} {
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

func TestStatusLineCustomShowsMissingNoticesOnOwnLine(t *testing.T) {
	base := func() model {
		m := limeTestModel()
		m.gitBranch = "main"
		m.statuslineCfg = config.StatusLineConfig{
			Lines: []config.StatusLineLine{{Left: []string{"mode", "branch"}}},
		}
		return m
	}

	t.Run("active loop on own line", func(t *testing.T) {
		m := base()
		m.loops = []*loopState{{id: "L1", nextRunAt: time.Date(2026, 11, 18, 15, 5, 0, 0, time.UTC)}}
		lines := strings.Split(plainRender(t, m.statusLine(160)), "\n")
		if len(lines) != 2 {
			t.Fatalf("expected 2 lines (custom layout + loop notice), got %d:\n%s", len(lines), strings.Join(lines, "\n"))
		}
		if !strings.Contains(lines[0], "automático") || strings.Contains(lines[0], "1 loop") {
			t.Fatalf("line 0 should keep custom layout only, got %q", lines[0])
		}
		if !strings.Contains(lines[1], "↻ 1 loop · próximo 3:05pm") {
			t.Fatalf("line 1 missing active loop notice, got %q", lines[1])
		}
	})

	t.Run("dictation model download on own line", func(t *testing.T) {
		m := base()
		m.dictation.downloading = true
		m.dictation.downloadLabel = "Motor"
		m.dictation.downloadDone = 11 << 20
		m.dictation.downloadTotal = 22 << 20
		lines := strings.Split(plainRender(t, m.statusLine(160)), "\n")
		if len(lines) != 2 {
			t.Fatalf("expected 2 lines (custom layout + download notice), got %d:\n%s", len(lines), strings.Join(lines, "\n"))
		}
		if !strings.Contains(lines[1], "⬇ Motor") || !strings.Contains(lines[1], "50%") {
			t.Fatalf("line 1 missing dictation download progress, got %q", lines[1])
		}
	})

	t.Run("update notice on own line even with three field lines", func(t *testing.T) {
		m := base()
		m.statuslineCfg = config.StatusLineConfig{
			Lines: []config.StatusLineLine{
				{Left: []string{"mode"}},
				{Left: []string{"branch"}},
				{Left: []string{"cwd"}},
			},
		}
		m.updateAvailable = "v0.9.9"
		lines := strings.Split(plainRender(t, m.statusLine(160)), "\n")
		if len(lines) != 4 {
			t.Fatalf("expected 4 lines (3 field lines + update notice), got %d:\n%s", len(lines), strings.Join(lines, "\n"))
		}
		if !strings.Contains(lines[3], "nova versão v0.9.9") {
			t.Fatalf("last line missing update notice, got %q", lines[3])
		}
	})
}

func TestStatusLineCustomKeepsIncludedNoticeFieldsUnchanged(t *testing.T) {
	// Update notice already included in layout stays single-line, byte-identical.
	withUpdate := limeTestModel()
	withUpdate.statuslineCfg = config.StatusLineConfig{
		Lines: []config.StatusLineLine{{Left: []string{"mode"}, Right: []string{"update"}}},
	}
	withUpdate.updateAvailable = "v0.9.9"
	gotUpdate := strings.Split(plainRender(t, withUpdate.statusLine(160)), "\n")
	if len(gotUpdate) != 1 || !strings.Contains(gotUpdate[0], "nova versão v0.9.9") {
		t.Fatalf("layout including update should stay 1 line with update in place, got %d:\n%s", len(gotUpdate), strings.Join(gotUpdate, "\n"))
	}

	// Voice field already included in layout renders download in-place without extra line.
	withVoice := limeTestModel()
	withVoice.statuslineCfg = config.StatusLineConfig{
		Lines: []config.StatusLineLine{{Left: []string{"mode", "voice"}}},
	}
	withVoice.dictation.downloading = true
	withVoice.dictation.downloadLabel = "Motor"
	withVoice.dictation.downloadDone = 5 << 20
	withVoice.dictation.downloadTotal = 10 << 20
	gotVoice := strings.Split(plainRender(t, withVoice.statusLine(160)), "\n")
	if len(gotVoice) != 1 || !strings.Contains(gotVoice[0], "⬇ Motor") {
		t.Fatalf("layout including voice should render download in-place on 1 line, got %d:\n%s", len(gotVoice), strings.Join(gotVoice, "\n"))
	}

	// Loop field already included in layout renders active loop in-place without extra line.
	withLoop := limeTestModel()
	withLoop.statuslineCfg = config.StatusLineConfig{
		Lines: []config.StatusLineLine{{Left: []string{"mode", "loop"}}},
	}
	withLoop.loops = []*loopState{{id: "L1", nextRunAt: time.Date(2026, 11, 18, 15, 5, 0, 0, time.UTC)}}
	gotLoop := strings.Split(plainRender(t, withLoop.statusLine(160)), "\n")
	if len(gotLoop) != 1 || !strings.Contains(gotLoop[0], "↻ 1 loop") {
		t.Fatalf("layout including loop should render loop in-place on 1 line, got %d:\n%s", len(gotLoop), strings.Join(gotLoop, "\n"))
	}
}
