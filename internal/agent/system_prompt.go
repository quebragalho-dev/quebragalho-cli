package agent

import (
	_ "embed"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/repomap"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"github.com/quebragalho-dev/quebragalho-cli/internal/workspaceseed"
)

// coreSystemPrompt is the de-branded coding-craft instruction set: identity,
// autonomy, workflow, editing discipline, the testing gate, tool use, and
// communication style.
//
//go:embed system_prompt.md
var coreSystemPrompt string

// confirmationPolicy is the de-branded safety policy appended to the system
// prompt so the model self-polices before risky actions. The sandbox enforces a
// subset of these rules, but the model applies judgement first.
//
//go:embed confirmation_policy.md
var confirmationPolicy string

// fallbackSystemPrompt is used only if the embedded core prompt is somehow empty
// (it never should be) so a run always has a non-empty system turn.
const fallbackSystemPrompt = "You are QG, a terminal coding agent. Help with the current workspace and use tools when needed."

// projectContextFiles are workspace docs injected into the system prompt so the
// agent honors project-specific conventions (mirrors AGENTS.md / CLAUDE.md).
// The first match at each directory level wins; the loader walks the chain
// from the git root down to the cwd and injects the matches in
// general-to-specific order.
var projectContextFiles = []string{"AGENTS.md", "QG.md", ".qg/AGENTS.md"}

// userContextFile is the per-user instruction file, resolved under
// config.UserConfigDir()/qg/ alongside the rest of QG's per-user config
// (config.json, commands, specialists) so users can keep personal guidance out
// of individual repositories.
const userContextFile = "QG.md"

var userConfigDirForPrompt = config.UserConfigDir

// maxProjectContextBytes caps how much of a single project doc is injected so
// a large guidelines file can't blow the context budget.
const maxProjectContextBytes = 8 << 10 // 8 KiB per file

// maxProjectContextTotalBytes caps the total bytes across all matched
// project guideline files. With path-walking, multiple files can match (root
// + sub-tree), so a per-file cap alone is not enough.
const maxProjectContextTotalBytes = 32 << 10 // 32 KiB total

// maxRepoMapContextBytes keeps the repository map useful but compact enough to
// remain stable across normal agent turns.
const maxRepoMapContextBytes = 4 << 10 // 4 KiB

const (
	workspaceSeedMaxLines = 12
	workspaceSeedWidth    = 100
)

// buildSystemPrompt assembles the full system prompt for a run: the core
// coding-craft instructions, dynamic workspace context (cwd, git branch, project
// guidelines), and the safety confirmation policy. It is built once per run so
// every turn shares one (cacheable) system turn.
func buildSystemPrompt(options Options) string {
	return buildSystemPromptParts(options).prompt
}

// systemPromptParts retains both the exact joined prompt and the diagnostic
// sections used by prompt-prefix tracing. Building these together prevents the
// tracing path from re-reading workspace state and guarantees that the hash is
// computed from the same prompt bytes placed in the request.
type systemPromptParts struct {
	prompt             string
	sections           []promptSection
	baseInstructions   string
	confirmationPolicy string
	projectContext     string
	skills             string
}

// promptSection is one independently tracked system-prompt section. The key is
// a stable identity used to match a section against its last announced value
// across turns; only non-empty texts join the prompt, in build order, so the
// prompt stays byte-identical to the unkeyed assembly.
type promptSection struct {
	key  string
	text string
}

// Section keys for drift tracking. One key per dynamic section; static
// sections keep keys too so a future change is attributed, never dumped.
const (
	sectionKeyCore           = "core"
	sectionKeyTransient      = "transient"
	sectionKeyModel          = "model"
	sectionKeySession        = "session"
	sectionKeyPrefixes       = "prefixes"
	sectionKeySeed           = "seed"
	sectionKeyUserGuidelines = "user-guidelines"
	sectionKeyEnvironment    = "environment"
	sectionKeyGuidelines     = "guidelines"
	sectionKeyRepoMap        = "repo-map"
	sectionKeyPermissionMode = "permission-mode"
	sectionKeyDelegation     = "delegation"
	sectionKeySkills         = "skills"
	sectionKeyStyle          = "style"
	sectionKeyPolicy         = "policy"
)

