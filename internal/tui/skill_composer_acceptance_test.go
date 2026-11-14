// Package tui acceptance tests for spec item 5 — "Skills no composer"
// (spec: scratchpad/spec-skills.md, approved 2026-11-11).
//
// HOW TO USE (Forja): copy this file into internal/tui/ on your branch and run
//
//	go test ./internal/tui/ -run 'TestAcceptSkill' -v
//
// On base cb16edce every test tagged RED must fail for the reason stated in
// its comment; tests tagged REGRESSION LOCK must pass already (they pin
// current behavior the spec says to preserve).
//
// Observable surfaces used (no implementation seams):
//   - composer content after Enter on a suggestion (m.composerValue / m.input)
//   - transcript rows (transcriptContains) — what the user sees
//   - m.queuedMessages — the FIFO, verbatim
//   - m.lastPrompt + recallHistory — what /retry resends and ↑ recalls
//   - whether a run was launched at all (no transcript "unknown command",
//     body inlined, no side effects)
package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/agent"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/skills"
)

// --- helpers (self-contained; do not rely on Forja's helpers existing) ------

func acceptSkillModel(t *testing.T, installed ...skills.Skill) model {
	t.Helper()
	infos := make([]agent.SkillInfo, 0, len(installed))
	for _, s := range installed {
		infos = append(infos, agent.SkillInfo{Name: s.Name, Description: s.Description})
	}
	return newModel(context.Background(), Options{
		Cwd:          t.TempDir(),
		LoadSkills:   func() []skills.Skill { return installed },
		AgentOptions: agent.Options{Skills: infos},
	})
}

// acceptType types text rune by rune (so autocomplete recomputes like a real
// key flow) and then presses Enter once.
func acceptTypeEnter(t *testing.T, m model, text string) model {
	t.Helper()
	for _, r := range text {
		updated, _ := m.Update(testKeyText(string(r)))
		m = updated.(model)
	}
	updated, _ := m.Update(testKey(tea.KeyEnter))
	return updated.(model)
}

// acceptNoLaunch reports whether NO turn was launched. Checking the transcript
// alone misfires (error rows quote the input verbatim), so the signal is
// lastPrompt: launchPromptInternal records every launched prompt there.
func acceptNoLaunch(m model, marker string) bool {
	return strings.TrimSpace(m.lastPrompt) == ""
}

// --- Spec bullet 1 -----------------------------------------------------------
// "No menu de sugestões (/), Enter numa SKILL insere "/<skill> " no composer e
//  NÃO envia. Repetível: várias skills, em qualquer posição do texto. Enter
//  sem menu aberto envia."

// RED(cb16edce): today the palette closes as soon as the token contains a
// space (commandSuggestionsOpen requires a single token), so a skill can only
// ever be the FIRST word; and Enter on a skill row goes through
// chooseSuggestion→handleSubmit for non-argument commands (resolveCommand
// fails for skills → requiresInput=false → submits immediately). The spec
// wants insertion without submit for skills, and a palette that still offers
// skills after the first token.
func TestAcceptSkill_EnterOnSkillSuggestionInsertsWithoutSending(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{
		Name: "reviewer", Description: "reviews", Content: "BODY-SENTINEL review instructions.",
	})

	// Type "/" then enough of the name to filter, then accept the suggestion.
	updated := m
	for _, r := range "/rev" {
		u, _ := updated.Update(testKeyText(string(r)))
		updated = u.(model)
	}
	if !updated.suggestionsActive() {
		t.Fatalf("palette should be open for /rev, got suggestions=%#v", updated.suggestions)
	}
	// Find the skill row and select it.
	found := false
	for i, s := range updated.suggestions {
		if s.Name == "/reviewer" {
			updated.suggestionIdx = i
			found = true
		}
	}
	if !found {
		t.Fatalf("skill /reviewer should be suggested, got %#v", updated.suggestions)
	}
	u, _ := updated.Update(testKey(tea.KeyEnter))
	next := u.(model)

	if got := next.composerValue(); got != "/reviewer " {
		t.Fatalf("Enter on a skill suggestion must insert \"/reviewer \" into the composer, got %q", got)
	}
	if transcriptContains(next.transcript, "BODY-SENTINEL") {
		t.Fatalf("Enter on a skill suggestion must NOT send; skill body reached the transcript: %#v", next.transcript)
	}
}

