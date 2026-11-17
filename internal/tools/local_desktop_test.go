package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/localcontrol"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sandbox"
)

type fakeDesktopRunner struct {
	args     []string
	calls    [][]string
	stdin    string
	hasStdin bool
	stdout   string
	stderr   string
	exitCode int
	err      error
}

func (runner *fakeDesktopRunner) Run(_ context.Context, path string, args []string, _ []string, _ time.Duration) (localcontrol.CommandResult, error) {
	return runner.record(path, args, "", false)
}

func (runner *fakeDesktopRunner) RunWithStdin(_ context.Context, path string, args []string, _ []string, stdin string, _ time.Duration) (localcontrol.CommandResult, error) {
	return runner.record(path, args, stdin, true)
}

func (runner *fakeDesktopRunner) record(path string, args []string, stdin string, hasStdin bool) (localcontrol.CommandResult, error) {
	runner.args = append([]string(nil), args...)
	runner.calls = append(runner.calls, append([]string(nil), args...))
	runner.stdin = stdin
	runner.hasStdin = hasStdin
	stdout := runner.stdout
	if stdout == "" && runner.stderr == "" && runner.err == nil {
		stdout = `{"version":"2.6","ok":true,"command":"test","data":{"status":"ok"}}` + "\n"
	}
	return localcontrol.CommandResult{
		Path:     path,
		Args:     append([]string(nil), args...),
		Stdout:   stdout,
		Stderr:   runner.stderr,
		ExitCode: runner.exitCode,
	}, runner.err
}

func localDesktopTestOptions(t *testing.T, runner localcontrol.CommandRunner) localcontrol.DesktopOptions {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "agent-desktop")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return localcontrol.DesktopOptions{
		Enabled:    true,
		HelperPath: helper,
		Runner:     runner,
	}
}

