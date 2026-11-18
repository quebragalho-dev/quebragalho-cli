package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
	"mvdan.cc/sh/v3/syntax"
)

type shellCommandSegment struct {
	tokens       []string
	dir          string
	dirAmbiguous bool
}

func proposedCommandPrefix(toolName string, args map[string]any) []string {
	return proposedCommandPrefixInDir(toolName, args, "")
}

func proposedCommandPrefixInDir(toolName string, args map[string]any, baseDir string) []string {
	if !isShellCommandTool(toolName) {
		return nil
	}
	command, ok := firstStringArg(args, "command", "cmd", "script", "shell")
	if !ok {
		return nil
	}
	startDir, startAmbiguous := resolveToolCommandStartDir(args, baseDir)
	segments, ok := safeShellCommandSegmentsWithDir(command, startDir, startAmbiguous)
	if !ok {
		return nil
	}
	if requested, ok := requestedPrefixRule(args); ok {
		if safeRequestedPrefixForSegmentsInDir(requested, segments) {
			return requested
		}
		return nil
	}
	// Only propose approving a prefix of one segment when every other segment
	// in the command is independently known-safe. Once a prefix is approved,
	// shellExecutionArgsForApproval escalates the whole command (every
	// segment, not just the approved one) to bypass the sandbox, so offering a
	// prefix that leaves an MSYS-prone (or otherwise unsafe) segment uncovered
	// would let that segment run unsandboxed without ever being reviewed.
	for index, seg := range segments {
		if knownSafeCommandSegmentInDir(seg) {
			continue
		}
		if !segmentAllowsPrefixProposal(seg) || !sandbox.ValidCommandPrefix(seg.tokens) || !otherSegmentsKnownSafeInDir(segments, index) {
			return nil
		}
		return append([]string(nil), seg.tokens...)
	}
	if len(segments) == 0 || !segmentAllowsPrefixProposal(segments[0]) || !sandbox.ValidCommandPrefix(segments[0].tokens) {
		return nil
	}
	return append([]string(nil), segments[0].tokens...)
}

// otherSegmentsKnownSafeInDir reports whether every segment other than the one
// at skip is known-safe on its own in its effective working directory.
func otherSegmentsKnownSafeInDir(segments []shellCommandSegment, skip int) bool {
	for index, seg := range segments {
		if index == skip {
			continue
		}
		if !knownSafeCommandSegmentInDir(seg) {
			return false
		}
	}
	return true
}

func segmentAllowsPrefixProposal(seg shellCommandSegment) bool {
	if len(seg.tokens) == 0 {
		return false
	}
	if commandName(seg.tokens[0]) == "git" && safeGitCommand(seg.tokens) {
		return gitSegmentConfigSafe(seg)
	}
	return true
}

func matchCommandPrefix(toolName string, args map[string]any, options Options) (sandbox.CommandPrefixGrant, bool, bool) {
	if !isShellCommandTool(toolName) || options.Sandbox == nil {
		return sandbox.CommandPrefixGrant{}, false, false
	}
	if shellCommandAdditionalPermissionsRequested(args) {
		return sandbox.CommandPrefixGrant{}, false, false
	}
	command, ok := firstStringArg(args, "command", "cmd", "script", "shell")
	if !ok {
		return sandbox.CommandPrefixGrant{}, false, false
	}
	startDir, startAmbiguous := resolveToolCommandStartDir(args, effectiveCommandBaseDir(options))
	segments, ok := safeShellCommandSegmentsWithDir(command, startDir, startAmbiguous)
	if !ok {
		return sandbox.CommandPrefixGrant{}, false, false
	}
	var matched sandbox.CommandPrefixGrant
	matchedAny := false
	matchedSession := false
	for _, seg := range segments {
		if commandName(seg.tokens[0]) == "git" && safeGitCommand(seg.tokens) && !gitSegmentConfigSafe(seg) {
			return sandbox.CommandPrefixGrant{}, false, false
		}
		if grant, ok := options.Sandbox.LookupCommandPrefix(toolName, seg.tokens); ok {
			if !matchedAny {
				matched = grant
			}
			matchedAny = true
			continue
		}
		if grant, ok := options.Sandbox.LookupCommandPrefixForSession(toolName, seg.tokens); ok {
			if !matchedAny {
				matched = grant
			}
			matchedAny = true
			matchedSession = true
			continue
		}
		if knownSafeCommandSegmentInDir(seg) {
			continue
		}
		return sandbox.CommandPrefixGrant{}, false, false
	}
	if matchedAny {
		return matched, true, matchedSession
	}
	return sandbox.CommandPrefixGrant{}, false, false
}

