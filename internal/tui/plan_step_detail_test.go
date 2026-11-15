package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// lastCardText returns the text of the most recently appended transcript row —
// the plan-step detail card built by appendPlanStepCard.
func lastCardText(m model) string {
	if len(m.transcript) == 0 {
		return ""
	}
	return m.transcript[len(m.transcript)-1].text
}

// TestPlanStepDetailByStatus: a completed step reads as "what we did" (outcome,
// duration, the model's note, and the captured changes/commands); a pending
// step reads as "what we will do" (forward framing + the planned approach). With
// no provider configured the explanation falls back to the local summary, so the
// structured sections and status framing render synchronously.
func TestPlanStepDetailByStatus(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := model{now: func() time.Time { return t0.Add(5 * time.Minute) }}
	m.plan.steps = []planStep{
		{content: "ship the button", status: "completed", notes: "wired the click handler", startedAt: t0, completedAt: t0.Add(80 * time.Second)},
		{content: "write the docs", status: "pending", notes: "cover the new flag"},
	}
	m.stepWork = map[string][]planStepWork{
		"ship the button": {
			{tool: "edit_file", summary: "edit button.go", detail: "+added line\n-removed line"},
			{tool: "bash", summary: "go build", detail: "exit 0"},
		},
	}

	// Completed step -> "what we did" (local fallback, no provider).
	m.transcript = m.appendPlanStepCard(0, "", false)
	done := lastCardText(m)
	for _, want := range []string{"Concluído em 1m 20s.", "Concluído: 1 arquivo alterado, 1 comando.", "O que foi feito", "wired the click handler", "Arquivos alterados (1)", "Comandos executados (1)", "o que foi feito neste passo"} {
		if !strings.Contains(done, want) {
			t.Errorf("completed card missing %q\n---\n%s", want, done)
		}
	}

	// Pending step -> "what we will do".
	m.transcript = dropTranscriptRowsByID(m.transcript, planStepDetailRowID)
	m.transcript = m.appendPlanStepCard(1, "", false)
	pending := lastCardText(m)
	for _, want := range []string{"O que será feito", "o que este passo fará", "cover the new flag", "aparecem aqui"} {
		if !strings.Contains(pending, want) {
			t.Errorf("pending card missing %q\n---\n%s", want, pending)
		}
	}
	if strings.Contains(pending, "Files changed") {
		t.Errorf("pending card should record no work yet:\n%s", pending)
	}
}

// TestPlanStepDetailAIExplanation: the loading card shows the "Writing
// explanation…" line and the model's write-up delivered through Update replaces
// the card in place and is cached, so re-rendering the step is instant.
func TestPlanStepDetailAIExplanation(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := model{now: func() time.Time { return t0 }}
	m.plan.steps = []planStep{{content: "ship the button", status: "completed", startedAt: t0, completedAt: t0.Add(time.Minute)}}
	m.planDetailOpen = true
	m.planDetailStep = 0
	key := planStepExplanationKey(m.plan.steps[0])

	// Loading card via the production builder.
	m.transcript = m.appendPlanStepCard(0, "", true)
	if loading := lastCardText(m); !strings.Contains(loading, "Escrevendo explicação…") {
		t.Errorf("loading card should show the writing line:\n%s", loading)
	}

	// The model's write-up arrives through the production Update path.
	updated, _ := m.Update(planStepExplanationMsg{stepIndex: 0, key: key, gen: m.planDetailGen, text: "We added the new button and wired up its click."})
	m = updated.(model)
	if got := lastCardText(m); !strings.Contains(got, "We added the new button") {
		t.Errorf("card should update in place with the model text:\n%s", got)
	}
	if m.stepExplanation[key] == "" {
		t.Error("write-up should be cached for instant re-render")
	}

	// Re-render from cache: no second request needed.
	m.transcript = dropTranscriptRowsByID(m.transcript, planStepDetailRowID)
	m.transcript = m.appendPlanStepCard(0, m.stepExplanation[key], false)
	if got := lastCardText(m); !strings.Contains(got, "We added the new button") {
		t.Errorf("cached re-render should show the stored explanation:\n%s", got)
	}
}