func TestDesktopSnapshotDefaultsAndFlags(t *testing.T) {
	runner := &fakeDesktopRunner{}
	tool := newDesktopSnapshotTool(localDesktopTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want := []string{"snapshot", "-i", "--compact"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("default args = %#v, want %#v", runner.args, want)
	}

	result = tool.Run(context.Background(), map[string]any{
		"app":         "Finder",
		"window_id":   "w-4521",
		"root":        "@s8f3k2p9:e3",
		"skeleton":    true,
		"interactive": false,
		"compact":     false,
		"timeout_ms":  float64(8000),
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want = []string{"snapshot", "--app", "Finder", "--window-id", "w-4521", "--root", "@s8f3k2p9:e3", "--skeleton", "--timeout-ms", "8000"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("custom args = %#v, want %#v", runner.args, want)
	}

	result = tool.Run(context.Background(), map[string]any{
		"app":     "Finder",
		"surface": "menu",
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want = []string{"snapshot", "--app", "Finder", "--surface", "menu", "-i", "--compact"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("surface args = %#v, want %#v", runner.args, want)
	}
}

func TestDesktopSnapshotRejectsInvalidArgsBeforePermission(t *testing.T) {
	tool := newDesktopSnapshotTool(localcontrol.DesktopOptions{Enabled: true})
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "bare ref", args: map[string]any{"root": "@e8"}, want: "@<snapshot_id>:e<N>"},
		{name: "root with surface", args: map[string]any{"root": "@s1:e1", "surface": "menu"}, want: "surface"},
		{name: "invalid surface", args: map[string]any{"surface": "bogus"}, want: "surface"},
		{name: "option-like app", args: map[string]any{"app": "--evil"}, want: "app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(tc.args)
			if !rejected || result.Status != StatusError || !strings.Contains(result.Output, tc.want) {
				t.Fatalf("reject = (%v, %#v), want substring %q", rejected, result, tc.want)
			}
		})
	}
}

func TestDesktopActionBuildsArgvForAllAllowedCommands(t *testing.T) {
	cases := []struct {
		name     string
		args     map[string]any
		wantArgs []string
		wantIn   string
		hasIn    bool
	}{
		{
			name:     "click",
			args:     map[string]any{"command": "click", "ref": "@s8f3k2p9:e5"},
			wantArgs: []string{"click", "@s8f3k2p9:e5"},
		},
		{
			name:     "double-click headed",
			args:     map[string]any{"command": "double-click", "ref": "@s8f3k2p9:e5", "headed": true},
			wantArgs: []string{"--headed", "double-click", "@s8f3k2p9:e5"},
		},
		{
			name:     "right-click",
			args:     map[string]any{"command": "right-click", "ref": "@s8f3k2p9:e5"},
			wantArgs: []string{"right-click", "@s8f3k2p9:e5"},
		},
		{
			name:     "type via stdin",
			args:     map[string]any{"command": "type", "ref": "@s8f3k2p9:e2", "text": "segredo-super-123", "args": map[string]any{"interval_ms": float64(50)}},
			wantArgs: []string{"type", "--stdin", "--interval-ms", "50", "@s8f3k2p9:e2"},
			wantIn:   "segredo-super-123",
			hasIn:    true,
		},
		{
			name:     "set-value via stdin",
			args:     map[string]any{"command": "set-value", "ref": "@s8f3k2p9:e2", "text": "valor secreto"},
			wantArgs: []string{"set-value", "--stdin", "@s8f3k2p9:e2"},
			wantIn:   "valor secreto",
			hasIn:    true,
		},
		{
			name:     "clear",
			args:     map[string]any{"command": "clear", "ref": "@s8f3k2p9:e2"},
			wantArgs: []string{"clear", "@s8f3k2p9:e2"},
		},
		{
			name:     "focus",
			args:     map[string]any{"command": "focus", "ref": "@s8f3k2p9:e2", "headed": true},
			wantArgs: []string{"--headed", "focus", "@s8f3k2p9:e2"},
		},
		{
			name:     "select",
			args:     map[string]any{"command": "select", "ref": "@s8f3k2p9:e4", "text": "Option B"},
			wantArgs: []string{"select", "@s8f3k2p9:e4", "--", "Option B"},
		},
		{
			name:     "toggle",
			args:     map[string]any{"command": "toggle", "ref": "@s8f3k2p9:e6"},
			wantArgs: []string{"toggle", "@s8f3k2p9:e6"},
		},
		{
			name:     "check",
			args:     map[string]any{"command": "check", "ref": "@s8f3k2p9:e6"},
			wantArgs: []string{"check", "@s8f3k2p9:e6"},
		},
		{
			name:     "uncheck",
			args:     map[string]any{"command": "uncheck", "ref": "@s8f3k2p9:e6"},
			wantArgs: []string{"uncheck", "@s8f3k2p9:e6"},
		},
		{
			name:     "expand",
			args:     map[string]any{"command": "expand", "ref": "@s8f3k2p9:e7"},
			wantArgs: []string{"expand", "@s8f3k2p9:e7"},
		},
		{
			name:     "collapse",
			args:     map[string]any{"command": "collapse", "ref": "@s8f3k2p9:e7"},
			wantArgs: []string{"collapse", "@s8f3k2p9:e7"},
		},
		{
			name:     "scroll",
			args:     map[string]any{"command": "scroll", "ref": "@s8f3k2p9:e1", "args": map[string]any{"direction": "down", "amount": float64(5)}},
			wantArgs: []string{"scroll", "--direction", "down", "--amount", "5", "@s8f3k2p9:e1"},
		},
		{
			name:     "scroll-to",
			args:     map[string]any{"command": "scroll-to", "ref": "@s8f3k2p9:e8"},
			wantArgs: []string{"scroll-to", "@s8f3k2p9:e8"},
		},
		{
			name:     "press",
			args:     map[string]any{"command": "press", "keys": "cmd+c", "args": map[string]any{"app": "Notes", "force": true}},
			wantArgs: []string{"press", "--app", "Notes", "--force", "--", "cmd+c"},
		},
		{
			name:     "hover with ref",
			args:     map[string]any{"command": "hover", "ref": "@s8f3k2p9:e5", "headed": true},
			wantArgs: []string{"--headed", "hover", "@s8f3k2p9:e5"},
		},
		{
			name:     "hover with xy",
			args:     map[string]any{"command": "hover", "headed": true, "args": map[string]any{"xy": "-100,50"}},
			wantArgs: []string{"--headed", "hover", "--xy=-100,50"},
		},
		{
			name:     "launch",
			args:     map[string]any{"command": "launch", "text": "TextEdit", "args": map[string]any{"activate": true, "no_attach": true, "timeout": float64(15000)}},
			wantArgs: []string{"launch", "--activate", "--no-attach", "--timeout", "15000", "--", "TextEdit"},
		},
		{
			name:     "close-app",
			args:     map[string]any{"command": "close-app", "text": "TextEdit", "args": map[string]any{"force": true}},
			wantArgs: []string{"close-app", "--force", "--", "TextEdit"},
		},
		{
			name:     "focus-window",
			args:     map[string]any{"command": "focus-window", "args": map[string]any{"window_id": "w-4521"}},
			wantArgs: []string{"focus-window", "--window-id", "w-4521"},
		},
		{
			name:     "list-apps",
			args:     map[string]any{"command": "list-apps", "args": map[string]any{"app": "Finder"}},
			wantArgs: []string{"list-apps", "--app", "Finder"},
		},
		{
			name:     "list-windows",
			args:     map[string]any{"command": "list-windows", "args": map[string]any{"app": "Finder", "timeout_ms": float64(4000)}},
			wantArgs: []string{"list-windows", "--app", "Finder", "--timeout-ms", "4000"},
		},
		{
			name:     "wait",
			args:     map[string]any{"command": "wait", "ref": "@s8f3k2p9:e9", "args": map[string]any{"predicate": "actionable", "timeout": float64(5000)}},
			wantArgs: []string{"wait", "--element", "@s8f3k2p9:e9", "--predicate", "actionable", "--timeout", "5000"},
		},
		{
			name:     "find",
			args:     map[string]any{"command": "find", "args": map[string]any{"app": "Notes", "role": "button", "name": "Save", "exact": true, "limit": float64(5)}},
			wantArgs: []string{"find", "--app", "Notes", "--role", "button", "--name", "Save", "--exact", "--limit", "5"},
		},
		{
			name:     "get",
			args:     map[string]any{"command": "get", "ref": "@s8f3k2p9:e2", "args": map[string]any{"property": "value"}},
			wantArgs: []string{"get", "--property", "value", "@s8f3k2p9:e2"},
		},
		{
			name:     "is",
			args:     map[string]any{"command": "is", "ref": "@s8f3k2p9:e6", "args": map[string]any{"property": "checked"}},
			wantArgs: []string{"is", "--property", "checked", "@s8f3k2p9:e6"},
		},
		{
			name:     "clipboard-get",
			args:     map[string]any{"command": "clipboard-get", "args": map[string]any{"format": "auto"}},
			wantArgs: []string{"clipboard-get", "--format", "auto"},
		},
		{
			name:     "clipboard-set via stdin",
			args:     map[string]any{"command": "clipboard-set", "text": "texto secreto"},
			wantArgs: []string{"clipboard-set", "--stdin"},
			wantIn:   "texto secreto",
			hasIn:    true,
		},
		{
			name:     "screenshot",
			args:     map[string]any{"command": "screenshot", "args": map[string]any{"app": "Finder", "path": "/tmp/shot.png"}},
			wantArgs: []string{"screenshot", "--app", "Finder", "--", "/tmp/shot.png"},
		},
		{
			name:     "skills get",
			args:     map[string]any{"command": "skills", "text": "desktop workflows"},
			wantArgs: []string{"skills", "get", "desktop", "workflows"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeDesktopRunner{}
			tool := newDesktopActionTool(localDesktopTestOptions(t, runner))
			result := tool.Run(context.Background(), tc.args)
			if result.Status != StatusOK {
				t.Fatalf("status = %s output = %q", result.Status, result.Output)
			}
			if !reflect.DeepEqual(runner.args, tc.wantArgs) {
				t.Fatalf("args = %#v, want %#v", runner.args, tc.wantArgs)
			}
			if runner.hasStdin != tc.hasIn || runner.stdin != tc.wantIn {
				t.Fatalf("stdin = (%q, %v), want (%q, %v)", runner.stdin, runner.hasStdin, tc.wantIn, tc.hasIn)
			}
			if tc.hasIn {
				for _, arg := range runner.args {
					if strings.Contains(arg, tc.wantIn) {
						t.Fatalf("stdin text leaked into argv: %#v", runner.args)
					}
				}
			}
		})
	}
}

func TestDesktopActionRejectsInvalidInputsBeforePermission(t *testing.T) {
	tool := newDesktopActionTool(localcontrol.DesktopOptions{Enabled: true})
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "unknown command", args: map[string]any{"command": "sandbox"}, want: "command must be one of"},
		{name: "bare ref", args: map[string]any{"command": "click", "ref": "@e8"}, want: "@<snapshot_id>:e<N>"},
		{name: "missing text for type", args: map[string]any{"command": "type", "ref": "@s1:e1"}, want: "text"},
		{name: "nul byte in stdin text", args: map[string]any{"command": "type", "ref": "@s1:e1", "text": "a\x00b"}, want: "NUL"},
		{name: "missing keys for press", args: map[string]any{"command": "press"}, want: "keys"},
		{name: "unknown arg key", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": map[string]any{"trace": "x"}}, want: "unsupported"},
		{name: "focus-window empty", args: map[string]any{"command": "focus-window"}, want: "window_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(tc.args)
			if !rejected || result.Status != StatusError || !strings.Contains(result.Output, tc.want) {
				t.Fatalf("reject = (%v, %#v), want %q", rejected, result, tc.want)
			}
		})
	}
}