func shellExecutionArgsForApproval(toolName string, args map[string]any, action PermissionDecisionAction, options Options) map[string]any {
	if !isShellCommandTool(toolName) || !shellPrefixApprovalBypassesSandbox(action) {
		return args
	}
	if options.Sandbox == nil || !options.Sandbox.UnsandboxedExecutionAllowed() {
		return args
	}
	if shellCommandAdditionalPermissionsRequested(args) || shellCommandRequiresEscalated(args) {
		return args
	}
	planned := cloneArgs(args)
	if planned == nil {
		planned = map[string]any{}
	}
	planned["sandbox_permissions"] = string(tools.SandboxPermissionsRequireEscalated)
	return planned
}

func shellPrefixApprovalBypassesSandbox(action PermissionDecisionAction) bool {
	return action == PermissionDecisionAllowPrefix || action == PermissionDecisionAlwaysAllowPrefix
}

func shellCommandRequiresEscalated(args map[string]any) bool {
	raw, ok := args["sandbox_permissions"]
	if !ok || raw == nil {
		return false
	}
	value, ok := raw.(string)
	if !ok {
		value = fmt.Sprint(raw)
	}
	return strings.TrimSpace(value) == string(tools.SandboxPermissionsRequireEscalated)
}

func isShellCommandTool(toolName string) bool {
	return toolName == "bash" || toolName == "exec_command"
}

func firstStringArg(args map[string]any, names ...string) (string, bool) {
	for _, name := range names {
		if raw, ok := args[name].(string); ok {
			value := strings.TrimSpace(raw)
			if value != "" {
				return value, true
			}
		}
	}
	return "", false
}

func requestedPrefixRule(args map[string]any) ([]string, bool) {
	raw, ok := args["prefix_rule"]
	if !ok {
		return nil, false
	}
	switch value := raw.(type) {
	case []string:
		return cleanPrefixRule(value), true
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			part, ok := item.(string)
			if !ok {
				return nil, true
			}
			out = append(out, part)
		}
		return cleanPrefixRule(out), true
	default:
		return nil, true
	}
}

func cleanPrefixRule(prefix []string) []string {
	cleaned := make([]string, 0, len(prefix))
	for _, part := range prefix {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil
		}
		cleaned = append(cleaned, part)
	}
	return cleaned
}

func safeRequestedPrefixForSegmentsInDir(prefix []string, segments []shellCommandSegment) bool {
	if len(prefix) == 0 || !sandbox.ValidCommandPrefix(prefix) {
		return false
	}
	matched := false
	for _, seg := range segments {
		if !segmentAllowsPrefixProposal(seg) {
			return false
		}
		if len(prefix) > len(seg.tokens) {
			if knownSafeCommandSegmentInDir(seg) {
				continue
			}
			return false
		}
		if hasStringPrefix(seg.tokens, prefix) {
			matched = true
			continue
		}
		if knownSafeCommandSegmentInDir(seg) {
			continue
		}
		return false
	}
	return matched
}

func effectiveCommandBaseDir(options Options) string {
	if base := strings.TrimSpace(options.Cwd); base != "" {
		return base
	}
	if options.Sandbox != nil {
		return strings.TrimSpace(options.Sandbox.WorkspaceRoot())
	}
	return ""
}

func resolveToolCommandStartDir(args map[string]any, baseDir string) (string, bool) {
	base := strings.TrimSpace(baseDir)
	argDir, hasArg := firstStringArg(args, "workdir", "cwd", "dir", "directory")
	if !hasArg || filepath.Clean(argDir) == "." {
		return base, false
	}
	if filepath.IsAbs(argDir) {
		return filepath.Clean(argDir), false
	}
	if base == "" {
		return "", true
	}
	return filepath.Clean(filepath.Join(base, argDir)), false
}

