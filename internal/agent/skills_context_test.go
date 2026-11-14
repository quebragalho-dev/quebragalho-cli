package agent

import (
	"strconv"
	"strings"
	"testing"
)

func TestSkillsContextListsEveryName(t *testing.T) {
	// 60 skills with realistic trigger-rich descriptions (~180 chars each, under
	// the 200-rune truncation) total well past the 4096-byte list budget — every
	// NAME must still appear (names are the discovery surface), while
	// descriptions stop once the budget is spent. No skill may be summarized
	// away into a count: an unnamed skill never triggers.
	longDesc := strings.Repeat("use when the request touches deployments, release notes, or verification; ", 2) + "number "
	skills := make([]SkillInfo, 0, 60)
	for i := 0; i < 60; i++ {
		n := strconv.Itoa(i)
		skills = append(skills, SkillInfo{Name: "skill-" + n, Description: longDesc + n})
	}
	got := skillsContext(skills, nil)
	if len(got) > skillsContextListBudget+2000 {
		t.Fatalf("skills block should stay bounded near the %d-byte budget, got %d bytes:\n%s", skillsContextListBudget, len(got), got)
	}
	if strings.Contains(got, "more (call skill") {
		t.Fatalf("no skill may be hidden behind an overflow count, got:\n%s", got)
	}
	for i := 0; i < 60; i++ {
		if !strings.Contains(got, "- skill-"+strconv.Itoa(i)) {
			t.Fatalf("skill-%d missing from the list:\n%s", i, got)
		}
	}
}

// A realistic mid-size skill set (20 skills, trigger-rich descriptions) must be
// listed in FULL — no overflow summary. This pins the fix for the old 640-byte
// budget, under which skills past ~#6 were invisible to the model and therefore
// never triggered.
func TestSkillsContextListsRealisticSetInFull(t *testing.T) {
	desc := "Use when the user asks about deployments, release notes, or pre-merge verification runs."
	skills := make([]SkillInfo, 0, 20)
	for i := 0; i < 20; i++ {
		skills = append(skills, SkillInfo{Name: "skill-" + strconv.Itoa(i), Description: desc})
	}
	got := skillsContext(skills, nil)
	if strings.Contains(got, "more (call skill") {
		t.Fatalf("20 described skills must all be listed without overflow, got:\n%s", got)
	}
	for i := 0; i < 20; i++ {
		if !strings.Contains(got, "- skill-"+strconv.Itoa(i)+":") {
			t.Fatalf("skill-%d missing from the list:\n%s", i, got)
		}
	}
}

func TestSkillsContext(t *testing.T) {
	if got := skillsContext(nil, nil); got != "" {
		t.Fatalf("no skills should yield an empty section, got %q", got)
	}
	got := skillsContext([]SkillInfo{
		{Name: "commit-writer", Description: "Write a conventional-commit message."},
		{Name: "  ", Description: "nameless, should be skipped"},
		{Name: "reviewer"},
	}, nil)
	if !strings.Contains(got, "<available_skills>") || !strings.Contains(got, "</available_skills>") {
		t.Fatalf("missing available_skills block: %q", got)
	}
	if !strings.Contains(got, "- commit-writer: Write a conventional-commit message.") {
		t.Fatalf("missing commit-writer line: %q", got)
	}
	if !strings.Contains(got, "- reviewer\n") {
		t.Fatalf("reviewer (no description) line missing: %q", got)
	}
	if strings.Contains(got, "nameless") {
		t.Fatalf("nameless entry should be skipped: %q", got)
	}
}

func TestSystemPromptIncludesSkillsOnlyWhenInstalled(t *testing.T) {
	with := buildSystemPrompt(Options{Skills: []SkillInfo{
		{Name: "commit-writer", Description: "Write a commit message."},
	}})
	if !strings.Contains(with, "<available_skills>") || !strings.Contains(with, "skill tool") {
		t.Fatalf("expected available_skills guidance in system prompt: %q", with)
	}
	// Default (no skills) must reproduce the prior prompt: no skills block.
	without := buildSystemPrompt(Options{})
	if strings.Contains(without, "<available_skills>") {
		t.Fatalf("available_skills block must not appear without skills")
	}
}
