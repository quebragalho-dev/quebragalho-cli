// plan_step_detail.go records the file mutations made while a plan step was
// in_progress ("what was built"). The work is captured from tool-result rows as
// they stream; the run-details surface renders the per-step summary.
package tui

import (
	"fmt"
	"strings"
	"time"
)

// planStepWork is one captured unit of implementation attributed to a plan step:
// a file mutation or command run while that step was in_progress.
type planStepWork struct {
	tool    string
	summary string
	detail  string // the tool's full output — the diff for edits, stdout/stderr for commands
}

// isPlanWorkTool reports whether a tool's result counts as implementation worth
// surfacing under a plan step — the file mutations and the commands run.
func isPlanWorkTool(name string) bool {
	switch name {
	case "write_file", "edit_file", "apply_patch", "bash", "exec_command":
		return true
	}
	return false
}

// isPlanCommandTool reports whether a captured work item is a command run (vs a
// file change), so the detail view can group them.
func isPlanCommandTool(name string) bool {
	return name == "bash" || name == "exec_command"
}

// planStepWorkSummary renders a concise one-line summary of a tool-result row
// for the step-detail card (the row's first line, else the tool name).
func planStepWorkSummary(row transcriptRow) string {
	text := strings.TrimSpace(strings.SplitN(row.text, "\n", 2)[0])
	if text == "" {
		text = row.tool
	}
	return text
}

// captureStepWork attributes a finished tool-result row to the plan step that
// was in_progress when it ran. No-op for non-mutation tools or when no step is
// active. Keyed by step content (stable enough across the model's full-replace
// plan updates for this read-only view).
func (m model) captureStepWork(row transcriptRow) model {
	if row.kind != rowToolResult || !isPlanWorkTool(row.tool) {
		return m
	}
	key := currentStepContent(m.plan.steps)
	if key == "" {
		return m
	}
	if m.stepWork == nil {
		m.stepWork = map[string][]planStepWork{}
	}
	m.stepWork[key] = append(m.stepWork[key], planStepWork{tool: row.tool, summary: planStepWorkSummary(row), detail: row.detail})
	return m
}

// captureStepNarration attributes a finalized assistant prose segment to the
// plan step that was in_progress when it streamed — the agent's own running
// account of the work, replayed as the step-detail card's explanation.
// Consecutive duplicates are collapsed so a re-emitted segment doesn't repeat.
func (m model) captureStepNarration(text string) model {
	text = strings.TrimSpace(text)
	if text == "" {
		return m
	}
	key := currentStepContent(m.plan.steps)
	if key == "" {
		return m
	}
	if m.stepNarration == nil {
		m.stepNarration = map[string][]string{}
	}
	prev := m.stepNarration[key]
	if n := len(prev); n > 0 && prev[n-1] == text {
		return m
	}
	m.stepNarration[key] = append(prev, text)
	return m
}

// planStepDetailRowID is the stable transcript id for the single plan-step
// detail card, so re-clicking toggles it instead of stacking duplicates.
const planStepDetailRowID = "plan/step-detail"

// dropTranscriptRowsByID returns the transcript with any rows carrying id removed.
func dropTranscriptRowsByID(rows []transcriptRow, id string) []transcriptRow {
	if id == "" {
		return rows
	}
	out := make([]transcriptRow, 0, len(rows))
	for _, r := range rows {
		if r.id != id {
			out = append(out, r)
		}
	}
	return out
}

