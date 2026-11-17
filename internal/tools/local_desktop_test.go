package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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
	args       []string
	calls      [][]string
	stdin      string
	hasStdin   bool
	stdout     string
	stderr     string
	exitCode   int
	err        error
	writePNG   []byte
	wrotePath  string
	pathExists bool
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
	if len(runner.writePNG) > 0 && len(args) > 0 {
		target := args[len(args)-1]
		if strings.HasSuffix(target, ".png") {
			runner.wrotePath = target
			if err := os.WriteFile(target, runner.writePNG, 0o600); err == nil {
				runner.pathExists = true
			}
		}
	}
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
		{name: "option-like app", args: map[string]any{"app": "--evil"}, want: "app"},
		{name: "option-like surface", args: map[string]any{"surface": "--evil"}, want: "surface"},
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

func TestDesktopActionBuildsArgvForCommandsAndStdin(t *testing.T) {
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
			name:     "type via stdin puts ref before args",
			args:     map[string]any{"command": "type", "ref": "@s8f3k2p9:e2", "text": "segredo-super-123", "args": []any{"--interval-ms", "50"}},
			wantArgs: []string{"type", "--stdin", "@s8f3k2p9:e2", "--interval-ms", "50"},
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
			name:     "select preserves ref then text order",
			args:     map[string]any{"command": "select", "ref": "@s8f3k2p9:e4", "text": "Option B"},
			wantArgs: []string{"select", "@s8f3k2p9:e4", "Option B"},
		},
		{
			name:     "toggle check uncheck expand collapse scroll-to",
			args:     map[string]any{"command": "toggle", "ref": "@s8f3k2p9:e6"},
			wantArgs: []string{"toggle", "@s8f3k2p9:e6"},
		},
		{
			name:     "scroll ref before args",
			args:     map[string]any{"command": "scroll", "ref": "@s8f3k2p9:e1", "args": []any{"--direction", "down", "--amount", "3"}},
			wantArgs: []string{"scroll", "@s8f3k2p9:e1", "--direction", "down", "--amount", "3"},
		},
		{
			name:     "press uses text as combo",
			args:     map[string]any{"command": "press", "text": "cmd+c", "args": []any{"--app", "Notes", "--force"}},
			wantArgs: []string{"press", "--app", "Notes", "--force", "cmd+c"},
		},
		{
			name:     "hover with xy",
			args:     map[string]any{"command": "hover", "headed": true, "args": []any{"--xy=-100,50"}},
			wantArgs: []string{"--headed", "hover", "--xy=-100,50"},
		},
		{
			name:     "launch",
			args:     map[string]any{"command": "launch", "text": "TextEdit", "args": []any{"--activate", "--no-attach"}},
			wantArgs: []string{"launch", "--activate", "--no-attach", "TextEdit"},
		},
		{
			name:     "close-app",
			args:     map[string]any{"command": "close-app", "text": "TextEdit", "args": []any{"--force"}},
			wantArgs: []string{"close-app", "--force", "TextEdit"},
		},
		{
			name:     "focus-window",
			args:     map[string]any{"command": "focus-window", "args": []any{"--window-id", "w-4521"}},
			wantArgs: []string{"focus-window", "--window-id", "w-4521"},
		},
		{
			name:     "list-apps",
			args:     map[string]any{"command": "list-apps", "args": []any{"--app", "Finder"}},
			wantArgs: []string{"list-apps", "--app", "Finder"},
		},
		{
			name:     "list-windows",
			args:     map[string]any{"command": "list-windows", "args": []any{"--app", "Finder", "--timeout-ms", "4000"}},
			wantArgs: []string{"list-windows", "--app", "Finder", "--timeout-ms", "4000"},
		},
		{
			name:     "wait",
			args:     map[string]any{"command": "wait", "args": []any{"--element", "@s8f3k2p9:e9", "--predicate", "actionable", "--timeout", "5000"}},
			wantArgs: []string{"wait", "--element", "@s8f3k2p9:e9", "--predicate", "actionable", "--timeout", "5000"},
		},
		{
			name:     "find",
			args:     map[string]any{"command": "find", "args": []any{"--app", "Notes", "--role", "button", "--name", "Save", "--exact"}},
			wantArgs: []string{"find", "--app", "Notes", "--role", "button", "--name", "Save", "--exact"},
		},
		{
			name:     "get",
			args:     map[string]any{"command": "get", "ref": "@s8f3k2p9:e2", "args": []any{"--property", "value"}},
			wantArgs: []string{"get", "@s8f3k2p9:e2", "--property", "value"},
		},
		{
			name:     "is",
			args:     map[string]any{"command": "is", "ref": "@s8f3k2p9:e6", "args": []any{"--property", "checked"}},
			wantArgs: []string{"is", "@s8f3k2p9:e6", "--property", "checked"},
		},
		{
			name:     "clipboard-get",
			args:     map[string]any{"command": "clipboard-get", "args": []any{"--format", "auto"}},
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
			name:     "clipboard-set with image flag skips stdin",
			args:     map[string]any{"command": "clipboard-set", "args": []any{"--image", "/tmp/a.png"}},
			wantArgs: []string{"clipboard-set", "--image", "/tmp/a.png"},
		},
		{
			name:     "skills get desktop",
			args:     map[string]any{"command": "skills", "args": []any{"get", "desktop"}},
			wantArgs: []string{"skills", "get", "desktop"},
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

func TestDesktopActionRejectsForbiddenFlagsAndInvalidInputsBeforePermission(t *testing.T) {
	tool := newDesktopActionTool(localcontrol.DesktopOptions{Enabled: true})
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{name: "unknown command", args: map[string]any{"command": "sandbox"}, want: "command must be one of"},
		{name: "bare ref", args: map[string]any{"command": "click", "ref": "@e8"}, want: "@<snapshot_id>:e<N>"},
		{name: "missing text for type", args: map[string]any{"command": "type", "ref": "@s1:e1"}, want: "text"},
		{name: "missing text for set-value", args: map[string]any{"command": "set-value", "ref": "@s1:e1"}, want: "text"},
		{name: "missing text and args for clipboard-set", args: map[string]any{"command": "clipboard-set"}, want: "text"},
		{name: "nul byte in stdin text", args: map[string]any{"command": "type", "ref": "@s1:e1", "text": "a\x00b"}, want: "NUL"},
		{name: "screenshot rejects text path", args: map[string]any{"command": "screenshot", "text": "/tmp/out.png"}, want: "screenshot"},
		{name: "forbidden --stdin", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"--stdin"}}, want: "--stdin"},
		{name: "forbidden --headed", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"--headed"}}, want: "--headed"},
		{name: "forbidden --screenshot", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"--screenshot=/tmp/x.html"}}, want: "--screenshot"},
		{name: "forbidden --debug", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"--debug"}}, want: "--debug"},
		{name: "forbidden --trace", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"--trace", "/tmp/t"}}, want: "--trace"},
		{name: "forbidden --trace-strict", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"--trace-strict"}}, want: "--trace-strict"},
		{name: "forbidden --session", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"--session=s1"}}, want: "--session"},
		{name: "forbidden --agent-id", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"--agent-id", "a1"}}, want: "--agent-id"},
		{name: "forbidden -v", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"-v"}}, want: "-v"},
		{name: "forbidden --verbose", args: map[string]any{"command": "click", "ref": "@s1:e1", "args": []any{"--verbose"}}, want: "--verbose"},
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

