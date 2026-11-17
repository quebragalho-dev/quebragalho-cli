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

func TestMergeBuiltinsUserSkillShadowsBuiltin(t *testing.T) {
	merged := MergeBuiltins(nil)
	if len(merged) == 0 || merged[0].Name != "computer-use" {
		t.Fatalf("MergeBuiltins(nil) = %#v, want computer-use", merged)
	}

	custom := []Skill{{Name: "computer-use", Description: "custom", Content: "custom body", Path: "/tmp/SKILL.md"}}
	merged = MergeBuiltins(custom)
	if len(merged) != 1 || merged[0].Content != "custom body" {
		t.Fatalf("user skill should shadow built-in, got %#v", merged)
	}
}
