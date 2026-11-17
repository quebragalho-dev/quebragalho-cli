package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	xdraw "golang.org/x/image/draw"

	"github.com/quebragalho-dev/quebragalho-cli/internal/localcontrol"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

const (
	localDesktopOutputBudgetBytes = localBrowserOutputBudgetBytes
	desktopScreenshotMaxDim       = 1568
	desktopScreenshotMaxPNGBytes  = 3500 * 1024
	desktopScreenshotJPEGQuality  = 85
)

var (
	desktopQualifiedRefPattern = regexp.MustCompile(`^@[A-Za-z0-9_-]+:e[1-9][0-9]*$`)
	desktopForbiddenFlags      = map[string]bool{
		"--stdin": true, "--headed": true, "--screenshot": true, "--debug": true,
		"--trace": true, "--trace-strict": true, "--session": true, "--agent-id": true,
		"--verbose": true, "-v": true,
	}
	desktopActionCommandList = []string{
		"check", "clear", "click", "clipboard-get", "clipboard-set", "close-app",
		"collapse", "double-click", "expand", "find", "focus", "focus-window",
		"get", "hover", "is", "launch", "list-apps", "list-windows", "press",
		"right-click", "screenshot", "scroll", "scroll-to", "select", "set-value",
		"skills", "toggle", "type", "uncheck", "wait",
	}
)

// NewLocalDesktopTools returns the deferred desktop automation tools backed by
// agent-desktop. list-apps and list-windows live inside desktop_action to keep
// the deferred tool catalog compact.
func NewLocalDesktopTools(options localcontrol.DesktopOptions) []Tool {
	return []Tool{newDesktopSnapshotTool(options), newDesktopActionTool(options)}
}

type desktopTool struct {
	baseTool
	desktop localcontrol.Desktop
	planFn  func(map[string]any) (desktopCommandPlan, error)
}

func (tool desktopTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, err := tool.planFn(args); err != nil {
		return errorResult("Error: Invalid arguments for " + tool.name + ": " + err.Error()), true
	}
	return Result{}, false
}

func (tool desktopTool) Run(ctx context.Context, args map[string]any) Result {
	plan, err := tool.planFn(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for " + tool.name + ": " + err.Error())
	}
	return desktopCommandResult(ctx, tool.desktop, plan)
}

func newDesktopSnapshotTool(options localcontrol.DesktopOptions) Tool {
	return desktopTool{
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
					"surface":     {Type: "string", Description: "Surface to snapshot (e.g. window, menu, menubar, sheet, popover, alert, focused)."},
					"timeout_ms":  {Type: "integer", Description: "Observation deadline in ms (default 3000; raise for Chromium/Electron).", Minimum: intPtr(100)},
				},
				AdditionalProperties: false,
			},
			safety:       localControlSafety(options.Enabled, SideEffectLocalDesktop, "Reads a native desktop accessibility snapshot."),
			capabilities: ToolCapabilities{Effect: EffectInteractive, ThreadSafe: false},
		},
		desktop: localcontrol.NewDesktop(options),
		planFn:  desktopSnapshotPlan,
	}
}

func newDesktopActionTool(options localcontrol.DesktopOptions) Tool {
	return desktopTool{
		baseTool: baseTool{
			name:     "desktop_action",
			deferred: true,
			description: `Run desktop/computer use UI action (clicar, digitar, abrir aplicativo/app, foco em janela no computador) via agent-desktop. ` +
				`Pass @<snapshot_id>:eN ref from desktop_snapshot (bare @e8 rejected). ` +
				`Loop: snapshot -i -> act -> snapshot to verify; on STALE_REF/AMBIGUOUS_TARGET re-snapshot or pass --window-id. ` +
				`Retry only if error.disposition.retry == "safe". ` +
				`Set headed=true only when the user explicitly permits physical mouse/keyboard control, never as a fallback after a headless failure. ` +
				`Guide: command="skills", args=["get","desktop"] (or "platform").`,
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"command": {Type: "string", Description: "agent-desktop command to execute.", Enum: desktopActionCommandList},
					"ref":     {Type: "string", Description: "Optional qualified @<snapshot_id>:eN element ref from desktop_snapshot/find."},
					"text":    {Type: "string", Description: "Optional text: sent via --stdin for type/set-value/clipboard-set, or positional (e.g. press combo, select value, launch/close-app name)."},
					"args":    {Type: "array", Items: &PropertySchema{Type: "string"}, Description: "Optional flags/positionals matching `agent-desktop <command> --help` (e.g. [\"--direction\",\"down\",\"--amount\",\"3\"])."},
					"headed":  {Type: "boolean", Description: "Pass global --headed only when the user explicitly permits physical mouse/keyboard control; never use to recover from headless failure.", Default: false},
				},
				Required:             []string{"command"},
				AdditionalProperties: false,
			},
			safety:       localControlSafety(options.Enabled, SideEffectLocalDesktop, "Operates native desktop applications and windows."),
			capabilities: ToolCapabilities{Effect: EffectInteractive, ThreadSafe: false},
		},
		desktop: localcontrol.NewDesktop(options),
		planFn:  desktopActionPlan,
	}
}

