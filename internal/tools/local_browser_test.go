package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/localcontrol"
)

type fakeBrowserRunner struct {
	args     []string
	calls    [][]string
	stdout   string
	stderr   string
	exitCode int
	err      error
}

type fakeBrowserAppLauncher struct {
	request localcontrol.BrowserAppLaunchRequest
	result  localcontrol.BrowserAppLaunchResult
	err     error
}

func (runner *fakeBrowserRunner) Run(_ context.Context, path string, args []string, _ []string, _ time.Duration) (localcontrol.CommandResult, error) {
	runner.args = append([]string(nil), args...)
	runner.calls = append(runner.calls, append([]string(nil), args...))
	stdout := runner.stdout
	if stdout == "" && runner.stderr == "" && runner.err == nil {
		stdout = "browser ok\n"
	}
	return localcontrol.CommandResult{
		Path:     path,
		Args:     append([]string(nil), args...),
		Stdout:   stdout,
		Stderr:   runner.stderr,
		ExitCode: runner.exitCode,
	}, runner.err
}

func (launcher *fakeBrowserAppLauncher) LaunchBrowserApp(_ context.Context, request localcontrol.BrowserAppLaunchRequest) (localcontrol.BrowserAppLaunchResult, error) {
	launcher.request = request
	if launcher.err != nil {
		return localcontrol.BrowserAppLaunchResult{}, launcher.err
	}
	if launcher.result.App == "" {
		launcher.result = localcontrol.BrowserAppLaunchResult{
			App:         request.App,
			PID:         1234,
			DebugPort:   request.DebugPort,
			DevToolsURL: "http://127.0.0.1:9222",
		}
	}
	return launcher.result, nil
}

func localBrowserTestOptions(t *testing.T, runner localcontrol.CommandRunner) localcontrol.BrowserOptions {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "agent-browser")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return localcontrol.BrowserOptions{
		Enabled:    true,
		HelperPath: helper,
		Runner:     runner,
	}
}

func TestBrowserLaunchRunsSupportedAppAndConnects(t *testing.T) {
	runner := &fakeBrowserRunner{}
	launcher := &fakeBrowserAppLauncher{}
	tool := newBrowserLaunchToolWithLauncher(localBrowserTestOptions(t, runner), launcher)

	result := tool.Run(context.Background(), map[string]any{"app": "discord"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	wantRequest := localcontrol.BrowserAppLaunchRequest{
		App:          "discord",
		DebugPort:    localcontrol.DefaultDevToolsPort,
		StopExisting: true,
		Wait:         true,
	}
	if !reflect.DeepEqual(launcher.request, wantRequest) {
		t.Fatalf("launch request = %#v, want %#v", launcher.request, wantRequest)
	}
	if want := []string{"connect", "9222"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("browser args = %#v, want %#v", runner.args, want)
	}
	if !strings.Contains(result.Output, "Launched discord") {
		t.Fatalf("output = %q, want launch summary", result.Output)
	}
	if !strings.Contains(result.Output, "already connected") {
		t.Fatalf("output = %q, want connected handoff", result.Output)
	}
}

func TestBrowserLaunchCanSkipConnect(t *testing.T) {
	runner := &fakeBrowserRunner{}
	launcher := &fakeBrowserAppLauncher{}
	tool := newBrowserLaunchToolWithLauncher(localBrowserTestOptions(t, runner), launcher)

	result := tool.Run(context.Background(), map[string]any{
		"app":           "discord",
		"debug_port":    float64(9333),
		"stop_existing": false,
		"wait":          false,
		"connect":       false,
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	wantRequest := localcontrol.BrowserAppLaunchRequest{
		App:          "discord",
		DebugPort:    9333,
		StopExisting: false,
		Wait:         false,
	}
	if !reflect.DeepEqual(launcher.request, wantRequest) {
		t.Fatalf("launch request = %#v, want %#v", launcher.request, wantRequest)
	}
	if runner.args != nil {
		t.Fatalf("browser args = %#v, want no connect", runner.args)
	}
	if !strings.Contains(result.Output, "Next call browser_connect with target 9333") {
		t.Fatalf("output = %q, want connect hint", result.Output)
	}
}

func TestBrowserLaunchRejectsUnsupportedAppBeforePermission(t *testing.T) {
	tool := newBrowserLaunchToolWithLauncher(localcontrol.BrowserOptions{Enabled: true}, &fakeBrowserAppLauncher{})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"app": "telegram"})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "app must be one of") {
		t.Fatalf("reject = (%v, %#v), want app rejection", rejected, result)
	}
}

func TestBrowserOpenNormalizesBareHost(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserOpenTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"url": "example.com"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if want := []string{"open", "https://example.com"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserClickTypeAndPressRunStructuredCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want []string
	}{
		{
			name: "click",
			args: map[string]any{"ref": "e114"},
			want: []string{"click", "e114"},
		},
		{
			name: "type",
			args: map[string]any{"ref": "e114", "text": "hey from qg"},
			want: []string{"type", "e114", "hey from qg"},
		},
		{
			name: "press",
			args: map[string]any{"key": "Enter"},
			want: []string{"press", "Enter"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeBrowserRunner{}
			var tool Tool
			switch tc.name {
			case "click":
				tool = newBrowserClickTool(localBrowserTestOptions(t, runner))
			case "type":
				tool = newBrowserTypeTool(localBrowserTestOptions(t, runner))
			case "press":
				tool = newBrowserPressTool(localBrowserTestOptions(t, runner))
			}
			result := tool.Run(context.Background(), tc.args)
			if result.Status != StatusOK {
				t.Fatalf("status = %s output = %q", result.Status, result.Output)
			}
			if !reflect.DeepEqual(runner.args, tc.want) {
				t.Fatalf("args = %#v, want %#v", runner.args, tc.want)
			}
		})
	}
}

func TestBrowserTypeRejectsMissingRefBeforePermission(t *testing.T) {
	tool := newBrowserTypeTool(localcontrol.BrowserOptions{Enabled: true})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"text": "hey from qg"})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "ref is required") {
		t.Fatalf("reject = (%v, %#v), want ref rejection", rejected, result)
	}
}

