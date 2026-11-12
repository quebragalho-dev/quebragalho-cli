package agent

import (
	"os"
	"runtime"
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
// skill Name plus the Signal that proved it (env:NAME or TERM_PROGRAM).
// Signals carry variable NAMES only, never values, so the block is safe to
// log and to send to the model.
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
	// declares its markers never matches by name pattern. Each declared name
	// must belong to the skill — end in a session suffix or start with the
	// normalized skill name — and ubiquitous variables never count, so no
	// skill can pin itself into every prompt by declaring PATH or HOME.
	if len(skill.Environments) > 0 {
		normalized := normalizeOrchestratorName(name)
		for _, declared := range skill.Environments {
			declared = strings.TrimSpace(declared)
			if declared == "" || isUbiquitousEnvironment(declared) {
				continue
			}
			if !declarationBelongsToSkill(declared, normalized) {
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
			if isUbiquitousEnvironment(key) {
				continue
			}
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

// declarationBelongsToSkill reports whether a declared marker belongs to the
// skill: it ends in a session suffix or starts with the normalized skill
// name. Comparison is case-insensitive on Windows, where variable names are.
func declarationBelongsToSkill(declared, normalizedSkill string) bool {
	d, n := declared, normalizedSkill
	if runtime.GOOS == "windows" {
		d, n = strings.ToUpper(d), strings.ToUpper(n)
	}
	for _, suffix := range orchestratorEnvSuffixes {
		if d == suffix || strings.HasSuffix(d, "_"+suffix) {
			return true
		}
	}
	return strings.HasPrefix(d, n)
}

// isUbiquitousEnvironment reports variables present in essentially every
// process, which must never elect an ambient skill on their own — neither by
// declaration nor by heuristic. Besides the usual shell variables this covers
// terminal multiplexers and emulators (their session markers exist in every
// terminal of that kind) and the Windows variable set; comparison is
// case-insensitive because the Windows environment is.
func isUbiquitousEnvironment(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	switch upper {
	case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "TERM", "TERM_PROGRAM",
		"COLORTERM", "PWD", "OLDPWD", "LANG", "TMPDIR", "TZ", "EDITOR",
		"VISUAL", "PAGER", "DISPLAY", "SHLVL", "_", "HOSTNAME", "SSH_AUTH_SOCK",
		"MAIL", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "TEMP", "TMP",
		"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "OS", "COMPUTERNAME",
		"USERNAME", "HOMEDRIVE", "HOMEPATH", "TERM_SESSION_ID",
		"ITERM_SESSION_ID", "WT_SESSION", "WEZTERM_UNIX_SOCKET", "WEZTERM_PANE",
		"KONSOLE_DBUS_SESSION", "KITTY_WINDOW_ID", "TMUX", "TMUX_PANE", "STY",
		"VTE_VERSION", "DBUS_SESSION_BUS_ADDRESS", "SSH_CONNECTION",
		"SSH_CLIENT", "SSH_TTY":
		return true
	}
	return strings.HasPrefix(upper, "LC_") || strings.HasPrefix(upper, "XDG_")
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
