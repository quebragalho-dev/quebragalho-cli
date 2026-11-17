package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/imageinput"
	"github.com/quebragalho-dev/quebragalho-cli/internal/localcontrol"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

const localDesktopOutputBudgetBytes = localBrowserOutputBudgetBytes

var desktopQualifiedRefPattern = regexp.MustCompile(`^@[A-Za-z0-9_-]+:e[1-9][0-9]*$`)

var desktopValidSurfaces = map[string]bool{
	"window":               true,
	"focused":              true,
	"menu":                 true,
	"menubar":              true,
	"sheet":                true,
	"popover":              true,
	"alert":                true,
	"desktop":              true,
	"taskbar":              true,
	"system-tray":          true,
	"quick-settings":       true,
	"notification-center":  true,
	"toolbar":              true,
	"dock":                 true,
	"spotlight":            true,
	"menu-bar-extras":      true,
	"system-tray-overflow": true,
	"start-menu":           true,
	"action-center":        true,
}

var desktopActionCommands = map[string]bool{
	"click":         true,
	"double-click":  true,
	"right-click":   true,
	"type":          true,
	"set-value":     true,
	"clear":         true,
	"focus":         true,
	"select":        true,
	"toggle":        true,
	"check":         true,
	"uncheck":       true,
	"expand":        true,
	"collapse":      true,
	"scroll":        true,
	"scroll-to":     true,
	"press":         true,
	"hover":         true,
	"launch":        true,
	"close-app":     true,
	"focus-window":  true,
	"list-apps":     true,
	"list-windows":  true,
	"wait":          true,
	"find":          true,
	"get":           true,
	"is":            true,
	"clipboard-get": true,
	"clipboard-set": true,
	"screenshot":    true,
	"skills":        true,
}

func desktopActionCommandNames() []string {
	names := make([]string, 0, len(desktopActionCommands))
	for name := range desktopActionCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// NewLocalDesktopTools returns the deferred desktop automation tools backed by
// agent-desktop. list-apps and list-windows live inside desktop_action to keep
// the deferred tool catalog compact.
func NewLocalDesktopTools(options localcontrol.DesktopOptions) []Tool {
	return []Tool{
		newDesktopSnapshotTool(options),
		newDesktopActionTool(options),
	}
}

type desktopSnapshotTool struct {
	baseTool
	desktop localcontrol.Desktop
}

func newDesktopSnapshotTool(options localcontrol.DesktopOptions) Tool {
	return desktopSnapshotTool{
		baseTool: baseTool{
			name:     "desktop_snapshot",
			deferred: true,
			description: `Capture desktop/computer use UI tree (janela, aplicativo/app, computador) via agent-desktop snapshot (-i --compact by default). ` +
				`Loop: snapshot -i -> act on @<snapshot_id>:eN ref (bare @e8 rejected) -> snapshot again to verify. ` +
				`On STALE_REF/AMBIGUOUS_TARGET, re-snapshot or pass window_id. Retry only if error.disposition.retry == "safe".`,
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"app":         {Type: "string", Description: "Application name from list-apps."},
					"window_id":   {Type: "string", Description: "Window ID from list-windows when an app has multiple windows."},
					"root":        {Type: "string", Description: "Qualified @<snapshot_id>:eN ref to drill into (cannot combine with surface)."},
					"skeleton":    {Type: "boolean", Description: "Shallow overview with children_count on cut containers.", Default: false},
					"interactive": {Type: "boolean", Description: "Include interactive elements only (-i).", Default: true},
					"compact":     {Type: "boolean", Description: "Collapse single-child unnamed wrappers (--compact).", Default: true},
					"surface":     {Type: "string", Description: "Surface to snapshot (window, menu, menubar, sheet, popover, alert, focused)."},
					"timeout_ms":  {Type: "integer", Description: "Observation deadline in ms (default 3000; raise for Chromium/Electron).", Minimum: intPtr(100)},
				},
				AdditionalProperties: false,
			},
			safety:       localControlSafety(options.Enabled, SideEffectLocalDesktop, "Reads a native desktop accessibility snapshot."),
			capabilities: ToolCapabilities{Effect: EffectInteractive, ThreadSafe: false},
		},
		desktop: localcontrol.NewDesktop(options),
	}
}

func (tool desktopSnapshotTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, err := desktopSnapshotArgs(args); err != nil {
		return errorResult("Error: Invalid arguments for desktop_snapshot: " + err.Error()), true
	}
	return Result{}, false
}

func (tool desktopSnapshotTool) Run(ctx context.Context, args map[string]any) Result {
	cmdArgs, err := desktopSnapshotArgs(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for desktop_snapshot: " + err.Error())
	}
	return desktopCommandResult(ctx, tool.desktop, desktopCommandPlan{
		command: "snapshot",
		args:    cmdArgs,
	})
}

type desktopActionTool struct {
	baseTool
	desktop localcontrol.Desktop
}