func TestBrowserInstallRunsHelperInstall(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserInstallTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"with_deps": true})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if want := []string{"install", "--with-deps"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserInstallUsesLocalBrowserSafety(t *testing.T) {
	tool := newBrowserInstallTool(localcontrol.BrowserOptions{Enabled: true})
	if tool.Safety().SideEffect != SideEffectLocalBrowser {
		t.Fatalf("browser_install side effect = %s, want %s", tool.Safety().SideEffect, SideEffectLocalBrowser)
	}
}

func TestBrowserConnectRunsHelperConnect(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserConnectTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"target": "9222"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if want := []string{"connect", "9222"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserConnectAcceptsLoopbackTargetsBeforePermission(t *testing.T) {
	tool := newBrowserConnectTool(localcontrol.BrowserOptions{Enabled: true})
	for _, target := range []string{
		"9222",
		"localhost:9222",
		"127.0.0.1:9222",
		"http://127.0.0.1:9222/json/version",
		"ws://[::1]:9222/devtools/browser/abc",
	} {
		if result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"target": target}); rejected {
			t.Fatalf("target %q rejected before permission: %#v", target, result)
		}
	}
}

func TestBrowserConnectRejectsShellLikeTargetsBeforePermission(t *testing.T) {
	tool := newBrowserConnectTool(localcontrol.BrowserOptions{Enabled: true})
	for _, target := range []string{"--version", "9222;rm", "http://127.0.0.1:9222/json list"} {
		result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"target": target})
		if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "browser_connect") {
			t.Fatalf("target %q reject = (%v, %#v), want target rejection", target, rejected, result)
		}
	}
}

func TestBrowserConnectRejectsRemoteTargetsBeforePermission(t *testing.T) {
	tool := newBrowserConnectTool(localcontrol.BrowserOptions{Enabled: true})
	for _, target := range []string{
		"example.com:9222",
		"192.168.1.20:9222",
		"https://example.com:9222/json/version",
		"ws://example.com:9222/devtools/browser/abc",
		"localhost:notaport",
	} {
		result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"target": target})
		if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "browser_connect") {
			t.Fatalf("target %q reject = (%v, %#v), want target rejection", target, rejected, result)
		}
	}
}

func TestBrowserOpenRejectsUnsupportedSchemeBeforePermission(t *testing.T) {
	tool := newBrowserOpenTool(localcontrol.BrowserOptions{Enabled: true})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"url": "file:///etc/passwd"})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "http or https") {
		t.Fatalf("reject = (%v, %#v), want scheme rejection", rejected, result)
	}
}

