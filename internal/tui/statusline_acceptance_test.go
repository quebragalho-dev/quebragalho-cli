package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// Acceptance suite for the custom statusline and /statusline, written from
// the spec, driven only through the model's Init/Update/View loop, the files
// the user owns (user/project config.json, the statusline scripts directory)
// and the provider request. The interface contract (config schema, staging
// format, how the TUI receives the resolved config) is the one documented in
// docs/TUI_TELEMETRY.md; it lives only in slConfigJSON, slProposalJSON,
// slStagingPath and newSLModel.

const (
	slProjectWarning  = "Statusline: comando do projeto ignorado (aviso de teste)"
	slBranch          = "feat/rodape-x"
	slQGKeySentinel   = "qg-key-SENTINEL-7f3a"
	slProfileSentinel = "profile-key-SENTINEL-91bd"
)

// slSetupWait is the deadline for anything these tests wait on as a
// condition: a preview appearing, a script installed, a config written, a
// command run. It is generous on purpose. A turn, a process spawn or a file
// write that takes milliseconds on a quiet machine can take seconds on a
// loaded one, and a condition either happens or it does not: a tight deadline
// only turns a slow machine into a false failure. Waits that measure that
// nothing happened, or that bound a timeout the product promises, keep their
// own tighter clocks.
const slSetupWait = 15 * time.Second

type slEnv struct {
	root          string
	configDir     string
	userConfig    string
	scriptsDir    string
	workspace     string
	projectConfig string
}

func newSLEnv(t *testing.T) slEnv {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	configHome := filepath.Join(root, "config")
	for _, dir := range []string{home, configHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("APPDATA", configHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "cache"))
	t.Setenv("QG_CRED_STORAGE", "file")
	t.Setenv("QG_THEME", "")
	t.Setenv("NO_COLOR", "")
	// An exported provider (a shell spawned by qg, or a test that switched
	// profiles) would re-point Resolve at a profile this sandbox lacks.
	t.Setenv(config.ActiveProviderEnv, "")

	base, err := config.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(base, root) {
		t.Fatalf("hermeticity: user config dir %q escaped the fake root %q", base, root)
	}
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".git", "HEAD"), []byte("ref: refs/heads/"+slBranch+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return slEnv{
		root:          root,
		configDir:     filepath.Join(base, "qg"),
		userConfig:    filepath.Join(base, "qg", "config.json"),
		scriptsDir:    filepath.Join(base, "qg", "statusline"),
		workspace:     workspace,
		projectConfig: filepath.Join(workspace, ".qg", "config.json"),
	}
}

// slLine is one statusline row: built-in field ids on each side, in order.
type slLine struct {
	left  []string
	right []string
}

type slLayout struct {
	lines     []slLine
	separator string
	command   string
	timeoutMS int
	refreshMS int
	// script is what a /statusline proposal stages next to proposal.json.
	script slScript
	// dropCommand writes "command": null into a proposal: the explicit
	// removal of the current command (an absent key keeps it).
	dropCommand bool
}

type slScript struct {
	name string
	body string
}

// "command" in a test layout marks where the command output is wanted. The
// contract has no such field id: the output renders as its own footer lines
// after the field lines, so the encoders drop it.
const slCommandMarker = "command"

// slConfigKey is the config.json key of the statusline block.
const slConfigKey = "statusline"

// slLinesJSON encodes the field lines, dropping the command marker and any
// line left empty by it.
func slLinesJSON(layout slLayout) []map[string]any {
	var lines []map[string]any
	for _, line := range layout.lines {
		entry := map[string]any{}
		for side, ids := range map[string][]string{"left": line.left, "right": line.right} {
			var kept []string
			for _, id := range ids {
				if id != slCommandMarker {
					kept = append(kept, id)
				}
			}
			if len(kept) > 0 {
				entry[side] = kept
			}
		}
		if len(entry) > 0 {
			lines = append(lines, entry)
		}
	}
	return lines
}

// slConfigJSON encodes the config block per docs/TUI_TELEMETRY.md:
//
//	"statusline": {
//	  "lines": [{"left": ["mode", "effort"], "right": ["context"]}],
//	  "separator": " │ ",
//	  "command": {"command": "meu-statusline"},
//	  "timeoutMs": 2000,
//	  "refreshMs": 5000
//	}
func slConfigJSON(layout slLayout) map[string]any {
	body := map[string]any{}
	if lines := slLinesJSON(layout); len(lines) > 0 {
		body["lines"] = lines
	}
	if layout.separator != "" {
		body["separator"] = layout.separator
	}
	if layout.command != "" {
		body["command"] = map[string]any{"command": layout.command}
	}
	if layout.timeoutMS > 0 {
		body["timeoutMs"] = layout.timeoutMS
	}
	if layout.refreshMS > 0 {
		body["refreshMs"] = layout.refreshMS
	}
	return body
}

// slProposalJSON encodes a /statusline proposal (staging proposal.json): the
// same layout keys plus "script", a file name inside the staging dir.
func slProposalJSON(layout slLayout) map[string]any {
	body := map[string]any{}
	if lines := slLinesJSON(layout); len(lines) > 0 {
		body["lines"] = lines
	}
	if layout.separator != "" {
		body["separator"] = layout.separator
	}
	if layout.script.name != "" {
		body["script"] = layout.script.name
	}
	if layout.dropCommand {
		body["command"] = nil
	}
	if layout.timeoutMS > 0 {
		body["timeoutMs"] = layout.timeoutMS
	}
	if layout.refreshMS > 0 {
		body["refreshMs"] = layout.refreshMS
	}
	return body
}

// slStatusLineFrom extracts the statusline block from a config document,
// case-insensitively (encoding/json matches keys that way too).
func slStatusLineFrom(t *testing.T, data []byte) (json.RawMessage, bool) {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("config is not valid JSON: %v\n%s", err, data)
	}
	for key, value := range doc {
		if strings.EqualFold(key, "statusline") {
			trimmed := strings.TrimSpace(string(value))
			if trimmed == "null" || trimmed == "{}" {
				return nil, false
			}
			return value, true
		}
	}
	return nil, false
}

// slUserConfigBase is a realistic pre-existing user config: provider wiring
// and preferences that a statusline write must never clobber.
func slUserConfigBase() map[string]any {
	return map[string]any{
		"activeProvider": "accept-gw",
		"providers": []map[string]any{{
			"name":         "accept-gw",
			"providerKind": "openai-compatible",
			"baseURL":      "http://127.0.0.1:9/v1",
			"model":        "accept-model",
			"apiKeyEnv":    "ACCEPT_GATEWAY_KEY",
		}},
		"preferences": map[string]any{"theme": "dark", "favoriteModels": []string{"accept-model"}},
	}
}

func writeJSONFile(t *testing.T, path string, doc any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeUserStatusLine(t *testing.T, env slEnv, layout slLayout) {
	t.Helper()
	doc := slUserConfigBase()
	doc[slConfigKey] = slConfigJSON(layout)
	writeJSONFile(t, env.userConfig, doc)
}

// writeScript drops an executable sh script into the user statusline dir and
// returns the command line that runs it.
func writeScript(t *testing.T, env slEnv, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(env.scriptsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(env.scriptsDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return "sh '" + path + "'"
}

// slProvider is a scripted model. onRequest runs inside the agent turn, which
// is where a real model would call write_file on the staging path.
type slProvider struct {
	mu        sync.Mutex
	requests  []qgruntime.CompletionRequest
	onRequest func(request qgruntime.CompletionRequest)
	// usage, when set, is reported on every call before the stream ends.
	usage *qgruntime.Usage
}

func (p *slProvider) StreamCompletion(_ context.Context, request qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, request)
	hook := p.onRequest
	reported := p.usage
	p.mu.Unlock()
	if hook != nil {
		hook(request)
	}
	ch := make(chan qgruntime.StreamEvent, 3)
	ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventText, Content: "Proposta gravada no staging."}
	if reported != nil {
		ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventUsage, Usage: *reported}
	}
	ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventDone}
	close(ch)
	return ch, nil
}

func (p *slProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *slProvider) requestText(index int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if index >= len(p.requests) {
		return ""
	}
	var all strings.Builder
	for _, message := range p.requests[index].Messages {
		all.WriteString(message.Content)
		all.WriteString("\n")
	}
	return all.String()
}

func newSLModel(t *testing.T, env slEnv, provider *slProvider) model {
	t.Helper()
	if provider == nil {
		provider = &slProvider{}
	}
	// Same resolution as the CLI launch (internal/cli/app.go): discover the
	// config files, Resolve them, hand the resolved statusline to the TUI.
	opts, err := config.DefaultResolveOptions(env.workspace)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := config.Resolve(opts)
	if err != nil && !errors.Is(err, config.ErrNoActiveProvider) && !errors.Is(err, config.ErrProviderRequiresModel) {
		t.Fatalf("QG refuses to start with this config: %v", err)
	}
	userConfigPath, err := config.DefaultUserConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	// Same join as the CLI launch: the ignored project command note, then the
	// adjustments Resolve reported. The CLI side is pinned in
	// internal/cli/statusline_acceptance_test.go.
	var notes []string
	if resolved.ProjectStatusLineCommandIgnored {
		notes = append(notes, slProjectWarning)
	}
	notes = append(notes, resolved.StatusLineWarnings...)
	warning := strings.Join(notes, "\n")
	m := newModel(context.Background(), Options{
		Cwd:               env.workspace,
		Version:           "9.9.9-accept",
		UserConfigPath:    userConfigPath,
		ProjectConfigPath: opts.ProjectConfigPath,
		StatusLine:        resolved.StatusLine,
		StatusLineWarning: warning,
		ProviderName:      "accept-gw",
		ModelName:         "accept-model",
		ProviderProfile: config.ProviderProfile{
			Name:      "accept-gw",
			Model:     "accept-model",
			APIKey:    slProfileSentinel,
			APIKeyEnv: "ACCEPT_GATEWAY_KEY",
		},
		Provider:        provider,
		Registry:        tools.NewRegistry(),
		ReasoningEffort: "high",
		AltScreen:       true,
	})
	// No SessionStore above, so turns take their presence lease in the
	// default store under the fake XDG_DATA_HOME; drop it before TempDir
	// removal (an open locked presence.lock cannot be unlinked on Windows).
	releaseSessionLocksUnder(t, sessions.DefaultRoot(nil))
	return m
}

