package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/skills"
)

// P2a: a pinned wire drained from the FIFO must leave /retry literal — a
// retry resends the wire verbatim instead of re-dispatching the body as a
// command line (which would re-expand any skill citations inside it).
func TestSondaPinDrainRetryStaysLiteral(t *testing.T) {
	installed := []skills.Skill{
		{Name: "has space", Content: "PINWIRE-BODY cites /reviewer today."},
		{Name: "reviewer", Content: "REVIEWER-BODY-V1."},
	}
	m := newModel(context.Background(), Options{
		Cwd:        t.TempDir(),
		LoadSkills: func() []skills.Skill { return installed },
		AgentOptions: agent.Options{Skills: []agent.SkillInfo{
			{Name: "has space"}, {Name: "reviewer"},
		}},
	})
	m.pending = true

	next, _ := m.invokeSkillByName("has space")
	if len(next.queuedMessages) != 1 {
		t.Fatalf("setup: pinned entry should queue, got %v", next.queuedMessages)
	}
	drained := next
	drained.pending = false
	out, _ := drained.launchQueuedMessageIfReady()
	if !transcriptContains(out.transcript, "PINWIRE-BODY") {
		t.Fatalf("setup: drain should deliver the wire, got %#v", out.transcript)
	}

	installed[1].Content = "REVIEWER-BODY-V2."
	out.loadSkills = func() []skills.Skill { return installed }
	updated, _ := out.dispatchCommand(parseCommand("/retry"))
	retried := updated.(model)

	joined := transcriptText(retried.transcript)
	if strings.Contains(joined, "REVIEWER-BODY-V2.") {
		t.Fatalf("/retry after a pinned drain must not re-expand the wire body, got %#v", retried.transcript)
	}
	if strings.Count(joined, "PINWIRE-BODY") < 2 {
		t.Fatalf("/retry must resend the wire, got %#v", retried.transcript)
	}
}

// UX: a ~100-line pinned wire renders in the Sussurro block as ONE line
// naming the skill — never as the body.
func TestSkillPinnedQueueEntryRendersNameOnly(t *testing.T) {
	longBody := strings.Repeat("lorem ipsum dolor sit amet consectetur adipiscing elit sed do eiusmod tempor.\n", 60)
	m := newModel(context.Background(), Options{
		Cwd: t.TempDir(),
		LoadSkills: func() []skills.Skill {
			return []skills.Skill{{Name: "has space", Content: longBody}}
		},
		AgentOptions: agent.Options{Skills: []agent.SkillInfo{{Name: "has space"}}},
	})
	m.pending = true

	next, _ := m.invokeSkillByName("has space")
	if len(next.queuedMessages) != 1 {
		t.Fatalf("setup: pinned entry should queue, got %v", next.queuedMessages)
	}
	block := next.renderQueuedTranscriptBlock(80)
	if !strings.Contains(block, "skill: has space") {
		t.Fatalf("pinned entry must show as one line naming the skill, got %q", block)
	}
	if strings.Contains(block, "lorem ipsum") {
		t.Fatalf("the pinned body must never render in the queue, got %q", block)
	}
	if strings.Count(block, "\n") > 2 {
		t.Fatalf("the pinned row must be a single line, got %d lines", strings.Count(block, "\n")+1)
	}
}

// UX: ↑ on a pinned tail pops it with a short notice instead of dumping the
// body into the composer.
func TestSkillPinnedQueueEntryArrowUpPopsWithNotice(t *testing.T) {
	m := newModel(context.Background(), Options{
		Cwd: t.TempDir(),
		LoadSkills: func() []skills.Skill {
			return []skills.Skill{{Name: "has space", Content: "long pinned body.\nsecond line."}}
		},
		AgentOptions: agent.Options{Skills: []agent.SkillInfo{{Name: "has space"}}},
	})
	m.pending = true

	next, _ := m.invokeSkillByName("has space")
	popped := next.popQueuedMessageForEdit()

	if len(popped.queuedMessages) != 0 {
		t.Fatalf("pinned entry must leave the queue, got %v", popped.queuedMessages)
	}
	if got := popped.composerValue(); got != "" {
		t.Fatalf("the body must not land in the composer, got %q", got)
	}
	if !transcriptContains(popped.transcript, "skill «has space» saiu da fila — escolha de novo pela lista para reenviar") {
		t.Fatalf("a short notice must say the entry left the queue, got %#v", popped.transcript)
	}
}

// UX: double-Esc (returnQueuedMessagesToComposer) drops pinned entries with
// a short notice each instead of pasting their bodies into the composer.
func TestSkillPinnedQueueEntryDoubleEscSurrendersWithNotice(t *testing.T) {
	m := newModel(context.Background(), Options{
		Cwd: t.TempDir(),
		LoadSkills: func() []skills.Skill {
			return []skills.Skill{{Name: "has space", Content: "long pinned body.\nsecond line."}}
		},
		AgentOptions: agent.Options{Skills: []agent.SkillInfo{{Name: "has space"}}},
	})
	m.pending = true

	next, _ := m.invokeSkillByName("has space")
	next = next.queueMessage("ordinary draft MARKESC")
	returned := next.returnQueuedMessagesToComposer()

	if got := returned.composerValue(); strings.Contains(got, "pinned body") {
		t.Fatalf("the pinned body must not land in the composer, got %q", got)
	}
	if !strings.Contains(returned.composerValue(), "MARKESC") {
		t.Fatalf("ordinary queued text returns to the composer, got %q", returned.composerValue())
	}
	if !transcriptContains(returned.transcript, "skill «has space» saiu da fila — escolha de novo pela lista para reenviar") {
		t.Fatalf("a short notice must say the entry left the queue, got %#v", returned.transcript)
	}
}