type shellDirState struct {
	dir       string
	ambiguous bool
}

func safeShellCommandSegmentsWithDir(command string, startDir string, startAmbiguous bool) ([]shellCommandSegment, bool) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil || len(file.Stmts) == 0 {
		return nil, false
	}
	segments := make([]shellCommandSegment, 0, len(file.Stmts))
	state := shellDirState{dir: startDir, ambiguous: startAmbiguous}
	for _, stmt := range file.Stmts {
		if !collectSafeShellStatement(stmt, &state, &segments) {
			return nil, false
		}
	}
	if len(segments) == 0 {
		return nil, false
	}
	return segments, true
}

func collectSafeShellStatement(stmt *syntax.Stmt, state *shellDirState, segments *[]shellCommandSegment) bool {
	if stmt == nil || stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 {
		return false
	}
	return collectSafeShellCommand(stmt.Cmd, state, segments)
}

func collectSafeShellCommand(cmd syntax.Command, state *shellDirState, segments *[]shellCommandSegment) bool {
	switch node := cmd.(type) {
	case *syntax.CallExpr:
		tokens, ok := literalCallTokens(node)
		if !ok {
			return false
		}
		*segments = append(*segments, shellCommandSegment{
			tokens:       tokens,
			dir:          state.dir,
			dirAmbiguous: state.ambiguous,
		})
		if commandName(tokens[0]) == "cd" {
			nextDir, ok := resolveCDTargetDir(state.dir, tokens[1:])
			if !ok {
				state.ambiguous = true
			} else {
				state.dir = nextDir
			}
		}
		return true
	case *syntax.BinaryCmd:
		switch node.Op {
		case syntax.AndStmt:
			return collectSafeShellStatement(node.X, state, segments) && collectSafeShellStatement(node.Y, state, segments)
		case syntax.OrStmt:
			before := *state
			if !collectSafeShellStatement(node.X, state, segments) {
				return false
			}
			afterX := *state
			yState := before
			if afterX != before {
				yState.ambiguous = true
			}
			if !collectSafeShellStatement(node.Y, &yState, segments) {
				return false
			}
			if afterX != before || yState != before {
				state.ambiguous = true
			}
			return true
		case syntax.Pipe:
			// Each pipeline stage executes in a subshell, so a `cd` inside one
			// stage neither changes sibling stages nor escapes to outer commands.
			leftState := *state
			rightState := *state
			return collectSafeShellStatement(node.X, &leftState, segments) && collectSafeShellStatement(node.Y, &rightState, segments)
		default:
			return false
		}
	default:
		return false
	}
}

func resolveCDTargetDir(currentDir string, args []string) (string, bool) {
	index := 0
	for index < len(args) {
		arg := args[index]
		if arg == "--" {
			index++
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			break
		}
		switch arg {
		case "-L", "-P", "-e", "-@":
			index++
		default:
			return "", false
		}
	}
	if len(args)-index != 1 {
		return "", false
	}
	target := args[index]
	if target == "" || target == "-" || strings.HasPrefix(target, "~") {
		return "", false
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target), true
	}
	if strings.TrimSpace(currentDir) == "" {
		return "", false
	}
	return filepath.Clean(filepath.Join(currentDir, target)), true
}

func literalCallTokens(call *syntax.CallExpr) ([]string, bool) {
	if call == nil || len(call.Assigns) > 0 || len(call.Args) == 0 {
		return nil, false
	}
	tokens := make([]string, 0, len(call.Args))
	for _, word := range call.Args {
		if len(word.Parts) != 1 {
			return nil, false
		}
		lit, ok := word.Parts[0].(*syntax.Lit)
		if !ok || strings.ContainsAny(lit.Value, "*?[]{}") {
			return nil, false
		}
		tokens = append(tokens, lit.Value)
	}
	return tokens, true
}

func knownSafeCommandSegment(command []string) bool {
	return knownSafeCommandSegmentInDir(shellCommandSegment{tokens: command})
}

