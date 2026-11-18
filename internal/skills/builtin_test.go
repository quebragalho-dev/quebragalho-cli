package skills

import (
	"strings"
	"testing"
)

func TestBuiltinSkillsIncludesComputerUse(t *testing.T) {
	builtins := BuiltinSkills()
	var found *Skill
	for i := range builtins {
		if builtins[i].Name == "computer-use" {
			found = &builtins[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("BuiltinSkills() missing computer-use: %#v", builtins)
	}
	if strings.TrimSpace(found.Description) == "" {
		t.Fatal("computer-use Description is empty")
	}
	for _, want := range []string{
		"desktop_snapshot",
		"desktop_action",
		"ler → agir → ler de novo",
		"@<snapshot_id>:eN",
		`error.disposition.retry == "safe"`,
		"headed=true",
		"screenshot",
		`command="skills"`,
		`args=["get","desktop"]`,
		"localControl.desktop.enabled=true",
	} {
		if !strings.Contains(found.Content, want) {
			t.Errorf("computer-use content missing %q:\n%s", want, found.Content)
		}
	}
}

func TestBuiltinSkillsIncludesBrowserUse(t *testing.T) {
	builtins := BuiltinSkills()
	var found *Skill
	for i := range builtins {
		if builtins[i].Name == "browser-use" {
			found = &builtins[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("BuiltinSkills() missing browser-use: %#v", builtins)
	}
	if strings.TrimSpace(found.Description) == "" {
		t.Fatal("browser-use Description is empty")
	}
	for _, want := range []string{
		"browser_open",
		"browser_snapshot",
		"browser_click",
		"browser_type",
		"browser_press",
		"browser_action",
		"browser_install",
		"capture_artifact",
		"browser_screenshot",
		"headed",
		`command="skills"`,
		`args=["get","core"]`,
		"localControl.browser.enabled=true",
	} {
		if !strings.Contains(found.Content, want) {
			t.Errorf("browser-use content missing %q:\n%s", want, found.Content)
		}
	}
}

func TestMergeBuiltinsUserSkillShadowsBuiltin(t *testing.T) {
	merged := MergeBuiltins(nil)
	var hasBrowser, hasComputer bool
	for _, skill := range merged {
		switch skill.Name {
		case "browser-use":
			hasBrowser = true
		case "computer-use":
			hasComputer = true
		}
	}
	if !hasBrowser || !hasComputer {
		t.Fatalf("MergeBuiltins(nil) = %#v, want browser-use and computer-use", merged)
	}

	custom := []Skill{{Name: "computer-use", Description: "custom", Content: "custom body", Path: "/tmp/SKILL.md"}}
	merged = MergeBuiltins(custom)
	var shadowed *Skill
	for i := range merged {
		if merged[i].Name == "computer-use" {
			shadowed = &merged[i]
			break
		}
	}
	if shadowed == nil || shadowed.Content != "custom body" {
		t.Fatalf("user skill should shadow built-in, got %#v", merged)
	}
}
