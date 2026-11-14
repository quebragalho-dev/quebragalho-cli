// Sonda (QA) — proof tests for the P1/P2 findings on fcdbfe87 (item 5 audit).
// Copy into internal/tui/ next to the acceptance suite. Both P1 and P2 FAIL on
// fcdbfe87 for the stated reason and must PASS after the fix; the P1-proof and
// the multi-skill drain proof must keep passing afterwards (regression locks
// for the pipeline the fix must not disturb).
package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/skills"
)

// P1 (fcdbfe87, skill_commands.go invokeSkillByName): the picker is the ONLY
// path that reaches a skill whose name cannot be typed as a slash command
// (e.g. "has space"). On fcdbfe87 it sends the raw "/has space" line — which
// parses as a plain prompt (space inside), matches no skill token, and reaches
// the model as prose. The picker must expand the body, as e391161e did.
//
// Fails on fcdbfe87: transcript shows the literal "/has space" user row, no
// body. Must pass after the fix.
func TestSondaPickerNonSlashableSkillStillExpands(t *testing.T) {
	m := newSkillTestModel(t, skills.Skill{Name: "has space", Content: "SPACE-SKILL-BODY."})

	got, _ := m.invokeSkillByName("has space")

	if !transcriptContains(got.transcript, "SPACE-SKILL-BODY.") {
		t.Fatalf("picker must expand the non-slashable skill body, got %#v", got.transcript)
	}
	// The bare-invocation note rides along (picker offers no args), exactly
	// like a bare slash invocation.
	if !transcriptContains(got.transcript, "ask for them first") {
		t.Fatalf("picker expansion must carry the bare-invocation note, got %#v", got.transcript)
	}
}

// P2 (fcdbfe87, user_commands.go + launchPromptsInternal): a user command's
// immediate path launches the EXPANDED prompt, which becomes lastPrompt — so
// /retry re-sends the stale template and /edit edits prose, while skills got
// raw-everywhere in this same PR. The raw line is what /retry must re-dispatch.
//
// Fails on fcdbfe87 at the lastPrompt assertion (lastPrompt holds
// "Say hello to MARKUC." instead of "/greet MARKUC"). Must pass after the fix.
func TestSondaRetryUserCommandRedispatchesRaw(t *testing.T) {
	root := t.TempDir()
	writeUserCommand(t, root, "greet.md", "Say hello to $1.")
	m := newModel(context.Background(), Options{Cwd: root})

	next := acceptTypeEnter(t, m, "/greet MARKUC")
	if !transcriptContains(next.transcript, "Say hello to MARKUC.") {
		t.Fatalf("setup: user command should run, got %#v", next.transcript)
	}
	if next.lastPrompt != "/greet MARKUC" {
		t.Fatalf("lastPrompt must hold the RAW line, got %q", next.lastPrompt)
	}
	// Mutate the in-memory command (what a fresh file load would produce);
	// /retry must re-dispatch the raw line through handleUserCommand and
	// expand the NEW template.
	for i, cmd := range next.userCommands {
		if cmd.Name == "greet" {
			next.userCommands[i].Template = "NEWBODY hello to $1."
		}
	}
	updated, _ := next.dispatchCommand(parseCommand("/retry"))
	retried := updated.(model)
	if !transcriptContains(retried.transcript, "NEWBODY hello to MARKUC.") {
		t.Fatalf("/retry must re-dispatch the raw line through handleUserCommand, got %#v", retried.transcript)
	}
	// The skill-args invariant must survive the fix: skill tokens inside
	// user-command args NEVER expand, on retry either (spec bullet 5).
	if transcriptContains(retried.transcript, "REVIEW-BODY") {
		t.Fatalf("user-command args must stay literal across /retry, got %#v", retried.transcript)
	}
}

// Pipeline lock (PASSES on fcdbfe87 — must keep passing): a queued message
// citing two skills re-expands BOTH, in order, with tokens stripped, when the
// FIFO drains. Proves the drain re-dispatch applies the full expansion
// pipeline; the P1/P2 fix must not regress it.
func TestSondaQueuedMultiSkillDrainReexpandsAll(t *testing.T) {
	installed := []skills.Skill{
		{Name: "alpha", Content: "ALPHA-BODY."},
		{Name: "beta", Content: "BETA-BODY."},
	}
	infos := []agent.SkillInfo{{Name: "alpha"}, {Name: "beta"}}
	m := newModel(context.Background(), Options{
		Cwd:          t.TempDir(),
		LoadSkills:   func() []skills.Skill { return installed },
		AgentOptions: agent.Options{Skills: infos},
	})
	m.pending = true

	next := acceptTypeEnter(t, m, "check /alpha and /beta MARKDRAIN")
	if len(next.queuedMessages) != 1 {
		t.Fatalf("message should queue, got %v", next.queuedMessages)
	}
	drained := next
	drained.pending = false
	out, _ := drained.launchQueuedMessageIfReady()

	joined := transcriptText(out.transcript)
	ia := strings.Index(joined, "ALPHA-BODY.")
	ib := strings.Index(joined, "BETA-BODY.")
	if ia < 0 || ib < 0 || ia > ib {
		t.Fatalf("drained message must re-expand both skills in order, got %#v", out.transcript)
	}
	if !strings.Contains(joined, "MARKDRAIN") {
		t.Fatalf("user text must survive the drain, got %#v", out.transcript)
	}
	if strings.Contains(joined, "/alpha") || strings.Contains(joined, "/beta") {
		t.Fatalf("skill tokens must be stripped on drain, got %#v", out.transcript)
	}
}