func knownSafeCommandSegmentInDir(seg shellCommandSegment) bool {
	command := seg.tokens
	if len(command) == 0 {
		return false
	}
	name := commandName(command[0])
	if runtime.GOOS == "windows" && tools.MsysProneCommandName(name) {
		return false
	}
	switch name {
	case "cat", "cd", "cut", "echo", "expr", "false", "grep", "head", "id",
		"ls", "nl", "paste", "pwd", "rev", "seq", "stat", "tail", "tr",
		"true", "uname", "uniq", "wc", "which", "whoami":
		return true
	case "base64":
		return safeBase64Command(command[1:])
	case "find":
		return safeFindCommand(command[1:])
	case "rg":
		return safeRipgrepCommand(command[1:])
	case "sed":
		return safeSedCommand(command[1:])
	case "git":
		return safeGitCommand(command) && gitSegmentConfigSafe(seg)
	default:
		return false
	}
}

func gitSegmentConfigSafe(seg shellCommandSegment) bool {
	if seg.dirAmbiguous {
		return false
	}
	if strings.TrimSpace(seg.dir) == "" {
		return true
	}
	return gitRepoConfigSafe(seg.dir)
}

const gitConfigCheckTimeout = 5 * time.Second

// gitRepoConfigSafe inspects the repository's effective local/worktree/command
// git config (including [include]/[includeIf] targets) and fails closed if any
// active setting can spawn an external program.
func gitRepoConfigSafe(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), gitConfigCheckTimeout)
	defer cancel()

	out, err := runGitConfigList(ctx, dir, true)
	if err == nil {
		return parseAndValidateGitConfigScopeList(out)
	}
	if isUnknownGitShowScopeError(err) {
		localOut, localErr := runGitConfigList(ctx, dir, false)
		if localErr != nil {
			return false
		}
		return parseAndValidateGitConfigLocalList(localOut)
	}
	return false
}

type gitConfigListError struct {
	err    error
	stderr string
}

func (e gitConfigListError) Error() string { return e.err.Error() }

func isUnknownGitShowScopeError(err error) bool {
	var cfgErr gitConfigListError
	if !errors.As(err, &cfgErr) {
		return false
	}
	lower := strings.ToLower(cfgErr.stderr)
	return strings.Contains(lower, "unknown option") || strings.Contains(lower, "usage:")
}