type desktopCommandPlan struct {
	command           string
	args              []string
	stdin             string
	hasStdin          bool
	managedScreenshot bool
}

func desktopSnapshotPlan(args map[string]any) (desktopCommandPlan, error) {
	cmdArgs := []string{"snapshot"}
	for _, spec := range [][2]string{{"app", "--app"}, {"window_id", "--window-id"}} {
		val, err := optionalDesktopFlagValue(args, spec[0])
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if val != "" {
			cmdArgs = append(cmdArgs, spec[1], val)
		}
	}
	root, hasRoot, err := optionalDesktopRefArg(args, "root")
	if err != nil {
		return desktopCommandPlan{}, err
	}
	surface, err := optionalDesktopFlagValue(args, "surface")
	if err != nil {
		return desktopCommandPlan{}, err
	}
	if hasRoot && surface != "" {
		return desktopCommandPlan{}, fmt.Errorf("root cannot be combined with surface")
	}
	if hasRoot {
		cmdArgs = append(cmdArgs, "--root", root)
	}
	if surface != "" {
		cmdArgs = append(cmdArgs, "--surface", surface)
	}
	for _, spec := range []struct {
		key, flag string
		def       bool
	}{{"skeleton", "--skeleton", false}, {"interactive", "-i", true}, {"compact", "--compact", true}} {
		on, err := boolArg(args, spec.key, spec.def)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		if on {
			cmdArgs = append(cmdArgs, spec.flag)
		}
	}
	if raw, ok := args["timeout_ms"]; ok && raw != nil {
		ms, err := intArg(args, "timeout_ms", 0, 100, 0)
		if err != nil {
			return desktopCommandPlan{}, err
		}
		cmdArgs = append(cmdArgs, "--timeout-ms", strconv.Itoa(ms))
	}
	return desktopCommandPlan{command: "snapshot", args: cmdArgs}, nil
}

func desktopActionPlan(args map[string]any) (desktopCommandPlan, error) {
	command, err := stringArg(args, "command", "", true)
	if err != nil {
		return desktopCommandPlan{}, err
	}
	command = strings.ToLower(strings.TrimSpace(command))
	if !slices.Contains(desktopActionCommandList, command) {
		return desktopCommandPlan{}, fmt.Errorf("command must be one of: %s", strings.Join(desktopActionCommandList, ", "))
	}
	headed, err := boolArg(args, "headed", false)
	if err != nil {
		return desktopCommandPlan{}, err
	}
	extraArgs, err := validateDesktopExtraArgs(args)
	if err != nil {
		return desktopCommandPlan{}, err
	}
	ref, hasRef, err := optionalDesktopRefArg(args, "ref")
	if err != nil {
		return desktopCommandPlan{}, err
	}
	text, hasText, err := optionalDesktopString(args, "text")
	if err != nil {
		return desktopCommandPlan{}, err
	}

	plan := desktopCommandPlan{command: command}
	if headed {
		plan.args = append(plan.args, "--headed")
	}
	plan.args = append(plan.args, command)

	switch command {
	case "type", "set-value":
		if !hasText || (command == "type" && text == "") {
			return desktopCommandPlan{}, fmt.Errorf("text is required for %s", command)
		}
		plan.args = append(plan.args, "--stdin")
		plan.stdin, plan.hasStdin = text, true
	case "clipboard-set":
		if hasText && (text != "" || len(extraArgs) == 0) {
			plan.args = append(plan.args, "--stdin")
			plan.stdin, plan.hasStdin = text, true
		} else if !hasText && len(extraArgs) == 0 {
			return desktopCommandPlan{}, fmt.Errorf("text (or args such as --image / --file-url) is required for clipboard-set")
		}
	case "screenshot":
		if hasText && text != "" {
			return desktopCommandPlan{}, fmt.Errorf("screenshot saves to a managed temporary file; omit text")
		}
		plan.managedScreenshot = true
	}

	plan.args = append(plan.args, extraArgs...)
	if hasRef {
		plan.args = append(plan.args, ref)
	}
	if hasText && text != "" && !plan.hasStdin && command != "screenshot" {
		if strings.HasPrefix(text, "-") && !slices.Contains(extraArgs, "--") {
			plan.args = append(plan.args, "--")
		}
		plan.args = append(plan.args, text)
	}
	return plan, nil
}

