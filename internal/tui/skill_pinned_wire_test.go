package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/skills"
)

// A non-typable skill picked from the picker has no raw spelling, so the
// wire it produces is pinned on the FIFO and sent literally when the turn
// drains — a skill token INSIDE the body must NOT expand a second time.
func TestPickerNonSlashableSkillPendingQueueStaysLiteralOnDrain(t *testing.T) {
	m := newModel(context.Background(), Options{
		Cwd: t.TempDir(),
		LoadSkills: func() []skills.Skill {
			return []skills.Skill{
				{Name: "has space", Content: "has space body, references /alpha inside."},
				{Name: "alpha", Content: "ALPHA-BODY."},
			}
		},
		AgentOptions: agent.Options{Skills: []agent.SkillInfo{
			{Name: "has space"}, {Name: "alpha"},
		}},
	})
	m.pending = true

	next, _ := m.invokeSkillByName("has space")
	if len(next.queuedMessages) != 1 {
		t.Fatalf("non-typable skill during a run must queue exactly once, got %v", next.queuedMessages)
	}
	if !strings.HasPrefix(next.queuedMessages[0], literalPinnedPrefix) {
		t.Fatalf("wire must be pinned on the FIFO, got %q", next.queuedMessages[0])
	}
	drained := next
	drained.pending = false
	out, _ := drained.launchQueuedMessageIfReady()

	joined := transcriptText(out.transcript)
	if !strings.Contains(joined, "SPACE-SKILL-BODY") && !strings.Contains(joined, "has space body") {
		t.Fatalf("pinned wire must reach the transcript, got %#v", out.transcript)
	}
	if strings.Count(joined, "ALPHA-BODY.") != 0 {
		t.Fatalf("skill tokens inside the pinned wire must not re-expand, got %#v", out.transcript)
	}
	if strings.Contains(joined, "\x00") {
		t.Fatalf("the FIFO marker must never reach the transcript, got %#v", out.transcript)
	}
}

// /retry after a picker pick of a non-typable skill resends the wire
// verbatim — the body is the prompt; re-dispatching it as a command line
// would re-expand any skill tokens inside it.
func TestPickerNonSlashableSkillRetryResendsWireLiteral(t *testing.T) {
	m := newModel(context.Background(), Options{
		Cwd: t.TempDir(),
		LoadSkills: func() []skills.Skill {
			return []skills.Skill{
				{Name: "has space", Content: "has space body, references /alpha inside."},
				{Name: "alpha", Content: "ALPHA-BODY."},
			}
		},
		AgentOptions: agent.Options{Skills: []agent.SkillInfo{
			{Name: "has space"}, {Name: "alpha"},
		}},
	})

	next, _ := m.invokeSkillByName("has space")
	if !transcriptContains(next.transcript, "has space body") {
		t.Fatalf("setup: picker should have launched the wire, got %#v", next.transcript)
	}
	updated, _ := next.dispatchCommand(parseCommand("/retry"))
	retried := updated.(model)
	joined := transcriptText(retried.transcript)
	if strings.Count(joined, "ALPHA-BODY.") != 0 {
		t.Fatalf("/retry must not re-expand the wire body, got %#v", retried.transcript)
	}
	if strings.Count(joined, "has space body") < 2 {
		t.Fatalf("/retry must resend the wire, got %#v", retried.transcript)
	}
}