func runGitConfigList(ctx context.Context, dir string, withScope bool) ([]byte, error) {
	args := []string{"--no-pager", "-C", dir, "config", "--list", "--includes"}
	if withScope {
		args = append(args, "--show-scope")
	} else {
		args = append(args, "--local")
	}
	args = append(args, "--null")
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, gitConfigListError{err: err, stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

func parseAndValidateGitConfigScopeList(data []byte) bool {
	records := splitGitNullRecords(data)
	if len(records)%2 != 0 {
		return false
	}
	effective := map[string][]string{}
	for i := 0; i < len(records); i += 2 {
		scope := string(records[i])
		if scope != "local" && scope != "worktree" && scope != "command" {
			continue
		}
		recordGitConfigEntry(effective, string(records[i+1]))
	}
	return gitConfigValuesSafe(effective)
}

func parseAndValidateGitConfigLocalList(data []byte) bool {
	records := splitGitNullRecords(data)
	effective := map[string][]string{}
	for _, rec := range records {
		recordGitConfigEntry(effective, string(rec))
	}
	if vals := effective["extensions.worktreeconfig"]; len(vals) > 0 && isGitBoolTrue(vals[len(vals)-1]) {
		// Older git without --show-scope cannot enumerate worktree scope directly.
		return false
	}
	return gitConfigValuesSafe(effective)
}

func splitGitNullRecords(data []byte) [][]byte {
	if len(data) == 0 {
		return nil
	}
	parts := bytes.Split(data, []byte{0})
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func recordGitConfigEntry(effective map[string][]string, entry string) {
	key, value, hasValue := strings.Cut(entry, "\n")
	if key == "" {
		return
	}
	normKey := normalizeGitConfigKey(key)
	if isMultiValuedGitProgramKey(normKey) {
		if !hasValue || strings.TrimSpace(value) == "" {
			effective[normKey] = nil
			return
		}
		effective[normKey] = append(effective[normKey], value)
		return
	}
	if !hasValue {
		effective[normKey] = []string{"true"}
		return
	}
	effective[normKey] = []string{value}
}

func normalizeGitConfigKey(key string) string {
	firstDot := strings.IndexByte(key, '.')
	lastDot := strings.LastIndexByte(key, '.')
	if firstDot <= 0 || lastDot < firstDot {
		return strings.ToLower(key)
	}
	if firstDot == lastDot {
		return strings.ToLower(key)
	}
	return strings.ToLower(key[:firstDot]) + key[firstDot:lastDot+1] + strings.ToLower(key[lastDot+1:])
}

func isMultiValuedGitProgramKey(key string) bool {
	section, _, variable, _ := splitGitConfigKey(key)
	return section == "credential" && variable == "helper"
}

func splitGitConfigKey(key string) (section, subsection, variable string, hasSubsection bool) {
	firstDot := strings.IndexByte(key, '.')
	lastDot := strings.LastIndexByte(key, '.')
	if firstDot <= 0 || lastDot < firstDot || lastDot == len(key)-1 {
		return "", "", "", false
	}
	section = strings.ToLower(key[:firstDot])
	variable = strings.ToLower(key[lastDot+1:])
	if lastDot > firstDot {
		return section, key[firstDot+1 : lastDot], variable, true
	}
	return section, "", variable, false
}

func gitConfigValuesSafe(effective map[string][]string) bool {
	for key, values := range effective {
		for _, value := range values {
			if gitConfigKeyRunsProgram(key, value) {
				return false
			}
		}
	}
	return true
}

func gitConfigKeyRunsProgram(key string, value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	section, _, variable, hasSub := splitGitConfigKey(key)
	switch {
	case !hasSub && section == "core" && variable == "fsmonitor":
		// Boolean true/false selects Git's built-in daemon or disables it;
		// any other value is a hook command path.
		return !isGitBool(trimmed)
	case !hasSub && section == "core" && (variable == "pager" || variable == "sshcommand" || variable == "askpass" || variable == "gitproxy" || variable == "alternaterefscommand"):
		return true
	case !hasSub && section == "pager":
		// pager.<cmd> may be a boolean toggle or a custom command string.
		return !isGitBool(trimmed)
	case !hasSub && section == "diff" && variable == "external":
		return true
	case hasSub && section == "diff" && (variable == "command" || variable == "textconv"):
		return true
	case hasSub && section == "filter" && (variable == "clean" || variable == "smudge" || variable == "process"):
		return true
	case section == "credential" && variable == "helper":
		return true
	case section == "gpg" && variable == "program":
		return true
	case !hasSub && section == "uploadpack" && variable == "packobjectshook":
		return true
	case hasSub && section == "remote" && (variable == "uploadpack" || variable == "receivepack"):
		return true
	case hasSub && (section == "mergetool" || section == "difftool" || section == "browser" || section == "man") && variable == "cmd":
		return true
	default:
		return false
	}
}

func isGitBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1", "false", "no", "off", "0":
		return true
	default:
		return false
	}
}

func isGitBoolTrue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1":
		return true
	default:
		return false
	}
}

func safeBase64Command(args []string) bool {
	for _, arg := range args {
		if arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "--output=") || (strings.HasPrefix(arg, "-o") && arg != "-o") {
			return false
		}
	}
	return true
}

func safeFindCommand(args []string) bool {
	unsafe := map[string]bool{
		"-exec": true, "-execdir": true, "-ok": true, "-okdir": true,
		"-delete": true,
		"-fls":    true, "-fprint": true, "-fprint0": true, "-fprintf": true,
	}
	for _, arg := range args {
		if unsafe[arg] {
			return false
		}
	}
	return true
}

func safeRipgrepCommand(args []string) bool {
	for _, arg := range args {
		if arg == "--search-zip" || arg == "-z" || arg == "--pre" || arg == "--hostname-bin" ||
			strings.HasPrefix(arg, "--pre=") || strings.HasPrefix(arg, "--hostname-bin=") {
			return false
		}
	}
	return true
}