func validateDesktopExtraArgs(args map[string]any) ([]string, error) {
	values, err := stringArrayArg(args, "args")
	if err != nil {
		return nil, err
	}
	for i, arg := range values {
		if strings.ContainsAny(arg, "\r\n\x00") {
			return nil, fmt.Errorf("args[%d] contains invalid control characters", i)
		}
		flag, _, _ := strings.Cut(arg, "=")
		if desktopForbiddenFlags[flag] || (strings.HasPrefix(flag, "-v") && !strings.HasPrefix(flag, "--")) {
			return nil, fmt.Errorf("flag %q is managed by QG and disallowed in args", flag)
		}
	}
	return values, nil
}

func optionalDesktopString(args map[string]any, key string) (string, bool, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return "", false, nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", false, fmt.Errorf("%s must be a string", key)
	}
	if strings.ContainsRune(text, '\x00') {
		return "", false, fmt.Errorf("%s must not contain NUL bytes", key)
	}
	return text, true, nil
}

func optionalDesktopRefArg(args map[string]any, key string) (string, bool, error) {
	text, ok, err := optionalDesktopString(args, key)
	if err != nil || !ok {
		return "", false, err
	}
	ref := strings.TrimSpace(text)
	if ref == "" {
		return "", false, nil
	}
	if !desktopQualifiedRefPattern.MatchString(ref) {
		return "", false, fmt.Errorf("ref must be a snapshot-qualified ref @<snapshot_id>:e<N> (e.g. @s8f3k2p9:e1; bare @eN is not accepted)")
	}
	return ref, true, nil
}

func optionalDesktopFlagValue(args map[string]any, key string) (string, error) {
	value, ok, err := optionalDesktopString(args, key)
	if err != nil || !ok {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("%s must be a single line", key)
	}
	if strings.HasPrefix(value, "-") {
		return "", fmt.Errorf("%s must not start with '-'", key)
	}
	return value, nil
}

