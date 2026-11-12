package agent

import (
	"strconv"
	"strings"
	"testing"
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
	t.Setenv("FOO_SOCKET", "sentinel-value-omit-7q9z")
	detected := DetectOrchestrators([]SkillInfo{{Name: "foo", Environments: []string{"FOO_SOCKET"}}}, nil)
	if len(detected) != 1 {
		t.Fatalf("detection through the process environment failed, got %+v", detected)
	}
	block := OrchestratorsContext(detected)
	if !strings.Contains(block, "foo") || !strings.Contains(block, "env:FOO_SOCKET") {
		t.Fatalf("block must name skill and signal, got:\n%s", block)
	}
	if strings.Contains(block, "sentinel-value-omit-7q9z") {
		t.Fatalf("block must never carry env values, got:\n%s", block)
	}
	if got := OrchestratorsContext(nil); got != "" {
		t.Fatalf("no detection must render nothing, got %q", got)
	}
}

func TestSystemPromptPinsOrchestratorOutsideBudget(t *testing.T) {
	t.Setenv("FOO_SOCKET", "sock-secret-pin-31c0")
	filler := make([]SkillInfo, 0, 200)
	for i := 0; i < 200; i++ {
		filler = append(filler, SkillInfo{Name: "skill-" + strconv.Itoa(i), Description: strings.Repeat("d", 180)})
	}
	skills := append([]SkillInfo{{Name: "foo", Description: "Use when coordinating agents in foo."}}, filler...)
	prompt := buildSystemPrompt(Options{Skills: skills})
	if !strings.Contains(prompt, "<environment_orchestrator>") {
		t.Fatalf("orchestrator block missing from prompt")
	}
	if !strings.Contains(prompt, "env:FOO_SOCKET") {
		t.Fatalf("detection signal missing from prompt")
	}
	if !strings.Contains(prompt, "- foo (orchestrator") || !strings.Contains(prompt, "Use when coordinating agents in foo.") {
		t.Fatalf("pinned foo line with full description missing")
	}
	if strings.Contains(prompt, "sock-secret-pin-31c0") {
		t.Fatalf("prompt must never leak env values")
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

func TestDetectOrchestratorsDeclaredSkipsHeuristic(t *testing.T) {
	skills := []SkillInfo{{Name: "harbor", Environments: []string{"HARBOR_DECLARED"}}}
	if got := DetectOrchestrators(skills, map[string]string{"HARBOR_SOCKET": "v"}); len(got) != 0 {
		t.Fatalf("declared skill must not match by heuristic, got %+v", got)
	}
	got := DetectOrchestrators(skills, map[string]string{"HARBOR_DECLARED": "v"})
	if len(got) != 1 || got[0].Signal != "env:HARBOR_DECLARED" {
		t.Fatalf("declared match expected, got %+v", got)
	}
}

func TestDetectOrchestratorsIgnoresUbiquitousDeclared(t *testing.T) {
	// Each skill is named by the variable it declares, so the declaration
	// passes the belonging rule and only the ubiquitous list can reject it.
	skills := []SkillInfo{
		{Name: "home", Environments: []string{"HOME"}},
		{Name: "tmux", Environments: []string{"TMUX"}},
		{Name: "user", Environments: []string{"USER"}},
		{Name: "zebra", Environments: []string{"HOME", "ZEBRA_SOCKET"}},
	}
	env := map[string]string{
		"HOME": "/home/orq", "TMUX": "/tmp/tmux-1", "USER": "orq",
		"ZEBRA_SOCKET": "v",
	}
	got := DetectOrchestrators(skills, env)
	if len(got) != 1 || got[0].Name != "zebra" || got[0].Signal != "env:ZEBRA_SOCKET" {
		t.Fatalf("only the non-ubiquitous declaration must match, got %+v", got)
	}
}

func TestDetectOrchestratorsDeclaredMustBelongToSkill(t *testing.T) {
	skills := []SkillInfo{
		{Name: "kestrel-hub", Environments: []string{"KESTREL_HUB_MARK"}},
		{Name: "osprey-desk", Environments: []string{"RELAY_SESSION"}},
		{Name: "plain-desk", Environments: []string{"BAR_X"}},
		{Name: "wsbare-desk", Environments: []string{"WORKSPACE"}},
		{Name: "shlvl-desk", Environments: []string{"SHLVL"}},
	}
	env := map[string]string{
		"KESTREL_HUB_MARK": "a", "RELAY_SESSION": "b", "BAR_X": "c", "WORKSPACE": "d", "SHLVL": "2",
	}
	got := DetectOrchestrators(skills, env)
	names := map[string]string{}
	for _, d := range got {
		names[d.Name] = d.Signal
	}
	if names["kestrel-hub"] != "env:KESTREL_HUB_MARK" {
		t.Fatalf("prefix declaration must match, got %+v", got)
	}
	if names["osprey-desk"] != "env:RELAY_SESSION" {
		t.Fatalf("suffix declaration must match, got %+v", got)
	}
	if _, ok := names["plain-desk"]; ok {
		t.Fatalf("unrelated declaration must not match, got %+v", got)
	}
	if _, ok := names["wsbare-desk"]; ok {
		t.Fatalf("bare suffix without the underscore must not match, got %+v", got)
	}
	if _, ok := names["shlvl-desk"]; ok {
		t.Fatalf("ubiquitous declaration must not match, got %+v", got)
	}
}

func TestDetectOrchestratorsHeuristicSkipsUbiquitousKeys(t *testing.T) {
	// WT_SESSION is ubiquitous: a skill named "wt" must not match it.
	got := DetectOrchestrators([]SkillInfo{{Name: "wt"}}, map[string]string{"WT_SESSION": "x"})
	if len(got) != 0 {
		t.Fatalf("ubiquitous heuristic key must not match, got %+v", got)
	}
}

func TestDeclarationBelongsToSkillFoldCase(t *testing.T) {
	if !declarationBelongsToSkill("kestrel_hub_MARK", "KESTREL_HUB", true) {
		t.Fatalf("foldCase must ignore letter case")
	}
	if declarationBelongsToSkill("kestrel_hub_MARK", "KESTREL_HUB", false) {
		t.Fatalf("exact mode must not ignore letter case")
	}
	// The underscore boundary: KITE_RELAYSOCKET belongs by prefix, while a
	// bare RELAYSOCKET belongs to nothing — without the "_" guard the second
	// case would match as a suffix.
	if !declarationBelongsToSkill("KITE_RELAYSOCKET", "KITE", false) {
		t.Fatalf("skill-name prefix must belong")
	}
	if declarationBelongsToSkill("RELAYSOCKET", "X", false) {
		t.Fatalf("bare suffix without the underscore must not belong")
	}
}

func TestDetectOrchestratorsIgnoresCommonTerminalPrograms(t *testing.T) {
	for _, term := range []string{"tmux", "iTerm.app", "vscode", "WezTerm", "ghostty", "Apple_Terminal", "screen"} {
		skills := []SkillInfo{{Name: strings.ToLower(term)}}
		if got := DetectOrchestrators(skills, map[string]string{"TERM_PROGRAM": term}); len(got) != 0 {
			t.Fatalf("TERM_PROGRAM=%q must not elect a skill, got %+v", term, got)
		}
	}
	got := DetectOrchestrators([]SkillInfo{{Name: "vortexterm"}}, map[string]string{"TERM_PROGRAM": "VortexTerm"})
	if len(got) != 1 || got[0].Signal != "TERM_PROGRAM" {
		t.Fatalf("uncommon TERM_PROGRAM must still match by name, got %+v", got)
	}
}

func TestFoldCaseForOS(t *testing.T) {
	if !foldCaseForOS("windows") {
		t.Fatalf("windows must compare variable names case-insensitively")
	}
	for _, goos := range []string{"darwin", "linux", "freebsd", "plan9"} {
		if foldCaseForOS(goos) {
			t.Fatalf("%s must compare variable names case-sensitively", goos)
		}
	}
}

func TestCanonicalEnvKeys(t *testing.T) {
	in := map[string]string{"kestrel_hub_mark": "v", "OTHER": "w"}
	folded := canonicalEnvKeys(in, true)
	if folded["KESTREL_HUB_MARK"] != "v" || folded["OTHER"] != "w" {
		t.Fatalf("fold mode must uppercase keys, got %+v", folded)
	}
	kept := canonicalEnvKeys(in, false)
	if kept["kestrel_hub_mark"] != "v" || kept["OTHER"] != "w" {
		t.Fatalf("exact mode must keep key spelling, got %+v", kept)
	}
}

func TestMatchOrchestratorFoldCaseLookup(t *testing.T) {
	skill := SkillInfo{Name: "kestrel-hub", Environments: []string{"kestrel_hub_mark"}}
	env := map[string]string{"KESTREL_HUB_MARK": "v"}
	if _, ok := matchOrchestrator(skill, "kestrel-hub", canonicalEnvKeys(env, false), false); ok {
		t.Fatalf("exact lookup must miss the differently-cased variable")
	}
	got, ok := matchOrchestrator(skill, "kestrel-hub", canonicalEnvKeys(env, true), true)
	if !ok || got != "env:kestrel_hub_mark" {
		t.Fatalf("folded lookup must match, got %q %v", got, ok)
	}
}