// appendPlanStepCard builds the plan-step detail card and appends it to the
// transcript (replacing any prior card by stable id is the caller's job). The
// explanation section shows: the loading line when loading is set; else the
// supplied model-written explanation; else (empty, not loading) a local
// status-aware fallback summary. The structured sections are always local.
func (m model) appendPlanStepCard(stepIndex int, explanation string, loading bool) []transcriptRow {
	step := m.plan.steps[stepIndex]
	work := m.stepWork[step.content]

	var changes, commands []planStepWork
	for _, w := range work {
		if isPlanCommandTool(w.tool) {
			commands = append(commands, w)
		} else {
			changes = append(changes, w)
		}
	}
	done := step.status == "completed" || step.status == "failed"

	// The lead section is titled with the step itself and opens with a
	// status-aware outcome line ("Done in 1m 20s." / "Not started yet…").
	sections := []commandSection{{
		Title: step.content,
		Lines: m.planStepOutcomeLines(step, len(changes), len(commands)),
	}}

	// The centerpiece: a short write-up of the step in simple Brazilian Portuguese,
	// written by the model on click — "what we did" for a finished step,
	// "what we'll do" for a queued one.
	sections = append(sections, commandSection{
		Title: planStepExplanationTitle(step.status),
		Lines: m.planStepExplanationLines(step, explanation, loading),
	})

	// The model's own per-step note is a second, distilled statement of intent
	// (before) or record (after); surface it verbatim under a fitting label.
	if note := strings.TrimSpace(step.notes); note != "" {
		label := "Plano"
		if done {
			label = "Notas"
		}
		sections = append(sections, commandSection{Title: label, Lines: planWrapText(note, 76)})
	}

	if len(changes) > 0 {
		sections = append(sections, commandSection{
			Title: fmt.Sprintf("Arquivos alterados (%d)", len(changes)),
			Lines: planChangeLines(changes),
		})
	}
	if len(commands) > 0 {
		sections = append(sections, commandSection{
			Title: fmt.Sprintf("Comandos executados (%d)", len(commands)),
			Lines: planCommandLines(commands),
		})
	}

	card := renderPlanCard(commandOutput{
		Title:    fmt.Sprintf("Passo %d de %d do plano · %s", stepIndex+1, len(m.plan.steps), planStepStateLabel(step.status)),
		Status:   planStepDetailStatus(step.status),
		Sections: sections,
		Hints:    []string{planStepDetailHint(step.status)},
	})
	return appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, tool: "plan", id: planStepDetailRowID, text: card})
}

// planStepExplanationKey is the cache key for a step's written explanation:
// content + status, so a step re-explains itself when it transitions (e.g.
// pending → completed reads forward then back), but a re-click in the same state
// is served from cache with no second model call.
func planStepExplanationKey(step planStep) string {
	return step.status + "\x00" + step.content
}

// planStepOutcomeLines opens the lead section with a status-aware framing of the
// step: a finished step gets an outcome + duration ("Done in 1m 20s.") and a
// tally of what was built; an in_progress step gets its running clock and the
// work so far; a pending step is framed forward-looking ("what this step will
// do"), since no work has been captured yet.
func (m model) planStepOutcomeLines(step planStep, nChanges, nCommands int) []string {
	switch step.status {
	case "completed":
		head := "Concluído."
		if d := formatPlanStepDuration(step.completedAt.Sub(step.startedAt)); d != "" {
			head = "Concluído em " + d + "."
		}
		return []string{head, planWorkTally("Concluído", nChanges, nCommands)}
	case "failed":
		head := "Falhou."
		if d := formatPlanStepDuration(step.completedAt.Sub(step.startedAt)); d != "" {
			head = "Falhou após " + d + "."
		}
		return []string{head, planWorkTally("Tentado", nChanges, nCommands)}
	case "in_progress":
		head := "Em andamento."
		if d := formatPlanStepDuration(m.planNow().Sub(step.startedAt)); d != "" {
			head = "Em andamento há " + d + "."
		}
		return []string{head, planWorkTally("Até agora", nChanges, nCommands)}
	default: // pending
		return []string{"Ainda não começou; veja o que fará."}
	}
}

// planStepExplanationTitle is the header for the prose explanation section,
// matching the status framing the user asked for: "what we did" for a finished
// step, "what we'll do" for a queued one.
func planStepExplanationTitle(status string) string {
	switch status {
	case "completed", "failed":
		return "O que foi feito"
	case "in_progress":
		return "O que está sendo feito"
	default:
		return "O que será feito"
	}
}

// planStepExplanationLines is the prose explanation of a step. The model writes
// it fresh on click: while that one-shot request is in flight, loading is set
// and a "Writing explanation…" line shows; once it returns, explanation carries
// the written paragraph. When no model text is available (loading off, empty
// explanation — e.g. no provider, or the write-up failed) a local status-aware
// summary stands in, pointing at the structured sections below.
func (m model) planStepExplanationLines(step planStep, explanation string, loading bool) []string {
	if loading {
		return []string{"Escrevendo explicação…"}
	}
	if e := strings.TrimSpace(explanation); e != "" {
		return planWrapText(e, 76)
	}
	switch step.status {
	case "completed":
		return planWrapText("Este passo terminou. Os arquivos e comandos tocados estão listados abaixo.", 76)
	case "failed":
		return planWrapText("Este passo falhou. O que ele tentou está listado abaixo.", 76)
	case "in_progress":
		return planWrapText("Trabalho em andamento. Alterações e comandos aparecem abaixo conforme acontecem.", 76)
	default: // pending
		return planWrapText("Quando o agente chegar nele, alterações e comandos aparecem aqui.", 76)
	}
}