func TestDesktopEnvelopeOKAndErrorFormatting(t *testing.T) {
	runner := &fakeDesktopRunner{
		stdout: `{"version":"2.6","ok":true,"command":"list-windows","data":{"windows":[]}}`,
	}
	tool := newDesktopActionTool(localDesktopTestOptions(t, runner))
	okRes := tool.Run(context.Background(), map[string]any{"command": "list-windows"})
	if okRes.Status != StatusOK || !strings.Contains(okRes.Output, `"ok":true`) {
		t.Fatalf("okRes = %#v", okRes)
	}

	runner.stdout = `{"version":"2.6","ok":false,"command":"click","error":{"code":"STALE_REF","message":"Ref not found","disposition":{"delivery":"not_delivered","retry":"safe"}}}`
	runner.exitCode = 1
	runner.err = errors.New("exit status 1")
	errRes := tool.Run(context.Background(), map[string]any{"command": "click", "ref": "@s1:e1"})
	if errRes.Status != StatusError {
		t.Fatalf("status = %s, want error", errRes.Status)
	}
	if !strings.Contains(errRes.Output, `"STALE_REF"`) || !strings.Contains(errRes.Output, `"retry":"safe"`) || !strings.Contains(errRes.Output, `"Ref not found"`) {
		t.Fatalf("errRes.Output = %q, want JSON error with code, message, retry", errRes.Output)
	}
	if errRes.Meta["error_code"] != "STALE_REF" || errRes.Meta["retry"] != "safe" {
		t.Fatalf("meta = %#v", errRes.Meta)
	}
}

