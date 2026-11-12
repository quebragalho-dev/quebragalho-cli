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

// foldCaseForOS reports whether environment variable names compare
// case-insensitively on the given OS (Windows only). A tiny pure function so
// the mode decision is unit-testable without Windows; callers pass
// runtime.GOOS.
func foldCaseForOS(goos string) bool {
	return goos == "windows"
}

func matchOrchestrator(skill SkillInfo, name string, env map[string]string) (string, bool) {
	// Case-insensitivity is decided here, once, from the OS: variable names
	// are case-insensitive on Windows and case-sensitive elsewhere.
	foldCase := foldCaseForOS(runtime.GOOS)
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
			if !declarationBelongsToSkill(declared, normalized, foldCase) {
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
	// TERM_PROGRAM matches the skill name — unless it is a common terminal or
	// multiplexer, which exists because of the terminal, not an orchestrator.
	term := strings.TrimSpace(env["TERM_PROGRAM"])
	if term == "" || isCommonTerminalProgram(term) {
		return "", false
	}
	if strings.EqualFold(term, name) {
		return "TERM_PROGRAM", true
	}
	return "", false
}

// declarationBelongsToSkill reports whether a declared marker belongs to the
// skill: it ends in `_` + session suffix or starts with the normalized skill
// name. A bare suffix alone ("WORKSPACE") never belongs to anyone — Jenkins
// defines it in every job. foldCase ignores letter case (Windows, where
// variable names are case-insensitive); otherwise the comparison is exact.
func declarationBelongsToSkill(declared, normalizedSkill string, foldCase bool) bool {
	d, n := declared, normalizedSkill
	if foldCase {
		d, n = strings.ToUpper(d), strings.ToUpper(n)
	}
	for _, suffix := range orchestratorEnvSuffixes {
		if strings.HasSuffix(d, "_"+suffix) {
			return true
		}
	}
	return strings.HasPrefix(d, n)
}

// isUbiquitousEnvironment reports variables present for reasons other than
// an orchestrator — the shell, the terminal, the editor, the desktop, or CI —
// which must never elect an ambient skill on their own, neither by
// declaration nor by heuristic. Comparison is case-insensitive because the
// Windows environment is.
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
		"SSH_CLIENT", "SSH_TTY", "CI", "DESKTOP_SESSION", "SESSION_MANAGER",
		"TERMINAL_EMULATOR":
		return true
	}
	for _, prefix := range []string{"LC_", "XDG_", "GITHUB_", "GITLAB_",
		"BITBUCKET_", "GITPOD_", "CODESPACE", "BUILDKITE_", "CIRCLE", "TRAVIS",
		"JENKINS_", "BUILD_", "GNOME_", "KDE_", "ALACRITTY_", "VSCODE_"} {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// commonTerminalPrograms are TERM_PROGRAM values set by plain terminals and
// multiplexers, which exist because of the terminal — never an orchestrator.
// Matched case-insensitively.
var commonTerminalPrograms = map[string]bool{
	"tmux": true, "screen": true, "apple_terminal": true, "iterm.app": true,
	"vscode": true, "wezterm": true, "ghostty": true, "alacritty": true,
	"kitty": true, "hyper": true, "tabby": true, "warpterminal": true,
	"konsole": true, "rio": true,
}

// isCommonTerminalProgram reports whether a TERM_PROGRAM value names a common
// terminal or multiplexer rather than an orchestrator.
func isCommonTerminalProgram(term string) bool {
	return commonTerminalPrograms[strings.ToLower(strings.TrimSpace(term))]
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