// RED(cb16edce): repeatable — a second skill token must be suggestible and
// insertable after existing text ("check this /rev" → palette opens on the
// trailing token; Enter inserts "/reviewer " at the cursor, keeps the rest).
func TestAcceptSkill_SecondSkillTokenInsertableMidText(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{
		Name: "reviewer", Content: "BODY-SENTINEL review instructions.",
	})

	updated := m
	for _, r := range "check this /rev" {
		u, _ := updated.Update(testKeyText(string(r)))
		updated = u.(model)
	}
	if !updated.suggestionsActive() {
		t.Fatalf("palette should open on a trailing /token mid-text (spec: skills em qualquer posição), got suggestions=%#v palette=%v",
			updated.suggestions, updated.commandPaletteOpen)
	}
	found := false
	for i, s := range updated.suggestions {
		if s.Name == "/reviewer" {
			updated.suggestionIdx = i
			found = true
		}
	}
	if !found {
		t.Fatalf("skill /reviewer should be suggested mid-text, got %#v", updated.suggestions)
	}
	u, _ := updated.Update(testKey(tea.KeyEnter))
	next := u.(model)

	got := next.composerValue()
	if !strings.Contains(got, "check this ") || !strings.Contains(got, "/reviewer ") {
		t.Fatalf("mid-text insertion must keep prior text and add the skill token, got %q", got)
	}
	if transcriptContains(next.transcript, "BODY-SENTINEL") {
		t.Fatalf("insertion must not send, got %#v", next.transcript)
	}
}

// REGRESSION LOCK: Enter with NO menu open submits (today's plain prompt
// path). Pins spec sentence "Enter sem menu aberto envia."
func TestAcceptSkill_EnterWithoutMenuStillSubmits(t *testing.T) {
	m := acceptSkillModel(t)

	next := acceptTypeEnter(t, m, "plain prompt MARKNOPALETTE")

	if acceptNoLaunch(next, "MARKNOPALETTE") {
		t.Fatalf("Enter without an open menu must submit the prompt, got %#v", next.transcript)
	}
}

// --- Spec bullet 2 -----------------------------------------------------------
// "Comandos embutidos (/model, /clear, /compact...) e comandos do usuário:
//  Enter no menu continua como hoje."

// REGRESSION LOCK: Enter on a builtin that requires no args executes it
// (today chooseSuggestion→handleSubmit). /clear empties the transcript — the
// observable signal.
func TestAcceptSkill_BuiltinEnterBehaviorUnchanged(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "x"})
	// Seed the transcript so /clear has a visible effect.
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "SEED-ROW"})

	updated := m
	for _, r := range "/clea" {
		u, _ := updated.Update(testKeyText(string(r)))
		updated = u.(model)
	}
	found := false
	for i, s := range updated.suggestions {
		if s.Name == "/clear" {
			updated.suggestionIdx = i
			found = true
		}
	}
	if !found {
		t.Fatalf("/clear should be suggested, got %#v", updated.suggestions)
	}
	u, _ := updated.Update(testKey(tea.KeyEnter))
	next := u.(model)

	if transcriptContains(next.transcript, "SEED-ROW") {
		t.Fatalf("Enter on builtin /clear must still execute it (spec: continua como hoje), got %#v", next.transcript)
	}
}