func TestDesktopPermissionAndPlatformGuidanceInPortuguese(t *testing.T) {
	permEnvelope := `{"version":"2.6","ok":false,"command":"snapshot","error":{"code":"PERM_DENIED","message":"Accessibility permission not granted","disposition":{"delivery":"not_delivered","retry":"safe"}}}`
	platEnvelope := `{"version":"2.6","ok":false,"command":"list-windows","error":{"code":"PLATFORM_NOT_SUPPORTED","message":"AT-SPI bus unavailable","disposition":{"delivery":"not_delivered","retry":"safe"}}}`

	if out := formatDesktopEnvelopeError("darwin", permEnvelope); !strings.Contains(out, "Ajustes do Sistema") || !strings.Contains(out, "Acessibilidade") || !strings.Contains(out, `"PERM_DENIED"`) {
		t.Fatalf("darwin guidance = %q", out)
	}
	if out := formatDesktopEnvelopeError("linux", platEnvelope); !strings.Contains(out, "AT-SPI") || !strings.Contains(out, "sessão gráfica") || !strings.Contains(out, `"PLATFORM_NOT_SUPPORTED"`) {
		t.Fatalf("linux guidance = %q", out)
	}
	if out := formatDesktopEnvelopeError("windows", permEnvelope); !strings.Contains(out, "normalmente") || !strings.Contains(out, `"PERM_DENIED"`) {
		t.Fatalf("windows guidance = %q", out)
	}
	if out := formatDesktopEnvelopeError("plan9", platEnvelope); !strings.Contains(out, "sem suporte") || !strings.Contains(out, `"PLATFORM_NOT_SUPPORTED"`) {
		t.Fatalf("unsupported guidance = %q", out)
	}
}

