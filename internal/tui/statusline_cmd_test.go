package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// The stdin payload carries session facts and never credentials: no key,
// token, secret, or password may appear in any field name or value.
func TestStatuslineStdinHasNoSecrets(t *testing.T) {
	m := limeTestModel()
	m.activeSession = sessions.Metadata{SessionID: "sess-1"}
	m.modelName = "model-x"
	m.providerName = "qg"
	m.cwd = "/tmp/work"
	m.gitBranch = "main"
	payload := m.statuslineStdin(100)
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	for _, key := range []string{"session_id", "cwd", "model", "provider", "effort", "branch", "version", "width"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("payload lacks %q: %s", key, payload)
		}
	}
	lowered := strings.ToLower(string(payload))
	for _, banned := range []string{"api_key", "apikey", "token", "secret", "password", "credential", "authorization"} {
		if strings.Contains(lowered, banned) {
			t.Fatalf("payload contains %q: %s", banned, payload)
		}
	}
}

// Context numbers and BRL cost ride the payload when known.
func TestStatuslineStdinContextAndCost(t *testing.T) {
	m := batchThreeLiveModel(t)
	m.sessionEvents = []sessions.Event{
		{Type: sessions.EventUsage, Payload: json.RawMessage(`{"cost_exact": "1/4", "cost_currency": "BRL"}`)},
	}
	payload := m.statuslineStdin(120)
	var decoded struct {
		CostBRL *float64 `json:"cost_brl"`
		Width   int      `json:"width"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.CostBRL == nil || *decoded.CostBRL != 0.25 {
		t.Fatalf("cost_brl = %v, want 0.25", decoded.CostBRL)
	}
	if decoded.Width != 120 {
		t.Fatalf("width = %d, want 120", decoded.Width)
	}
}

// Only SGR color escapes survive sanitizing; OSC links, cursor moves, and
// control bytes are removed. At most three lines reach the footer.
func TestSanitizeStatuslineOutput(t *testing.T) {
	in := "\x1b[32mgreen\x1b[0m plain\n" +
		"\x1b]8;;https://example.com\x07link\x1b]8;;\x07\n" +
		"\x1b[2Kcleared\x00nul\x07bel\n" +
		"fourth line\n"
	lines := sanitizeStatuslineOutput(in)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %#v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "\x1b[32m") {
		t.Fatalf("SGR color was stripped: %q", lines[0])
	}
	for _, line := range lines[1:] {
		if strings.Contains(line, "\x1b]") || strings.Contains(line, "\x00") || strings.Contains(line, "\x07") {
			t.Fatalf("non-SGR escape or control byte leaked: %q", line)
		}
	}
	huge := sanitizeStatuslineOutput(strings.Repeat("x", 2000))
	if got := ansiPattern.ReplaceAllString(huge[0], ""); len([]rune(got)) > statuslineSanitizeWidth {
		t.Fatalf("cached line exceeds memory cap: %d runes", len([]rune(got)))
	}
}

// The child environment is an allowlist: PATH and locale basics in, every
// credential-like variable out — even when set in the parent process.
func TestStatuslineChildEnvScrubbed(t *testing.T) {
	t.Setenv("QG_API_KEY", "key-parents-must-not-leak")
	t.Setenv("MY_SERVICE_TOKEN", "tok-parents-must-not-leak")
	t.Setenv("SOME_SECRET", "shh")
	env := statuslineChildEnv(100, 40)
	joined := strings.Join(env, "\n")
	for _, banned := range []string{"QG_API_KEY", "MY_SERVICE_TOKEN", "SOME_SECRET"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("child env leaks %s:\n%s", banned, joined)
		}
	}
	if !strings.Contains(joined, "PATH=") {
		t.Fatalf("child env lacks PATH:\n%s", joined)
	}
}

// A run that exceeds the timeout is killed: the message carries a timeout
// note and empty output, so the model keeps the last good value.
func TestStatuslineCommandTimeout(t *testing.T) {
	restore := stubStatuslineShell(sleepHelperCommand("5s"))
	defer restore()
	msg := execStatuslineRun(context.Background(), "whatever", string(`{}`), statuslineChildEnv(80, 24), 50*time.Millisecond)
	if len(msg.lines) != 0 {
		t.Fatalf("timed-out run returned output: %#v", msg.lines)
	}
	if !strings.Contains(msg.errNote, "tempo") {
		t.Fatalf("errNote = %q, want a timeout note", msg.errNote)
	}
}

// Output over three lines is capped; a failing command keeps the error note.
func TestStatuslineCommandRun(t *testing.T) {
	restore := stubStatuslineShell(echoHelperCommand("a\nb\nc\nd"))
	defer restore()
	msg := execStatuslineRun(context.Background(), "whatever", string(`{}`), statuslineChildEnv(80, 24), 2*time.Second)
	if len(msg.lines) != 3 {
		t.Fatalf("lines = %#v, want 3", msg.lines)
	}
	if msg.errNote != "" {
		t.Fatalf("errNote = %q, want empty", msg.errNote)
	}
}

// The Update loop starts a due run async, folds its result, drops stale
// results, and never starts a second run while one is in flight.
func TestStatuslinePollRunLifecycle(t *testing.T) {
	restore := stubStatuslineShell(echoHelperCommand("hello custom"))
	defer restore()
	m := limeTestModel()
	m.width, m.height = 100, 30
	m.statuslineCfg = config.StatusLineConfig{
		Lines:   []config.StatusLineLine{{Left: []string{"mode"}}},
		Command: &config.StatusLineCommand{Command: "echo hello custom"},
	}
	updated, cmd := m.Update(statuslineTickMsg{})
	if cmd == nil {
		t.Fatal("tick produced no command, want a statusline run")
	}
	nm := updated.(model)
	if !nm.statuslineCmdInFlight {
		t.Fatal("in-flight flag not set after starting a run")
	}
	// A second trigger while the run is in flight starts nothing.
	if _, second := nm.maybeStartStatuslineRun(); second != nil {
		t.Fatal("second trigger started an overlapping run")
	}
	msg, ok := execCmd(cmd).(statuslineCmdMsg)
	if !ok {
		t.Fatalf("run returned %T, want statuslineCmdMsg", execCmd(cmd))
	}
	final, _ := nm.Update(msg)
	fm := final.(model)
	if fm.statuslineCmdInFlight {
		t.Fatal("in-flight flag still set after the run landed")
	}
	if !strings.Contains(fm.statuslineCmdOutput, "hello custom") {
		t.Fatalf("output = %q, want the script text", fm.statuslineCmdOutput)
	}
	// A stale result (newer run started since) is dropped, never painted.
	stale := msg
	stale.seq = fm.statuslineCmdSeq - 1
	kept, _ := fm.Update(stale)
	if got := kept.(model).statuslineCmdOutput; !strings.Contains(got, "hello custom") {
		t.Fatalf("stale result overwrote output: %q", got)
	}
}

// A failure keeps the last good value and marks discreetly; the next success
// clears the mark.
func TestStatuslinePollFailureKeepsLastGood(t *testing.T) {
	m := limeTestModel()
	m.statuslineCfg = config.StatusLineConfig{
		Command: &config.StatusLineCommand{Command: "exit 3"},
	}
	m.statuslineCmdSeq = 2
	m.statuslineCmdOutput = "last good"
	failed, _ := m.Update(statuslineCmdMsg{seq: 2, errNote: "statusline: comando falhou (saída 3)"})
	fm := failed.(model)
	if fm.statuslineCmdOutput != "last good" {
		t.Fatalf("failure overwrote last good: %q", fm.statuslineCmdOutput)
	}
	if fm.statuslineCmdErr == "" {
		t.Fatal("failure mark missing")
	}
	cleared, _ := fm.Update(statuslineCmdMsg{seq: 2, lines: []string{"fresh"}})
	cm := cleared.(model)
	if cm.statuslineCmdOutput != "fresh" || cm.statuslineCmdErr != "" {
		t.Fatalf("success did not replace output and clear the mark: %#v", cm.statuslineCmdOutput)
	}
}

// A grandchild that outlives the shell holding stdout open must not wedge
// the runner: the timeout kills the tree and the next cycle runs.
func TestStatuslineCommandKillsTreeOnTimeout(t *testing.T) {
	restore := stubStatuslineShell(leakHelperCommand())
	defer restore()
	start := time.Now()
	msg := execStatuslineRun(context.Background(), "whatever", "{}", statuslineChildEnv(80, 24), 200*time.Millisecond)
	elapsed := time.Since(start)
	if msg.errNote == "" || !strings.Contains(msg.errNote, "tempo") {
		t.Fatalf("errNote = %q, want a timeout note", msg.errNote)
	}
	if bound := 200*time.Millisecond + statuslineTreeKillGrace + 10*time.Second; elapsed > bound {
		t.Fatalf("leaked grandchild stalled the run for %v (bound %v)", elapsed, bound)
	}
	// The next cycle still runs: nothing wedged.
	restore()
	restore = stubStatuslineShell(echoHelperCommand("again"))
	defer restore()
	again := execStatuslineRun(context.Background(), "whatever", "{}", statuslineChildEnv(80, 24), 5*time.Second)
	if again.errNote != "" {
		t.Fatalf("follow-up run failed: %q", again.errNote)
	}
	if len(again.lines) != 1 || !strings.Contains(again.lines[0], "again") {
		t.Fatalf("follow-up lines = %#v", again.lines)
	}
}

// stubStatuslineShell swaps the shell constructor for a helper-process
// re-exec, so runner tests never depend on a platform shell or sleep binary.
func stubStatuslineShell(args []string) func() {
	previous := statuslineShellCommand
	statuslineShellCommand = func(ctx context.Context, command, stdin string, env []string) *exec.Cmd {
		helper := append([]string{"-test.run=TestStatuslineSleepHelper", "--"}, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helper...)
		cmd.Env = append(env, "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}
	return func() { statuslineShellCommand = previous }
}

func sleepHelperCommand(duration string) []string { return []string{"sleep", duration} }

func echoHelperCommand(text string) []string { return []string{"echo", text} }

// leakHelperCommand spawns a helper that orphans a grandchild holding the
// stdout pipe open, then exits at once: without tree kill + WaitDelay, the
// run below would block until the grandchild's hold expires (30s, far past
// the test bound).
func leakHelperCommand() []string { return []string{"leak", "hold", "30s"} }

// TestStatuslineSleepHelper is not a real test: the stubbed shell re-execs
// the test binary into this function, which sleeps or echoes.
func TestStatuslineSleepHelper(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) < 2 {
		os.Exit(2)
	}
	switch args[0] {
	case "sleep":
		duration, err := time.ParseDuration(args[1])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(duration)
	case "echo":
		os.Stdout.WriteString(args[1] + "\n")
	case "leak":
		// Orphan a grandchild that holds the stdout pipe: the direct child
		// exits at once, so only a tree kill (or WaitDelay) unblocks the
		// reader. Stdout is inherited explicitly — exec does not inherit it
		// by default.
		grandchild := exec.Command(os.Args[0], append([]string{"-test.run=TestStatuslineSleepHelper", "--"}, args[1:]...)...)
		grandchild.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
		grandchild.Stdout = os.Stdout
		if err := grandchild.Start(); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	case "hold":
		duration, err := time.ParseDuration(args[1])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(duration)
	}
	os.Exit(0)
}