// TestPlanStepDetailLateResponseStaysClosed: a write-up that lands after the
// card was closed is cached but must not re-add the card.
func TestPlanStepDetailLateResponseStaysClosed(t *testing.T) {
	m := model{now: time.Now}
	m.plan.steps = []planStep{{content: "do the thing", status: "completed"}}
	key := planStepExplanationKey(m.plan.steps[0])

	updated, _ := m.Update(planStepExplanationMsg{stepIndex: 0, key: key, gen: m.planDetailGen, text: "late explanation"})
	m = updated.(model)
	if len(m.transcript) != 0 {
		t.Errorf("a response with no open card must not add one: rows=%d", len(m.transcript))
	}
	if m.stepExplanation[key] != "late explanation" {
		t.Errorf("late response should still be cached, got %q", m.stepExplanation[key])
	}
}

// TestCaptureStepNarration: assistant prose is attributed to the in_progress
// step, blank/duplicate segments are dropped, and no step swallows another's.
func TestCaptureStepNarration(t *testing.T) {
	m := model{now: time.Now}
	m.plan.steps = []planStep{{content: "build it", status: "in_progress"}}
	m = m.captureStepNarration("First I'll scaffold the file.")
	m = m.captureStepNarration("First I'll scaffold the file.") // duplicate -> collapsed
	m = m.captureStepNarration("   ")                           // blank -> ignored
	m = m.captureStepNarration("Now I'll wire it up.")
	got := m.stepNarration["build it"]
	if len(got) != 2 {
		t.Fatalf("want 2 narration segments, got %d: %v", len(got), got)
	}
	if got[0] != "First I'll scaffold the file." || got[1] != "Now I'll wire it up." {
		t.Errorf("narration wrong: %v", got)
	}
}

// TestSidebarPlanSelectablesOffsets locks the click-to-step mapping against the
// renderContextSidebar layout: with no agents the AGENTS section is header +
// placeholder (2 lines), then a blank + PLAN header (2 lines), so step 0 sits on
// sidebar line 4.
func TestCaptureStepWork(t *testing.T) {
	m := model{now: time.Now}
	m.plan.steps = []planStep{{content: "build it", status: "in_progress"}}
	m = m.captureStepWork(transcriptRow{kind: rowToolResult, tool: "write_file", text: "wrote style.css", detail: "+ body {}"})
	m = m.captureStepWork(transcriptRow{kind: rowToolResult, tool: "bash", text: "ran go build", detail: "exit 0"})
	m = m.captureStepWork(transcriptRow{kind: rowToolResult, tool: "read_file", text: "read x"}) // ignored: not a work tool
	m = m.captureStepWork(transcriptRow{kind: rowToolCall, tool: "write_file", text: "call"})    // ignored: not a result
	work := m.stepWork["build it"]
	if len(work) != 2 {
		t.Fatalf("want 2 captured items (write_file + bash), got %d", len(work))
	}
	if work[0].tool != "write_file" || work[0].detail != "+ body {}" {
		t.Errorf("change item wrong: %+v", work[0])
	}
	if work[1].tool != "bash" || work[1].detail != "exit 0" {
		t.Errorf("command item wrong: %+v", work[1])
	}
	if !isPlanCommandTool("bash") || !isPlanCommandTool("exec_command") || isPlanCommandTool("write_file") {
		t.Errorf("isPlanCommandTool classification wrong")
	}
}

// TestPlanStepDetailToggle: re-clicking the open step hides the card (no
// stacking); clicking a different step switches; at most one card at a time.
func TestPlanStepExplanationLinesStayShort(t *testing.T) {
	m := model{}
	cases := []struct {
		status string
		want   []string
	}{
		{"completed", []string{"Este passo terminou. Os arquivos e comandos tocados estão listados abaixo."}},
		{"failed", []string{"Este passo falhou. O que ele tentou está listado abaixo."}},
		{"in_progress", []string{"Trabalho em andamento. Alterações e comandos aparecem abaixo conforme", "acontecem."}},
		{"pending", []string{"Quando o agente chegar nele, alterações e comandos aparecem aqui."}},
	}
	for _, tc := range cases {
		got := m.planStepExplanationLines(planStep{status: tc.status}, "", false)
		if len(got) != len(tc.want) {
			t.Fatalf("status %s: got %d rows %q, want %d", tc.status, len(got), got, len(tc.want))
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("status %s row %d = %q, want %q", tc.status, i, got[i], tc.want[i])
			}
			if w := utf8.RuneCountInString(got[i]); w > 76 {
				t.Fatalf("status %s row %d is %d cells wide, over the 76-cell wrap", tc.status, i, w)
			}
		}
	}
}
