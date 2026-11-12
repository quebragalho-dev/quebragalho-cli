package tui

import (
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/skills"
)

// Skill invocation inside free text: tokens delimited by whitespace or
// newlines, exactly like the slash palette sees them. This file is the one
// place that decides what a "/token" means for the composer: it expands
// known skills, leaves unknown tokens alone (literal, spec bullet 4), and
// never expands tokens whose leading slash is inside the word —
// "/docs/SKILL.md" is one long token, never a skill reference.

// skillTokenSpan is one whitespace-delimited token that resolves to an
// installed skill. All matched tokens are recorded, so duplicates can be
// stripped even though only the first occurrence expands.
type skillTokenSpan struct {
	name  string // slash name, lowercased, no leading slash
	start int    // rune index, inclusive
	end   int    // rune index, exclusive
}

// scanSkillTokens returns every resolved skill token (matched against the
// installed catalogue), in order of appearance. Duplicates are reported too
// (the caller removes them from the text); dedup for bodies happens in
// expandSkillTokens.
func scanSkillTokens(text string, installed []skills.Skill) []skillTokenSpan {
	out := []skillTokenSpan{}
	runes := []rune(text)
	i := 0
	for i < len(runes) {
		if runes[i] != '/' || (i > 0 && !isTokenBoundary(runes[i-1])) {
			i++
			continue
		}
		j := i + 1
		for j < len(runes) && !isTokenBoundary(runes[j]) {
			j++
		}
		name := strings.ToLower(string(runes[i+1 : j]))
		if name != "" && skillSlashName(name) == name && skillRegistered(installed, name) {
			out = append(out, skillTokenSpan{name: name, start: i, end: j})
		}
		i = j
	}
	return out
}

// skillExactToken reports whether token is a fully-typed "/name" that resolves
// to a known skill with no args. The palette stays closed in that case: the
// token is already complete, and Enter executes it (bare invocation), the
// same way a complete builtin does — the user never picks a row.
func skillExactToken(installed []skills.Skill, token string) bool {
	name, ok := strings.CutPrefix(strings.ToLower(token), "/")
	if !ok {
		return false
	}
	name = strings.TrimSpace(name)
	return name != "" && !strings.ContainsAny(name, " \t\n") && skillRegistered(installed, name)
}

func skillRegistered(installed []skills.Skill, name string) bool {
	for _, skill := range installed {
		if skillSlashName(skill.Name) == name {
			return true
		}
	}
	return false
}

func isTokenBoundary(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// containsSkillToken reports whether the message cites at least one known
// skill. Plain prompts with no skill tokens are unaffected.
func containsSkillToken(text string, installed []skills.Skill) bool {
	return len(scanSkillTokens(text, installed)) > 0
}

// expandSkillTokensParts splits text into the resolved skill bodies and the
// remaining user text with every resolved skill token stripped. ok is false
// when no known skill was cited. Duplicates expand once (first occurrence);
// duplicates are stripped from rest either way.
func expandSkillTokensParts(text string, installed []skills.Skill) (bodies string, rest string, ok bool) {
	spans := scanSkillTokens(text, installed)
	if len(spans) == 0 {
		return "", text, false
	}
	seen := map[string]bool{}
	var bodyParts []string
	for _, span := range spans {
		if seen[span.name] {
			continue
		}
		seen[span.name] = true
		for _, skill := range installed {
			if skillSlashName(skill.Name) == span.name {
				bodyParts = append(bodyParts, strings.TrimSpace(skill.Content))
				break
			}
		}
	}
	runes := []rune(text)
	var kept []rune
	cursor := 0
	for _, span := range spans {
		kept = append(kept, runes[cursor:span.start]...)
		cursor = span.end
	}
	kept = append(kept, runes[cursor:]...)
	body := strings.TrimSpace(strings.Join(bodyParts, "\n\n"))
	return body, strings.TrimSpace(string(kept)), true
}

// composeSkillPrompt joins the expanded bodies and the remaining text.
// An empty rest (text was only skill tokens) carries the bare-invocation note.
func composeSkillPrompt(bodies, rest string) string {
	switch {
	case bodies == "" && rest == "":
		// Every cited skill had an empty body and there is no user text:
		// the bare-invocation note is all that is left to say.
		return bareSkillInvocationNote
	case bodies == "":
		return rest + "\n\n" + bareSkillInvocationNote
	case rest == "":
		// Text was only skill tokens: the instructions alone have no target,
		// so the note asks for one.
		return bodies + "\n\n" + bareSkillInvocationNote
	default:
		return bodies + "\n\n" + rest
	}
}