// slPump drives the model the way tea.Program does: every tea.Cmd runs on its
// own goroutine, every message comes back through Update on the test
// goroutine, and View is called between messages.
type slPump struct {
	t             *testing.T
	m             tea.Model
	msgs          chan tea.Msg
	done          chan struct{}
	slowest       time.Duration
	slowestUpdate time.Duration
	width         int
}

func startSLPump(t *testing.T, m model, width, height int) *slPump {
	t.Helper()
	p := &slPump{t: t, m: m, msgs: make(chan tea.Msg, 1<<14), done: make(chan struct{}), width: width}
	t.Cleanup(func() {
		// A turn still in flight keeps writing session files under the test
		// dirs; let it land before t.TempDir removes them (cleanups run in
		// reverse order, so this one runs first).
		p.waitFor(5*time.Second, func(string) bool {
			current, ok := p.m.(model)
			return !ok || !current.pending
		})
		close(p.done)
	})
	p.dispatch(p.m.Init())
	p.deliver(tea.WindowSizeMsg{Width: width, Height: height})
	return p
}

func (p *slPump) dispatch(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		select {
		case p.msgs <- msg:
		case <-p.done:
		}
	}()
}

func (p *slPump) deliver(msg tea.Msg) {
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, cmd := range batch {
			p.dispatch(cmd)
		}
		return
	}
	// tea.Sequence's message is an unexported []tea.Cmd; keep its order.
	if value := reflect.ValueOf(msg); value.Kind() == reflect.Slice && value.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		cmds := make([]tea.Cmd, value.Len())
		for i := range cmds {
			cmds[i], _ = value.Index(i).Interface().(tea.Cmd)
		}
		go func() {
			for _, cmd := range cmds {
				if cmd == nil {
					continue
				}
				msg := cmd()
				select {
				case p.msgs <- msg:
				case <-p.done:
					return
				}
			}
		}()
		return
	}
	start := time.Now()
	next, cmd := p.m.Update(msg)
	if took := time.Since(start); took > p.slowestUpdate {
		p.slowestUpdate = took
	}
	p.m = next
	p.dispatch(cmd)
}

// view renders a frame and records how long View took.
func (p *slPump) view() string {
	start := time.Now()
	out := renderContent(p.m.View())
	if took := time.Since(start); took > p.slowest {
		p.slowest = took
	}
	return out
}

func (p *slPump) waitFor(timeout time.Duration, cond func(frame string) bool) (string, bool) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		frame := p.view()
		if cond(frame) {
			return frame, true
		}
		select {
		case msg := <-p.msgs:
			p.deliver(msg)
		case <-deadline.C:
			return p.view(), false
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (p *slPump) runFor(d time.Duration) string {
	frame, _ := p.waitFor(d, func(string) bool { return false })
	return frame
}

func (p *slPump) submit(text string) {
	m, ok := p.m.(model)
	if !ok {
		p.t.Fatalf("pump model has type %T", p.m)
	}
	m.input.SetValue(text)
	p.m = m
	p.deliver(testKey(tea.KeyEnter))
}

func (p *slPump) key(code rune) { p.deliver(testKey(code)) }

func plainFrame(frame string) string { return ansiPattern.ReplaceAllString(frame, "") }

func containsPlain(want string) func(string) bool {
	return func(frame string) bool { return strings.Contains(plainFrame(frame), want) }
}

func useTrueColor(t *testing.T) {
	t.Helper()
	old := lipgloss.Writer.Profile
	lipgloss.Writer.Profile = colorprofile.TrueColor
	t.Cleanup(func() { lipgloss.Writer.Profile = old })
}

func requireSh(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts below are POSIX sh; the cmd /C path needs its own Windows run")
	}
}

func fileLines(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func waitForFile(p *slPump, path string, timeout time.Duration) bool {
	_, ok := p.waitFor(timeout, func(string) bool {
		_, err := os.Stat(path)
		return err == nil
	})
	return ok
}

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	lines := fileLines(path)
	if len(lines) == 0 {
		t.Fatalf("pid file %s is empty", path)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		t.Fatalf("pid file %s: %v", path, err)
	}
	t.Cleanup(func() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	})
	return pid
}

func frameLinesContaining(frame, marker string) []string {
	var out []string
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(plainFrame(line), marker) {
			out = append(out, line)
		}
	}
	return out
}