// planWorkTally summarizes how much work a step captured, or notes that none was
// recorded yet. verb frames it for the step's state ("Concluído", "Até agora", …).
func planWorkTally(verb string, nChanges, nCommands int) string {
	if nChanges == 0 && nCommands == 0 {
		return "Sem alterações ou comandos ainda."
	}
	var parts []string
	if nChanges > 0 {
		parts = append(parts, pluralNoun(nChanges, "arquivo alterado", "arquivos alterados"))
	}
	if nCommands > 0 {
		parts = append(parts, pluralNoun(nCommands, "comando", "comandos"))
	}
	return verb + ": " + strings.Join(parts, ", ") + "."
}

// planChangeLines lists each file mutation as a bullet with a +added/−removed
// diffstat and a short excerpt of the changed lines.
func planChangeLines(items []planStepWork) []string {
	var out []string
	for _, w := range items {
		head := "• " + w.summary
		if add, del := planDiffStat(w.detail); add+del > 0 {
			head += fmt.Sprintf("  (+%d −%d)", add, del)
		}
		out = append(out, head)
		out = append(out, planDetailExcerpt(w.detail, 3)...)
	}
	return out
}

// planCommandLines lists each command run as a bullet with a short excerpt of
// its output.
func planCommandLines(items []planStepWork) []string {
	var out []string
	for _, w := range items {
		out = append(out, "• "+w.summary)
		out = append(out, planDetailExcerpt(w.detail, 3)...)
	}
	return out
}

// planDiffStat counts added/removed lines in a unified diff, ignoring the
// +++/--- file headers. Output that isn't a diff (e.g. a written file's body or
// command stdout) yields no counts.
func planDiffStat(detail string) (added, removed int) {
	for _, line := range strings.Split(detail, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

// planDetailExcerpt returns up to max non-blank lines of a tool's output for the
// card, trimmed, with a trailing "… (N more lines)" when truncated. The card
// renderer collapses indentation, so this only needs to keep each line short.
func planDetailExcerpt(detail string, max int) []string {
	if strings.TrimSpace(detail) == "" {
		return nil
	}
	var kept []string
	for _, line := range strings.Split(detail, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, strings.TrimSpace(line))
	}
	more := 0
	if len(kept) > max {
		more = len(kept) - max
		kept = kept[:max]
	}
	if more > 0 {
		kept = append(kept, fmt.Sprintf("… (%d more line%s)", more, map[bool]string{true: "s", false: ""}[more != 1]))
	}
	return kept
}

// planStepStateLabel is the short human label after "Plan step N of M ·" in the
// card title.
func planStepStateLabel(status string) string {
	switch status {
	case "completed":
		return "concluído"
	case "failed":
		return "falhou"
	case "in_progress":
		return "em andamento"
	default:
		return "a seguir"
	}
}

// planStepDetailStatus maps a step's status to the card's status banner: a
// completed step reads ok, a failed step blocked, everything else neutral info.
func planStepDetailStatus(status string) commandStatus {
	switch status {
	case "completed":
		return commandStatusOK
	case "failed":
		return commandStatusBlocked
	default:
		return commandStatusInfo
	}
}

// planStepDetailHint is the trailing one-line hint, framing the card as a record
// ("what was done") or a preview ("what this step will do").
func planStepDetailHint(status string) string {
	switch status {
	case "completed", "failed":
		return "o que foi feito neste passo"
	case "in_progress":
		return "o que este passo está fazendo agora"
	default:
		return "o que este passo fará"
	}
}

// formatPlanStepDuration renders a step's span in a friendlier form than the raw
// footer clock: seconds under a minute, "Nm Ns" above. A qg or negative span
// renders empty so callers can drop the clause.
func formatPlanStepDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()+0.5))
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm %ds", mins, secs)
}

// planWrapText word-wraps s to width columns, returning one line per wrapped
// row, so a long note isn't truncated by the card's single-line fitter.
func planWrapText(s string, width int) []string {
	if width < 8 {
		width = 8
	}
	var out []string
	for _, para := range strings.Split(strings.TrimSpace(s), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			continue
		}
		line := words[0]
		for _, w := range words[1:] {
			if len(line)+1+len(w) > width {
				out = append(out, line)
				line = w
				continue
			}
			line += " " + w
		}
		out = append(out, line)
	}
	return out
}
