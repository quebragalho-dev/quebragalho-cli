package agent

import (
	"os"
	"strings"
)

// Ambient orchestrator detection.
//
// Some developers run QG inside an orchestrating environment (a canvas app,
// a team runner, CI with sibling agents) that publishes its presence through
// environment variables and ships a skill describing how to talk back to it.
// The model only follows that skill when it can SEE it, so detection runs once
// per run start (never per turn) and renders a short fixed block plus a pinned
// skill line — both rebuilt with the system prompt, which is why they survive
// compaction while transcript instructions do not.
//
// The rule is generic on purpose: no orchestrator name lives in the binary.
// A skill either declares its marker variables (frontmatter `environment:`)
// or its normalized name matches a marker variable in the environment.

// DetectedOrchestrator is one installed skill recognized as ambient: the
// skill Name plus the Signal that proved it (env:NAME or term:VALUE). Signal
// never carries a variable VALUE — only names — so the block is safe to log.
type DetectedOrchestrator struct {
	Name   string
	Signal string
}

// orchestratorEnvSuffixes are the session markers a skill name may combine
// with. A bare prefix alone never matches: GIT_EDITOR must not elect a skill
// named git, and GOPATH must not elect one named go.
var orchestratorEnvSuffixes = []string{
	"SOCKET",
	"SESSION",
	"SESSION_ID",
	"TERMINAL_ID",
	"WORKSPACE",
	"WORKSPACE_ID",
	"CLI",
}

// normalizeOrchestratorName uppercases a skill name and turns dashes into
// underscores so `my-chat` matches MY_CHAT_SOCKET and friends.
func normalizeOrchestratorName(name string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(name)), "-", "_")
}

// DetectOrchestrators matches installed skills against the environment, in
// strength order per skill: declared `environment:` names first, then the
// NAME_SUFFIX heuristic, then TERM_PROGRAM. Every matching skill is reported
// (rare); order follows the input skills slice, which upstream keeps sorted
// by name, so repeated builds are byte-stable. A nil env snapshots the
// process environment; tests pass an explicit map.
func DetectOrchestrators(skills []SkillInfo, env map[string]string) []DetectedOrchestrator {
	if env == nil {
		env = snapshotEnv()
	}
	var out []DetectedOrchestrator
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		signal, ok := matchOrchestrator(skill, name, env)
		if !ok {
			continue
		}
		out = append(out, DetectedOrchestrator{Name: name, Signal: signal})
	}
	return out
}

func matchOrchestrator(skill SkillInfo, name string, env map[string]string) (string, bool) {
	// A declared `environment:` list replaces the heuristic: a skill that
	// declares its markers never matches by name pattern. Ubiquitous
	// variables are ignored in declarations — a skill cannot pin itself into
	// every prompt by declaring PATH, HOME, or any XDG_* / LC_* variable.
	if len(skill.Environments) > 0 {
		for _, declared := range skill.Environments {
			declared = strings.TrimSpace(declared)
			if declared == "" || isUbiquitousEnvironment(declared) {
				continue
			}
			if strings.TrimSpace(env[declared]) != "" {
				return "env:" + declared, true
			}
		}
	} else {
		normalized := normalizeOrchestratorName(name)
		for _, suffix := range orchestratorEnvSuffixes {
			key := normalized + "_" + suffix
			if strings.TrimSpace(env[key]) != "" {
				return "env:" + key, true
			}
		}
	}
	if term := strings.TrimSpace(env["TERM_PROGRAM"]); term != "" && strings.EqualFold(term, name) {
		return "TERM_PROGRAM", true
	}
	return "", false
}

// isUbiquitousEnvironment reports variables present in essentially every
// process, which must never elect an ambient skill on their own.
func isUbiquitousEnvironment(name string) bool {
	switch name {
	case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "TERM_PROGRAM",
		"COLORTERM", "PWD", "OLDPWD", "LANG", "TMPDIR", "TZ", "EDITOR",
		"VISUAL", "PAGER", "DISPLAY":
		return true
	}
	return strings.HasPrefix(name, "LC_") || strings.HasPrefix(name, "XDG_")
}

func snapshotEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	return env
}

// OrchestratorsContext renders the fixed <environment_orchestrator> block for
// detected ambient skills. Empty input renders nothing, so runs outside any
// orchestrator reproduce the previous prompt byte-for-byte. Project
// guidelines (AGENTS.md/QG.md) override this block on conflict — the sentence
// below says so because position alone cannot promise precedence to a model.
func OrchestratorsContext(detected []DetectedOrchestrator) string {
	kept := make([]DetectedOrchestrator, 0, len(detected))
	for _, d := range detected {
		name := strings.TrimSpace(d.Name)
		if name == "" {
			continue
		}
		kept = append(kept, DetectedOrchestrator{Name: name, Signal: strings.TrimSpace(d.Signal)})
	}
	if len(kept) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<environment_orchestrator>\n")
	for _, d := range kept {
		b.WriteString("You run inside " + d.Name + " (detected by " + d.Signal + "). ")
		b.WriteString("Before coordinating other agents, terminals, or notes, load the skill \"" + d.Name + "\" and follow it. ")
		b.WriteString("Project guidelines (AGENTS.md/QG.md) override this block on conflict.\n")
	}
	b.WriteString("</environment_orchestrator>")
	return b.String()
}

// orchestratorPinNames returns the detected skill names present in the skill
// list, in detection order, so the prompt pins exactly what it advertised.
func orchestratorPinNames(skills []SkillInfo, detected []DetectedOrchestrator) []string {
	available := map[string]bool{}
	for _, info := range skills {
		if name := strings.TrimSpace(info.Name); name != "" {
			available[name] = true
		}
	}
	var pins []string
	for _, d := range detected {
		if name := strings.TrimSpace(d.Name); name != "" && available[name] {
			pins = append(pins, name)
		}
	}
	return pins
}