func newDesktopActionTool(options localcontrol.DesktopOptions) Tool {
	return desktopActionTool{
		baseTool: baseTool{
			name:     "desktop_action",
			deferred: true,
			description: `Run desktop/computer use UI action (clicar, digitar, abrir aplicativo/app, foco em janela no computador) via agent-desktop. ` +
				`Pass @<snapshot_id>:eN ref from desktop_snapshot (bare @e8 rejected). ` +
				`Loop: snapshot -i -> act -> snapshot to verify; on STALE_REF/AMBIGUOUS_TARGET re-snapshot or pass window_id. ` +
				`Retry only if error.disposition.retry == "safe". Call command="skills" (text="desktop" or "platform") for the full guide.`,
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"command": {Type: "string", Description: "agent-desktop command to execute.", Enum: desktopActionCommandNames()},
					"ref":     {Type: "string", Description: "Qualified @<snapshot_id>:eN element ref from desktop_snapshot/find."},
					"text":    {Type: "string", Description: "Text for type/set-value/clipboard-set (sent via --stdin), option for select, app for launch/close-app, text for find/wait, skill for skills, or path for screenshot."},
					"keys":    {Type: "string", Description: "Key combo for press (e.g. return, escape, cmd+c, shift+tab)."},
					"args":    {Type: "object", Description: "Optional command flags (e.g. app, window_id, direction, amount, property, role, name, exact, predicate, event, timeout_ms, xy, format, path)."},
					"headed":  {Type: "boolean", Description: "Pass global --headed for physical input/focus (required for double-click, hover).", Default: false},
				},
				Required:             []string{"command"},
				AdditionalProperties: false,
			},
			safety:       localControlSafety(options.Enabled, SideEffectLocalDesktop, "Operates native desktop applications and windows."),
			capabilities: ToolCapabilities{Effect: EffectInteractive, ThreadSafe: false},
		},
		desktop: localcontrol.NewDesktop(options),
	}
}

func (tool desktopActionTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, err := desktopActionPlan(args); err != nil {
		return errorResult("Error: Invalid arguments for desktop_action: " + err.Error()), true
	}
	return Result{}, false
}

func (tool desktopActionTool) Run(ctx context.Context, args map[string]any) Result {
	plan, err := desktopActionPlan(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for desktop_action: " + err.Error())
	}
	return desktopCommandResult(ctx, tool.desktop, plan)
}

type desktopCommandPlan struct {
	command        string
	args           []string
	stdin          string
	hasStdin       bool
	screenshotPath string
}

func desktopSnapshotArgs(args map[string]any) ([]string, error) {
	app, err := optionalDesktopFlagValue(args, "app")
	if err != nil {
		return nil, err
	}
	windowID, err := optionalDesktopFlagValue(args, "window_id")
	if err != nil {
		return nil, err
	}
	rootRaw, err := stringArgWithEmpty(args, "root", "", false, false)
	if err != nil {
		return nil, err
	}
	var root string
	if strings.TrimSpace(rootRaw) != "" {
		root, err = desktopRefFromString(rootRaw)
		if err != nil {
			return nil, err
		}
	}
	surface, err := optionalDesktopSurface(args, "surface")
	if err != nil {
		return nil, err
	}
	if root != "" && surface != "" {
		return nil, fmt.Errorf("root cannot be combined with surface")
	}
	skeleton, err := boolArg(args, "skeleton", false)
	if err != nil {
		return nil, err
	}
	interactive, err := boolArg(args, "interactive", true)
	if err != nil {
		return nil, err
	}
	compact, err := boolArg(args, "compact", true)
	if err != nil {
		return nil, err
	}
	timeoutMS, hasTimeout, err := optionalIntArg(args, "timeout_ms", 100, 0)
	if err != nil {
		return nil, err
	}

	cmdArgs := []string{"snapshot"}
	if app != "" {
		cmdArgs = append(cmdArgs, "--app", app)
	}
	if windowID != "" {
		cmdArgs = append(cmdArgs, "--window-id", windowID)
	}
	if root != "" {
		cmdArgs = append(cmdArgs, "--root", root)
	}
	if surface != "" {
		cmdArgs = append(cmdArgs, "--surface", surface)
	}
	if skeleton {
		cmdArgs = append(cmdArgs, "--skeleton")
	}
	if interactive {
		cmdArgs = append(cmdArgs, "-i")
	}
	if compact {
		cmdArgs = append(cmdArgs, "--compact")
	}
	if hasTimeout {
		cmdArgs = append(cmdArgs, "--timeout-ms", strconv.Itoa(timeoutMS))
	}
	return cmdArgs, nil
}