func TestBrowserSnapshotBuildsStructuredArgs(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserSnapshotTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{
		"compact":  true,
		"depth":    float64(3),
		"selector": "#app",
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want := []string{"snapshot", "-i", "-c", "-d", "3", "-s", "#app"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserSnapshotRejectsObviousInvalidSelectorBeforePermission(t *testing.T) {
	tool := newBrowserSnapshotTool(localcontrol.BrowserOptions{Enabled: true})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{
		"selector": `main[aria-label*="channel"] [role="textbox"]*`,
	})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "selector appears invalid") {
		t.Fatalf("reject = (%v, %#v), want selector rejection", rejected, result)
	}
}

func TestBrowserActionMapsAllowedCommand(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{
		"command": "keyboard_insert_text",
		"args":    []any{"hello"},
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want := []string{"keyboard", "inserttext", "hello"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserActionConnectAcceptsLoopbackTarget(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{
		"command": "connect",
		"args":    []any{"127.0.0.1:9222"},
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want := []string{"connect", "127.0.0.1:9222"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserActionScrollUsesDirectionAndAmount(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []any
		want []string
	}{
		{name: "default", args: nil, want: []string{"scroll"}},
		{name: "direction only", args: []any{"down"}, want: []string{"scroll", "down"}},
		{name: "direction and amount", args: []any{"up", "500"}, want: []string{"scroll", "up", "500"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeBrowserRunner{}
			tool := newBrowserActionTool(localBrowserTestOptions(t, runner))
			input := map[string]any{"command": "scroll"}
			if tc.args != nil {
				input["args"] = tc.args
			}
			result := tool.Run(context.Background(), input)
			if result.Status != StatusOK {
				t.Fatalf("status = %s output = %q", result.Status, result.Output)
			}
			if !reflect.DeepEqual(runner.args, tc.want) {
				t.Fatalf("args = %#v, want %#v", runner.args, tc.want)
			}
		})
	}
}

func TestBrowserActionRejectsUnknownCommandBeforePermission(t *testing.T) {
	tool := newBrowserActionTool(localcontrol.BrowserOptions{Enabled: true})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{
		"command": "shell",
	})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "command must be one of") {
		t.Fatalf("reject = (%v, %#v), want command rejection", rejected, result)
	}
}

func TestBrowserActionRejectsInvalidStructuredArgsBeforePermission(t *testing.T) {
	tool := newBrowserActionTool(localcontrol.BrowserOptions{Enabled: true})
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "remote connect",
			args: map[string]any{"command": "connect", "args": []any{"example.com:9222"}},
			want: "loopback",
		},
		{
			name: "click ref with space",
			args: map[string]any{"command": "click", "args": []any{"e 1"}},
			want: "ref",
		},
		{
			name: "type ref option",
			args: map[string]any{"command": "type", "args": []any{"--ref", "hello"}},
			want: "ref",
		},
		{
			name: "press key with space",
			args: map[string]any{"command": "press", "args": []any{"Control L"}},
			want: "key",
		},
		{
			name: "drag target ref with space",
			args: map[string]any{"command": "drag", "args": []any{"e1", "e 2"}},
			want: "ref",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(tc.args)
			if !rejected || result.Status != StatusError || !strings.Contains(result.Output, tc.want) {
				t.Fatalf("reject = (%v, %#v), want %q", rejected, result, tc.want)
			}
		})
	}
}

func TestBrowserCDPErrorsDoNotSuggestBrowserInstall(t *testing.T) {
	runner := &fakeBrowserRunner{
		stdout:   "",
		stderr:   "✗ CDP error (DOM.describeNode): Object id doesn't reference a Node\n",
		exitCode: 1,
		err:      errors.New("exit status 1"),
	}
	tool := newBrowserSnapshotTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{})
	if result.Status != StatusError {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if strings.Contains(result.Output, "browser_install") {
		t.Fatalf("output = %q, should not suggest browser_install for CDP errors", result.Output)
	}
}

func TestMissingBrowserHelperSuggestsBrowserInstall(t *testing.T) {
	browser := localcontrol.NewBrowser(localcontrol.BrowserOptions{
		Enabled: true,
		Driver:  "definitely-missing-agent-browser",
	})

	result := browserCommandResult(context.Background(), browser, "snapshot", []string{"snapshot"})
	if result.Status != StatusError {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if !strings.Contains(result.Output, "browser_install") {
		t.Fatalf("output = %q, want browser_install hint", result.Output)
	}
}

func TestDisabledBrowserToolsAreDenied(t *testing.T) {
	var toolset []Tool
	toolset = append(toolset, NewLocalBrowserTools(localcontrol.BrowserOptions{})...)
	toolset = append(toolset, NewLocalDesktopTools(localcontrol.DesktopOptions{})...)
	toolset = append(toolset, NewLocalTerminalTools(localcontrol.TerminalOptions{})...)
	for _, tool := range toolset {
		if tool.Safety().Permission != PermissionDeny {
			t.Fatalf("%s permission = %s, want deny", tool.Name(), tool.Safety().Permission)
		}
	}
}

func TestBrowserOpenHeadedFlag(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserOpenTool(localBrowserTestOptions(t, runner))

	res := tool.Run(context.Background(), map[string]any{"url": "example.com", "headed": true})
	if res.Status != StatusOK {
		t.Fatalf("status = %s output = %q", res.Status, res.Output)
	}
	if want := []string{"--headed", "open", "https://example.com"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}

	res = tool.Run(context.Background(), map[string]any{"url": "example.com", "headed": false})
	if res.Status != StatusOK {
		t.Fatalf("status = %s output = %q", res.Status, res.Output)
	}
	if want := []string{"open", "https://example.com"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}

	if rej, ok := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"url": "example.com", "headed": "maybe"}); !ok || rej.Status != StatusError {
		t.Fatalf("non-boolean headed should reject before permission, got (%v, %#v)", ok, rej)
	}
}