func buildSystemPromptParts(options Options) systemPromptParts {
	core := strings.TrimSpace(options.SystemPrompt)
	if core == "" {
		core = strings.TrimSpace(coreSystemPrompt)
	}
	if core == "" {
		core = fallbackSystemPrompt
	}
	sections := []promptSection{{key: sectionKeyCore, text: core}}
	if transient := strings.TrimSpace(options.TransientSystemPrompt); transient != "" {
		sections = append(sections, promptSection{key: sectionKeyTransient, text: transient})
	}
	if addendum := modelPromptAddendum(options.Model); addendum != "" {
		sections = append(sections, promptSection{key: sectionKeyModel, text: addendum})
	}
	if session := sessionRuntimeContext(options); session != "" {
		sections = append(sections, promptSection{key: sectionKeySession, text: session})
	}
	if prefixes := approvedCommandPrefixContext(options); prefixes != "" {
		sections = append(sections, promptSection{key: sectionKeyPrefixes, text: prefixes})
	}
	if seed := workspaceSeedContext(options.Cwd); seed != "" {
		sections = append(sections, promptSection{key: sectionKeySeed, text: seed})
	}
	// User guidelines are injected before workspace/project guidelines so the
	// project's AGENTS.md/QG.md is the later, more specific instruction
	// block. See userGuidelines for the explicit precedence note carried in
	// the section text itself.
	if user := userGuidelines(); user != "" {
		sections = append(sections, promptSection{key: sectionKeyUserGuidelines, text: user})
	}
	env, guidelines, repoSection := workspaceSections(options.Cwd)
	if env != "" {
		sections = append(sections, promptSection{key: sectionKeyEnvironment, text: env})
	}
	if guidelines != "" {
		sections = append(sections, promptSection{key: sectionKeyGuidelines, text: guidelines})
	}
	if repoSection != "" {
		sections = append(sections, promptSection{key: sectionKeyRepoMap, text: repoSection})
	}
	project := joinWorkspaceSections(env, guidelines, repoSection)
	if modeCtx := permissionModeContext(options); modeCtx != "" {
		sections = append(sections, promptSection{key: sectionKeyPermissionMode, text: modeCtx})
	}
	if delegation := specialistDelegationContext(options); delegation != "" {
		sections = append(sections, promptSection{key: sectionKeyDelegation, text: delegation})
	}
	skillsBlock := skillsContext(options)
	if skillsBlock != "" {
		sections = append(sections, promptSection{key: sectionKeySkills, text: skillsBlock})
	}
	if style := responseStyleContext(options); style != "" {
		sections = append(sections, promptSection{key: sectionKeyStyle, text: style})
	}
	policy := strings.TrimSpace(confirmationPolicy)
	if policy != "" {
		sections = append(sections, promptSection{key: sectionKeyPolicy, text: policy})
	}
	return systemPromptParts{
		prompt:             joinPromptSections(sections),
		sections:           sections,
		baseInstructions:   core,
		confirmationPolicy: policy,
		projectContext:     project,
		skills:             skillsBlock,
	}
}

