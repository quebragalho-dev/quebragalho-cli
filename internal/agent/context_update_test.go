package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// C1: changing only the repo map must not resend env or AGENTS.md,
// and an announced change must not repeat on the following turn.
func TestC1RepoMapChangeAnnouncedOnceWithoutEnv(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Always run make lint."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	base := buildSystemPromptParts(Options{Cwd: dir})
	frozen := []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: base.prompt}}

	// Turn 2: repo map changes (new file).
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed := buildSystemPromptParts(Options{Cwd: dir})

	note := contextUpdateNote(changed.sections, frozen)
	if note == "" {
		t.Fatal("expected a context-update note after repo map change")
	}
	if strings.Contains(note, "Always run make lint.") {
		t.Fatalf("repo-only change must not resend AGENTS.md, got:\n%s", note)
	}
	if strings.Contains(note, "<environment>") {
		t.Fatalf("repo-only change must not resend environment block, got:\n%s", note)
	}
	if !strings.Contains(note, "## Repo map") {
		t.Fatalf("repo change must announce the repo map, got:\n%s", note)
	}

	// Turn 3: nothing else changes. The prior note is now in history,
	// so the same sections must not be announced again.
	history := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: base.prompt},
		{Role: qgruntime.MessageRoleUser, Content: note + "\n\nprompt2"},
	}
	again := buildSystemPromptParts(Options{Cwd: dir})
	if note2 := contextUpdateNote(again.sections, history); note2 != "" {
		t.Fatalf("announced change must not repeat, got:\n%s", note2)
	}
}

func TestC1DifferentChangesAnnouncedOnceEach(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := buildSystemPromptParts(Options{Cwd: dir})
	frozen := base.prompt

	// Turn 2: repo-only change.
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	turn2 := buildSystemPromptParts(Options{Cwd: dir})
	note2 := contextUpdateNote(turn2.sections, []qgruntime.Message{{Role: qgruntime.MessageRoleSystem, Content: frozen}})
	if note2 == "" || !strings.Contains(note2, "## Repo map") {
		t.Fatalf("turn 2 must announce repo map, got:\n%s", note2)
	}

	// Turn 3: guidelines change (repo stays as in turn 2).
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("v2 changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	turn3 := buildSystemPromptParts(Options{Cwd: dir})
	history := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: frozen},
		{Role: qgruntime.MessageRoleUser, Content: note2 + "\n\nprompt2"},
	}
	note3 := contextUpdateNote(turn3.sections, history)
	if note3 == "" {
		t.Fatal("expected a note for the guidelines change")
	}
	if strings.Contains(note3, "## Repo map") {
		t.Fatalf("repo already announced must not repeat, got:\n%s", note3)
	}
	if !strings.Contains(note3, "v2 changed") {
		t.Fatalf("turn 3 must announce new guidelines, got:\n%s", note3)
	}

	// Turn 4: nothing changes; both prior notes are in history.
	history = append(history,
		qgruntime.Message{Role: qgruntime.MessageRoleAssistant, Content: "ok"},
		qgruntime.Message{Role: qgruntime.MessageRoleUser, Content: note3 + "\n\nprompt3"},
	)
	turn4 := buildSystemPromptParts(Options{Cwd: dir})
	if note4 := contextUpdateNote(turn4.sections, history); note4 != "" {
		t.Fatalf("no change must yield no note, got:\n%s", note4)
	}
}