func TestBrowserTypeAndFillEscapeDashedText(t *testing.T) {
	runner := &fakeBrowserRunner{}
	typeTool := newBrowserTypeTool(localBrowserTestOptions(t, runner))
	res := typeTool.Run(context.Background(), map[string]any{"ref": "@e1", "text": "-abc"})
	if res.Status != StatusOK {
		t.Fatalf("browser_type status = %s output = %q", res.Status, res.Output)
	}
	if want := []string{"type", "@e1", "--", "-abc"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("browser_type args = %#v, want %#v", runner.args, want)
	}

	actionTool := newBrowserActionTool(localBrowserTestOptions(t, runner))
	for _, cmd := range []string{"fill", "type"} {
		res := actionTool.Run(context.Background(), map[string]any{"command": cmd, "args": []any{"@e2", "--json"}})
		if res.Status != StatusOK {
			t.Fatalf("%s status = %s output = %q", cmd, res.Status, res.Output)
		}
		if want := []string{cmd, "@e2", "--", "--json"}; !reflect.DeepEqual(runner.args, want) {
			t.Fatalf("%s args = %#v, want %#v", cmd, runner.args, want)
		}
	}
}

func TestBrowserActionScrollRejectsOldNumericDeltas(t *testing.T) {
	tool := newBrowserActionTool(localcontrol.BrowserOptions{Enabled: true})
	rej, ok := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{
		"command": "scroll",
		"args":    []any{"0", "-500"},
	})
	if !ok || rej.Status != StatusError {
		t.Fatalf("old numeric scroll deltas should be rejected, got (%v, %#v)", ok, rej)
	}
}

func TestBrowserActionTabNewWithLabel(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))
	res := tool.Run(context.Background(), map[string]any{
		"command": "tab",
		"args":    []any{"new", "--label", "docs", "https://example.com"},
	})
	if res.Status != StatusOK {
		t.Fatalf("status = %s output = %q", res.Status, res.Output)
	}
	want := []string{"tab", "new", "--label", "docs", "https://example.com"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserActionSkillsReadOnlyValidation(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))

	for _, tc := range []struct {
		args []any
		want []string
	}{
		{args: nil, want: []string{"skills"}},
		{args: []any{"list"}, want: []string{"skills", "list"}},
		{args: []any{"get", "core"}, want: []string{"skills", "get", "core"}},
		{args: []any{"get", "core", "--full"}, want: []string{"skills", "get", "core", "--full"}},
		{args: []any{"get", "core", "--ref", "commands"}, want: []string{"skills", "get", "core", "--ref", "commands"}},
		{args: []any{"path", "core"}, want: []string{"skills", "path", "core"}},
	} {
		input := map[string]any{"command": "skills"}
		if tc.args != nil {
			input["args"] = tc.args
		}
		res := tool.Run(context.Background(), input)
		if res.Status != StatusOK {
			t.Fatalf("skills %v failed: %q", tc.args, res.Output)
		}
		if !reflect.DeepEqual(runner.args, tc.want) {
			t.Fatalf("skills %v args = %#v, want %#v", tc.args, runner.args, tc.want)
		}
	}

	for _, bad := range [][]any{
		{"add", "foo"},
		{"get", "core", "--executable-path"},
		{"get"},
		{"list", "extra"},
	} {
		if rej, ok := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{
			"command": "skills",
			"args":    bad,
		}); !ok || rej.Status != StatusError {
			t.Fatalf("skills %v should reject before permission, got (%v, %#v)", bad, ok, rej)
		}
	}
}