type agentDesktopEnvelope struct {
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

func parseDesktopEnvelope(stdout string) (agentDesktopEnvelope, bool) {
	trimmed := strings.TrimSpace(stdout)
	if !strings.HasPrefix(trimmed, "{") {
		return agentDesktopEnvelope{}, false
	}
	var env agentDesktopEnvelope
	return env, json.Unmarshal([]byte(trimmed), &env) == nil
}

func formatDesktopEnvelopeError(goos string, rawStdout string) string {
	env, ok := parseDesktopEnvelope(rawStdout)
	if !ok || env.Error == nil {
		return strings.TrimSpace(rawStdout)
	}
	encoded, err := json.Marshal(struct {
		Command string                `json:"command,omitempty"`
		Error   *agentDesktopErrorObj `json:"error"`
	}{Command: env.Command, Error: env.Error})
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
	runArgs := plan.args
	var shotPath string
	if plan.managedScreenshot {
		tmpDir, err := os.MkdirTemp("", "qg-desktop-screenshot-*")
		if err != nil {
			return errorResult("Error creating temporary screenshot directory: " + err.Error())
		}
		defer os.RemoveAll(tmpDir)
		shotPath = filepath.Join(tmpDir, "screenshot.png")
		runArgs = append(append([]string(nil), plan.args...), shotPath)
	}

	var result localcontrol.CommandResult
	var err error
	if plan.hasStdin {
		result, err = desktop.RunWithStdin(ctx, plan.stdin, runArgs...)
	} else {
		result, err = desktop.Run(ctx, runArgs...)
	}

	env, hasEnv := parseDesktopEnvelope(result.Stdout)
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

	if err != nil || (hasEnv && (!env.OK || env.Error != nil)) {
		rawMsg := ""
		if hasEnv && env.Error != nil {
			rawMsg = formatDesktopEnvelopeError(runtime.GOOS, result.Stdout)
		} else {
			rawMsg = "Error running desktop helper"
			if err != nil {
				rawMsg += ": " + err.Error()
			}
			if extraOut := strings.TrimSpace(result.Output()); extraOut != "" {
				rawMsg += "\n\n" + extraOut
			}
			lower := strings.ToLower(rawMsg)
			if strings.Contains(lower, "unsupported platform") || strings.Contains(lower, "platform not supported") || strings.Contains(lower, "not supported on") {
				rawMsg += "\n\n" + desktopUserGuidance("unsupported", "PLATFORM_NOT_SUPPORTED")
			}
		}
		return desktopBudgetedResult(StatusError, plan.command, "failed", rawMsg, "narrow the desktop query or request a scoped snapshot", meta)
	}

	if plan.managedScreenshot {
		img, summary, loadErr := prepareDesktopScreenshot(shotPath)
		if loadErr != nil {
			return errorResult("Error loading desktop screenshot: " + loadErr.Error())
		}
		return Result{Status: StatusOK, Output: summary, Images: []qgruntime.ImageBlock{img}, Meta: meta, Display: Display{Summary: "screenshot completed", Kind: "desktop"}}
	}

	output := result.Output()
	if strings.TrimSpace(output) == "" {
		output = "desktop command completed."
	}
	return desktopBudgetedResult(StatusOK, plan.command, "completed", output, "narrow the desktop snapshot with skeleton=true, root, app, or window_id", meta)
}

func prepareDesktopScreenshot(path string) (qgruntime.ImageBlock, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return qgruntime.ImageBlock{}, "", err
	}
	src, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		return qgruntime.ImageBlock{}, "", fmt.Errorf("decode png: %w", err)
	}
	bounds := src.Bounds()
	origW, origH := bounds.Dx(), bounds.Dy()
	if origW <= 0 || origH <= 0 {
		return qgruntime.ImageBlock{}, "", fmt.Errorf("empty screenshot dimensions %dx%d", origW, origH)
	}

	target := src
	newW, newH, scale := origW, origH, 1.0
	pngBytes := raw
	if origW > desktopScreenshotMaxDim || origH > desktopScreenshotMaxDim {
		if origW >= origH {
			newW = desktopScreenshotMaxDim
			newH = max(1, int(math.Round(float64(origH)*float64(desktopScreenshotMaxDim)/float64(origW))))
			scale = float64(newW) / float64(origW)
		} else {
			newH = desktopScreenshotMaxDim
			newW = max(1, int(math.Round(float64(origW)*float64(desktopScreenshotMaxDim)/float64(origH))))
			scale = float64(newH) / float64(origH)
		}
		scaled := image.NewNRGBA(image.Rect(0, 0, newW, newH))
		xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), src, bounds, xdraw.Over, nil)
		var pngBuf bytes.Buffer
		if err := png.Encode(&pngBuf, scaled); err != nil {
			return qgruntime.ImageBlock{}, "", fmt.Errorf("encode resized png: %w", err)
		}
		target = scaled
		pngBytes = pngBuf.Bytes()
	}

	mediaType, outBytes := "image/png", pngBytes
	if len(pngBytes) > desktopScreenshotMaxPNGBytes {
		var jpgBuf bytes.Buffer
		if err := jpeg.Encode(&jpgBuf, target, &jpeg.Options{Quality: desktopScreenshotJPEGQuality}); err != nil {
			return qgruntime.ImageBlock{}, "", fmt.Errorf("encode fallback jpeg: %w", err)
		}
		mediaType, outBytes = "image/jpeg", jpgBuf.Bytes()
	}

	summary := fmt.Sprintf("Desktop screenshot attached (%dx%d, scale 1.000).", origW, origH)
	if newW != origW || newH != origH {
		summary = fmt.Sprintf("Desktop screenshot attached (%dx%d -> %dx%d, scale %.3f).", origW, origH, newW, newH, scale)
	}
	return qgruntime.ImageBlock{MediaType: mediaType, Data: outBytes}, summary, nil
}

func desktopBudgetedResult(status Status, command string, verb string, output string, hint string, meta map[string]string) Result {
	budget := applyOutputBudget(output, localDesktopOutputBudgetBytes, hint)
	for k, v := range outputBudgetMeta(budget) {
		meta[k] = v
	}
	return Result{Status: status, Output: budget.Output, Truncated: budget.Truncated, Meta: meta, Display: Display{Summary: command + " " + verb, Kind: "desktop"}}
}