// --- Spec bullet 3 -----------------------------------------------------------
// "No envio de mensagem comum: TODAS as skills citadas como tokens "/<skill>"
//  (separados por espaço/quebra de linha) carregam, cada uma 1x (1ª ocorrência),
//  na ordem de aparição; prompt = corpo1 + corpo2 + ... + texto sem os tokens;
//  texto vazio → nota de invocação sem args (igual hoje)."

// RED(cb16edce): today a plain message containing skill tokens mid-text is
// sent verbatim (parseCommand sees commandPrompt; no expansion), and a LEADING
// skill token short-circuits to handleSkillCommand which treats the REST of
// the line as opaque args (no second-skill expansion, no dedup, order forced
// to exactly one skill).
func TestAcceptSkill_SubmitExpandsAllSkillsInOrderWithDedup(t *testing.T) {
	m := acceptSkillModel(t,
		skills.Skill{Name: "alpha", Content: "ALPHA-BODY."},
		skills.Skill{Name: "beta", Content: "BETA-BODY."},
	)

	next := acceptTypeEnter(t, m, "please /alpha review and /beta then /alpha again MARKORDER")

	joined := transcriptText(next.transcript)
	// Both bodies inlined, in appearance order, alpha only once.
	ia := strings.Index(joined, "ALPHA-BODY.")
	ib := strings.Index(joined, "BETA-BODY.")
	if ia < 0 || ib < 0 {
		t.Fatalf("both skill bodies must reach the provider prompt, got transcript %#v", next.transcript)
	}
	if ia > ib {
		t.Fatalf("order must follow appearance (/alpha before /beta), got ...%q...", joined)
	}
	if strings.Count(joined, "ALPHA-BODY.") != 1 {
		t.Fatalf("duplicate /alpha must expand once (1st occurrence), got %d copies", strings.Count(joined, "ALPHA-BODY."))
	}
	// The surviving user text keeps its words but not the skill tokens.
	if !strings.Contains(joined, "MARKORDER") {
		t.Fatalf("user text must survive, got %#v", next.transcript)
	}
	if strings.Contains(joined, "/alpha") || strings.Contains(joined, "/beta") {
		t.Fatalf("skill tokens must be stripped from the sent text, got %#v", next.transcript)
	}
}

// RED(cb16edce): tokens split by newline count as tokens too. (A real
// multiline message: typed Enter would submit, so the composer is set
// directly, exactly what a pasted multiline draft looks like at submit.)
func TestAcceptSkill_SkillTokensSeparatedByNewline(t *testing.T) {
	m := acceptSkillModel(t,
		skills.Skill{Name: "alpha", Content: "ALPHA-BODY."},
		skills.Skill{Name: "beta", Content: "BETA-BODY."},
	)
	m.setComposerState(composerState{text: "/alpha\n/beta do it MARKNL", cursor: len([]rune("/alpha\n/beta do it MARKNL"))})
	m.resetComposerFromInput()

	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	joined := transcriptText(next.transcript)
	if !strings.Contains(joined, "ALPHA-BODY.") || !strings.Contains(joined, "BETA-BODY.") {
		t.Fatalf("newline-separated skill tokens must both expand, got %#v", next.transcript)
	}
	if !strings.Contains(joined, "MARKNL") {
		t.Fatalf("user text must survive, got %#v", next.transcript)
	}
}

// RED(cb16edce) for shape, REGRESSION for content: a message that is ONLY
// skill tokens leaves empty user text → the bare-invocation note ("ask for
// them first") rides along, exactly like today's bare /skill. Spec bullet 1
// (decided 2026-11-11): Enter on an exact skill match in the menu ALWAYS
// inserts "/skill " and never sends — sending takes a SECOND Enter with the
// menu closed.
func TestAcceptSkill_OnlySkillTokensCarryBareInvocationNote(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})

	// 1st Enter: the menu is open on the exact match — insert, do NOT send.
	mid := acceptTypeEnter(t, m, "/reviewer")
	if transcriptContains(mid.transcript, "REVIEW-BODY.") {
		t.Fatalf("Enter on an exact skill match must insert, not send (spec bullet 1), got %#v", mid.transcript)
	}
	if got := mid.composerValue(); got != "/reviewer " {
		t.Fatalf("1st Enter must leave \"/reviewer \" in the composer, got %q", got)
	}

	// 2nd Enter: menu closed, line ends in space — sends body + bare note.
	updated, _ := mid.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if !transcriptContains(next.transcript, "REVIEW-BODY.") {
		t.Fatalf("skill body must be sent on the 2nd Enter, got %#v", next.transcript)
	}
	if !transcriptContains(next.transcript, "ask for them first") {
		t.Fatalf("empty text after stripping tokens must carry the bare-invocation note, got %#v", next.transcript)
	}
}

