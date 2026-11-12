package agent

import (
	"strconv"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

func TestDetectOrchestratorsDeclared(t *testing.T) {
	skills := []SkillInfo{{Name: "foo", Environments: []string{"FOO_SOCKET"}}}
	got := DetectOrchestrators(skills, map[string]string{"FOO_SOCKET": "sock-1"})
	if len(got) != 1 || got[0].Name != "foo" || got[0].Signal != "env:FOO_SOCKET" {
		t.Fatalf("declared match expected, got %+v", got)
	}
	if got := DetectOrchestrators(skills, map[string]string{}); len(got) != 0 {
		t.Fatalf("absent env must not match, got %+v", got)
	}
}

func TestDetectOrchestratorsHeuristicSuffixes(t *testing.T) {
	suffixes := []string{"SOCKET", "SESSION", "SESSION_ID", "TERMINAL_ID", "WORKSPACE", "WORKSPACE_ID", "CLI"}
	for _, suffix := range suffixes {
		key := "FOO_" + suffix
		got := DetectOrchestrators([]SkillInfo{{Name: "foo"}}, map[string]string{key: "v"})
		if len(got) != 1 || got[0].Name != "foo" || got[0].Signal != "env:"+key {
			t.Fatalf("suffix %s: expected match, got %+v", suffix, got)
		}
	}
}

func TestDetectOrchestratorsIgnoresBarePrefix(t *testing.T) {
	cases := []struct {
		skill string
		env   map[string]string
	}{
		{"git", map[string]string{"GIT_EDITOR": "vim"}},
		{"go", map[string]string{"GOPATH": "/tmp/gopath"}},
		{"mytool", map[string]string{"MYTOOL_CONFIG": "x"}},
	}
	for _, c := range cases {
		if got := DetectOrchestrators([]SkillInfo{{Name: c.skill}}, c.env); len(got) != 0 {
			t.Fatalf("skill %s: bare prefix must not match, got %+v", c.skill, got)
		}
	}
}

func TestDetectOrchestratorsTermProgram(t *testing.T) {
	skills := []SkillInfo{{Name: "maestri"}}
	got := DetectOrchestrators(skills, map[string]string{"TERM_PROGRAM": "Maestri"})
	if len(got) != 1 || got[0].Name != "maestri" {
		t.Fatalf("TERM_PROGRAM match expected, got %+v", got)
	}
	if got := DetectOrchestrators(skills, map[string]string{"TERM_PROGRAM": "xterm-256color"}); len(got) != 0 {
		t.Fatalf("unrelated TERM_PROGRAM must not match, got %+v", got)
	}
}

func TestDetectOrchestratorsRequiresSkill(t *testing.T) {
	got := DetectOrchestrators([]SkillInfo{{Name: "other"}}, map[string]string{"FOO_SOCKET": "v"})
	if len(got) != 0 {
		t.Fatalf("env without matching skill must not match, got %+v", got)
	}
}

func TestDetectOrchestratorsMultipleStableOrder(t *testing.T) {
	skills := []SkillInfo{{Name: "bar"}, {Name: "foo"}}
	env := map[string]string{"FOO_SOCKET": "a", "BAR_CLI": "b"}
	first := DetectOrchestrators(skills, env)
	second := DetectOrchestrators(skills, env)
	if len(first) != 2 || first[0].Name != "bar" || first[1].Name != "foo" {
		t.Fatalf("both matches in skill order expected, got %+v", first)
	}
	if len(second) != 2 || second[0].Name != first[0].Name || second[1].Name != first[1].Name {
		t.Fatalf("detection must be stable, got %+v vs %+v", second, first)
	}
}

func TestOrchestratorBlockOmitsValues(t *testing.T) {
	detected := []DetectedOrchestrator{{Name: "foo", Signal: "env:FOO_SOCKET"}}
	block := OrchestratorsContext(detected)
	if !strings.Contains(block, "foo") || !strings.Contains(block, "env:FOO_SOCKET") {
		t.Fatalf("block must name skill and signal, got:\n%s", block)
	}
	for _, secret := range []string{"sock-1", "secret-token"} {
		if strings.Contains(block, secret) {
			t.Fatalf("block must never carry env values, got:\n%s", block)
		}
	}
	if got := OrchestratorsContext(nil); got != "" {
		t.Fatalf("no detection must render nothing, got %q", got)
	}
}

func TestSystemPromptPinsOrchestratorOutsideBudget(t *testing.T) {
	filler := make([]SkillInfo, 0, 200)
	for i := 0; i < 200; i++ {
		filler = append(filler, SkillInfo{Name: "skill-" + strconv.Itoa(i), Description: strings.Repeat("d", 180)})
	}
	skills := append([]SkillInfo{{Name: "foo", Description: "Use when coordinating agents in foo."}}, filler...)
	prompt := buildSystemPrompt(Options{
		Skills:        skills,
		Orchestrators: []DetectedOrchestrator{{Name: "foo", Signal: "env:FOO_SOCKET"}},
	})
	if !strings.Contains(prompt, "<environment_orchestrator>") {
		t.Fatalf("orchestrator block missing from prompt")
	}
	if !strings.Contains(prompt, "env:FOO_SOCKET") {
		t.Fatalf("detection signal missing from prompt")
	}
	if !strings.Contains(prompt, "- foo (orchestrator") || !strings.Contains(prompt, "Use when coordinating agents in foo.") {
		t.Fatalf("pinned foo line with full description missing")
	}
	if strings.Contains(prompt, "sock-") {
		t.Fatalf("prompt must not leak env values")
	}
}

func TestSystemPromptListsEverySkillName(t *testing.T) {
	skills := make([]SkillInfo, 0, 254)
	for i := 0; i < 254; i++ {
		skills = append(skills, SkillInfo{Name: "skill-" + strconv.Itoa(i), Description: "Use when testing skill listings."})
	}
	first := buildSystemPrompt(Options{Skills: skills})
	second := buildSystemPrompt(Options{Skills: skills})
	if first != second {
		t.Fatalf("prompt assembly must be byte-stable between builds")
	}
	for i := 0; i < 254; i++ {
		if !strings.Contains(first, "- skill-"+strconv.Itoa(i)) {
			t.Fatalf("skill-%d missing from the list", i)
		}
	}
}

func TestOrchestratorBlockSurvivesCompaction(t *testing.T) {
	prompt := buildSystemPrompt(Options{
		Skills:        []SkillInfo{{Name: "foo"}},
		Orchestrators: []DetectedOrchestrator{{Name: "foo", Signal: "env:FOO_SOCKET"}},
	})
	system := ""
	for _, section := range buildSystemPromptParts(Options{
		Skills:        []SkillInfo{{Name: "foo"}},
		Orchestrators: []DetectedOrchestrator{{Name: "foo", Signal: "env:FOO_SOCKET"}},
	}).sections {
		if strings.Contains(section, "<environment_orchestrator>") {
			system = section
		}
	}
	_ = prompt
	if system == "" {
		t.Fatalf("orchestrator section missing from prompt parts")
	}
	messages := []qgruntime.Message{
		{Role: qgruntime.MessageRoleSystem, Content: system},
	}
	for i := 0; i < 10; i++ {
		messages = append(messages,
			qgruntime.Message{Role: qgruntime.MessageRoleUser, Content: "turn " + strconv.Itoa(i)},
			qgruntime.Message{Role: qgruntime.MessageRoleAssistant, Content: "ack " + strconv.Itoa(i)},
		)
	}
	result, err := CompactMessages(messages, CompactionOptions{
		PreserveLast: 2,
		Summarize: func([]qgruntime.Message) (string, error) {
			return "summary of earlier turns", nil
		},
	})
	if err != nil {
		t.Fatalf("CompactMessages failed: %v", err)
	}
	found := false
	for _, m := range result.Messages {
		if strings.Contains(m.Content, "<environment_orchestrator>") {
			found = true
		}
	}
	if !found {
		t.Fatalf("orchestrator block must survive compaction")
	}
}
