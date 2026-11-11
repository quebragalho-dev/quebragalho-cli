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

// expandSkillTokens builds the wire prompt: the skill bodies (in order of
// first appearance, each once), then the remaining text with every resolved
// skill token removed. Empty remainder carries bareSkillInvocationNote.
func expandSkillTokens(text string, installed []skills.Skill) (string, bool) {
	spans := scanSkillTokens(text, installed)
	if len(spans) == 0 {
		return text, false
	}
	seen := map[string]bool{}
	var bodies []string
	for _, span := range spans {
		if seen[span.name] {
			continue
		}
		seen[span.name] = true
		for _, skill := range installed {
			if skillSlashName(skill.Name) == span.name {
				bodies = append(bodies, strings.TrimSpace(skill.Content))
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
	rest := strings.TrimSpace(string(kept))
	body := strings.TrimSpace(strings.Join(bodies, "\n\n"))
	if body == "" {
		// An installed skill with an empty body still expands: the user sees the
		// bare-invocation note, never a silent no-op.
		if rest == "" {
			return bareSkillInvocationNote, true
		}
		return rest + "\n\n" + bareSkillInvocationNote, true
	}
	if rest == "" {
		return body + "\n\n" + bareSkillInvocationNote, true
	}
	return body + "\n\n" + rest, true
}