// --- Spec bullet 4 -----------------------------------------------------------
// "Primeiro token com / que não resolve nada: comportamento de hoje (aviso de
//  comando desconhecido, não envia). Token desconhecido em outra posição:
//  literal, sem aviso. "/docs/SKILL.md" não é token."

// REGRESSION LOCK: unknown leading token → "unknown command", nothing sent.
func TestAcceptSkill_UnknownLeadingSlashStillWarns(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})

	next := acceptTypeEnter(t, m, "/definitelynotaskill do things MARKUNK")

	if !transcriptContains(next.transcript, "unknown command") {
		t.Fatalf("unknown leading token must warn, got %#v", next.transcript)
	}
	if !acceptNoLaunch(next, "MARKUNK") {
		t.Fatalf("unknown leading token must not send, got %#v", next.transcript)
	}
}

// RED(cb16edce): unknown token in NON-leading position is literal text, sent
// verbatim, no warning. Today "/reviewer /nosuch go" short-circuits in
// handleSkillCommand and swallows "/nosuch go" as args (it rides along by
// accident but through the command path, and a fully-unknown mid token with a
// KNOWN first token never sees the expansion path the spec defines). The
// observable spec contract: the message is treated as a normal prompt
// containing one skill + literal "/nosuch", with NO unknown-command warning.
func TestAcceptSkill_UnknownMidTokenIsLiteralNoWarning(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})

	next := acceptTypeEnter(t, m, "check /nosuch thing MARKMID")

	if transcriptContains(next.transcript, "unknown command") {
		t.Fatalf("mid-text unknown token must NOT warn, got %#v", next.transcript)
	}
	joined := transcriptText(next.transcript)
	if !strings.Contains(joined, "/nosuch") || !strings.Contains(joined, "MARKMID") {
		t.Fatalf("mid-text unknown token must be sent literally, got %#v", next.transcript)
	}
}

// RED(cb16edce): "/docs/SKILL.md" is not a token (slash inside the word) —
// literal text, no expansion attempt, no warning.
func TestAcceptSkill_PathLikeSlashIsNotAToken(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "docs", Content: "DOCS-BODY."})

	next := acceptTypeEnter(t, m, "read /docs/SKILL.md please MARKPATH")

	if transcriptContains(next.transcript, "unknown command") {
		t.Fatalf("path-like token must not warn, got %#v", next.transcript)
	}
	if transcriptContains(next.transcript, "DOCS-BODY.") {
		t.Fatalf("path-like token must not expand the /docs skill, got %#v", next.transcript)
	}
	if !transcriptContains(next.transcript, "MARKPATH") {
		t.Fatalf("message must still send, got %#v", next.transcript)
	}
}

// --- Spec bullet 5 -----------------------------------------------------------
// "Builtin no 1º token vence ("/clear /reviewer" = /clear). Comando de usuário
//  no 1º token: os args são dele; skills NÃO expandem dentro."