func TestDesktopSnapshotRespectsOutputBudget(t *testing.T) {
	runner := &fakeDesktopRunner{
		stdout: strings.Repeat("x", localBrowserOutputBudgetBytes+4096),
	}
	tool := newDesktopSnapshotTool(localDesktopTestOptions(t, runner))
	result := tool.Run(context.Background(), map[string]any{})
	if result.Status != StatusOK || !result.Truncated {
		t.Fatalf("result = (status=%s truncated=%v), want truncated OK", result.Status, result.Truncated)
	}
}

func TestDesktopToolsDisabledByConfigAndDiscoverableViaToolSearch(t *testing.T) {
	disabledTools := NewLocalDesktopTools(localcontrol.DesktopOptions{Enabled: false})
	if len(disabledTools) != 2 {
		t.Fatalf("len(NewLocalDesktopTools) = %d, want 2 (desktop_snapshot, desktop_action)", len(disabledTools))
	}
	for _, tool := range disabledTools {
		if tool.Safety().Permission != PermissionDeny {
			t.Fatalf("%s permission = %s, want deny when disabled", tool.Name(), tool.Safety().Permission)
		}
		if !IsDeferred(tool) {
			t.Fatalf("%s must be deferred", tool.Name())
		}
	}

	reg := NewRegistry()
	for _, tool := range NewLocalDesktopTools(localcontrol.DesktopOptions{Enabled: true}) {
		reg.Register(tool)
	}
	search := NewToolSearchTool(reg)
	for _, query := range []string{"computador", "desktop", "aplicativo", "app", "janela", "clicar", "digitar", "computer use"} {
		res := search.Run(context.Background(), map[string]any{"query": query})
		if res.Status != StatusOK {
			t.Fatalf("tool_search(%q) failed: %s", query, res.Output)
		}
		loaded := res.Meta["load_tools"]
		if !strings.Contains(loaded, "desktop_action") && !strings.Contains(loaded, "desktop_snapshot") {
			t.Fatalf("tool_search(%q) load_tools = %q, expected desktop tools", query, loaded)
		}
	}
}

func TestDesktopHelperRunsOutsideSandbox(t *testing.T) {
	runner := &fakeDesktopRunner{}
	reg := NewRegistry()
	for _, tool := range NewLocalDesktopTools(localDesktopTestOptions(t, runner)) {
		if _, isSandboxed := tool.(sandboxAwareTool); isSandboxed {
			t.Fatalf("%s must not implement sandboxAwareTool; it needs the user's graphical session", tool.Name())
		}
		reg.Register(tool)
	}
	engine := sandbox.NewEngine(sandbox.EngineOptions{
		WorkspaceRoot: t.TempDir(),
		Policy:        sandbox.DefaultPolicy(),
	})
	res := reg.RunWithOptions(context.Background(), "desktop_snapshot", map[string]any{}, RunOptions{
		Sandbox:           engine,
		PermissionGranted: true,
	})
	if res.Status != StatusOK {
		t.Fatalf("status = %s output = %q", res.Status, res.Output)
	}
	if !reflect.DeepEqual(runner.args, []string{"snapshot", "-i", "--compact"}) {
		t.Fatalf("runner args = %#v, expected direct helper execution outside sandbox", runner.args)
	}
}

var _ = json.Valid