// joinPromptSections joins non-empty section texts in build order. Centralized
// so the prompt stays byte-identical to the unkeyed assembly.
func joinPromptSections(sections []promptSection) string {
	texts := make([]string, 0, len(sections))
	for _, section := range sections {
		if section.text != "" {
			texts = append(texts, section.text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// continuationUserMessage builds the user turn appended to a prior turn's
// messages: the prompt plus a [context update] note listing the sections whose
// current value differs from their last announced value. No drift means the
// bare prompt, so the request prefix stays cache-stable.
func continuationUserMessage(promptParts systemPromptParts, options Options, prompt string) qgruntime.Message {
	content := prompt
	if note := contextUpdateNote(promptParts, options.Continuation); note != "" {
		content = note + "\n\n" + prompt
	}
	return qgruntime.Message{Role: qgruntime.MessageRoleUser, Content: content, Images: qgruntime.CloneImageBlocks(options.Images)}
}

// sectionBuildOrder is the assembly order of prompt sections. Drift notes
// follow it so repeated runs render deterministically.
var sectionBuildOrder = []string{
	sectionKeyCore,
	sectionKeyTransient,
	sectionKeyModel,
	sectionKeySession,
	sectionKeyPrefixes,
	sectionKeySeed,
	sectionKeyUserGuidelines,
	sectionKeyEnvironment,
	sectionKeyGuidelines,
	sectionKeyRepoMap,
	sectionKeyPermissionMode,
	sectionKeyDelegation,
	sectionKeySkills,
	sectionKeyStyle,
	sectionKeyPolicy,
}

// sectionFrozenMarker maps a section key to a stable head its text always
// carries. It lets a removal be detected from the frozen system message when
// a section disappears before its first note: the marker proves the section
// was announced at build time. Keys without a marker (free-form transient
// text) cannot prove frozen presence and skip silent-removal detection.
var sectionFrozenMarker = map[string]string{
	sectionKeyModel:          "<model_guidance>",
	sectionKeySession:        "<session>",
	sectionKeyPrefixes:       "## Approved Command Prefixes",
	sectionKeySeed:           "<workspace_seed>",
	sectionKeyUserGuidelines: "## User guidelines (",
	sectionKeyEnvironment:    "<environment>",
	sectionKeyGuidelines:     "## Project guidelines (",
	sectionKeyRepoMap:        "## Repo map",
	sectionKeyPermissionMode: "Plan mode is active",
	sectionKeyDelegation:     "<specialists>",
	sectionKeySkills:         "<available_skills>",
	sectionKeyStyle:          "Response style:",
	sectionKeyPolicy:         "# Confirmation Policy",
}

// contextUpdateNote lists the sections whose current value differs from the
// last announced value for their key. A key announced in a prior note compares
// against that note; a key never noted compares against the frozen system,
// which holds until its first note. A previously announced key that is now
// absent announces as removed, as does a never-noted key whose frozen marker
// proves it was in the frozen system. Empty when nothing changed.
func contextUpdateNote(parts systemPromptParts, continuation []qgruntime.Message) string {
	announced := lastAnnouncedSections(continuation)
	frozen := frozenSystemText(continuation)
	current := map[string]string{}
	raw := map[string]string{}
	for _, section := range parts.sections {
		current[section.key] = noteSectionBody(section.text)
		raw[section.key] = section.text
	}
	var missing []string
	for _, key := range sectionBuildOrder {
		body, present := current[key]
		if last, ok := announced[key]; ok {
			if !present {
				body = "(removed)"
			}
			if announcedSectionCurrent(body, last) {
				continue
			}
		} else if present {
			if frozenSectionMatches(key, raw[key], continuation) {
				continue
			}
		} else if marker, ok := sectionFrozenMarker[key]; !ok || !strings.Contains(frozen, marker) {
			continue
		} else {
			body = "(removed)"
		}
		missing = append(missing, "[context update: "+key+"]\n"+body+"\n"+contextUpdateEndMarker)
	}
	if len(missing) == 0 {
		return ""
	}
	return strings.Join(missing, "\n\n")
}

// frozenSystemText concatenates the system-role messages in history. The
// first is normally the frozen system prompt the run started from; later
// system messages (compaction summaries) only widen the match set for
// removal detection, never for value comparison.
func frozenSystemText(continuation []qgruntime.Message) string {
	var b strings.Builder
	for _, msg := range continuation {
		if msg.Role == qgruntime.MessageRoleSystem {
			b.WriteString(msg.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// noteSectionBody is the announced form of a section text. An absent section
// (removed file, default style, exited plan mode) announces as removed so the
// model learns the loss instead of silently keeping stale instructions.
func noteSectionBody(text string) string {
	if strings.Trim(text, "\n") == "" {
		return "(removed)"
	}
	return strings.Trim(text, "\n")
}

// contextUpdateEndMarker closes every [context update] block so the recorded
// body is delimited exactly. Without it the last block in a note absorbs the
// trailing user prompt, forcing prefix matching that hides a later FIM cut.
const contextUpdateEndMarker = "[/context update]"

// frozenSectionMatches reports whether the current body for key equals the
// frozen system value for that same key. The frozen value is extracted from
// the first system message by section limits (start marker to the next
// section's marker), never by substring, so a truncated tail that remains a
// substring of the full form still compares as different.
func frozenSectionMatches(key, section string, continuation []qgruntime.Message) bool {
	currentBody := noteSectionBody(section)
	frozen := firstFrozenSystem(continuation)
	if frozen == "" {
		return false
	}
	marker, ok := sectionFrozenMarker[key]
	if !ok {
		// Keys without a stable head (free-form text) cannot be extracted by
		// limits; fall back to delimited presence in the frozen prompt.
		if frozen == section {
			return true
		}
		if strings.HasPrefix(frozen, section+"\n\n") {
			return true
		}
		if strings.HasSuffix(frozen, "\n\n"+section) {
			return true
		}
		return strings.Contains(frozen, "\n\n"+section+"\n\n")
	}
	extracted, ok := frozenSectionForKey(frozen, key, marker)
	if !ok {
		return false
	}
	return noteSectionBody(extracted) == currentBody
}

// firstFrozenSystem returns the first system message content in history, the
// frozen prompt the run started from. Later system messages (compaction
// summaries) only widen removal detection, never value comparison.
func firstFrozenSystem(continuation []qgruntime.Message) string {
	for _, msg := range continuation {
		if msg.Role == qgruntime.MessageRoleSystem {
			return msg.Content
		}
	}
	return ""
}

// frozenSectionForKey extracts the section starting at marker from the frozen
// prompt, ending at the next later section's marker or the prompt end.
func frozenSectionForKey(frozen, key, marker string) (string, bool) {
	start := strings.Index(frozen, marker)
	if start < 0 {
		return "", false
	}
	orderIdx := -1
	for i, k := range sectionBuildOrder {
		if k == key {
			orderIdx = i
			break
		}
	}
	if orderIdx < 0 {
		return "", false
	}
	end := len(frozen)
	for _, later := range sectionBuildOrder[orderIdx+1:] {
		laterMarker, ok := sectionFrozenMarker[later]
		if !ok || laterMarker == "" {
			continue
		}
		if rel := strings.Index(frozen[start+len(marker):], laterMarker); rel >= 0 {
			candidate := start + len(marker) + rel
			if candidate < end {
				end = candidate
			}
		}
	}
	section := strings.Trim(frozen[start:end], "\n")
	return section, true
}

// lastAnnouncedSections replays user notes in order and records, per section
// key, the exact body of its most recent [context update] block delimited by
// contextUpdateEndMarker. Only messages with role user whose content starts
// with a note are read, so a forged "[context update: ...]" inside a tool
// result never suppresses a real warning. Keys without a note keep the frozen
// system value.
func lastAnnouncedSections(continuation []qgruntime.Message) map[string]string {
	const prefix = "[context update: "
	announced := map[string]string{}
	for _, msg := range continuation {
		if msg.Role != qgruntime.MessageRoleUser {
			continue
		}
		rest := msg.Content
		if !strings.HasPrefix(rest, prefix) {
			continue
		}
		for strings.HasPrefix(rest, prefix) {
			rest = rest[len(prefix):]
			end := strings.Index(rest, "]")
			if end < 0 {
				break
			}
			key := rest[:end]
			rest = rest[end+1:]
			rest = strings.TrimPrefix(rest, "\n")
			closeIdx := strings.Index(rest, contextUpdateEndMarker)
			if closeIdx < 0 {
				break
			}
			body := strings.TrimSuffix(rest[:closeIdx], "\n")
			if key != "" {
				announced[key] = body
			}
			rest = rest[closeIdx+len(contextUpdateEndMarker):]
			rest = strings.TrimPrefix(rest, "\n\n")
			rest = strings.TrimPrefix(rest, "\n")
		}
	}
	return announced
}

// announcedSectionCurrent reports whether body equals the recorded announcement
// for a section. Exact only: a truncated body is a prefix of the recorded full
// form and must not compare as current.
func announcedSectionCurrent(body, recorded string) bool {
	return recorded == body
}

// responseStyleContext renders the operator-selected reply style (TUI /style) as
// a system-prompt directive so the choice actually shapes responses. "balanced"
// (the default) and unknown/empty values add nothing, keeping the prompt
// byte-identical to the pre-style behavior.
func responseStyleContext(options Options) string {
	switch strings.ToLower(strings.TrimSpace(options.ResponseStyle)) {
	case "concise":
		return "Response style: concise. Lead with the result and keep answers short and direct; omit preamble, restating the question, and any explanation not needed to be correct."
	case "explanatory":
		return "Response style: explanatory. Explain the reasoning behind your answers and changes, surface relevant trade-offs, and add brief context a learner would want — while staying on task and not padding."
	case "review":
		return "Response style: review. Work like a critical reviewer: call out risks, edge cases, and unstated assumptions, flag anything questionable, and prefer precise, evidence-backed statements over reassurance."
	default:
		return ""
	}
}

func approvedCommandPrefixContext(options Options) string {
	if options.Sandbox == nil {
		return ""
	}
	prefixes := options.Sandbox.ApprovedCommandPrefixes()
	if len(prefixes) == 0 {
		return ""
	}
	lines := make([]string, 0, len(prefixes))
	for _, grant := range prefixes {
		if len(grant.Prefix) == 0 {
			continue
		}
		lines = append(lines, "- "+grant.ToolName+": "+strings.Join(grant.Prefix, " "))
	}
	if len(lines) == 0 {
		return ""
	}
	return "## Approved Command Prefixes\n\nThe following command prefixes have already been approved and do not need another permission prompt:\n" + strings.Join(lines, "\n")
}

// specialistDelegationContext describes when a specialist earns its extra
// context and coordination cost. It renders only when specialists are known
// (which is only where the Task tool is actually registered), so a run with no
// delegatable specialists produces the previous prompt unchanged.
func specialistDelegationContext(options Options) string {
	if len(options.Specialists) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<specialists>\n")
	b.WriteString("Use the Task tool when a specialist's focused context, expertise, or an independent parallel subtask is likely to improve quality or reduce total context. ")
	b.WriteString("Do not delegate small direct work, duplicate exploration, or split sequential steps merely because a specialist is available. ")
	b.WriteString("When delegating, give a bounded assignment and consume its concise evidence instead of importing a long transcript.\n")
	b.WriteString("Available specialists (call Task with the matching name when the task fits its purpose):\n")
	for _, info := range options.Specialists {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		if desc := strings.TrimSpace(info.WhenToUse); desc != "" {
			b.WriteString("- " + name + ": " + desc + "\n")
		} else {
			b.WriteString("- " + name + "\n")
		}
	}
	b.WriteString("</specialists>")
	return b.String()
}

// skillsContext lists the reusable skills the model can pull in on demand via the
// skill tool, so it invokes the right one on the first try instead of guessing a
// name, failing, and reading the error. It mirrors specialistDelegationContext:
// names + one-line descriptions only (never skill bodies), and it renders nothing
// when no skills are installed, so a skill-less run reproduces the previous prompt
// byte-for-byte.
// skillsContextListBudget bounds the bytes spent listing individual skills so a
// workspace with an extreme number of them can't bloat every turn's prompt.
// Skills past the budget are summarized as a count rather than dropped — the
// model can still load any of them by name (an unknown name returns the full
// list from the skill tool).
//
// The budget is deliberately generous — roughly 18 skills with maximally long
// (200-rune, ASCII) descriptions, or 40+ with typical terser ones; non-ASCII
// descriptions consume it faster since the budget counts bytes. This list is
// the model's ONLY discovery surface, so a skill squeezed out of it effectively
// never triggers. The previous 640-byte cap silently broke invocation for
// anyone with more than ~6 skills — the model cannot match a request against a
// skill it cannot see.
const skillsContextListBudget = 4096

func skillsContext(options Options) string {
	if len(options.Skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<available_skills>\n")
	b.WriteString("Reusable, on-demand instruction sets you can load with the skill tool. Before acting on a request, scan this list; when the request matches a skill's name or description, call skill with its exact name FIRST and follow the loaded guidance — do not guess names, do not skip a matching skill, and do not substitute your own approach for its instructions.\n")
	listed, spent, omitted := 0, 0, 0
	for _, info := range options.Skills {
		name := strings.TrimSpace(info.Name)
		if name == "" {
			continue
		}
		line := "- " + name
		if desc := strings.TrimSpace(info.Description); desc != "" {
			line += ": " + truncateForSkillLine(desc)
		}
		line += "\n"
		// Always list at least one skill; past the budget, summarize the remainder
		// as a count instead of silently dropping it.
		if listed > 0 && spent+len(line) > skillsContextListBudget {
			omitted++
			continue
		}
		b.WriteString(line)
		spent += len(line)
		listed++
	}
	if omitted > 0 {
		b.WriteString("- …and " + strconv.Itoa(omitted) + " more (call skill with a name; an unknown name lists them all)\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}

// truncateForSkillLine keeps a skill's one-line description short so a single
// verbose description can't dominate the skills-list budget. The cap is roomy on
// purpose: the description carries the skill's trigger conditions ("use when…"),
// and truncating those is what stops the model from ever invoking the skill.
func truncateForSkillLine(desc string) string {
	const maxDescRunes = 200
	runes := []rune(desc)
	if len(runes) <= maxDescRunes {
		return desc
	}
	return strings.TrimSpace(string(runes[:maxDescRunes])) + "…"
}

func sessionRuntimeContext(options Options) string {
	provider := strings.TrimSpace(options.ProviderName)
	model := strings.TrimSpace(options.Model)
	if provider == "" && model == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<session>\n")
	if provider != "" {
		b.WriteString("Active provider: " + provider + "\n")
	}
	if model != "" {
		b.WriteString("Active model: " + model + "\n")
	}
	b.WriteString("Use the active provider/model above when answering questions about what is currently running. Persisted config commands may show saved defaults that differ from this live run/session.\n")
	b.WriteString("</session>")
	return b.String()
}

// workspaceSections splits the workspace context into three independent
// sections so a repo-map change does not resend the environment block or
// project guidelines. The joined prompt stays byte-identical to the old
// monolithic workspaceContext: env + "\n\n" + guidelines + "\n\n" + repo.
func workspaceSections(cwd string) (env, guidelines, repoSection string) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", "", ""
	}
	env = environmentContext(cwd)
	if raw := projectGuidelines(cwd, FindProjectGitRoot(cwd)); raw != "" {
		guidelines = strings.TrimLeft(raw, "\n")
	}
	repoSection = repoMapSection(cwd)
	return env, guidelines, repoSection
}

// joinWorkspaceSections recombines split sections into the single
// projectContext string used for fingerprinting.
func joinWorkspaceSections(env, guidelines, repoSection string) string {
	var parts []string
	if env != "" {
		parts = append(parts, env)
	}
	if guidelines != "" {
		parts = append(parts, guidelines)
	}
	if repoSection != "" {
		parts = append(parts, repoSection)
	}
	return strings.Join(parts, "\n\n")
}

// environmentContext returns the <environment> block describing the working
// directory, OS, and git branch. Returns "" when cwd is unset.
func environmentContext(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("<environment>\n")
	b.WriteString("Working directory: " + cwd + "\n")
	b.WriteString("Operating system: " + runtime.GOOS + "\n")
	b.WriteString(tools.HostShellEnvironmentGuidance() + "\n")
	if branch := gitBranchForPrompt(cwd); branch != "" {
		b.WriteString("Git branch: " + branch + "\n")
	}
	b.WriteString("</environment>")
	return b.String()
}

// repoMapSection returns the repo-map section ("## Repo map" + content) or ""
// when the scan yields nothing.
func repoMapSection(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	if repoMap := repoMapContext(cwd); repoMap != "" {
		return "## Repo map\n\n" + repoMap
	}
	return ""
}

// workspaceContext returns an <environment> block describing the working
// directory, git branch, and any project guideline doc, so the model grounds its
// work in the actual repo. Returns "" when cwd is unset (keeps headless/test
// runs deterministic).
func workspaceContext(cwd string) string {
	env, guidelines, repoSection := workspaceSections(cwd)
	return joinWorkspaceSections(env, guidelines, repoSection)
}

// projectGuidelines walks the directory chain from the git root to cwd
// (inclusive), finds the first matching project context file at each level
// (case-insensitive basename match), reads it, and returns the joined
// content in general-to-specific order — the most general file (at the git
// root) appears first, the most specific (at cwd) last. Each file is capped
// at maxProjectContextBytes; the total across all files is capped at
// maxProjectContextTotalBytes. Returns "" when no file matches.
func projectGuidelines(cwd, gitRoot string) string {
	dirs := projectGuidelineDirs(cwd, gitRoot)
	if len(dirs) == 0 {
		return ""
	}
	var b strings.Builder
	totalUsed := 0
	for _, dir := range dirs {
		if totalUsed >= maxProjectContextTotalBytes {
			break
		}
		match := findProjectContextFile(dir)
		if match == "" {
			continue
		}
		data, err := os.ReadFile(match)
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		// Per-file cap is the lesser of the configured per-file limit and
		// whatever remains of the total budget, so a single file can never
		// blow past either bound.
		limit := maxProjectContextBytes
		if remaining := maxProjectContextTotalBytes - totalUsed; remaining < limit {
			limit = remaining
		}
		content = truncateGuidelineContent(content, limit)
		label := projectGuidelineLabel(match, gitRoot)
		b.WriteString("\n\n## Project guidelines (" + label + ")\n\n" + content)
		totalUsed += len(content)
	}
	return b.String()
}

// userGuidelines returns the per-user QG.md instructions block, if present.
// The file lives in config.UserConfigDir()/qg/ next to QG's other
// per-user config; the basename match is case-insensitive so a file saved as
// qg.md still resolves on case-sensitive filesystems, mirroring the project
// guideline loader. The section carries an explicit precedence note because
// this is a global, personal preferences file: it is injected earlier in the
// prompt than the project's AGENTS.md/QG.md (see buildSystemPrompt), and
// the note keeps that precedence unambiguous even if a model weighs later
// context more heavily than section order alone implies.
func userGuidelines() string {
	configDir, err := userConfigDirForPrompt()
	if err != nil {
		return ""
	}
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return ""
	}
	match := findCaseInsensitiveFile(filepath.Join(configDir, "qg"), userContextFile)
	if match == "" {
		return ""
	}
	data, err := os.ReadFile(match)
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}
	content = truncateGuidelineContent(content, maxProjectContextBytes)
	return "## User guidelines (" + filepath.Base(match) + ")\n\n" +
		"These are the operator's personal preferences, not project policy. " +
		"Where they conflict with a repository's project guidelines below (AGENTS.md/QG.md), the project guidelines take precedence.\n\n" +
		content
}

// truncationMarker is appended to guideline content that was cut short.
const truncationMarker = "\n… (truncated)"

// truncateGuidelineContent caps content at limit bytes without splitting a
// UTF-8 rune, appending a truncation marker when anything was cut. Space for
// the marker is reserved before choosing the cut point. When limit is too
// small to fit the marker itself, the marker is dropped instead, so the
// returned string never exceeds limit for any non-negative limit.
func truncateGuidelineContent(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	if limit <= 0 {
		return ""
	}
	cut := limit - len(truncationMarker)
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	if truncated := content[:cut] + truncationMarker; len(truncated) <= limit {
		return truncated
	}
	// limit is smaller than the marker itself: fall back to a hard,
	// rune-safe cut at limit bytes with no marker rather than exceeding it.
	end := limit
	for end > 0 && !utf8.RuneStart(content[end]) {
		end--
	}
	return content[:end]
}

// findCaseInsensitiveFile returns the on-disk path of the regular file in dir
// whose name matches basename case-insensitively, or "" when absent.
func findCaseInsensitiveFile(dir, basename string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), basename) {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// projectGuidelineDirs returns the directory chain from gitRoot to cwd
// (inclusive), in root-to-leaf order. If gitRoot is empty or unreachable
// from cwd, the chain collapses to [cwd].
func projectGuidelineDirs(cwd, gitRoot string) []string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil
	}
	gitRoot = strings.TrimSpace(gitRoot)
	if gitRoot == "" {
		return []string{cwd}
	}
	rel, err := filepath.Rel(gitRoot, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return []string{cwd}
	}
	if rel == "." {
		return []string{gitRoot}
	}
	dirs := []string{gitRoot}
	cur := gitRoot
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		if seg == "" || seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		dirs = append(dirs, cur)
	}
	return dirs
}

// projectGuidelineLabel renders the path of match as a short label relative
// to gitRoot. When gitRoot is empty, the basename is returned.
func projectGuidelineLabel(match, gitRoot string) string {
	if gitRoot == "" {
		return filepath.Base(match)
	}
	rel, err := filepath.Rel(gitRoot, match)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(match)
	}
	return rel
}

// findProjectContextFile returns the first matching project guideline file
// in dir. Lookup is case-insensitive on the basename and on the actual
// filename returned, so a git-tracked file like AGENTS.md still resolves to
// its true filename on case-sensitive filesystems — the returned path is what
// should appear in the project guidelines label.
// Returns "" when nothing matches.
func findProjectContextFile(dir string) string {
	for _, name := range projectContextFiles {
		// Walk to the file's parent through dir with case-insensitive segment
		// matching, then find a regular file with the same basename. This works
		// on both case-sensitive and case-insensitive filesystems and always
		// returns the on-disk filename.
		parent, ok := resolveDirCaseInsensitive(filepath.Dir(filepath.Join(dir, name)), dir)
		if !ok {
			continue
		}
		if match := findCaseInsensitiveFile(parent, filepath.Base(name)); match != "" {
			return match
		}
	}
	return ""
}

// resolveDirCaseInsensitive walks from anchor to target, matching each
// segment case-insensitively. Returns the resolved absolute path and true on
// success, or "" and false if any segment cannot be found.
func resolveDirCaseInsensitive(target, anchor string) (string, bool) {
	if target == "" || target == "." {
		return anchor, true
	}
	rel, err := filepath.Rel(anchor, target)
	if err != nil {
		return "", false
	}
	if rel == "." {
		return anchor, true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := anchor
	for _, p := range parts {
		if p == "" {
			continue
		}
		entries, err := os.ReadDir(cur)
		if err != nil {
			return "", false
		}
		found := false
		for _, e := range entries {
			if e.IsDir() && strings.EqualFold(e.Name(), p) {
				cur = filepath.Join(cur, e.Name())
				found = true
				break
			}
		}
		if !found {
			return "", false
		}
	}
	return cur, true
}

// FindProjectGitRoot returns the nearest ancestor of cwd that contains a
// .git entry (file or directory). Returns "" when no git root is found, so
// the caller can fall back to cwd-only lookup.
func FindProjectGitRoot(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	cur := cwd
	for {
		if HasGitMetadata(cur) {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return ""
		}
		cur = parent
	}
}

func HasGitMetadata(dir string) bool {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	if info.IsDir() {
		_, err := os.Stat(filepath.Join(gitPath, "HEAD"))
		return err == nil
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(data)), "gitdir: ")
}

func workspaceSeedContext(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return ""
	}
	seed, err := workspaceseed.BuildFromWorkspace(cwd, workspaceseed.GitInfo{
		Branch: gitBranchForPrompt(cwd),
	})
	if err != nil {
		return ""
	}
	rendered := strings.TrimSpace(workspaceseed.Render(seed, workspaceseed.RenderOptions{
		MaxLines: workspaceSeedMaxLines,
		Width:    workspaceSeedWidth,
	}))
	if rendered == "" {
		return ""
	}
	return "<workspace_seed>\n" + rendered + "\n</workspace_seed>"
}

func repoMapContext(cwd string) string {
	// repomap.Scan is best-effort supplemental context for the prompt. If it
	// fails, omit the repo map instead of failing the agent run; successful scans
	// are still capped by repomap.RenderPrompt and maxRepoMapContextBytes.
	snapshot, err := repomap.Scan(cwd, repomap.Options{
		MaxFiles: 300,
		MaxDepth: 5,
	})
	if err != nil {
		return ""
	}
	return repomap.RenderPrompt(snapshot, maxRepoMapContextBytes)
}

// gitBranchForPrompt reads the current branch (or short SHA when detached) for
// cwd, handling both a regular checkout (.git dir) and a worktree (.git file).
// Returns "" on any problem — the prompt simply omits the branch segment.
func gitBranchForPrompt(cwd string) string {
	gitRoot := FindProjectGitRoot(cwd)
	if gitRoot == "" {
		return ""
	}
	gitPath := filepath.Join(gitRoot, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	headPath := filepath.Join(gitPath, "HEAD")
	if !info.IsDir() {
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return ""
		}
		dir := strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir: ")
		if dir == "" {
			return ""
		}
		// In worktree mode the gitdir is often RELATIVE (e.g.
		// "gitdir: ../.git/worktrees/<name>") — resolve it against the worktree root (gitRoot), not the
		// process working directory, or HEAD lookup fails and we drop the branch.
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(gitRoot, dir)
		}
		headPath = filepath.Join(dir, "HEAD")
	}
	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(data))
	if strings.HasPrefix(ref, "ref: ") {
		return strings.TrimPrefix(strings.TrimPrefix(ref, "ref: "), "refs/heads/")
	}
	if len(ref) >= 7 {
		return ref[:7]
	}
	return ref
}

func permissionModeContext(options Options) string {
	switch options.PermissionMode {
	case PermissionModePlan:
		return "Plan mode is active on this session. Your role is read-only exploration and planning: inspect the workspace and shape the plan with update_plan, but do not make changes to files or execute commands."
	default:
		return ""
	}
}