// REGRESSION LOCK: "/clear /reviewer" runs /clear; the skill never expands.
// With the menu open on the trailing skill token, the 1st Enter only inserts
// "/reviewer " (spec bullet 1, 2026-11-11); the 2nd Enter submits.
func TestAcceptSkill_BuiltinFirstTokenWins(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "SEED-ROW"})

	mid := acceptTypeEnter(t, m, "/clear /reviewer")
	if transcriptContains(mid.transcript, "REVIEW-BODY.") {
		t.Fatalf("builtin first token must win; skill must not expand, got %#v", mid.transcript)
	}
	updated, _ := mid.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if transcriptContains(next.transcript, "REVIEW-BODY.") {
		t.Fatalf("builtin first token must win; skill must not expand, got %#v", next.transcript)
	}
	if transcriptContains(next.transcript, "SEED-ROW") {
		t.Fatalf("/clear should have run, got %#v", next.transcript)
	}
}

// RED(cb16edce) partially / LOCK partially: user command first token — its
// args are its own; a skill token INSIDE the args must not expand. Today the
// user-command path expands the template with the raw args (so the literal
// text "/reviewer" is sent as prose — acceptable per spec since the skill must
// NOT load). The RED part for Forja: the skill body must never be inlined, and
// no second-pass skill expansion may touch the expanded prompt.
func TestAcceptSkill_UserCommandArgsDoNotExpandSkills(t *testing.T) {
	root := t.TempDir()
	writeUserCommand(t, root, "greet.md", "Say hello to $1.")
	m := newModel(context.Background(), Options{
		Cwd:        root,
		LoadSkills: func() []skills.Skill { return []skills.Skill{{Name: "reviewer", Content: "REVIEW-BODY."}} },
	})

	// 1st Enter inserts the trailing skill token (menu open, spec bullet 1);
	// 2nd Enter submits. The user-command expansion must see the RAW args,
	// with no skill expansion inside them.
	mid := acceptTypeEnter(t, m, "/greet /reviewer")
	if transcriptContains(mid.transcript, "REVIEW-BODY.") {
		t.Fatalf("skill tokens inside user-command args must NOT expand, got %#v", mid.transcript)
	}
	updated, _ := mid.Update(testKey(tea.KeyEnter))
	next := updated.(model)

	if transcriptContains(next.transcript, "REVIEW-BODY.") {
		t.Fatalf("skill tokens inside user-command args must NOT expand, got %#v", next.transcript)
	}
	if !transcriptContains(next.transcript, "Say hello to /reviewer.") {
		t.Fatalf("user command must receive the raw args, got %#v", next.transcript)
	}
}

// --- Spec bullet 6 -----------------------------------------------------------
// "Skill + @especialista: skills expandem, @especialista expande depois sobre
//  o texto. Skill + anexo: anexos vão junto (como hoje)."

// RED(cb16edce): "/reviewer @explorer fix this" — today handleSkillCommand
// inlines the body with raw args and launchPromptInternal's specialist
// expansion only looks at the START of the final prompt (the body now leads),
// so the @mention expansion silently doesn't fire... or worse, fires on the
// wrong text. Spec: skill expands first, THEN @especialista expands over the
// resulting text (the user text still begins with @explorer).
func TestAcceptSkill_SkillThenSpecialistMention(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})
	m.agentOptions.Specialists = []agent.SpecialistInfo{
		{Name: "explorer", WhenToUse: "explores"},
	}

	next := acceptTypeEnter(t, m, "/reviewer @explorer MARKSPEC map the parser")

	joined := transcriptText(next.transcript)
	if !strings.Contains(joined, "REVIEW-BODY.") {
		t.Fatalf("skill must expand, got %#v", next.transcript)
	}
	if !strings.Contains(joined, "Task tool") || !strings.Contains(joined, "explorer") {
		t.Fatalf("@specialist must expand over the post-skill text (Task delegation directive), got %#v", next.transcript)
	}
	if !strings.Contains(joined, "MARKSPEC") {
		t.Fatalf("user task must survive both expansions, got %#v", next.transcript)
	}
}

