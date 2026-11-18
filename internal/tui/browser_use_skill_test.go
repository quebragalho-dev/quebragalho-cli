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

func TestBrowserUseBuiltinSkillAppearsAndRunsWithoutUserFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	m := newModel(context.Background(), Options{
		Cwd: t.TempDir(),
		LoadSkills: func() []skills.Skill {
			return skills.MergeBuiltins(nil)
		},
	})

	// Autocomplete surfaces /browser-use without any user skill file.
	suggestions := m.matchSkillSuggestions("/brow")
	var found bool
	for _, s := range suggestions {
		if s.Name == "/browser-use" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("matchSkillSuggestions(/brow) = %#v, want /browser-use", suggestions)
	}

	// /browser-use runs and expands the built-in skill instructions.
	next, _, handled := m.handleSkillCommand("/browser-use abrir example.com")
	if !handled {
		t.Fatal("/browser-use should be handled as a built-in skill")
	}
	if !transcriptContains(next.transcript, "browser_open") || !transcriptContains(next.transcript, "browser_snapshot") {
		t.Fatalf("expected browser-use skill body in transcript, got %#v", next.transcript)
	}
}

func TestBrowserUseSkillWarnsWhenBrowserDisabled(t *testing.T) {
	var disabled config.LocalControlConfig
	if err := json.Unmarshal([]byte(`{"browser":{"enabled":false}}`), &disabled); err != nil {
		t.Fatal(err)
	}
	m := newModel(context.Background(), Options{
		Cwd:          t.TempDir(),
		LocalControl: disabled,
		LoadSkills: func() []skills.Skill {
			return skills.MergeBuiltins(nil)
		},
	})

	next, cmd, handled := m.handleSkillCommand("/browser-use abrir example.com")
	if !handled {
		t.Fatal("/browser-use should be handled when browser is disabled")
	}
	if cmd != nil {
		t.Fatal("disabled /browser-use must not launch an agent turn")
	}
	if next.pending {
		t.Fatal("disabled /browser-use must not set pending")
	}
	if !transcriptContains(next.transcript, "Controle do navegador está desligado") {
		t.Fatalf("expected disabled guidance notice, got %#v", next.transcript)
	}
	if !transcriptContains(next.transcript, "localControl.browser.enabled=true") {
		t.Fatalf("expected config hint in disabled notice, got %#v", next.transcript)
	}
	if transcriptContains(next.transcript, "browser_open") {
		t.Fatalf("disabled /browser-use should not expand skill prompt, got %#v", next.transcript)
	}
}

func TestToolSearchStillDiscoversBrowserToolsWithoutSkill(t *testing.T) {
	reg := tools.NewRegistry()
	for _, tool := range tools.NewLocalBrowserTools(localcontrol.BrowserOptions{Enabled: true}) {
		reg.Register(tool)
	}
	searchTool := tools.NewToolSearchTool(reg)

	res := searchTool.Run(context.Background(), map[string]any{"query": "browser"})
	if res.Status != tools.StatusOK {
		t.Fatalf("tool_search status = %s, want ok (%s)", res.Status, res.Output)
	}
	for _, want := range []string{"browser_open", "browser_snapshot", "browser_action"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("tool_search should discover %s without /browser-use skill, got: %s", want, res.Output)
		}
	}
}
