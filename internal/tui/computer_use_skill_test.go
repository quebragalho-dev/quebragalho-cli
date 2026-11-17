package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/localcontrol"
	"github.com/quebragalho-dev/quebragalho-cli/internal/skills"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

func TestComputerUseBuiltinSkillAppearsAndRunsWithoutUserFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m := newModel(context.Background(), Options{
		Cwd: t.TempDir(),
		LoadSkills: func() []skills.Skill {
			return skills.MergeBuiltins(nil)
		},
	})

	// Autocomplete surfaces /computer-use without any user skill file.
	suggestions := m.matchSkillSuggestions("/comp")
	var found bool
	for _, s := range suggestions {
		if s.Name == "/computer-use" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("matchSkillSuggestions(/comp) = %#v, want /computer-use", suggestions)
	}

	// /computer-use runs and expands the built-in skill instructions.
	next, _, handled := m.handleSkillCommand("/computer-use abrir a calculadora")
	if !handled {
		t.Fatal("/computer-use should be handled as a built-in skill")
	}
	if !transcriptContains(next.transcript, "desktop_snapshot") || !transcriptContains(next.transcript, "desktop_action") {
		t.Fatalf("expected computer-use skill body in transcript, got %#v", next.transcript)
	}
}

func TestComputerUseSkillWarnsWhenDesktopDisabled(t *testing.T) {
	var disabled config.LocalControlConfig
	if err := json.Unmarshal([]byte(`{"desktop":{"enabled":false}}`), &disabled); err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Cwd:          t.TempDir(),
		LocalControl: disabled,
		LoadSkills: func() []skills.Skill {
			return skills.MergeBuiltins(nil)
		},
	})

	next, cmd, handled := m.handleSkillCommand("/computer-use abrir a calculadora")
	if !handled {
		t.Fatal("/computer-use should be handled when desktop is disabled")
	}
	if cmd != nil {
		t.Fatal("disabled /computer-use must not launch an agent turn")
	}
	if next.pending {
		t.Fatal("disabled /computer-use must not set pending")
	}
	if !transcriptContains(next.transcript, "Controle do computador está desligado") {
		t.Fatalf("expected disabled guidance notice, got %#v", next.transcript)
	}
	if !transcriptContains(next.transcript, "localControl.desktop.enabled=true") {
		t.Fatalf("expected config hint in disabled notice, got %#v", next.transcript)
	}
	if transcriptContains(next.transcript, "ler → agir → ler de novo") {
		t.Fatalf("disabled /computer-use should not expand skill prompt, got %#v", next.transcript)
	}
}

func TestToolSearchStillDiscoversDesktopToolsWithoutSkill(t *testing.T) {
	reg := tools.NewRegistry()
	for _, tool := range tools.NewLocalDesktopTools(localcontrol.DesktopOptions{Enabled: true}) {
		reg.Register(tool)
	}
	searchTool := tools.NewToolSearchTool(reg)

	res := searchTool.Run(context.Background(), map[string]any{"query": "desktop"})
	if res.Status != tools.StatusOK {
		t.Fatalf("tool_search status = %s, want ok (%s)", res.Status, res.Output)
	}
	if !strings.Contains(res.Output, "desktop_snapshot") || !strings.Contains(res.Output, "desktop_action") {
		t.Fatalf("tool_search should discover desktop tools without /computer-use skill, got: %s", res.Output)
	}
}