func TestDesktopActionScreenshotAttachesImageWithoutBase64AndCleansTempFile(t *testing.T) {
	// 1x1 PNG bytes.
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	runner := &fakeDesktopRunner{
		writePNG: pngBytes,
		stdout:   `{"version":"2.6","ok":true,"command":"screenshot","data":{"base64":"SHOULD_NOT_APPEAR"}}`,
	}
	tool := newDesktopActionTool(localDesktopTestOptions(t, runner))
	result := tool.Run(context.Background(), map[string]any{
		"command": "screenshot",
		"args":    []any{"--app", "Finder"},
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if !runner.pathExists || runner.wrotePath == "" {
		t.Fatalf("expected managed temp png path in argv, got args=%#v", runner.args)
	}
	if _, err := os.Stat(runner.wrotePath); !os.IsNotExist(err) {
		t.Fatalf("temp screenshot file %q should be deleted after loading", runner.wrotePath)
	}
	if len(result.Images) != 1 || result.Images[0].MediaType != "image/png" {
		t.Fatalf("Images = %#v, want 1 PNG image", result.Images)
	}
	if !strings.Contains(result.Output, "1x1") || !strings.Contains(result.Output, "scale 1.000") {
		t.Fatalf("Output = %q, want dimensions and scale", result.Output)
	}
	if strings.Contains(result.Output, "SHOULD_NOT_APPEAR") || strings.Contains(result.Output, "base64") {
		t.Fatalf("Output should be a short summary without base64, got %q", result.Output)
	}
}

func TestDesktopActionScreenshotDownscalesOversizedPNG(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3932, 2190))
	for y := 0; y < 2190; y += 64 {
		for x := 0; x < 3932; x += 64 {
			src.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	runner := &fakeDesktopRunner{writePNG: buf.Bytes()}
	tool := newDesktopActionTool(localDesktopTestOptions(t, runner))
	result := tool.Run(context.Background(), map[string]any{"command": "screenshot"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if len(result.Images) != 1 || result.Images[0].MediaType != "image/png" {
		t.Fatalf("Images = %#v, want 1 resized PNG", result.Images)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(result.Images[0].Data))
	if err != nil {
		t.Fatalf("decode resized png: %v", err)
	}
	if cfg.Width != 1568 || cfg.Height != 873 {
		t.Fatalf("resized dimensions = %dx%d, want 1568x873", cfg.Width, cfg.Height)
	}
	if !strings.Contains(result.Output, "3932x2190 -> 1568x873, scale 0.399") {
		t.Fatalf("Output = %q, want scale summary", result.Output)
	}
}

func TestDesktopActionScreenshotFallsBackToJPEGWhenPNGExceedsLimit(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1568, 1200))
	var s uint32 = 0x12345678
	for i := 0; i < len(img.Pix); i += 4 {
		s = s*1664525 + 1013904223
		img.Pix[i] = uint8(s >> 24)
		img.Pix[i+1] = uint8(s >> 16)
		img.Pix[i+2] = uint8(s >> 8)
		img.Pix[i+3] = 255
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	runner := &fakeDesktopRunner{writePNG: buf.Bytes()}
	tool := newDesktopActionTool(localDesktopTestOptions(t, runner))
	result := tool.Run(context.Background(), map[string]any{"command": "screenshot"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if len(result.Images) != 1 || result.Images[0].MediaType != "image/jpeg" {
		t.Fatalf("MediaType = %q, want image/jpeg fallback", result.Images[0].MediaType)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(result.Images[0].Data))
	if err != nil {
		t.Fatalf("decode fallback jpeg: %v", err)
	}
	if cfg.Width != 1568 || cfg.Height != 1200 {
		t.Fatalf("jpeg dimensions = %dx%d, want 1568x1200", cfg.Width, cfg.Height)
	}
	if !strings.Contains(result.Output, "1568x1200") || !strings.Contains(result.Output, "scale 1.000") {
		t.Fatalf("Output = %q, want dimensions and scale", result.Output)
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

func TestDesktopHelperFailureGuidanceExplainsMissingWindowsRuntime(t *testing.T) {
	msg := "Error running desktop helper: exit status 0xc0000135"
	for goarch, want := range map[string]string{"arm64": "Microsoft.VCRedist.2015+.arm64", "amd64": "vc_redist.x64.exe"} {
		if got := desktopHelperFailureGuidance("windows", goarch, msg); !strings.Contains(got, want) || !strings.Contains(got, "VCRUNTIME140.dll") {
			t.Fatalf("%s guidance = %q, want %q", goarch, got, want)
		}
	}
	if got := desktopHelperFailureGuidance("linux", "arm64", msg); got != "" {
		t.Fatalf("linux guidance = %q, want empty", got)
	}
	if got := desktopHelperFailureGuidance("plan9", "amd64", "unsupported platform"); !strings.Contains(got, "sem suporte") {
		t.Fatalf("unsupported guidance = %q", got)
	}
}