func TestBrowserFirstUseErrorsV0430AndLinuxARM64(t *testing.T) {
	realV0430Missing := "✗ Auto-launch failed: Chrome not found. Checked:\n" +
		"  - agent-browser cache: /tmp/fake/.agent-browser/browsers\n" +
		"  - System Chrome installations\n" +
		"  - Puppeteer browser cache\n" +
		"  - Playwright browser cache\n" +
		"Run `agent-browser install` to download Chrome, or use --executable-path.\n"

	if got := browserFirstUseHint("linux", "amd64", "open", realV0430Missing); !strings.Contains(got, "browser_install") {
		t.Fatalf("linux/amd64 open hint = %q, want browser_install", got)
	}
	if got := browserFirstUseHint("linux", "arm64", "open", realV0430Missing); !strings.Contains(got, "sudo apt install chromium") || !strings.Contains(got, "dnf install chromium") {
		t.Fatalf("linux/arm64 open hint = %q, want system chromium instructions", got)
	}
	if got := browserFirstUseHint("linux", "arm64", "install", "Chrome for Testing is not available"); !strings.Contains(got, "sudo apt install chromium") || !strings.Contains(got, "dnf install chromium") {
		t.Fatalf("linux/arm64 install hint = %q, want system chromium instructions", got)
	}

	runner := &fakeBrowserRunner{
		stderr:   realV0430Missing,
		exitCode: 1,
		err:      errors.New("exit status 1"),
	}
	tool := newBrowserOpenTool(localBrowserTestOptions(t, runner))
	res := tool.Run(context.Background(), map[string]any{"url": "https://example.com"})
	if res.Status != StatusError {
		t.Fatalf("status = %s, want error", res.Status)
	}
	if !strings.Contains(res.Output, "browser_install") && !strings.Contains(res.Output, "chromium") {
		t.Fatalf("real v0.43.0 missing browser error should surface install guidance, got %q", res.Output)
	}
}

func TestBrowserToolsRealExampleCom(t *testing.T) {
	helperPath := strings.TrimSpace(os.Getenv("QG_AGENT_BROWSER_BIN"))
	if helperPath == "" {
		t.Skip("set QG_AGENT_BROWSER_BIN to run real agent-browser integration test")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AGENT_BROWSER_SESSION", "qg-real-example")

	reg := NewRegistry()
	for _, tool := range NewLocalBrowserTools(localcontrol.BrowserOptions{
		Enabled:    true,
		HelperPath: helperPath,
	}) {
		reg.Register(tool)
	}

	ctx := context.Background()
	opts := RunOptions{PermissionGranted: true}
	defer reg.RunWithOptions(ctx, "browser_action", map[string]any{"command": "close"}, opts)

	openRes := reg.RunWithOptions(ctx, "browser_open", map[string]any{"url": "https://example.com"}, opts)
	if openRes.Status != StatusOK {
		t.Fatalf("browser_open failed: %s", openRes.Output)
	}
	t.Logf("browser_open:\n%s", openRes.Output)

	snapRes := reg.RunWithOptions(ctx, "browser_snapshot", map[string]any{"interactive": true}, opts)
	if snapRes.Status != StatusOK {
		t.Fatalf("browser_snapshot failed: %s", snapRes.Output)
	}
	t.Logf("browser_snapshot:\n%s", snapRes.Output)

	clickRes := reg.RunWithOptions(ctx, "browser_click", map[string]any{"ref": "@e1"}, opts)
	if clickRes.Status != StatusOK {
		t.Fatalf("browser_click failed: %s", clickRes.Output)
	}
	t.Logf("browser_click:\n%s", clickRes.Output)

	urlRes := reg.RunWithOptions(ctx, "browser_action", map[string]any{"command": "get_url"}, opts)
	if urlRes.Status != StatusOK {
		t.Fatalf("browser_action get_url failed: %s", urlRes.Output)
	}
	t.Logf("browser_action get_url:\n%s", urlRes.Output)
	if !strings.Contains(urlRes.Output, "iana.org") {
		t.Fatalf("expected navigated URL on iana.org, got %q", urlRes.Output)
	}
}