func safeSedCommand(args []string) bool {
	if len(args) < 2 || len(args) > 3 || args[0] != "-n" {
		return false
	}
	return validSedPrintArg(args[1])
}

func validSedPrintArg(arg string) bool {
	if !strings.HasSuffix(arg, "p") {
		return false
	}
	body := strings.TrimSuffix(arg, "p")
	if body == "" {
		return false
	}
	for _, part := range strings.Split(body, ",") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func safeGitCommand(command []string) bool {
	subIndex, subcommand, ok := gitSubcommand(command)
	if !ok {
		return false
	}
	if gitHasUnsafeGlobalOption(command[1:subIndex]) {
		return false
	}
	args := command[subIndex+1:]
	switch subcommand {
	case "status", "log", "diff", "show":
		return gitArgsReadOnly(args)
	case "branch":
		return gitArgsReadOnly(args) && gitBranchReadOnly(args)
	default:
		return false
	}
}

func gitSubcommand(command []string) (int, string, bool) {
	for index := 1; index < len(command); index++ {
		arg := command[index]
		if gitOptionConsumesValue(arg) {
			index++
			continue
		}
		if gitOptionHasInlineValue(arg) || arg == "--" || strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "status", "log", "diff", "show", "branch":
			return index, arg, true
		default:
			return 0, "", false
		}
	}
	return 0, "", false
}

func gitOptionConsumesValue(arg string) bool {
	switch arg {
	case "-C", "-c", "--config-env", "--exec-path", "--git-dir", "--namespace", "--super-prefix", "--work-tree":
		return true
	default:
		return false
	}
}

func gitOptionHasInlineValue(arg string) bool {
	return strings.HasPrefix(arg, "--config-env=") ||
		strings.HasPrefix(arg, "--exec-path=") ||
		strings.HasPrefix(arg, "--git-dir=") ||
		strings.HasPrefix(arg, "--namespace=") ||
		strings.HasPrefix(arg, "--super-prefix=") ||
		strings.HasPrefix(arg, "--work-tree=") ||
		((strings.HasPrefix(arg, "-C") || strings.HasPrefix(arg, "-c")) && len(arg) > 2)
}

func gitHasUnsafeGlobalOption(args []string) bool {
	for _, arg := range args {
		if strings.HasPrefix(arg, "--upload-pack") || gitOptionConsumesValue(arg) || gitOptionHasInlineValue(arg) {
			return true
		}
	}
	return false
}

// gitUnsafeLongOptions are subcommand options that write a file or run an
// external program. git accepts any unique prefix of a long option, so
// "--outp=x" is "--output=x"; a prefix match fails closed.
var gitUnsafeLongOptions = []string{"--output", "--exec", "--ext-diff", "--textconv"}

func gitArgIsUnsafeLongOption(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	if len(name) < 3 || !strings.HasPrefix(name, "--") {
		return false
	}
	for _, option := range gitUnsafeLongOptions {
		if strings.HasPrefix(option, name) {
			// Note: "--text" fails closed as a prefix of "--textconv".
			return true
		}
	}
	return false
}

func gitArgsReadOnly(args []string) bool {
	for _, arg := range args {
		if gitArgIsUnsafeLongOption(arg) {
			return false
		}
	}
	return true
}

func gitBranchReadOnly(args []string) bool {
	if len(args) == 0 {
		return true
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--format=") {
			continue
		}
		switch arg {
		case "--list", "-l", "--show-current", "-a", "--all", "-r", "--remotes", "-v", "-vv", "--verbose":
			continue
		default:
			return false
		}
	}
	return true
}

func commandName(raw string) string {
	name := strings.TrimSpace(raw)
	if index := strings.LastIndexAny(name, `/\`); index >= 0 {
		name = name[index+1:]
	}
	name = strings.ToLower(name)
	for _, suffix := range []string{".exe", ".cmd", ".bat", ".com"} {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix)
		}
	}
	return name
}

func hasStringPrefix(values []string, prefix []string) bool {
	if len(prefix) == 0 || len(prefix) > len(values) {
		return false
	}
	for index := range prefix {
		if values[index] != prefix[index] {
			return false
		}
	}
	return true
}