// REGRESSION LOCK (attachment half): staging an attachment and submitting a
// skill-carrying message keeps the attachment on the request. Observable via
// the transcript attachment summary chip, same as today's /skill path.
func TestAcceptSkill_SkillMessageKeepsAttachments(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})
	m.pendingImages = []qgruntime.ImageBlock{previewImageBlock(t)}

	next := acceptTypeEnter(t, m, "/reviewer MARKATTACH look at this")

	if !transcriptContains(next.transcript, "REVIEW-BODY.") {
		t.Fatalf("skill must expand, got %#v", next.transcript)
	}
	if len(next.lastImages) != 1 {
		t.Fatalf("attachment must ride the expanded prompt (como hoje), lastImages=%v", next.lastImages)
	}
}

// --- Spec bullet 7 -----------------------------------------------------------
// "Durante um turno: mensagem com skill vai para a FIFO (espera o fim do
//  turno), nunca intercala nem vai no send-now."

// RED(cb16edce): with m.pending, Enter on "text /reviewer" must APPEND the RAW
// message to m.queuedMessages (FIFO) — not interleave, not send-now, and the
// expansion happens when the queue drains, not before. Today a plain prompt
// queues verbatim (good), but a LEADING "/reviewer ..." goes through
// handleSkillCommand which queues the EXPANDED prompt (spec violation: the
// queue must hold the raw text so /retry re-expands from raw; see bullet 8)
// — and Ctrl+Enter would interleave it.
func TestAcceptSkill_SkillMessageQueuesRawDuringTurn(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})
	m.pending = true

	next := acceptTypeEnter(t, m, "/reviewer MARKQUEUE check this")

	if len(next.queuedMessages) != 1 {
		t.Fatalf("skill message during a turn must queue exactly once, got %v", next.queuedMessages)
	}
	if !strings.Contains(next.queuedMessages[0], "/reviewer") {
		t.Fatalf("queue must hold the RAW text (re-expanded on drain), got %q", next.queuedMessages[0])
	}
	if strings.Contains(next.queuedMessages[0], "REVIEW-BODY.") {
		t.Fatalf("queue must not hold the pre-expanded body, got %q", next.queuedMessages[0])
	}
	if next.interleave != nil && len(next.interleave.snapshot()) > 0 {
		t.Fatalf("skill message must never interleave, got %v", next.interleave.snapshot())
	}
}

// RED(cb16edce): Ctrl+Enter (send-now gesture) on a skill message must NOT
// interleave; it takes the FIFO path like plain Enter.
func TestAcceptSkill_CtrlEnterOnSkillMessageDoesNotInterleave(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})
	m.pending = true
	m.input.SetValue("/reviewer MARKCTRL go")

	updated, _ := m.handleInterleaveEnter(testKey(tea.KeyEnter))
	next := updated.(model)

	if next.interleave != nil && len(next.interleave.snapshot()) > 0 {
		t.Fatalf("skill message must not interleave via Ctrl+Enter, got %v", next.interleave.snapshot())
	}
	if len(next.queuedMessages) != 1 || !strings.Contains(next.queuedMessages[0], "/reviewer") {
		t.Fatalf("skill message must land raw on the FIFO, got %v", next.queuedMessages)
	}
}

// --- Spec bullet 8 -----------------------------------------------------------
// "/retry re-expande do texto cru; ↑ devolve o texto cru (nunca o corpo da
//  skill)."