// slFooter is what sits below the composer box: the statusline rows. With no
// box in the frame it returns the whole frame, so the check never weakens.
func slFooter(frame string) string {
	lines := strings.Split(frame, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "╰") {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return frame
}

func lastLine(frame string) string {
	lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	return lines[len(lines)-1]
}

// Captured on 172d6231: alt screen (as tui.Run always uses), TrueColor
// profile, workspace ~/workspace on branch feat/rodape-x, effort high, no
// statusline config.
var slGoldenDefault = []struct {
	ask   bool
	width int
	want  string
}{
	{false, 40, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[1;38;2;52;211;153mautomático\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m"},
	{false, 58, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[1;38;2;52;211;153mautomático\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m"},
	{false, 70, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[1;38;2;52;211;153mautomático\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m          \x1b[38;2;163;163;163mcache —\x1b[m"},
	{false, 80, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[1;38;2;52;211;153mautomático\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m                    \x1b[38;2;163;163;163mcache —\x1b[m"},
	{false, 96, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[1;38;2;52;211;153mautomático\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m                                    \x1b[38;2;163;163;163mcache —\x1b[m"},
	{false, 100, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[1;38;2;52;211;153mautomático\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m                                        \x1b[38;2;163;163;163mcache —\x1b[m"},
	{false, 120, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[1;38;2;52;211;153mautomático\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m                                                            \x1b[38;2;163;163;163mcache —\x1b[m"},
	{true, 40, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[38;2;245;184;61maprovação\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m"},
	{true, 58, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[38;2;245;184;61maprovação\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m"},
	{true, 70, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[38;2;245;184;61maprovação\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m           \x1b[38;2;163;163;163mcache —\x1b[m"},
	{true, 80, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[38;2;245;184;61maprovação\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m                     \x1b[38;2;163;163;163mcache —\x1b[m"},
	{true, 96, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[38;2;245;184;61maprovação\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m                                     \x1b[38;2;163;163;163mcache —\x1b[m"},
	{true, 100, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[38;2;245;184;61maprovação\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m                                         \x1b[38;2;163;163;163mcache —\x1b[m"},
	{true, 120, "  \x1b[1;38;2;183;254;96m●\x1b[m \x1b[38;2;245;184;61maprovação\x1b[m\x1b[38;2;163;163;163m · \x1b[m\x1b[1;38;2;183;254;96malto\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;163;163;163m\ue0a0 feat/rodape-x\x1b[m\x1b[38;2;34;34;34m │ \x1b[m\x1b[38;2;138;138;138m~/workspace\x1b[m                                                             \x1b[38;2;163;163;163mcache —\x1b[m"},
}

// slNativeCwd is how shortenPath renders the workspace on this OS: "~"
// plus the OS separator plus "workspace". The goldens below are written
// with the Unix "/", so comparisons go through this: on Windows the footer
// shows `~\workspace`, which is the correct product behaviour (a literal "/"
// would fail spuriously in positive checks and pass vacuously in negative
// ones there).
func slNativeCwd() string {
	return filepath.Join("~", "workspace")
}

// 1. Sem config, a statusline é a de hoje, byte a byte.
func TestStatusLineAcceptance_NoConfigIsByteIdentical(t *testing.T) {
	useTrueColor(t)
	for _, variant := range []string{"no user config file", "user config without statusLine"} {
		t.Run(variant, func(t *testing.T) {
			env := newSLEnv(t)
			if variant == "user config without statusLine" {
				writeJSONFile(t, env.userConfig, slUserConfigBase())
			}
			for _, tc := range slGoldenDefault {
				m := newSLModel(t, env, nil)
				if tc.ask {
					m.permissionMode = "ask"
				}
				next, _ := m.Update(tea.WindowSizeMsg{Width: tc.width, Height: 30})
				got := lastLine(renderContent(next.(model).View()))
				// The golden rows render "~/workspace"; the footer uses the
				// OS separator, so compare against the native form.
				want := strings.ReplaceAll(tc.want, "~/workspace", slNativeCwd())
				if got != want {
					t.Errorf("ask=%v width=%d: statusline changed without config\n got=%q\nwant=%q", tc.ask, tc.width, got, want)
				}
			}
		})
	}
}

// 2. Campos, ordem, lado e separador vêm da config.
func TestStatusLineAcceptance_ConfigOrdersFieldsSidesAndSeparator(t *testing.T) {
	env := newSLEnv(t)
	writeUserStatusLine(t, env, slLayout{
		lines:     []slLine{{left: []string{"branch", "mode"}, right: []string{"effort"}}},
		separator: " ¦ ",
	})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	frame := p.runFor(300 * time.Millisecond)
	row := strings.TrimRight(plainFrame(lastLine(frame)), " ")
	if !strings.Contains(row, slBranch) {
		t.Fatalf("configured branch field missing from the statusline row %q", row)
	}
	branchAt, modeAt := strings.Index(row, slBranch), strings.Index(row, "automático")
	if modeAt < 0 || branchAt > modeAt {
		t.Errorf("config order is branch then mode; row=%q", row)
	}
	if !strings.Contains(row, " ¦ ") {
		t.Errorf("configured separator \" ¦ \" missing; row=%q", row)
	}
	if !strings.HasSuffix(row, "alto") {
		t.Errorf("effort is configured on the right side, so the row must end with it; row=%q", row)
	}
	if strings.Contains(row, slNativeCwd()) {
		t.Errorf("cwd is not in the config, so it must not render; row=%q", row)
	}
}

// 3. Até 3 linhas, cada uma com seus campos.
func TestStatusLineAcceptance_ConfigRendersThreeLines(t *testing.T) {
	env := newSLEnv(t)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{
		{left: []string{"mode"}},
		{left: []string{"branch"}},
		{left: []string{"cwd"}},
	}})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	frame := plainFrame(p.runFor(300 * time.Millisecond))
	lines := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("frame too short: %q", frame)
	}
	tail := lines[len(lines)-3:]
	checks := []struct {
		row     int
		want    string
		notWant []string
	}{
		{0, "automático", []string{slBranch, slNativeCwd()}},
		{1, slBranch, []string{"automático", slNativeCwd()}},
		{2, slNativeCwd(), []string{"automático", slBranch}},
	}
	for _, c := range checks {
		if !strings.Contains(tail[c.row], c.want) {
			t.Errorf("statusline line %d must hold %q; got %q (tail=%q)", c.row+1, c.want, tail[c.row], tail)
		}
		for _, bad := range c.notWant {
			if strings.Contains(tail[c.row], bad) {
				t.Errorf("statusline line %d must not hold %q; got %q", c.row+1, bad, tail[c.row])
			}
		}
	}
}

// 4. Comando da config do usuário roda e a saída aparece.
func TestStatusLineAcceptance_UserCommandOutputRenders(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	command := writeScript(t, env, "user.sh", `printf 'SL-USER-OK\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if frame, ok := p.waitFor(slSetupWait, containsPlain("SL-USER-OK")); !ok {
		t.Fatalf("user-config command output never reached the statusline:\n%s", plainFrame(frame))
	}
}

// 5. JSON no stdin com o schema da spec e sem credencial.
func TestStatusLineAcceptance_StdinJSONHasSchemaAndNoCredentials(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	t.Setenv("QG_API_KEY", slQGKeySentinel)
	t.Setenv("ACCEPT_GATEWAY_KEY", "gateway-SENTINEL-55c2")
	captured := filepath.Join(env.root, "stdin.json")
	command := writeScript(t, env, "stdin.sh", `cat > '`+captured+`'; printf 'SL-STDIN-OK\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if frame, ok := p.waitFor(slSetupWait, containsPlain("SL-STDIN-OK")); !ok {
		t.Fatalf("statusline command never ran:\n%s", plainFrame(frame))
	}
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("command stdin not captured: %v", err)
	}
	for _, secret := range []string{slQGKeySentinel, slProfileSentinel, "gateway-SENTINEL-55c2"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("stdin leaks a credential value %q: %s", secret, raw)
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stdin is not JSON: %v\n%s", err, raw)
	}
	var walk func(prefix string, value any)
	walk = func(prefix string, value any) {
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		for key, inner := range object {
			lower := strings.ToLower(strings.ReplaceAll(key, "_", ""))
			for _, banned := range []string{"apikey", "token", "secret", "password", "credential", "authorization"} {
				if strings.Contains(lower, banned) {
					t.Errorf("stdin carries a credential-shaped key %s%s", prefix, key)
				}
			}
			walk(prefix+key+".", inner)
		}
	}
	walk("", doc)
	for _, key := range []string{"session_id", "cwd", "model", "provider", "effort", "context", "cache", "cost_brl", "branch", "version", "width"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("stdin JSON lacks %q: %s", key, raw)
		}
	}
	// Unknown values are null, never missing: a null object passes (no call
	// measured the context yet; the cache rate may be unwired), but an object
	// always carries its full shape.
	nested := map[string][]string{"context": {"used", "window", "pct"}, "cache": {"hit_pct"}}
	for parent, keys := range nested {
		value, present := doc[parent]
		if !present || value == nil {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			t.Errorf("stdin %s must be an object or null, got %T: %s", parent, value, raw)
			continue
		}
		for _, key := range keys {
			if _, ok := object[key]; !ok {
				t.Errorf("stdin JSON lacks %s.%s: %s", parent, key, raw)
			}
		}
	}
	wantValues := map[string]any{"cwd": env.workspace, "model": "accept-model", "provider": "accept-gw", "branch": slBranch, "version": "9.9.9-accept", "width": float64(100)}
	for key, want := range wantValues {
		if got, ok := doc[key]; ok && got != want {
			t.Errorf("stdin %s = %v, want the live session value %v", key, got, want)
		}
	}
}

// 5b. Valor desconhecido é null: nem chave ausente (fora do git) nem zero falso
// (nenhuma chamada mediu o cache ainda).
func TestStatusLineAcceptance_StdinUnknownsAreNull(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	if err := os.RemoveAll(filepath.Join(env.workspace, ".git")); err != nil {
		t.Fatal(err)
	}
	captured := filepath.Join(env.root, "stdin.json")
	command := writeScript(t, env, "stdin.sh", `cat > '`+captured+`'; printf 'SL-STDIN-OK\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if frame, ok := p.waitFor(slSetupWait, containsPlain("SL-STDIN-OK")); !ok {
		t.Fatalf("statusline command never ran:\n%s", plainFrame(frame))
	}
	raw, err := os.ReadFile(captured)
	if err != nil {
		t.Fatalf("command stdin not captured: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("stdin is not JSON: %v\n%s", err, raw)
	}
	if value, present := doc["branch"]; !present || value != nil {
		t.Errorf("outside git the branch is unknown: want \"branch\": null, got present=%v value=%v: %s", present, value, raw)
	}
	if cache, ok := doc["cache"].(map[string]any); ok && cache["hit_pct"] != nil {
		t.Errorf("no call measured the cache yet: want cache.hit_pct null, got %v: %s", cache["hit_pct"], raw)
	}
}

// 6. Ambiente do filho sem QG_API_KEY e sem variáveis de credencial.
func TestStatusLineAcceptance_ChildEnvHasNoCredentials(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	secrets := map[string]string{
		"QG_API_KEY":            slQGKeySentinel,
		"QUEBRAGALHO_API_KEY":   "quebragalho-SENTINEL-1",
		"ACCEPT_GATEWAY_KEY":    "gateway-SENTINEL-2",
		"ANTHROPIC_API_KEY":     "anthropic-SENTINEL-3",
		"OPENAI_API_KEY":        "openai-SENTINEL-4",
		"GEMINI_API_KEY":        "gemini-SENTINEL-5",
		"QG_WEBSEARCH_API_KEY":  "websearch-SENTINEL-6",
		"GH_TOKEN":              "gh-SENTINEL-7",
		"AWS_SECRET_ACCESS_KEY": "aws-SENTINEL-8",
	}
	for key, value := range secrets {
		t.Setenv(key, value)
	}
	dump := filepath.Join(env.root, "env.txt")
	command := writeScript(t, env, "env.sh", `env > '`+dump+`'; printf 'SL-ENV-OK\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if frame, ok := p.waitFor(slSetupWait, containsPlain("SL-ENV-OK")); !ok {
		t.Fatalf("statusline command never ran:\n%s", plainFrame(frame))
	}
	data, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("child env not captured: %v", err)
	}
	text := string(data)
	for key, value := range secrets {
		if strings.Contains(text, "\n"+key+"=") || strings.HasPrefix(text, key+"=") {
			t.Errorf("child env still carries %s", key)
		}
		if strings.Contains(text, value) {
			t.Errorf("child env leaks the value of %s", key)
		}
	}
	if !strings.Contains(text, "PATH=") {
		t.Errorf("child env lost PATH, so no real script can run:\n%s", text)
	}
}

// 7. Comando do projeto é ignorado com aviso; campos do projeto valem.
func TestStatusLineAcceptance_ProjectCommandIgnoredWithWarning(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	marker := filepath.Join(env.root, "PROJECT_COMMAND_RAN")
	script := filepath.Join(env.workspace, ".qg", "rodape.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\nprintf 'SL-PROJECT-PWNED\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, env.projectConfig, map[string]any{slConfigKey: slConfigJSON(slLayout{
		lines:     []slLine{{left: []string{"branch", "mode", "command"}}},
		command:   "sh '" + script + "'",
		refreshMS: 1000,
	})})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	frame := p.runFor(3 * time.Second)
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a command from .qg/config.json ran; only the user config may define one")
	}
	if strings.Contains(plainFrame(frame), "SL-PROJECT-PWNED") {
		t.Errorf("project command output reached the statusline")
	}
	if !strings.Contains(strings.ToLower(plainFrame(frame)), "statusline") {
		t.Errorf("ignoring the project command must warn the user (mentioning the statusline):\n%s", plainFrame(frame))
	}
	row := plainFrame(lastLine(frame))
	if branchAt, modeAt := strings.Index(row, slBranch), strings.Index(row, "automático"); branchAt < 0 || modeAt < 0 || branchAt > modeAt {
		t.Errorf("the project's field list is harmless and must apply (branch before mode); row=%q", row)
	}
}

// 8. Timeout mata o comando e mantém o último valor bom.
func TestStatusLineAcceptance_TimeoutKillsAndKeepsLastGood(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	counter := filepath.Join(env.root, "count")
	pidFile := filepath.Join(env.root, "hang.pid")
	command := writeScript(t, env, "flaky.sh", `n=$(cat '`+counter+`' 2>/dev/null || echo 0); n=$((n+1)); echo $n > '`+counter+`'
if [ "$n" -eq 1 ]; then printf 'SL-GOOD-1\n'; exit 0; fi
echo $$ > '`+pidFile+`'
exec sleep 30`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, timeoutMS: 300, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if frame, ok := p.waitFor(4*time.Second, containsPlain("SL-GOOD-1")); !ok {
		t.Fatalf("first good run never rendered:\n%s", plainFrame(frame))
	}
	if !waitForFile(p, pidFile, 4*time.Second) {
		t.Fatal("the refresh never re-ran the command")
	}
	pid := readPID(t, pidFile)
	frame := p.runFor(1500 * time.Millisecond)
	if processAlive(pid) {
		t.Errorf("timeoutMs=300 but the command (pid %d) is still alive 1.5s later", pid)
	}
	if !strings.Contains(plainFrame(frame), "SL-GOOD-1") {
		t.Errorf("after a timeout the statusline must keep the last good value:\n%s", plainFrame(frame))
	}
}

// 8b. O kill alcança os filhos do script (senão o pipe herdado prende a execução).
// 9. Timeout mata a árvore inteira do comando, netos do mesmo grupo inclusive.
func TestStatusLineAcceptance_TimeoutKillsProcessTree(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	childPID := filepath.Join(env.root, "child.pid")
	command := writeScript(t, env, "tree.sh", `sleep 30 &
echo $! > '`+childPID+`'
wait`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, timeoutMS: 300, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if !waitForFile(p, childPID, 4*time.Second) {
		t.Fatal("statusline command never ran")
	}
	pid := readPID(t, childPID)
	p.runFor(1500 * time.Millisecond)
	if processAlive(pid) {
		t.Errorf("timeout left the script's child (pid %d) running", pid)
	}
}

// 10. View e Update nunca bloqueiam enquanto o comando roda.
func TestStatusLineAcceptance_ViewNeverBlocksWhileCommandRuns(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	started := filepath.Join(env.root, "started")
	command := writeScript(t, env, "slow.sh", `touch '`+started+`'; sleep 3; printf 'SL-SLOW-DONE\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, timeoutMS: 5000, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if !waitForFile(p, started, 4*time.Second) {
		t.Fatal("statusline command never started")
	}
	p.slowest, p.slowestUpdate = 0, 0
	for i := 0; i < 5; i++ {
		p.deliver(tea.WindowSizeMsg{Width: 90 + i, Height: 30})
	}
	p.runFor(time.Second)
	if p.slowest > 500*time.Millisecond {
		t.Errorf("View took %s while the command was running; it must never wait on it", p.slowest)
	}
	if p.slowestUpdate > 500*time.Millisecond {
		t.Errorf("Update took %s while the command was running; the run belongs in a tea.Cmd", p.slowestUpdate)
	}
}

// 11. No máximo uma execução em voo, mesmo com refresh e mudanças de estado.
func TestStatusLineAcceptance_AtMostOneRunInFlight(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	lock := filepath.Join(env.root, "lock")
	overlap := filepath.Join(env.root, "overlap.log")
	runs := filepath.Join(env.root, "runs.log")
	command := writeScript(t, env, "serial.sh", `if ! mkdir '`+lock+`' 2>/dev/null; then echo overlap >> '`+overlap+`'; fi
echo run >> '`+runs+`'
sleep 1.5
rmdir '`+lock+`' 2>/dev/null
printf 'SL-SERIAL\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, timeoutMS: 5000, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	deadline := time.Now().Add(4500 * time.Millisecond)
	for width := 80; time.Now().Before(deadline); width++ {
		p.deliver(tea.WindowSizeMsg{Width: 80 + width%5, Height: 30})
		p.runFor(150 * time.Millisecond)
	}
	if n := len(fileLines(runs)); n < 2 {
		t.Fatalf("command ran %d time(s) in 4.5s with refreshMs=1000; expected repeated runs", n)
	}
	if lines := fileLines(overlap); len(lines) > 0 {
		t.Errorf("%d run(s) started while another was still in flight", len(lines))
	}
}

// 12. Refresh mínimo de 1s, e o View não dispara execuções.
func TestStatusLineAcceptance_RefreshFloorIsOneSecond(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	runs := filepath.Join(env.root, "runs.log")
	command := writeScript(t, env, "tick.sh", `echo run >> '`+runs+`'; printf 'SL-TICK\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, refreshMS: 100})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	p.runFor(2600 * time.Millisecond)
	n := len(fileLines(runs))
	if n == 0 {
		t.Fatal("statusline command never ran")
	}
	if n > 4 {
		t.Errorf("refreshMs=100 must clamp to 1s: %d runs in 2.6s", n)
	}
}

// 13. Timeout padrão de 2s: 1.2s passa, algo travado morre até ~3s.
func TestStatusLineAcceptance_DefaultTimeoutIsTwoSeconds(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	counter := filepath.Join(env.root, "count")
	pidFile := filepath.Join(env.root, "hang.pid")
	command := writeScript(t, env, "default-timeout.sh", `n=$(cat '`+counter+`' 2>/dev/null || echo 0); n=$((n+1)); echo $n > '`+counter+`'
if [ "$n" -eq 1 ]; then sleep 1.2; printf 'SL-UNDER-2S\n'; exit 0; fi
echo $$ > '`+pidFile+`'
exec sleep 30`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if frame, ok := p.waitFor(5*time.Second, containsPlain("SL-UNDER-2S")); !ok {
		t.Fatalf("a 1.2s command must finish under the default 2s timeout:\n%s", plainFrame(frame))
	}
	if !waitForFile(p, pidFile, 4*time.Second) {
		t.Fatal("the refresh never re-ran the command")
	}
	pid := readPID(t, pidFile)
	p.runFor(3200 * time.Millisecond)
	if processAlive(pid) {
		t.Errorf("hung command (pid %d) still alive 3.2s later; default timeout is 2s", pid)
	}
}

// 14. Escapes não-SGR removidos, SGR mantido, até 3 linhas. The two
// "\033\033..." pieces are split sequences: stripping OSC then CSI (or the
// reverse) in fixed passes rejoins one of them into a live escape.
func TestStatusLineAcceptance_OutputSanitizedAndCapped(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	command := writeScript(t, env, "escapes.sh", `printf 'SL-SAN-A\033]8;;https://evil.example\007link\033]8;;\007\033[2K\033[31mRED\033[0m\033]0;titulo\007\033]52;c;cHduZWQ=\007\033[?25l\302\2332J\033\033]0;x\007[2J\033\033[2K]0;rotulo\007SL-SAN-END\n'
printf 'SL-L2\n'
printf 'SL-L3\n'
printf 'SL-L4\n'
printf 'SL-L5\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 120, 30)
	frame, ok := p.waitFor(4*time.Second, containsPlain("SL-SAN-A"))
	if !ok {
		t.Fatalf("statusline command output never rendered:\n%s", plainFrame(frame))
	}
	rows := frameLinesContaining(frame, "SL-SAN-A")
	row := strings.Join(rows, "\n")
	sgr := regexp.MustCompile(`^\x1b\[[0-9;:]*m`)
	t.Run("non-SGR escapes removed", func(t *testing.T) {
		for i := 0; i < len(row); i++ {
			if row[i] == 0x1b && !sgr.MatchString(row[i:]) {
				t.Fatalf("non-SGR escape survived at byte %d: %q", i, row)
			}
		}
		for _, bad := range []string{"\a", "\u009b", "evil.example", "]52;", "titulo"} {
			if strings.Contains(row, bad) {
				t.Errorf("row keeps %q: %q", bad, row)
			}
		}
	})
	t.Run("SGR colour kept", func(t *testing.T) {
		if !regexp.MustCompile(`\x1b\[(?:[0-9;]*;)?(?:31|91|38;5;(?:1|9)|38;2;[0-9]+;[0-9]+;[0-9]+)(?:;[0-9;]*)?mRED`).MatchString(row) {
			t.Errorf("SGR colours are allowed; red was dropped: %q", row)
		}
	})
	t.Run("at most three lines", func(t *testing.T) {
		plain := plainFrame(frame)
		for _, want := range []string{"SL-L2", "SL-L3"} {
			if !strings.Contains(plain, want) {
				t.Errorf("line %s of the command output is missing", want)
			}
		}
		for _, extra := range []string{"SL-L4", "SL-L5"} {
			if strings.Contains(plain, extra) {
				t.Errorf("stdout is capped at 3 lines; %s rendered", extra)
			}
		}
	})
}

// 15. Largura medida sem ANSI: corta pela largura visível, nem antes nem depois.
func TestStatusLineAcceptance_WidthMeasuredWithoutANSI(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	var body strings.Builder
	body.WriteString("SL-W")
	for i := 0; i < 30; i++ {
		body.WriteString(`\033[3` + strconv.Itoa(1+i%6) + `mxxxxxxxxxx\033[0m`)
	}
	command := writeScript(t, env, "wide.sh", `printf '`+body.String()+`\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 80, 30)
	frame, ok := p.waitFor(4*time.Second, containsPlain("SL-W"))
	if !ok {
		t.Fatalf("statusline command output never rendered:\n%s", plainFrame(frame))
	}
	for _, line := range strings.Split(frame, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("frame line is %d cells wide at width 80: %q", w, plainFrame(line))
		}
	}
	rows := frameLinesContaining(frame, "SL-W")
	if visible := lipgloss.Width(rows[0]); visible < 60 {
		t.Errorf("the 304-cell line was cut to %d visible cells at width 80; width must ignore ANSI bytes", visible)
	}
}

// 16. /statusline existe no /help.
func TestStatusLineAcceptance_HelpListsStatusline(t *testing.T) {
	env := newSLEnv(t)
	p := startSLPump(t, newSLModel(t, env, nil), 120, 60)
	p.submit("/help")
	if frame, ok := p.waitFor(time.Second, containsPlain("/statusline")); !ok {
		t.Fatalf("/help must list /statusline:\n%s", plainFrame(frame))
	}
}

// 17. /statusline sem texto mostra a config atual e a ajuda, sem turno do modelo.
func TestStatusLineAcceptance_BareCommandShowsConfigAndHelp(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	command := writeScript(t, env, "meu-rodape.sh", `printf 'SL-MINE\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode", "command"}}}, command: command})
	provider := &slProvider{}
	p := startSLPump(t, newSLModel(t, env, provider), 140, 60)
	p.submit("/statusline")
	frame := plainFrame(p.runFor(time.Second))
	if !strings.Contains(frame, "meu-rodape.sh") {
		t.Errorf("/statusline must show the current config (its command):\n%s", frame)
	}
	if !strings.Contains(frame, "/statusline reset") {
		t.Errorf("/statusline must show the help, including /statusline reset:\n%s", frame)
	}
	if n := provider.requestCount(); n != 0 {
		t.Errorf("bare /statusline must not start a model turn; provider saw %d request(s)", n)
	}
}

var slCatalog = []string{"mode", "effort", "voice", "background", "goal", "context", "branch", "update", "cwd", "cache", "model", "speed", "cost", "time"}

// 18. /statusline <pedido> dispara um turno com a instrução dedicada.
func TestStatusLineAcceptance_RequestStartsDedicatedTurn(t *testing.T) {
	env := newSLEnv(t)
	t.Setenv("QG_API_KEY", slQGKeySentinel)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode", "branch"}}}, separator: " ⁂ "})
	provider := &slProvider{}
	p := startSLPump(t, newSLModel(t, env, provider), 117, 40)
	request := "mostre só o branch e a hora"
	p.submit("/statusline " + request)
	text := ""
	p.waitFor(5*time.Second, func(string) bool {
		for i := 0; i < provider.requestCount(); i++ {
			if candidate := provider.requestText(i); strings.Contains(candidate, request) {
				text = candidate
				return true
			}
		}
		return false
	})
	if text == "" {
		t.Fatalf("/statusline <pedido> must start a model turn carrying the request (provider saw %d request(s))", provider.requestCount())
	}
	// Control: an ordinary prompt in an identical session. Whatever the
	// standard system prompt already says does not count as the instruction.
	controlProvider := &slProvider{}
	control := startSLPump(t, newSLModel(t, env, controlProvider), 117, 40)
	control.submit("oi")
	if _, ok := control.waitFor(5*time.Second, func(string) bool { return controlProvider.requestCount() > 0 }); !ok {
		t.Fatal("control prompt never reached the provider")
	}
	baseline := controlProvider.requestText(0)
	adds := func(want string) bool {
		return strings.Count(strings.ToLower(text), strings.ToLower(want)) > strings.Count(strings.ToLower(baseline), strings.ToLower(want))
	}
	// The stdin example is JSON-encoded: on Windows the workspace backslashes
	// reach the instruction escaped (C:\\Users\\...), so compare against the
	// encoded form. Encoding is a no-op for Unix paths, where no byte needs
	// escaping, so what Linux asserts stays byte-identical.
	wantCwdJSON, _ := json.Marshal(env.workspace)
	must := map[string]string{
		"user request":         request,
		"current config":       " ⁂ ",
		"terminal width":       "117",
		"session JSON example": `"cost_brl"`,
		"real session cwd":     strings.Trim(string(wantCwdJSON), `"`),
		"real session branch":  slBranch,
		"scripts directory":    env.scriptsDir,
	}
	for _, id := range slCatalog {
		must["catalog id "+id] = id
	}
	for what, want := range must {
		if !adds(want) {
			t.Errorf("dedicated instruction does not add the %s (%q)", what, want)
		}
	}
	// Whole words, so "pct" inside "hit_pct" does not count for the context
	// shape: the instruction must spell the full stdin schema even when the
	// session example carries null objects.
	addsWord := func(word string) bool {
		pattern := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(word) + `\b`)
		return len(pattern.FindAllStringIndex(text, -1)) > len(pattern.FindAllStringIndex(baseline, -1))
	}
	for _, key := range []string{"session_id", "cwd", "model", "provider", "effort", "context", "used", "window", "pct", "cache", "hit_pct", "cost_brl", "branch", "version", "width"} {
		if !addsWord(key) {
			t.Errorf("dedicated instruction does not add the stdin schema key %q", key)
		}
	}
	osNames := map[string]string{"darwin": "macos", "linux": "linux", "windows": "windows"}
	if !adds(runtime.GOOS) && !adds(osNames[runtime.GOOS]) {
		t.Errorf("dedicated instruction does not add the operating system (%s)", runtime.GOOS)
	}
	if slStagingPath(env, text) == "" {
		t.Errorf("instruction must name the staging file under %s", env.scriptsDir)
	}
	for _, secret := range []string{slQGKeySentinel, slProfileSentinel} {
		if strings.Contains(text, secret) {
			t.Errorf("instruction leaks a credential value")
		}
	}
}

// slStagingPath finds the staging file the instruction tells the model to
// write: a .json path inside the user statusline directory. The separator in
// the instruction is the OS one (filepath.Join), so the pattern accepts both
// slashes: a literal "/" never matches on Windows.
func slStagingPath(env slEnv, instruction string) string {
	pattern := regexp.MustCompile(regexp.QuoteMeta(env.scriptsDir) + `[\\/][^\s"'` + "`" + `]+\.json`)
	return pattern.FindString(instruction)
}

// slProposer is a model that answers /statusline the way the instruction
// asks: proposal.json at the staging path it names, plus the script (if any)
// in that same staging dir.
func slProposer(t *testing.T, env slEnv, proposal slLayout, staged *string) *slProvider {
	var mu sync.Mutex
	return &slProvider{onRequest: func(request qgruntime.CompletionRequest) {
		mu.Lock()
		defer mu.Unlock()
		if *staged != "" {
			return
		}
		var all strings.Builder
		for _, message := range request.Messages {
			all.WriteString(message.Content)
			all.WriteString("\n")
		}
		path := slStagingPath(env, all.String())
		if path == "" {
			return
		}
		if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
			return
		}
		if proposal.script.name != "" {
			body := []byte("#!/bin/sh\n" + proposal.script.body + "\n")
			if os.WriteFile(filepath.Join(filepath.Dir(path), proposal.script.name), body, 0o600) != nil {
				return
			}
		}
		data, err := json.Marshal(slProposalJSON(proposal))
		if err == nil && os.WriteFile(path, data, 0o600) == nil {
			*staged = path
		}
	}}
}

func slAssertUserConfigPreserved(t *testing.T, data []byte) {
	t.Helper()
	var doc struct {
		ActiveProvider string `json:"activeProvider"`
		Providers      []struct {
			APIKeyEnv string `json:"apiKeyEnv"`
		} `json:"providers"`
		Preferences struct {
			Theme string `json:"theme"`
		} `json:"preferences"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("user config is no longer valid JSON: %v\n%s", err, data)
	}
	if doc.ActiveProvider != "accept-gw" || len(doc.Providers) != 1 || doc.Providers[0].APIKeyEnv != "ACCEPT_GATEWAY_KEY" || doc.Preferences.Theme != "dark" {
		t.Errorf("writing the statusline clobbered the rest of the user config:\n%s", data)
	}
}

// 19. Prévia com dados reais, nada gravado antes do Enter; Enter aplica.
func TestStatusLineAcceptance_ProposalPreviewThenEnterApplies(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	before, _ := os.ReadFile(env.userConfig)
	seen := filepath.Join(env.root, "preview-stdin.json")
	script := slScript{name: "proposta.sh", body: `cat > '` + seen + `'; printf 'SL-PREVIEW-OK\n'`}
	var staged string
	provider := slProposer(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, script: script, refreshMS: 1000, timeoutMS: 8000}, &staged)
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline quero uma linha minha")
	frame, ok := p.waitFor(slSetupWait, containsPlain("SL-PREVIEW-OK"))
	if !ok {
		t.Fatalf("after the turn QG must render a preview of the staged proposal (staging=%q):\n%s", staged, plainFrame(frame))
	}
	if !strings.Contains(plainFrame(frame), "Enter") || !strings.Contains(plainFrame(frame), "Esc") {
		t.Errorf("preview must ask for confirmation (Enter aplica · Esc cancela):\n%s", plainFrame(frame))
	}
	if stdin, err := os.ReadFile(seen); err != nil || !strings.Contains(string(stdin), env.workspace) {
		t.Errorf("preview must run the proposal with the real session JSON; got %q (%v)", stdin, err)
	}
	if now, _ := os.ReadFile(env.userConfig); string(now) != string(before) {
		t.Fatalf("live user config changed before confirmation:\n%s", now)
	}
	p.key(tea.KeyEnter)
	p.waitFor(slSetupWait, func(string) bool {
		data, _ := os.ReadFile(env.userConfig)
		_, ok := slStatusLineFrom(t, data)
		return ok
	})
	data, _ := os.ReadFile(env.userConfig)
	block, ok := slStatusLineFrom(t, data)
	if !ok {
		t.Fatalf("Enter must write the proposal to the user config:\n%s", data)
	}
	if !strings.Contains(string(block), "proposta.sh") {
		t.Errorf("applied statusline is not the staged proposal: %s", block)
	}
	slAssertUserConfigPreserved(t, data)
	if info, err := os.Stat(env.userConfig); err == nil && info.Mode().Perm()&0o077 != 0 {
		t.Errorf("user config mode became %v; it may hold keys and must stay owner-only", info.Mode().Perm())
	}
}

// 20. Esc cancela: staging descartado, config intacta, prévia fora do rodapé.
func TestStatusLineAcceptance_ProposalEscDiscardsStaging(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	before, _ := os.ReadFile(env.userConfig)
	script := slScript{name: "proposta.sh", body: `printf 'SL-PREVIEW-OK\n'`}
	var staged string
	provider := slProposer(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, script: script, refreshMS: 1000}, &staged)
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline quero uma linha minha")
	if frame, ok := p.waitFor(slSetupWait, containsPlain("SL-PREVIEW-OK")); !ok {
		t.Fatalf("no preview to cancel (staging=%q):\n%s", staged, plainFrame(frame))
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("precondition: staging file %q should exist while the preview is open: %v", staged, err)
	}
	p.key(tea.KeyEscape)
	frame, discarded := p.waitFor(slSetupWait, func(string) bool {
		_, err := os.Stat(staged)
		return os.IsNotExist(err)
	})
	if !discarded {
		t.Errorf("Esc must discard the staging file %s", staged)
	}
	if now, _ := os.ReadFile(env.userConfig); string(now) != string(before) {
		t.Errorf("Esc must leave the user config untouched:\n%s", now)
	}
	if footer := slFooter(plainFrame(frame)); strings.Contains(footer, "SL-PREVIEW-OK") {
		t.Errorf("cancelled proposal still renders in the statusline:\n%s", footer)
	}
}

// 21. Id fora do catálogo: a prévia avisa antes de perguntar, e o id nunca
// chega à config (o resto da proposta pode ser aplicado).
func TestStatusLineAcceptance_UnknownFieldNeverReachesConfig(t *testing.T) {
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	var staged string
	provider := slProposer(t, env, slLayout{lines: []slLine{{left: []string{"mode", "nao_existe"}}}}, &staged)
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline algo impossível")
	frame, ok := p.waitFor(slSetupWait, func(frame string) bool {
		plain := plainFrame(frame)
		return strings.Contains(plain, "Enter aplica") && strings.Contains(plain, "nao_existe")
	})
	if !ok {
		t.Fatalf("the preview must name the unknown field id before asking (staging=%q):\n%s", staged, plainFrame(frame))
	}
	p.key(tea.KeyEnter)
	p.waitFor(slSetupWait, func(string) bool {
		data, _ := os.ReadFile(env.userConfig)
		_, ok := slStatusLineFrom(t, data)
		return ok
	})
	now, _ := os.ReadFile(env.userConfig)
	if strings.Contains(string(now), "nao_existe") {
		t.Errorf("an unknown field id reached the user config:\n%s", now)
	}
	slAssertUserConfigPreserved(t, now)
}

// 22. Script da proposta que estoura o timeout é recusado.
func TestStatusLineAcceptance_SlowScriptProposalRejected(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	before, _ := os.ReadFile(env.userConfig)
	script := slScript{name: "lento.sh", body: `sleep 2; printf 'SL-TOO-SLOW\n'`}
	var staged string
	provider := slProposer(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, script: script, timeoutMS: 300}, &staged)
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline algo lento")
	if _, ok := p.waitFor(slSetupWait, func(string) bool { return provider.requestCount() > 0 }); !ok {
		t.Fatal("/statusline <pedido> must start a model turn")
	}
	// Past the script's own 2s: a dry run that ignored the 300ms bound would
	// have finished by now and be waiting for Enter, so Enter would apply it.
	// The 2s is the script's own sleep, not a wait for something to happen.
	frame := p.runFor(3 * time.Second)
	if strings.Contains(plainFrame(frame), "SL-TOO-SLOW") {
		t.Errorf("the slow script's output reached the preview:\n%s", plainFrame(frame))
	}
	p.key(tea.KeyEnter)
	// A refused proposal writes nothing: this waits for the absence of a
	// write, so it keeps a short window instead of a deadline.
	p.runFor(time.Second)
	if now, _ := os.ReadFile(env.userConfig); string(now) != string(before) {
		t.Errorf("a proposal whose script exceeds its timeout reached the user config:\n%s", now)
	}
}

// 23. /statusline reset volta ao padrão, ao vivo, sem perder o resto da config.
func TestStatusLineAcceptance_ResetRestoresDefault(t *testing.T) {
	requireSh(t)
	useTrueColor(t)
	env := newSLEnv(t)
	command := writeScript(t, env, "custom.sh", `printf 'SL-CUSTOM-LIVE\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, refreshMS: 1000})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if frame, ok := p.waitFor(slSetupWait, containsPlain("SL-CUSTOM-LIVE")); !ok {
		t.Fatalf("precondition: the current command never rendered:\n%s", plainFrame(frame))
	}
	p.submit("/statusline reset")
	// The spec does not say whether reset asks first; a second Enter on an
	// empty composer is inert when it does not.
	p.key(tea.KeyEnter)
	frame, _ := p.waitFor(slSetupWait, func(frame string) bool {
		data, _ := os.ReadFile(env.userConfig)
		_, custom := slStatusLineFrom(t, data)
		return !custom && !strings.Contains(plainFrame(frame), "SL-CUSTOM-LIVE")
	})
	data, _ := os.ReadFile(env.userConfig)
	if block, ok := slStatusLineFrom(t, data); ok {
		t.Errorf("/statusline reset must drop the custom statusline from the user config; still %s", block)
	}
	slAssertUserConfigPreserved(t, data)
	if strings.Contains(plainFrame(frame), "SL-CUSTOM-LIVE") {
		t.Errorf("custom statusline still renders after reset")
	}
	for _, tc := range slGoldenDefault {
		if !tc.ask && tc.width == 100 {
			if got := lastLine(frame); got != tc.want {
				t.Errorf("after reset the statusline must be today's default\n got=%q\nwant=%q", got, tc.want)
			}
		}
	}
}

// 24. Fim de chamada re-executa o comando sem esperar o refresh.
func TestStatusLineAcceptance_TurnEndRefreshesCommand(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	counter := filepath.Join(env.root, "count")
	command := writeScript(t, env, "turns.sh", `n=$(cat '`+counter+`' 2>/dev/null || echo 0); n=$((n+1)); echo $n > '`+counter+`'; printf 'SL-RUN-%s\n' "$n"`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, refreshMS: 600000})
	provider := &slProvider{}
	p := startSLPump(t, newSLModel(t, env, provider), 100, 30)
	if frame, ok := p.waitFor(4*time.Second, containsPlain("SL-RUN-1")); !ok {
		t.Fatalf("first run never rendered:\n%s", plainFrame(frame))
	}
	p.submit("oi")
	if _, ok := p.waitFor(5*time.Second, func(string) bool { return provider.requestCount() > 0 }); !ok {
		t.Fatal("prompt never reached the provider")
	}
	if frame, ok := p.waitFor(4*time.Second, containsPlain("SL-RUN-2")); !ok {
		t.Errorf("end of a model call must refresh the statusline command (refreshMs is 10min here):\n%s", plainFrame(frame))
	}
}

// 26. Um neto que sai do grupo de processos não prende o executor: o timeout
// mais a folga de encerramento liberam a próxima execução, mesmo com o neto
// ainda vivo segurando o stdout.
func TestStatusLineAcceptance_EscapedGrandchildDoesNotStallRunner(t *testing.T) {
	requireSh(t)
	cases := []struct{ name, redirect string }{
		{"grandchild keeps stdout and stderr", ""},
		{"grandchild keeps only stdout", "2>/dev/null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSLEnv(t)
			counter := filepath.Join(env.root, "count")
			grandchildPID := filepath.Join(env.root, "grandchild.pid")
			// The grandchild only counts as escaped once it reports its own
			// pid AFTER setpgrp: a $!-file written by the shell races the
			// 300ms group kill (the kill can land before perl runs setpgrp,
			// leaving a pid file for a dead child). Retrying every run while
			// the file is missing turns that race into a slow first escape
			// instead of a precondition failure. The pid file appears via an
			// atomic rename, so existence implies complete content (no
			// empty-file race between creat and write under load). The same
			// holds for the counter below.
			command := writeScript(t, env, "escape.sh", `n=$(cat '`+counter+`' 2>/dev/null || echo 0); n=$((n+1)); echo $n > '`+counter+`.tmp' && mv '`+counter+`.tmp' '`+counter+`'
if [ ! -f '`+grandchildPID+`' ]; then
  perl -e 'setpgrp(0,0); open my $fh, ">", $ARGV[0].".tmp"; print $fh "$$\n"; close $fh; rename $ARGV[0].".tmp", $ARGV[0]; sleep 30' '`+grandchildPID+`' `+tc.redirect+` &
  sleep 5
fi
printf 'SL-GC-RUN-%s\n' "$n"`)
			writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"command"}}}, command: command, timeoutMS: 300, refreshMS: 1000})
			p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
			if !waitForFile(p, grandchildPID, 4*time.Second) {
				t.Fatal("statusline command never ran")
			}
			base := slReadCounter(t, counter)
			pid := readPID(t, grandchildPID)
			frame, ok := p.waitFor(4*time.Second, func(frame string) bool {
				return slGCRunAfter(plainFrame(frame), base)
			})
			if !processAlive(pid) {
				t.Fatalf("precondition: the grandchild (pid %d) did not outlive the group kill, so nothing escaped", pid)
			}
			if !ok {
				t.Errorf("a grandchild outside the process group still blocks the runner 4s after a 300ms timeout; the next run never started:\n%s", plainFrame(frame))
			}
		})
	}
}

// slGCRunAfter reports whether the frame shows a statusline run newer than
// base: SL-GC-RUN-<n> with n > base.
func slGCRunAfter(plain string, base int) bool {
	for _, m := range slGCRunPattern.FindAllStringSubmatch(plain, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > base {
			return true
		}
	}
	return false
}

var slGCRunPattern = regexp.MustCompile(`SL-GC-RUN-(\d+)`)

func slReadCounter(t *testing.T, path string) int {
	t.Helper()
	lines := fileLines(path)
	if len(lines) == 0 {
		t.Fatalf("counter file %s is empty", path)
	}
	n, err := strconv.Atoi(strings.TrimSpace(lines[len(lines)-1]))
	if err != nil {
		t.Fatalf("counter file %s: %v", path, err)
	}
	return n
}

// 27. Comando aplicado pelo /statusline roda na hora e segue o refreshMs, sem
// esperar um turno nem reiniciar o qg.
func TestStatusLineAcceptance_AppliedCommandRunsOnItsCadence(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	runs := filepath.Join(env.root, "runs.log")
	script := slScript{name: "cadencia.sh", body: `echo run >> '` + runs + `'; printf 'SL-CADENCE\n'`}
	var staged string
	provider := slProposer(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, script: script, refreshMS: 1000}, &staged)
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline quero uma linha que se atualiza")
	// The preview only exists once the turn lands and the staged script has
	// been dry-run: a condition, not a deadline.
	if frame, ok := p.waitFor(slSetupWait, containsPlain("Enter aplica")); !ok {
		t.Fatalf("no preview to apply (staging=%q):\n%s", staged, plainFrame(frame))
	}
	turns := provider.requestCount()
	afterPreview := len(fileLines(runs))
	p.key(tea.KeyEnter)
	// The applied command must show up by itself; the cadence check below
	// measures how often it runs.
	frame, ok := p.waitFor(slSetupWait, func(frame string) bool {
		return strings.Contains(slFooter(plainFrame(frame)), "SL-CADENCE")
	})
	if !ok {
		t.Errorf("the applied command must render right away, not at the next turn:\n%s", slFooter(plainFrame(frame)))
	}
	// Three more runs at refreshMs=1000 is the cadence actually starting
	// without a restart: wait for them (a loaded machine delays each run, it
	// does not drop it) and only then stop the clock.
	window := time.Now()
	got, _ := p.waitFor(slSetupWait, func(string) bool {
		return len(fileLines(runs))-afterPreview >= 3
	})
	elapsed := time.Since(window)
	if n := len(fileLines(runs)) - afterPreview; n < 3 {
		t.Fatalf("refreshMs=1000 after apply: %d run(s) in %s with no turn; the cadence must start without a restart:\n%s", n, elapsed.Round(time.Millisecond), slFooter(plainFrame(got)))
	}
	if provider.requestCount() != turns {
		t.Fatalf("precondition: no model turn may run in the idle window")
	}
}

// 28. Filho em background que herda o stdout: a saída vale e o filho morre com
// o grupo. Senão cada refresh deixa um órfão.
func TestStatusLineAcceptance_BackgroundChildKeepsOutputAndDies(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	pids := filepath.Join(env.root, "bg.pids")
	command := writeScript(t, env, "fundo.sh", `sleep 30 & echo $! >> '`+pids+`'; printf 'SL-BG-OK\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, timeoutMS: 2000, refreshMS: 600000})
	t.Cleanup(func() {
		for _, line := range fileLines(pids) {
			if pid, err := strconv.Atoi(line); err == nil {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Kill()
				}
			}
		}
	})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	frame, ok := p.waitFor(3*time.Second, func(frame string) bool {
		return strings.Contains(slFooter(plainFrame(frame)), "SL-BG-OK")
	})
	if !ok {
		t.Errorf("a script that exits 0 with output must render it, even with a background child still holding stdout:\n%s", slFooter(plainFrame(frame)))
	}
	p.runFor(1500 * time.Millisecond)
	started := fileLines(pids)
	if len(started) == 0 {
		t.Fatal("precondition: the script never started its background child")
	}
	for _, line := range started {
		if pid, err := strconv.Atoi(line); err == nil && processAlive(pid) {
			t.Errorf("background child %d outlived its run; every refresh would leak one", pid)
		}
	}
}

// 29. Proposta sem script mexe só nos campos: o comando e os tempos atuais
// ficam.
func TestStatusLineAcceptance_FieldsOnlyProposalKeepsCommand(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	command := writeScript(t, env, "atual.sh", `printf 'SL-CURRENT\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, timeoutMS: 1500, refreshMS: 3000})
	var staged string
	provider := slProposer(t, env, slLayout{lines: []slLine{{left: []string{"mode", "branch"}}}}, &staged)
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline põe o branch do lado do modo")
	if frame, ok := p.waitFor(slSetupWait, containsPlain("Enter aplica")); !ok {
		t.Fatalf("no preview to apply (staging=%q):\n%s", staged, plainFrame(frame))
	}
	p.key(tea.KeyEnter)
	p.waitFor(slSetupWait, func(string) bool {
		data, _ := os.ReadFile(env.userConfig)
		_, ok := slStatusLineFrom(t, data)
		return ok
	})
	data, _ := os.ReadFile(env.userConfig)
	block, ok := slStatusLineFrom(t, data)
	if !ok {
		t.Fatalf("Enter must write the proposal to the user config:\n%s", data)
	}
	var got struct {
		Lines []struct {
			Left []string `json:"left"`
		} `json:"lines"`
		Command *struct {
			Command string `json:"command"`
		} `json:"command"`
		TimeoutMs int `json:"timeoutMs"`
		RefreshMs int `json:"refreshMs"`
	}
	if err := json.Unmarshal(block, &got); err != nil {
		t.Fatalf("statusline block is not an object: %v\n%s", err, block)
	}
	if len(got.Lines) != 1 || strings.Join(got.Lines[0].Left, ",") != "mode,branch" {
		t.Errorf("the proposed layout must apply; got %s", block)
	}
	if got.Command == nil || got.Command.Command != command {
		t.Errorf("a proposal without a script must keep the current command %q; got %s", command, block)
	}
	if got.TimeoutMs != 1500 || got.RefreshMs != 3000 {
		t.Errorf("a proposal without a script must keep timeoutMs 1500 and refreshMs 3000; got %s", block)
	}
	slAssertUserConfigPreserved(t, data)
}

// 30. Separador vindo do projeto não quebra a tela: sem controles (C0 com ESC,
// C1, quebras de linha), até 8 células, e um aviso no startup.
func TestStatusLineAcceptance_ProjectSeparatorIsSanitized(t *testing.T) {
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	writeJSONFile(t, env.projectConfig, map[string]any{slConfigKey: map[string]any{
		"lines":     []map[string]any{{"left": []string{"mode", "effort"}}},
		"separator": strings.Repeat("\n", 40) + "\x1b]8;;https://evil.example\x07\u0085XYZXYZXYZXYZ\x1b]8;;\x07",
	}})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	frame := p.runFor(300 * time.Millisecond)
	plain := plainFrame(frame)
	if lines := strings.Split(frame, "\n"); len(lines) > 30 {
		t.Errorf("the frame grew to %d lines in a 30-line terminal", len(lines))
	}
	if !strings.Contains(plain, "descreva uma tarefa") {
		t.Errorf("the composer must stay on screen:\n%s", plain)
	}
	sameRow := false
	for _, row := range strings.Split(slFooter(plain), "\n") {
		if strings.Contains(row, "automático") && strings.Contains(row, "alto") {
			sameRow = true
		}
	}
	if !sameRow {
		t.Errorf("mode and effort share one configured line, so they must share one row:\n%s", slFooter(plain))
	}
	if strings.Contains(frame, "\x1b]8;;https://evil") || strings.Contains(plain, "evil.example") {
		t.Errorf("a hyperlink smuggled in the project separator reached the frame")
	}
	if strings.Contains(plain, "XYZXYZXYZ") {
		t.Errorf("the separator renders wider than 8 cells:\n%s", slFooter(plain))
	}
	if !strings.Contains(strings.ToLower(plain), "separ") {
		t.Errorf("cleaning the separator must warn at startup (naming the separator):\n%s", plain)
	}
}

// 31. Script do staging que é symlink é recusado, e o alvo fica intocado: não
// roda, não ganha chmod, não vira o comando do usuário.
func TestStatusLineAcceptance_SymlinkedStagedScriptRefused(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	before, _ := os.ReadFile(env.userConfig)
	outside := filepath.Join(env.root, "fora", "alvo.sh")
	ran := filepath.Join(env.root, "OUTSIDE_RAN")
	body := []byte("#!/bin/sh\ntouch '" + ran + "'\nprintf 'SL-OUTSIDE\\n'\n")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	done := false
	provider := &slProvider{onRequest: func(request qgruntime.CompletionRequest) {
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		var all strings.Builder
		for _, message := range request.Messages {
			all.WriteString(message.Content)
			all.WriteString("\n")
		}
		path := slStagingPath(env, all.String())
		if path == "" || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
			return
		}
		// The target is written during the turn, like any file the model
		// writes in the workspace, so it is as fresh as a staged script.
		if os.WriteFile(outside, body, 0o644) != nil || os.Chmod(outside, 0o644) != nil {
			return
		}
		if os.Symlink(outside, filepath.Join(filepath.Dir(path), "link.sh")) != nil {
			return
		}
		data, err := json.Marshal(slProposalJSON(slLayout{lines: []slLine{{left: []string{"mode"}}}, script: slScript{name: "link.sh"}, refreshMS: 1000}))
		if err == nil && os.WriteFile(path, data, 0o600) == nil {
			done = true
		}
	}}
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline quero uma linha minha")
	if _, ok := p.waitFor(slSetupWait, func(string) bool { return provider.requestCount() > 0 }); !ok {
		t.Fatal("/statusline <pedido> must start a model turn")
	}
	// The refusal must land before Enter can apply anything: wait for the
	// turn to settle, then give the setup its window to report.
	p.waitFor(slSetupWait, func(string) bool {
		current, isModel := p.m.(model)
		return isModel && !current.pending
	})
	p.key(tea.KeyEnter)
	// Nothing may happen after Enter: a short window, not a deadline.
	frame := p.runFor(time.Second)
	mu.Lock()
	staged := done
	mu.Unlock()
	if !staged {
		t.Fatal("precondition: the model never staged the symlinked proposal")
	}
	if _, err := os.Stat(ran); err == nil || strings.Contains(plainFrame(frame), "SL-OUTSIDE") {
		t.Errorf("a staged symlink made QG run a file outside the staging dir")
	}
	if info, err := os.Stat(outside); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("the symlink target must keep mode 0644; got %v (%v)", info.Mode().Perm(), err)
	}
	if now, _ := os.ReadFile(outside); string(now) != string(body) {
		t.Errorf("the symlink target changed:\n%s", now)
	}
	if now, _ := os.ReadFile(env.userConfig); string(now) != string(before) {
		t.Errorf("a symlinked staged script reached the user config:\n%s", now)
	}
	if _, err := os.Lstat(filepath.Join(env.scriptsDir, "link.sh")); !os.IsNotExist(err) {
		t.Errorf("the staged symlink was installed in the scripts dir (lstat err=%v)", err)
	}
}

// 32. Id desconhecido na config avisa já no startup, inclusive "command", que
// não é campo: a saída do comando sai uma vez só, depois dos campos.
func TestStatusLineAcceptance_UnknownConfigIdWarnsAtStartup(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	command := writeScript(t, env, "uma-vez.sh", `printf 'SL-ONCE\n'`)
	doc := slUserConfigBase()
	doc[slConfigKey] = map[string]any{
		"lines":     []map[string]any{{"left": []string{"mode", "nao_existe", "command"}}},
		"command":   map[string]any{"command": command},
		"refreshMs": 1000,
	}
	writeJSONFile(t, env.userConfig, doc)
	p := startSLPump(t, newSLModel(t, env, nil), 120, 40)
	frame, ok := p.waitFor(4*time.Second, func(frame string) bool {
		return strings.Contains(slFooter(plainFrame(frame)), "SL-ONCE")
	})
	if !ok {
		t.Fatalf("statusline command never rendered:\n%s", plainFrame(frame))
	}
	plain := plainFrame(frame)
	footer := slFooter(plain)
	if n := strings.Count(footer, "SL-ONCE"); n != 1 {
		t.Errorf("the command output must render once, after the fields; it rendered %d times:\n%s", n, footer)
	}
	modeRow, outputRow := -1, -1
	for i, row := range strings.Split(footer, "\n") {
		if modeRow < 0 && strings.Contains(row, "automático") {
			modeRow = i
		}
		if outputRow < 0 && strings.Contains(row, "SL-ONCE") {
			outputRow = i
		}
	}
	if modeRow < 0 || outputRow <= modeRow {
		t.Errorf("the command output goes on its own row after the field line (mode row %d, output row %d):\n%s", modeRow, outputRow, footer)
	}
	above := strings.TrimSuffix(plain, footer)
	for _, id := range []string{"nao_existe", "command"} {
		if !strings.Contains(above, id) {
			t.Errorf("unknown field id %q in the config must be named in a startup warning:\n%s", id, above)
		}
	}
}

// 33. "command": null na proposta remove o comando: a instrução ensina isso, o
// card diz "comando: removido", a prévia não usa a saída antiga e, aplicado, o
// comando sai da config, do rodapé e para de rodar. Chave ausente mantém (29).
func TestStatusLineAcceptance_NullCommandProposalRemovesCommand(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	runs := filepath.Join(env.root, "old-runs")
	command := writeScript(t, env, "antigo.sh", `echo run >> '`+runs+`'; printf 'SL-OLD\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 1000})
	var staged string
	provider := slProposer(t, env, slLayout{lines: []slLine{{left: []string{"mode", "branch"}}}, dropCommand: true}, &staged)
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	if frame, ok := p.waitFor(slSetupWait, func(frame string) bool {
		return strings.Contains(slFooter(plainFrame(frame)), "SL-OLD")
	}); !ok {
		t.Fatalf("precondition: the current command output never rendered:\n%s", plainFrame(frame))
	}
	p.submit("/statusline tira o comando e põe o branch")
	frame, ok := p.waitFor(slSetupWait, containsPlain("Enter aplica"))
	if !ok {
		t.Fatalf("no preview to apply (staging=%q):\n%s", staged, plainFrame(frame))
	}
	if instruction := provider.requestText(0); !regexp.MustCompile(`"command"\s*:\s*null`).MatchString(instruction) {
		t.Errorf("the setup instruction must teach that \"command\": null removes the command; its proposal.json lines:\n%s", strings.Join(frameLinesContaining(instruction, "proposal.json"), "\n"))
	}
	plain := plainFrame(frame)
	above := strings.TrimSuffix(plain, slFooter(plain))
	if !strings.Contains(above, "comando: removido") {
		t.Errorf("a proposal with \"command\": null must say \"comando: removido\" in its preview:\n%s", above)
	}
	if strings.Contains(above, "SL-OLD") {
		t.Errorf("the preview of a proposal that removes the command must not show the old output:\n%s", above)
	}
	p.key(tea.KeyEnter)
	p.waitFor(slSetupWait, func(string) bool {
		data, _ := os.ReadFile(env.userConfig)
		block, ok := slStatusLineFrom(t, data)
		return ok && strings.Contains(string(block), "branch")
	})
	data, _ := os.ReadFile(env.userConfig)
	block, ok := slStatusLineFrom(t, data)
	if !ok {
		t.Fatalf("Enter must write the proposal to the user config:\n%s", data)
	}
	var got struct {
		Lines []struct {
			Left []string `json:"left"`
		} `json:"lines"`
		Command json.RawMessage `json:"command"`
	}
	if err := json.Unmarshal(block, &got); err != nil {
		t.Fatalf("statusline block is not an object: %v\n%s", err, block)
	}
	if len(got.Lines) != 1 || strings.Join(got.Lines[0].Left, ",") != "mode,branch" {
		t.Errorf("the proposed layout must apply; got %s", block)
	}
	if command := strings.TrimSpace(string(got.Command)); command != "" && command != "null" {
		t.Errorf("\"command\": null must remove the command from the config; got %s", block)
	}
	slAssertUserConfigPreserved(t, data)
	// The removed command must stop: both waits below measure that nothing
	// happened, so they keep their short windows.
	p.runFor(500 * time.Millisecond)
	before := len(fileLines(runs))
	frame = p.runFor(2500 * time.Millisecond)
	if footer := slFooter(plainFrame(frame)); strings.Contains(footer, "SL-OLD") {
		t.Errorf("the removed command's output must leave the statusline:\n%s", footer)
	}
	if after := len(fileLines(runs)); after != before {
		t.Errorf("the removed command kept running on its old cadence: %d runs in 2.5s after Enter", after-before)
	}
}

// 34. Id "cache" reusa o segmento do cache da sessão: "cache —" antes da 1ª
// chamada, "cache 75%" depois de uma chamada com 3000 de 4000 tokens de
// entrada em cache. No stdin, cache.hit_pct é null antes e 75 depois, na
// escala 0–100 de context.pct.
func TestStatusLineAcceptance_CacheFieldAndStdinHitPct(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	captured := filepath.Join(env.root, "stdin.json")
	command := writeScript(t, env, "cache.sh", `cat > '`+captured+`.tmp' && mv '`+captured+`.tmp' '`+captured+`'; printf 'SL-CACHE-RUN\n'`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode", "cache"}}}, command: command, refreshMS: 1000})
	provider := &slProvider{usage: &qgruntime.Usage{InputTokens: 4000, CachedInputTokens: 3000, OutputTokens: 10}}
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	fieldRow := func(frame string) string {
		for _, row := range strings.Split(slFooter(plainFrame(frame)), "\n") {
			if strings.Contains(row, "automático") {
				return row
			}
		}
		return ""
	}
	// hitPct reads cache.hit_pct from the last stdin the command saw; a null
	// cache object reads as a null rate, like test 5 allows.
	hitPct := func() (any, bool) {
		raw, err := os.ReadFile(captured)
		if err != nil {
			return nil, false
		}
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			return nil, false
		}
		cache, _ := doc["cache"].(map[string]any)
		return cache["hit_pct"], true
	}
	frame, ok := p.waitFor(4*time.Second, func(frame string) bool {
		_, ran := hitPct()
		return ran && strings.Contains(plainFrame(frame), "SL-CACHE-RUN")
	})
	if !ok {
		t.Fatalf("statusline command never ran:\n%s", plainFrame(frame))
	}
	if row := fieldRow(frame); !strings.Contains(row, "cache —") {
		t.Errorf("before any call the \"cache\" field must render \"cache —\" on its row; got %q", row)
	}
	if value, _ := hitPct(); value != nil {
		t.Errorf("before any call cache.hit_pct must be null, got %v", value)
	}
	p.submit("oi")
	frame, ok = p.waitFor(6*time.Second, func(frame string) bool {
		value, _ := hitPct()
		return value != nil && strings.Contains(fieldRow(frame), "cache 75%")
	})
	if !ok {
		raw, _ := os.ReadFile(captured)
		t.Fatalf("after a call with 3000 of 4000 input tokens cached the field row must read \"cache 75%%\" and stdin cache.hit_pct must be set; row %q, stdin %s", fieldRow(frame), raw)
	}
	if value, _ := hitPct(); value != float64(75) {
		t.Errorf("cache.hit_pct must be the session hit on the 0-100 scale of context.pct: want 75, got %v", value)
	}
}

// 35. Staging trocado por link durante o turno: a proposta é recusada e nada
// fora do staging é executado, alterado ou apagado.
func TestStatusLineAcceptance_SwappedStagingDirRefused(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	before, _ := os.ReadFile(env.userConfig)
	outside := filepath.Join(env.root, "fora")
	marker := filepath.Join(env.root, "outside-ran")
	script := filepath.Join(outside, "x.sh")
	body := "#!/bin/sh\ntouch '" + marker + "'\nprintf 'SL-SWAP\\n'\n"
	var mu sync.Mutex
	swapped := false
	provider := &slProvider{onRequest: func(request qgruntime.CompletionRequest) {
		mu.Lock()
		defer mu.Unlock()
		if swapped {
			return
		}
		var all strings.Builder
		for _, message := range request.Messages {
			all.WriteString(message.Content)
			all.WriteString("\n")
		}
		path := slStagingPath(env, all.String())
		if path == "" {
			return
		}
		if os.MkdirAll(outside, 0o700) != nil || os.WriteFile(script, []byte(body), 0o644) != nil {
			return
		}
		if os.WriteFile(filepath.Join(outside, "proposal.json"), []byte(`{"lines":[{"left":["mode"]}],"script":"x.sh"}`), 0o600) != nil {
			return
		}
		staging := filepath.Dir(path)
		if os.RemoveAll(staging) == nil && os.Symlink(outside, staging) == nil {
			swapped = true
		}
	}}
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline quero uma linha minha")
	frame, _ := p.waitFor(slSetupWait, func(frame string) bool {
		mu.Lock()
		defer mu.Unlock()
		return swapped || strings.Contains(plainFrame(frame), "Enter aplica")
	})
	mu.Lock()
	ok := swapped
	mu.Unlock()
	if !ok {
		t.Fatal("precondition: the staging dir was never swapped for a link")
	}
	if strings.Contains(plainFrame(frame), "Enter aplica") {
		p.key(tea.KeyEscape)
		// Esc must leave the swapped-in dir alone: a short window, not a
		// deadline.
		p.runFor(time.Second)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("QG ran a script outside the staging dir through the swapped link")
	}
	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("QG deleted a file outside the staging dir through the swapped link: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("QG changed the mode of a file outside the staging dir to %v", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(script); string(data) != body {
		t.Errorf("QG changed a file outside the staging dir:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(outside, "proposal.json")); err != nil {
		t.Errorf("QG deleted proposal.json outside the staging dir: %v", err)
	}
	if now, _ := os.ReadFile(env.userConfig); string(now) != string(before) {
		t.Errorf("a proposal read through a swapped staging dir reached the user config:\n%s", now)
	}
}

// 36. Script que sai com erro deixando um filho em background: nenhum
// processo do grupo sobrevive à execução (o 28 cobre a saída 0).
func TestStatusLineAcceptance_FailingRunBackgroundChildDies(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	pids := filepath.Join(env.root, "bg.pids")
	command := writeScript(t, env, "fundo-falha.sh", `sleep 30 & echo $! >> '`+pids+`'; printf 'SL-BG-FAIL\n'; exit 3`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, timeoutMS: 2000, refreshMS: 600000})
	t.Cleanup(func() {
		for _, line := range fileLines(pids) {
			if pid, err := strconv.Atoi(line); err == nil {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Kill()
				}
			}
		}
	})
	p := startSLPump(t, newSLModel(t, env, nil), 100, 30)
	if !waitForFile(p, pids, 3*time.Second) {
		t.Fatal("precondition: the script never started its background child")
	}
	p.runFor(2 * time.Second)
	for _, line := range fileLines(pids) {
		if pid, err := strconv.Atoi(line); err == nil && processAlive(pid) {
			t.Errorf("background child %d outlived a failed run; every refresh would leak one", pid)
		}
	}
}

// 37. "command" literal (objeto) na proposta nunca chega à config nem roda:
// comando novo só entra por script no staging, que passa pelo dry run e
// fica dentro da pasta de scripts.
func TestStatusLineAcceptance_LiteralCommandProposalNeverApplies(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	outside := filepath.Join(env.root, "fora")
	runs := filepath.Join(env.root, "literal-runs")
	target := filepath.Join(outside, "literal.sh")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho run >> '"+runs+"'\nprintf 'SL-LITERAL\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	staged := false
	provider := &slProvider{onRequest: func(request qgruntime.CompletionRequest) {
		mu.Lock()
		defer mu.Unlock()
		if staged {
			return
		}
		var all strings.Builder
		for _, message := range request.Messages {
			all.WriteString(message.Content)
			all.WriteString("\n")
		}
		path := slStagingPath(env, all.String())
		if path == "" || os.MkdirAll(filepath.Dir(path), 0o700) != nil {
			return
		}
		proposal := map[string]any{
			"lines":     []map[string]any{{"left": []string{"mode", "branch"}}},
			"command":   map[string]any{"command": "sh '" + target + "'"},
			"refreshMs": 1000,
		}
		data, err := json.Marshal(proposal)
		if err == nil && os.WriteFile(path, data, 0o600) == nil {
			staged = true
		}
	}}
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline põe o branch do lado do modo")
	frame, _ := p.waitFor(6*time.Second, containsPlain("Enter aplica"))
	mu.Lock()
	ok := staged
	mu.Unlock()
	if !ok {
		t.Fatal("precondition: the proposal was never staged")
	}
	if strings.Contains(plainFrame(frame), "Enter aplica") {
		p.key(tea.KeyEnter)
	}
	frame = p.runFor(2500 * time.Millisecond)
	if data, _ := os.ReadFile(env.userConfig); strings.Contains(string(data), "literal.sh") {
		t.Errorf("a literal command from the proposal reached the user config:\n%s", data)
	}
	if n := len(fileLines(runs)); n > 0 {
		t.Errorf("a literal command from the proposal ran %d time(s)", n)
	}
	if footer := slFooter(plainFrame(frame)); strings.Contains(footer, "SL-LITERAL") {
		t.Errorf("a literal command from the proposal reached the statusline:\n%s", footer)
	}
}