func desktopActionPlan(args map[string]any) (desktopCommandPlan, error) {
	command, err := stringArg(args, "command", "", true)
	if err != nil {
		return desktopCommandPlan{}, err
	}
	command = strings.ToLower(strings.TrimSpace(command))
	if !desktopActionCommands[command] {
		return desktopCommandPlan{}, fmt.Errorf("command must be one of: %s", strings.Join(desktopActionCommandNames(), ", "))
	}
	headed, err := boolArg(args, "headed", false)
	if err != nil {
		return desktopCommandPlan{}, err
	}
	extra, err := objectArg(args, "args", false)
	if err != nil {
		return desktopCommandPlan{}, err
	}
	consumed := map[string]bool{}

	plan := desktopCommandPlan{command: command}
	if headed {
		plan.args = append(plan.args, "--headed")
	}
	plan.args = append(plan.args, command)

	var tail []string
	switch command {
	case "click", "double-click", "right-click", "clear", "focus",
		"toggle", "check", "uncheck", "expand", "collapse", "scroll-to":
		ref, err := desktopRefArg(args)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if err := appendDesktopTimeoutMS(&plan.args, extra, consumed, 1); err != nil {
			return desktopCommandPlan{}, err
		}
		if err := appendDesktopWaitFor(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		tail = append(tail, ref)

	case "type":
		ref, err := desktopRefArg(args)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		text, err := desktopStdinText(args, true)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		plan.args = append(plan.args, "--stdin")
		if interval, ok, err := consumeIntArg(extra, consumed, "interval_ms", 1, 2000); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			plan.args = append(plan.args, "--interval-ms", strconv.Itoa(interval))
		}
		if err := appendDesktopTimeoutMS(&plan.args, extra, consumed, 0); err != nil {
			return desktopCommandPlan{}, err
		}
		if err := appendDesktopWaitFor(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		plan.stdin = text
		plan.hasStdin = true
		tail = append(tail, ref)

	case "set-value":
		ref, err := desktopRefArg(args)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		text, err := desktopStdinText(args, false)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		plan.args = append(plan.args, "--stdin")
		if err := appendDesktopTimeoutMS(&plan.args, extra, consumed, 1); err != nil {
			return desktopCommandPlan{}, err
		}
		if err := appendDesktopWaitFor(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		plan.stdin = text
		plan.hasStdin = true
		tail = append(tail, ref)

	case "select":
		ref, err := desktopRefArg(args)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		value, err := desktopTextOrArg(args, extra, consumed, "value", true)
		if err != nil {
			return desktopCommandPlan{}, fmt.Errorf("text (or args.value) is required for select: %w", err)
		}
		if err := appendDesktopTimeoutMS(&plan.args, extra, consumed, 1); err != nil {
			return desktopCommandPlan{}, err
		}
		if err := appendDesktopWaitFor(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		tail = append(tail, ref, "--", value)

	case "scroll":
		ref, err := desktopRefArg(args)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if dir, ok, err := consumeStringArg(extra, consumed, "direction"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			switch dir {
			case "up", "down", "left", "right":
				plan.args = append(plan.args, "--direction", dir)
			default:
				return desktopCommandPlan{}, fmt.Errorf("direction must be up, down, left, or right")
			}
		}
		if amount, ok, err := consumeIntArg(extra, consumed, "amount", 1, 0); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			plan.args = append(plan.args, "--amount", strconv.Itoa(amount))
		}
		if err := appendDesktopTimeoutMS(&plan.args, extra, consumed, 1); err != nil {
			return desktopCommandPlan{}, err
		}
		if err := appendDesktopWaitFor(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		tail = append(tail, ref)

	case "press":
		combo, err := desktopPressCombo(args, extra, consumed)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if _, err := appendDesktopAppAndWindow(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		if force, ok, err := consumeBoolArg(extra, consumed, "force"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok && force {
			plan.args = append(plan.args, "--force")
		}
		if err := appendDesktopWaitFor(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		tail = append(tail, "--", combo)

	case "hover":
		ref, hasRef, err := optionalDesktopRefArg(args)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		xy, hasXY, err := consumeStringArg(extra, consumed, "xy")
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if !hasRef && !hasXY {
			return desktopCommandPlan{}, fmt.Errorf("ref or args.xy is required for hover")
		}
		if hasRef && hasXY {
			return desktopCommandPlan{}, fmt.Errorf("hover accepts either ref or args.xy, not both")
		}
		if hasXY {
			if err := validateDesktopXY(xy); err != nil {
				return desktopCommandPlan{}, err
			}
			plan.args = append(plan.args, "--xy="+xy)
		}
		if err := appendDesktopTimeoutMS(&plan.args, extra, consumed, 1); err != nil {
			return desktopCommandPlan{}, err
		}
		if err := appendDesktopWaitFor(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		if hasRef {
			tail = append(tail, ref)
		}

	case "launch":
		app, err := desktopTextOrArg(args, extra, consumed, "app", true)
		if err != nil {
			return desktopCommandPlan{}, fmt.Errorf("text (or args.app) is required for launch: %w", err)
		}
		if err := rejectFlagLikeValue("app", app); err != nil {
			return desktopCommandPlan{}, err
		}
		if activate, ok, err := consumeBoolArg(extra, consumed, "activate"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok && activate {
			plan.args = append(plan.args, "--activate")
		}
		if noAttach, ok, err := consumeBoolArg(extra, consumed, "no_attach"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok && noAttach {
			plan.args = append(plan.args, "--no-attach")
		}
		if timeout, ok, err := consumeIntArg(extra, consumed, "timeout", 1, 0); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			plan.args = append(plan.args, "--timeout", strconv.Itoa(timeout))
		}
		if cwd, ok, err := consumeSingleLineArg(extra, consumed, "cwd"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			plan.args = append(plan.args, "--cwd", cwd)
		}
		if err := appendDesktopLaunchCDP(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		if err := appendDesktopLaunchEnvAndArgs(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		tail = append(tail, "--", app)

	case "close-app":
		app, err := desktopTextOrArg(args, extra, consumed, "app", true)
		if err != nil {
			return desktopCommandPlan{}, fmt.Errorf("text (or args.app) is required for close-app: %w", err)
		}
		if err := rejectFlagLikeValue("app", app); err != nil {
			return desktopCommandPlan{}, err
		}
		if force, ok, err := consumeBoolArg(extra, consumed, "force"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok && force {
			plan.args = append(plan.args, "--force")
		}
		tail = append(tail, "--", app)

	case "focus-window":
		hasID, err := appendDesktopAppAndWindow(&plan.args, extra, consumed)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if title, ok, err := consumeSingleLineArg(extra, consumed, "title"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			plan.args = append(plan.args, "--title", title)
			hasID = true
		}
		if !hasID {
			return desktopCommandPlan{}, fmt.Errorf("args.window_id, args.app, or args.title is required for focus-window")
		}

	case "list-apps", "list-windows":
		if app, ok, err := consumeSingleLineArg(extra, consumed, "app"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			if err := rejectFlagLikeValue("app", app); err != nil {
				return desktopCommandPlan{}, err
			}
			plan.args = append(plan.args, "--app", app)
		}
		if err := appendDesktopTimeoutMS(&plan.args, extra, consumed, 1); err != nil {
			return desktopCommandPlan{}, err
		}

	case "wait":
		var err error
		tail, err = appendDesktopWaitArgs(&plan.args, args, extra, consumed)
		if err != nil {
			return desktopCommandPlan{}, err
		}

	case "find":
		if err := appendDesktopFindArgs(&plan.args, args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}

	case "get":
		ref, err := desktopRefArg(args)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if prop, ok, err := consumeStringArg(extra, consumed, "property"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			switch prop {
			case "text", "value", "title", "bounds", "role", "states":
				plan.args = append(plan.args, "--property", prop)
			default:
				return desktopCommandPlan{}, fmt.Errorf("property must be text, value, title, bounds, role, or states")
			}
		}
		tail = append(tail, ref)

	case "is":
		ref, err := desktopRefArg(args)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if prop, ok, err := consumeStringArg(extra, consumed, "property"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			switch prop {
			case "visible", "enabled", "checked", "focused", "expanded", "selected":
				plan.args = append(plan.args, "--property", prop)
			default:
				return desktopCommandPlan{}, fmt.Errorf("property must be visible, enabled, checked, focused, expanded, or selected")
			}
		}
		tail = append(tail, ref)

	case "clipboard-get":
		if format, ok, err := consumeStringArg(extra, consumed, "format"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			switch format {
			case "text", "auto", "image", "file-urls":
				plan.args = append(plan.args, "--format", format)
			default:
				return desktopCommandPlan{}, fmt.Errorf("format must be text, auto, image, or file-urls")
			}
		}
		if out, ok, err := consumeSingleLineArg(extra, consumed, "out"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			plan.args = append(plan.args, "--out", out)
		}

	case "clipboard-set":
		hasFileOrImg := false
		if img, ok, err := consumeSingleLineArg(extra, consumed, "image"); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			plan.args = append(plan.args, "--image", img)
			hasFileOrImg = true
		}
		if urls, err := consumeStringOrSliceArg(extra, consumed, "file_url"); err != nil {
			return desktopCommandPlan{}, err
		} else if len(urls) > 0 {
			for _, u := range urls {
				plan.args = append(plan.args, "--file-url", u)
			}
			hasFileOrImg = true
		}
		if !hasFileOrImg {
			text, err := desktopStdinText(args, false)
			if err != nil {
				return desktopCommandPlan{}, err
			}
			plan.args = append(plan.args, "--stdin")
			plan.stdin = text
			plan.hasStdin = true
		}

	case "screenshot":
		if _, err := appendDesktopAppAndWindow(&plan.args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
		if screen, ok, err := consumeIntArg(extra, consumed, "screen", 0, 0); err != nil {
			return desktopCommandPlan{}, err
		} else if ok {
			plan.args = append(plan.args, "--screen", strconv.Itoa(screen))
		}
		path, err := desktopTextOrArg(args, extra, consumed, "path", false)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if path != "" {
			if strings.ContainsAny(path, "\r\n") {
				return desktopCommandPlan{}, fmt.Errorf("path must be a single line")
			}
			plan.screenshotPath = path
			tail = append(tail, "--", path)
		}

	case "skills":
		if err := appendDesktopSkillsArgs(&plan.args, args, extra, consumed); err != nil {
			return desktopCommandPlan{}, err
		}
	}

	if err := rejectUnconsumedArgs(command, extra, consumed); err != nil {
		return desktopCommandPlan{}, err
	}
	plan.args = append(plan.args, tail...)
	return plan, nil
}

func desktopRefArg(args map[string]any) (string, error) {
	ref, err := stringArg(args, "ref", "", true)
	if err != nil {
		return "", err
	}
	return desktopRefFromString(ref)
}

func optionalDesktopRefArg(args map[string]any) (string, bool, error) {
	raw, ok := args["ref"]
	if !ok || raw == nil {
		return "", false, nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", false, fmt.Errorf("ref must be a string")
	}
	if strings.TrimSpace(text) == "" {
		return "", false, nil
	}
	ref, err := desktopRefFromString(text)
	if err != nil {
		return "", false, err
	}
	return ref, true, nil
}

func desktopRefFromString(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if !desktopQualifiedRefPattern.MatchString(ref) {
		return "", fmt.Errorf("ref must be a snapshot-qualified ref @<snapshot_id>:e<N> (e.g. @s8f3k2p9:e1; bare @eN is not accepted)")
	}
	return ref, nil
}

func desktopStdinText(args map[string]any, requireNonEmpty bool) (string, error) {
	raw, ok := args["text"]
	if !ok || raw == nil {
		return "", fmt.Errorf("text is required")
	}
	text, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("text must be a string")
	}
	if requireNonEmpty && text == "" {
		return "", fmt.Errorf("text must be a non-empty string")
	}
	if strings.ContainsRune(text, '\x00') {
		return "", fmt.Errorf("text must not contain NUL bytes")
	}
	return text, nil
}

func requiredIntArg(args map[string]any, key string, min int, max int) (int, error) {
	if _, ok := args[key]; !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	return intArg(args, key, 0, min, max)
}

func objectArg(args map[string]any, key string, required bool) (map[string]any, error) {
	value, ok := args[key]
	if !ok || value == nil {
		if required {
			return nil, fmt.Errorf("%s is required", key)
		}
		return map[string]any{}, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return object, nil
}

func desktopTextOrArg(args map[string]any, extra map[string]any, consumed map[string]bool, key string, required bool) (string, error) {
	if value, ok, err := consumeSingleLineArg(extra, consumed, key); err != nil {
		return "", err
	} else if ok {
		return value, nil
	}
	text, err := stringArgWithEmpty(args, "text", "", false, false)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		if required {
			return "", fmt.Errorf("value is required")
		}
		return "", nil
	}
	if strings.ContainsAny(text, "\r\n") {
		return "", fmt.Errorf("value must be a single line")
	}
	return text, nil
}

func desktopPressCombo(args map[string]any, extra map[string]any, consumed map[string]bool) (string, error) {
	combo, err := stringArgWithEmpty(args, "keys", "", false, false)
	if err != nil {
		return "", err
	}
	combo = strings.TrimSpace(combo)
	if combo == "" {
		if fromArgs, ok, err := consumeSingleLineArg(extra, consumed, "combo"); err != nil {
			return "", err
		} else if ok {
			combo = fromArgs
		}
	}
	if combo == "" {
		return "", fmt.Errorf("keys is required for press (e.g. return, escape, cmd+c)")
	}
	if strings.ContainsAny(combo, " \t\r\n;") {
		return "", fmt.Errorf("keys must be a single key or +-joined combo without spaces")
	}
	return combo, nil
}

func appendDesktopAppAndWindow(dst *[]string, extra map[string]any, consumed map[string]bool) (bool, error) {
	added := false
	if app, ok, err := consumeSingleLineArg(extra, consumed, "app"); err != nil {
		return false, err
	} else if ok {
		if err := rejectFlagLikeValue("app", app); err != nil {
			return false, err
		}
		*dst = append(*dst, "--app", app)
		added = true
	}
	if windowID, ok, err := consumeSingleLineArg(extra, consumed, "window_id"); err != nil {
		return false, err
	} else if ok {
		if err := rejectFlagLikeValue("window_id", windowID); err != nil {
			return false, err
		}
		*dst = append(*dst, "--window-id", windowID)
		added = true
	}
	return added, nil
}

func appendDesktopTimeoutMS(dst *[]string, extra map[string]any, consumed map[string]bool, min int) error {
	if timeoutMS, ok, err := consumeIntArg(extra, consumed, "timeout_ms", min, 0); err != nil {
		return err
	} else if ok {
		*dst = append(*dst, "--timeout-ms", strconv.Itoa(timeoutMS))
	}
	return nil
}

func appendDesktopWaitFor(dst *[]string, extra map[string]any, consumed map[string]bool) error {
	if waitFor, ok, err := consumeSingleLineArg(extra, consumed, "wait_for"); err != nil {
		return err
	} else if ok {
		*dst = append(*dst, "--wait-for", waitFor)
	}
	if waitGone, ok, err := consumeSingleLineArg(extra, consumed, "wait_for_gone"); err != nil {
		return err
	} else if ok {
		*dst = append(*dst, "--wait-for-gone", waitGone)
	}
	if waitTimeout, ok, err := consumeIntArg(extra, consumed, "wait_timeout", 1, 0); err != nil {
		return err
	} else if ok {
		*dst = append(*dst, "--wait-timeout", strconv.Itoa(waitTimeout))
	}
	return nil
}

func appendDesktopLaunchCDP(dst *[]string, extra map[string]any, consumed map[string]bool) error {
	raw, ok := extra["cdp"]
	if !ok || raw == nil {
		return nil
	}
	consumed["cdp"] = true
	switch v := raw.(type) {
	case bool:
		if v {
			*dst = append(*dst, "--cdp")
		}
		return nil
	case float64:
		if v != float64(int(v)) || int(v) < 0 || int(v) > 65535 {
			return fmt.Errorf("cdp port must be between 0 and 65535")
		}
		*dst = append(*dst, "--cdp="+strconv.Itoa(int(v)))
		return nil
	default:
		return fmt.Errorf("cdp must be a boolean or port integer")
	}
}

func appendDesktopLaunchEnvAndArgs(dst *[]string, extra map[string]any, consumed map[string]bool) error {
	if rawEnv, ok := extra["env"]; ok && rawEnv != nil {
		consumed["env"] = true
		envMap, ok := rawEnv.(map[string]any)
		if !ok {
			return fmt.Errorf("env must be an object")
		}
		keys := make([]string, 0, len(envMap))
		for k := range envMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			val, ok := envMap[k].(string)
			if !ok {
				return fmt.Errorf("env[%q] must be a string", k)
			}
			if strings.ContainsAny(k, "=\r\n\x00") || strings.ContainsAny(val, "\r\n\x00") {
				return fmt.Errorf("env[%q] contains invalid characters", k)
			}
			*dst = append(*dst, "--env", k+"="+val)
		}
	}
	if rawArgs, ok := extra["args"]; ok && rawArgs != nil {
		consumed["args"] = true
		list, ok := rawArgs.([]any)
		if !ok {
			return fmt.Errorf("args.args must be an array of strings")
		}
		for i, item := range list {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("args.args[%d] must be a string", i)
			}
			if strings.ContainsAny(s, "\r\n\x00") {
				return fmt.Errorf("args.args[%d] contains invalid characters", i)
			}
			*dst = append(*dst, "--arg="+s)
		}
	}
	return nil
}

func appendDesktopWaitArgs(dst *[]string, args map[string]any, extra map[string]any, consumed map[string]bool) ([]string, error) {
	hasCondition := false
	if ref, ok, err := optionalDesktopRefArg(args); err != nil {
		return nil, err
	} else if ok {
		*dst = append(*dst, "--element", ref)
		hasCondition = true
	} else if elemRaw, ok, err := consumeStringArg(extra, consumed, "element"); err != nil {
		return nil, err
	} else if ok {
		elem, err := desktopRefFromString(elemRaw)
		if err != nil {
			return nil, err
		}
		*dst = append(*dst, "--element", elem)
		hasCondition = true
	}

	if pred, ok, err := consumeStringArg(extra, consumed, "predicate"); err != nil {
		return nil, err
	} else if ok {
		switch pred {
		case "exists", "enabled", "visible", "actionable", "value":
			*dst = append(*dst, "--predicate", pred)
		default:
			return nil, fmt.Errorf("predicate must be exists, enabled, visible, actionable, or value")
		}
	}
	if val, ok, err := consumeSingleLineArg(extra, consumed, "value"); err != nil {
		return nil, err
	} else if ok {
		*dst = append(*dst, "--value", val)
	}
	if action, ok, err := consumeStringArg(extra, consumed, "action"); err != nil {
		return nil, err
	} else if ok {
		switch action {
		case "click", "type", "set-value", "clear":
			*dst = append(*dst, "--action", action)
		default:
			return nil, fmt.Errorf("wait action must be click, type, set-value, or clear")
		}
	}
	if win, ok, err := consumeSingleLineArg(extra, consumed, "window"); err != nil {
		return nil, err
	} else if ok {
		*dst = append(*dst, "--window", win)
		hasCondition = true
	}
	if txt, err := desktopTextOrArg(args, extra, consumed, "text", false); err != nil {
		return nil, err
	} else if txt != "" {
		*dst = append(*dst, "--text", txt)
		hasCondition = true
	}
	if count, ok, err := consumeIntArg(extra, consumed, "count", 0, 0); err != nil {
		return nil, err
	} else if ok {
		*dst = append(*dst, "--count", strconv.Itoa(count))
	}
	for _, pair := range []struct {
		key  string
		flag string
	}{
		{key: "menu", flag: "--menu"},
		{key: "menu_closed", flag: "--menu-closed"},
		{key: "notification", flag: "--notification"},
	} {
		if b, ok, err := consumeBoolArg(extra, consumed, pair.key); err != nil {
			return nil, err
		} else if ok && b {
			*dst = append(*dst, pair.flag)
			hasCondition = true
		}
	}
	if ev, ok, err := consumeStringArg(extra, consumed, "event"); err != nil {
		return nil, err
	} else if ok {
		switch ev {
		case "window-opened", "window-closed", "app-launched", "app-terminated",
			"focus-changed", "surface-appeared", "surface-dismissed":
			*dst = append(*dst, "--event", ev)
			hasCondition = true
		default:
			return nil, fmt.Errorf("unsupported wait event %q", ev)
		}
	}
	if _, err := appendDesktopAppAndWindow(dst, extra, consumed); err != nil {
		return nil, err
	}
	if timeout, ok, err := consumeIntArg(extra, consumed, "timeout", 1, 0); err != nil {
		return nil, err
	} else if ok {
		*dst = append(*dst, "--timeout", strconv.Itoa(timeout))
	}
	var tail []string
	if ms, ok, err := consumeIntArg(extra, consumed, "ms", 0, 0); err != nil {
		return nil, err
	} else if ok {
		tail = append(tail, strconv.Itoa(ms))
		hasCondition = true
	}
	if !hasCondition {
		return nil, fmt.Errorf("wait requires a condition (ms, element/ref, window, text, menu, menu_closed, notification, or event)")
	}
	return tail, nil
}

func appendDesktopFindArgs(dst *[]string, args map[string]any, extra map[string]any, consumed map[string]bool) error {
	if _, err := appendDesktopAppAndWindow(dst, extra, consumed); err != nil {
		return err
	}
	var root string
	if ref, ok, err := optionalDesktopRefArg(args); err != nil {
		return err
	} else if ok {
		root = ref
	} else if rootRaw, ok, err := consumeStringArg(extra, consumed, "root"); err != nil {
		return err
	} else if ok {
		parsed, err := desktopRefFromString(rootRaw)
		if err != nil {
			return err
		}
		root = parsed
	}
	surface, err := optionalDesktopSurface(extra, "surface")
	if err != nil {
		return err
	}
	if surface != "" {
		consumed["surface"] = true
	}
	if root != "" && surface != "" {
		return fmt.Errorf("root cannot be combined with surface")
	}
	if root != "" {
		*dst = append(*dst, "--root", root)
	}
	if surface != "" {
		*dst = append(*dst, "--surface", surface)
	}
	for _, key := range []string{"role", "name", "value", "description", "native_id"} {
		if val, ok, err := consumeSingleLineArg(extra, consumed, key); err != nil {
			return err
		} else if ok {
			flag := "--" + strings.ReplaceAll(key, "_", "-")
			*dst = append(*dst, flag, val)
		}
	}
	if txt, err := desktopTextOrArg(args, extra, consumed, "text", false); err != nil {
		return err
	} else if txt != "" {
		*dst = append(*dst, "--text", txt)
	}
	for _, pair := range []struct {
		key  string
		flag string
	}{
		{key: "exact", flag: "--exact"},
		{key: "count", flag: "--count"},
		{key: "first", flag: "--first"},
		{key: "last", flag: "--last"},
	} {
		if b, ok, err := consumeBoolArg(extra, consumed, pair.key); err != nil {
			return err
		} else if ok && b {
			*dst = append(*dst, pair.flag)
		}
	}
	if states, err := consumeStringOrSliceArg(extra, consumed, "state"); err != nil {
		return err
	} else {
		for _, s := range states {
			*dst = append(*dst, "--state", s)
		}
	}
	for _, key := range []string{"nth", "limit"} {
		if n, ok, err := consumeIntArg(extra, consumed, key, 0, 0); err != nil {
			return err
		} else if ok {
			*dst = append(*dst, "--"+key, strconv.Itoa(n))
		}
	}
	return appendDesktopTimeoutMS(dst, extra, consumed, 1)
}

func appendDesktopSkillsArgs(dst *[]string, args map[string]any, extra map[string]any, consumed map[string]bool) error {
	action, _, err := consumeStringArg(extra, consumed, "action")
	if err != nil {
		return err
	}
	skill, hasSkill, err := consumeStringArg(extra, consumed, "skill")
	if err != nil {
		return err
	}
	ref, hasRef, err := consumeStringArg(extra, consumed, "reference")
	if err != nil {
		return err
	}
	if !hasSkill {
		text, err := stringArgWithEmpty(args, "text", "", false, false)
		if err != nil {
			return err
		}
		fields := strings.Fields(text)
		if len(fields) > 0 {
			skill = fields[0]
			hasSkill = true
		}
		if len(fields) > 1 && !hasRef {
			ref = fields[1]
			hasRef = true
		}
		if len(fields) > 2 {
			return fmt.Errorf("skills expects at most '<skill> [reference]'")
		}
	}
	if action == "list" || (action == "" && !hasSkill) {
		*dst = append(*dst, "list")
		return nil
	}
	if action != "" && action != "get" {
		return fmt.Errorf("skills action must be list or get")
	}
	if !hasSkill {
		return fmt.Errorf("skill name is required for skills get (e.g. desktop, platform, macos, windows, linux)")
	}
	if err := rejectFlagLikeValue("skill", skill); err != nil {
		return err
	}
	*dst = append(*dst, "get", skill)
	if hasRef {
		if err := rejectFlagLikeValue("reference", ref); err != nil {
			return err
		}
		*dst = append(*dst, ref)
	}
	return nil
}

func optionalDesktopFlagValue(args map[string]any, key string) (string, error) {
	value, err := stringArgWithEmpty(args, key, "", false, false)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if err := rejectFlagLikeValue(key, value); err != nil {
		return "", err
	}
	return value, nil
}

func optionalDesktopSurface(args map[string]any, key string) (string, error) {
	value, err := stringArgWithEmpty(args, key, "", false, false)
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "", nil
	}
	if !desktopValidSurfaces[value] {
		return "", fmt.Errorf("unsupported surface %q", value)
	}
	return value, nil
}

func rejectFlagLikeValue(name string, value string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s must be a single line", name)
	}
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("%s must not start with '-'", name)
	}
	return nil
}

func validateDesktopXY(xy string) error {
	xStr, yStr, ok := strings.Cut(strings.TrimSpace(xy), ",")
	if !ok || strings.Contains(yStr, ",") {
		return fmt.Errorf("xy must be formatted as x,y")
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(xStr), 64); err != nil {
		return fmt.Errorf("xy x coordinate must be numeric")
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(yStr), 64); err != nil {
		return fmt.Errorf("xy y coordinate must be numeric")
	}
	return nil
}

func optionalIntArg(args map[string]any, key string, min int, max int) (int, bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return 0, false, nil
	}
	val, err := intArg(args, key, 0, min, max)
	if err != nil {
		return 0, false, err
	}
	return val, true, nil
}

func consumeIntArg(extra map[string]any, consumed map[string]bool, key string, min int, max int) (int, bool, error) {
	val, ok, err := optionalIntArg(extra, key, min, max)
	if ok || err != nil {
		consumed[key] = true
	}
	return val, ok, err
}

func consumeBoolArg(extra map[string]any, consumed map[string]bool, key string) (bool, bool, error) {
	raw, ok := extra[key]
	if !ok || raw == nil {
		return false, false, nil
	}
	consumed[key] = true
	val, err := boolArg(extra, key, false)
	if err != nil {
		return false, false, err
	}
	return val, true, nil
}

func consumeStringArg(extra map[string]any, consumed map[string]bool, key string) (string, bool, error) {
	raw, ok := extra[key]
	if !ok || raw == nil {
		return "", false, nil
	}
	consumed[key] = true
	val, err := stringArg(extra, key, "", true)
	if err != nil {
		return "", false, err
	}
	val = strings.TrimSpace(val)
	if val == "" {
		return "", false, fmt.Errorf("%s must not be empty", key)
	}
	return val, true, nil
}

func consumeSingleLineArg(extra map[string]any, consumed map[string]bool, key string) (string, bool, error) {
	val, ok, err := consumeStringArg(extra, consumed, key)
	if !ok || err != nil {
		return val, ok, err
	}
	if strings.ContainsAny(val, "\r\n\x00") {
		return "", false, fmt.Errorf("%s must be a single line", key)
	}
	return val, true, nil
}

func consumeStringOrSliceArg(extra map[string]any, consumed map[string]bool, key string) ([]string, error) {
	raw, ok := extra[key]
	if !ok || raw == nil {
		return nil, nil
	}
	consumed[key] = true
	switch v := raw.(type) {
	case string:
		v = strings.TrimSpace(v)
		if v == "" {
			return nil, fmt.Errorf("%s must not be empty", key)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return nil, fmt.Errorf("%s must be a single line", key)
		}
		return []string{v}, nil
	case []any:
		out, err := stringArrayArg(extra, key)
		if err != nil {
			return nil, err
		}
		for i, item := range out {
			item = strings.TrimSpace(item)
			if item == "" || strings.ContainsAny(item, "\r\n\x00") {
				return nil, fmt.Errorf("%s[%d] must be a non-empty single line", key, i)
			}
			out[i] = item
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be a string or array of strings", key)
	}
}

func rejectUnconsumedArgs(command string, extra map[string]any, consumed map[string]bool) error {
	if len(extra) == 0 {
		return nil
	}
	var unknown []string
	for key := range extra {
		if !consumed[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return fmt.Errorf("unsupported arg(s) %s for command %s", strings.Join(unknown, ", "), command)
}

type agentDesktopEnvelope struct {
	Version string                `json:"version,omitempty"`
	OK      bool                  `json:"ok"`
	Command string                `json:"command,omitempty"`
	Error   *agentDesktopErrorObj `json:"error,omitempty"`
}

type agentDesktopErrorObj struct {
	Code           string                   `json:"code"`
	Message        string                   `json:"message"`
	Suggestion     string                   `json:"suggestion,omitempty"`
	PlatformDetail string                   `json:"platform_detail,omitempty"`
	Details        json.RawMessage          `json:"details,omitempty"`
	Recovery       json.RawMessage          `json:"recovery,omitempty"`
	Disposition    *agentDesktopDisposition `json:"disposition,omitempty"`
}

type agentDesktopDisposition struct {
	Delivery string `json:"delivery,omitempty"`
	Retry    string `json:"retry,omitempty"`
}

type desktopErrorPayload struct {
	Command string                `json:"command,omitempty"`
	Error   *agentDesktopErrorObj `json:"error"`
}

func parseDesktopEnvelope(stdout string) (agentDesktopEnvelope, bool) {
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return agentDesktopEnvelope{}, false
	}
	var env agentDesktopEnvelope
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return agentDesktopEnvelope{}, false
	}
	return env, true
}

func formatDesktopEnvelopeError(goos string, rawStdout string) string {
	env, ok := parseDesktopEnvelope(rawStdout)
	if !ok || env.Error == nil {
		return strings.TrimSpace(rawStdout)
	}
	payload := desktopErrorPayload{
		Command: env.Command,
		Error:   env.Error,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return strings.TrimSpace(rawStdout)
	}
	out := string(encoded)
	if env.Error.Code == "PERM_DENIED" || env.Error.Code == "PLATFORM_NOT_SUPPORTED" {
		out += "\n\n" + desktopUserGuidance(goos, env.Error.Code)
	}
	return out
}

func desktopUserGuidance(goos string, code string) string {
	switch goos {
	case "darwin":
		return "Orientação ao usuário (macOS): abra Ajustes do Sistema › Privacidade e Segurança › Acessibilidade e ative a permissão para o app do terminal em uso (Terminal, iTerm2, Warp, VS Code etc.). Para capturas de tela (screenshot), ative também Gravação de Tela."
	case "linux":
		return "Orientação ao usuário (Linux): verifique se a acessibilidade (AT-SPI / pacote at-spi2-core) está ligada na sessão gráfica do usuário e se o QG está rodando dentro dessa sessão."
	case "windows":
		if code == "PLATFORM_NOT_SUPPORTED" {
			return "Orientação ao usuário (Windows): normalmente nenhuma permissão adicional é necessária na sessão interativa do usuário; sessões sem área de trabalho interativa (Session 0/serviços/tela bloqueada) não têm suporte."
		}
		return "Orientação ao usuário (Windows): normalmente nenhuma permissão adicional é necessária na sessão interativa do usuário; se o app alvo rodar elevado (UIPI), execute no mesmo nível de privilégio."
	default:
		return "Orientação ao usuário: plataforma sem suporte para automação de desktop via agent-desktop (suportado apenas em macOS, Linux com sessão gráfica AT-SPI e Windows interativo)."
	}
}

func desktopCommandResult(ctx context.Context, desktop localcontrol.Desktop, plan desktopCommandPlan) Result {
	var result localcontrol.CommandResult
	var err error
	if plan.hasStdin {
		result, err = desktop.RunWithStdin(ctx, plan.stdin, plan.args...)
	} else {
		result, err = desktop.Run(ctx, plan.args...)
	}

	env, hasEnv := parseDesktopEnvelope(result.Stdout)
	isEnvelopeErr := hasEnv && (!env.OK || env.Error != nil)
	meta := map[string]string{"desktop_command": plan.command}
	if result.ExitCode != 0 {
		meta["exit_code"] = strconv.Itoa(result.ExitCode)
	}
	if hasEnv && env.Error != nil {
		if env.Error.Code != "" {
			meta["error_code"] = env.Error.Code
		}
		if env.Error.Disposition != nil && env.Error.Disposition.Retry != "" {
			meta["retry"] = env.Error.Disposition.Retry
		}
	}

	if err != nil || isEnvelopeErr {
		var rawMsg string
		if hasEnv && env.Error != nil {
			rawMsg = formatDesktopEnvelopeError(runtime.GOOS, result.Stdout)
		} else {
			errText := ""
			if err != nil {
				errText = err.Error()
			}
			rawMsg = "Error running desktop helper"
			if errText != "" {
				rawMsg += ": " + errText
			}
			if extraOut := strings.TrimSpace(result.Output()); extraOut != "" {
				rawMsg += "\n\n" + extraOut
			}
			lower := strings.ToLower(rawMsg)
			if strings.Contains(lower, "unsupported platform") || strings.Contains(lower, "platform not supported") || strings.Contains(lower, "not supported on") {
				rawMsg += "\n\n" + desktopUserGuidance("unsupported", "PLATFORM_NOT_SUPPORTED")
			}
		}
		budget := applyOutputBudget(rawMsg, localDesktopOutputBudgetBytes, "narrow the desktop query or request a scoped snapshot")
		for k, v := range outputBudgetMeta(budget) {
			meta[k] = v
		}
		return Result{
			Status:    StatusError,
			Output:    budget.Output,
			Truncated: budget.Truncated,
			Meta:      meta,
			Display: Display{
				Summary: plan.command + " failed",
				Kind:    "desktop",
			},
		}
	}

	output := result.Output()
	if strings.TrimSpace(output) == "" {
		output = "desktop command completed."
	}
	budget := applyOutputBudget(output, localDesktopOutputBudgetBytes, "narrow the desktop snapshot with skeleton=true, root, app, or window_id")
	for k, v := range outputBudgetMeta(budget) {
		meta[k] = v
	}

	var images []qgruntime.ImageBlock
	if plan.screenshotPath != "" {
		if img, loadErr := imageinput.LoadFile(plan.screenshotPath, ""); loadErr == nil {
			images = []qgruntime.ImageBlock{img}
		}
	}

	return Result{
		Status:    StatusOK,
		Output:    budget.Output,
		Images:    images,
		Truncated: budget.Truncated,
		Meta:      meta,
		Display: Display{
			Summary: plan.command + " completed",
			Kind:    "desktop",
		},
	}
}