// RED(cb16edce): after submitting "/reviewer MARKRETRY", ↑ must recall the RAW
// typed text. Today handleSkillCommand→launchOrDeferExpandedPrompt queues/
// launches the expanded prompt and launchPromptInternal stores THAT in
// lastPrompt; rememberInput stores the raw composer line, so ↑ works today
// only on the plain path — the skill path never calls rememberInput with the
// raw line before clearComposer (handleSubmit does rememberInput(input)
// BEFORE dispatch, so ↑ recall of raw text actually holds). The RED half is
// /retry: lastPrompt is the expanded body, so retry re-sends the body, and if
// the skill file changed between attempts the retry silently runs the OLD
// body.
func TestAcceptSkill_RetryReexpandsFromRawText(t *testing.T) {
	body := "REVIEW-BODY-V1."
	installed := []skills.Skill{{Name: "reviewer", Content: body}}
	m := acceptSkillModel(t, installed...)

	next := acceptTypeEnter(t, m, "/reviewer MARKRETRY")
	if !transcriptContains(next.transcript, "REVIEW-BODY-V1.") {
		t.Fatalf("setup: skill should have run, got %#v", next.transcript)
	}

	// The skill changes on disk (loader returns the new body); /retry must
	// re-expand from the raw typed text and pick up V2.
	installed[0].Content = "REVIEW-BODY-V2."
	next.loadSkills = func() []skills.Skill { return installed }

	updated, _ := next.dispatchCommand(parseCommand("/retry"))
	retried := updated.(model)

	joined := transcriptText(retried.transcript)
	if !strings.Contains(joined, "REVIEW-BODY-V2.") {
		t.Fatalf("/retry must re-expand from the raw text (V2 body), got %#v", retried.transcript)
	}
	if strings.Contains(joined, "MARKRETRY") && strings.Contains(joined, "V1") && !strings.Contains(joined, "V2") {
		t.Fatalf("/retry resent the stale expanded prompt, got %#v", retried.transcript)
	}
}

// RED(cb16edce): ↑ recalls the raw line, not the expanded body. (On the plain
// path rememberInput already stores raw; this test locks the SPEC invariant at
// the acceptance level — Forja must keep it true once the expansion pipeline
// moves.)
func TestAcceptSkill_ArrowUpRecallsRawSkillText(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})

	next := acceptTypeEnter(t, m, "/reviewer MARKUP check this")
	if !transcriptContains(next.transcript, "REVIEW-BODY.") {
		t.Fatalf("setup: skill should have run, got %#v", next.transcript)
	}

	recalled := next.recallHistory(-1)
	got := recalled.composerValue()
	if !strings.Contains(got, "/reviewer") || !strings.Contains(got, "MARKUP") {
		t.Fatalf("↑ must recall the raw typed text, got %q", got)
	}
	if strings.Contains(got, "REVIEW-BODY.") {
		t.Fatalf("↑ must never recall the expanded skill body, got %q", got)
	}
}

// --- Spec bullet 9 (highlight) ----------------------------------------------
// "Tokens de skill conhecidos destacados no composer (cor de comando), sem
//  tocar cursor/seleção."

// RED(cb16edce): the composer render applies the command color to known skill
// tokens. Observable: the rendered composer line for "/reviewer hi" contains
// the command style's ANSI sequence around "/reviewer" and plain "hi", and
// the cursor cell is unchanged. There is no such styling today, so this fails
// at the first assertion.
func TestAcceptSkill_KnownSkillTokensHighlightedInComposer(t *testing.T) {
	m := acceptSkillModel(t, skills.Skill{Name: "reviewer", Content: "REVIEW-BODY."})
	m.input.SetValue("/reviewer MARKHI there")
	m.input.CursorEnd()
	m.resetComposerFromInput()

	rendered := stripToANSIContainingLines(m, "/reviewer")
	if len(rendered) == 0 {
		t.Fatalf("composer render must style the known skill token with the command color, none found")
	}
	// Cursor position is untouched by highlighting.
	if got := m.input.Position(); got != len([]rune("/reviewer MARKHI there")) {
		t.Fatalf("highlighting must not move the cursor, got %d", got)
	}
}

// --- small utilities the acceptance tests need -------------------------------

// stripToANSIContainingLines renders the composer area and returns the lines
// that contain BOTH an ANSI escape and the token — i.e. styled.
func stripToANSIContainingLines(m model, token string) []string {
	view := m.View().Content
	var out []string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, token) && strings.Contains(line, "\x1b[") {
			out = append(out, line)
		}
	}
	return out
}
