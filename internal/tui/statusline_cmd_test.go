package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

// Unknown values ride as null, never absent: keys are always present.
func TestStatuslineStdinNullsWhenUnknown(t *testing.T) {
	m := limeTestModel()
	m.gitBranch = "" // unknown branch reads as null, not as the test runner's branch
	payload := m.statuslineStdin(100)
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	for _, key := range []string{"context", "cache", "cost_brl", "branch"} {
		value, ok := decoded[key]
		if !ok {
			t.Fatalf("payload lacks %q: %s", key, payload)
		}
		if value != nil && key != "context" && key != "cache" {
			t.Fatalf("%s = %v, want null when unknown", key, value)
		}
	}
	context, ok := decoded["context"].(map[string]any)
	if !ok {
		t.Fatalf("context must stay an object: %s", payload)
	}
	for _, key := range []string{"used", "window", "pct"} {
		value, present := context[key]
		if !present {
			t.Fatalf("context lacks %q: %s", key, payload)
		}
		if value != nil {
			t.Fatalf("context.%s = %v, want null when unknown", key, value)
		}
	}
	cache, ok := decoded["cache"].(map[string]any)
	if !ok {
		t.Fatalf("cache must stay an object: %s", payload)
	}
	if value, present := cache["hit_pct"]; !present || value != nil {
		t.Fatalf("cache.hit_pct = %v, want present null: %s", value, payload)
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

// The allowlist matches exactly on Unix and case-insensitively on Windows,
// where "Path" and "SystemRoot" carry the canonical casing.
func TestStatuslineChildEnvMatchesHostCaseRules(t *testing.T) {
	environ := []string{"PATH=/bin", "Path=C:\\Windows", "SystemRoot=C:\\Windows", "FOO=1", "TMPDIR=/tmp", "LANG=en_US.UTF-8", "QG_API_KEY=x"}
	unix := statuslineChildEnvFor("linux", environ, 0, 0)
	for _, want := range []string{"PATH=/bin", "TMPDIR=/tmp", "LANG=en_US.UTF-8"} {
		if !slices.Contains(unix, want) {
			t.Fatalf("unix env lacks %q: %q", want, unix)
		}
	}
	for _, banned := range []string{"Path=", "SystemRoot=", "FOO=", "QG_API_KEY="} {
		for _, kv := range unix {
			if strings.HasPrefix(kv, banned) {
				t.Fatalf("unix env keeps %q: %q", kv, unix)
			}
		}
	}
	windows := statuslineChildEnvFor("windows", environ, 0, 0)
	for _, want := range []string{"Path=C:\\Windows", "SystemRoot=C:\\Windows"} {
		if !slices.Contains(windows, want) {
			t.Fatalf("windows env lacks %q (case-insensitive): %q", want, windows)
		}
	}
	for _, kv := range windows {
		if strings.HasPrefix(kv, "TMPDIR=") || strings.HasPrefix(kv, "FOO=") {
			t.Fatalf("windows env keeps unix-only or unknown %q: %q", kv, windows)
		}
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
	if _, second := nm.startStatuslineRun(false); second != nil {
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

// A forced refresh that lands while a run is in flight is not lost: it is
// queued and consumed when the in-flight run completes.
func TestStatuslineForcedRefreshQueuedDuringFlight(t *testing.T) {
	m := limeTestModel()
	m.statuslineCfg = config.StatusLineConfig{Command: &config.StatusLineCommand{Command: "echo hi"}}
	m, _ = m.startStatuslineRun(false)
	if !m.statuslineCmdInFlight {
		t.Fatal("precondition: no run in flight")
	}
	m, cmd := m.startStatuslineRun(true)
	if cmd != nil {
		t.Fatalf("overlapping run started: %T", cmd)
	}
	m, cmd = m.applyStatuslineCmdMsg(statuslineCmdMsg{seq: m.statuslineCmdSeq})
	if cmd == nil {
		t.Fatal("queued refresh was dropped on completion")
	}
	msg, ok := execCmd(cmd).(statuslineCmdMsg)
	if !ok {
		t.Fatalf("queued refresh returned %T, want statuslineCmdMsg", execCmd(cmd))
	}
	if msg.seq != m.statuslineCmdSeq {
		t.Fatalf("queued refresh seq = %d, want %d", msg.seq, m.statuslineCmdSeq)
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

// When only a grandchild holds stdout open (the shell itself already exited),
// Wait can only unblock via WaitDelay: the output already received is kept
// and the process group is killed, so no orphan survives per refresh.
func TestStatuslineCommandKeepsOutputOnWaitDelay(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	restore := stubStatuslineShell(leakPrintHelperCommand(pidFile, "SL-PARTIAL-OK"))
	defer restore()
	msg := execStatuslineRun(context.Background(), "whatever", "{}", statuslineChildEnv(80, 24), 30*time.Second)
	if msg.errNote != "" {
		t.Fatalf("errNote = %q, want the kept output instead of a failure", msg.errNote)
	}
	if len(msg.lines) != 1 || !strings.Contains(msg.lines[0], "SL-PARTIAL-OK") {
		t.Fatalf("lines = %#v, want the output received before WaitDelay", msg.lines)
	}
	grandchild := statuslineTestWaitPID(t, pidFile)
	if grandchild == 0 {
		t.Fatal("grandchild never recorded its pid")
	}
	deadline := time.Now().Add(5 * time.Second)
	for statuslineTestProcessAlive(grandchild) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if statuslineTestProcessAlive(grandchild) {
		t.Fatalf("grandchild pid %d still alive: the group kill did not run", grandchild)
	}
}

// statuslineTestWaitPID polls a pid file written by a helper grandchild.
func statuslineTestWaitPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

// statuslineTestProcessAlive reports whether pid still runs (zombies count
// as gone: the group kill reaps through init).
func statuslineTestProcessAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return !statuslineTestProcessZombie(pid)
}

// statuslineTestProcessZombie reports a zombie state via ps, mirroring the
// execution package's reap-aware check without importing test-only helpers.
func statuslineTestProcessZombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return true
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// Unbounded output is capped in memory: a 200 MiB flood grows the heap by
// megabytes, not hundreds of megabytes. TotalAlloc is cumulative, so the
// gap between capped (64 KiB kept) and uncapped is unmistakable.
func TestStatuslineCommandCapsOutput(t *testing.T) {
	restore := stubStatuslineShell(spamHelperCommand(200 << 20))
	defer restore()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	msg := execStatuslineRun(context.Background(), "whatever", "{}", statuslineChildEnv(80, 24), 30*time.Second)
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if msg.errNote != "" {
		t.Fatalf("errNote = %q, want success", msg.errNote)
	}
	if len(msg.lines) != 3 {
		t.Fatalf("lines = %d, want the 3-line sanitize cap", len(msg.lines))
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 64<<20 {
		t.Fatalf("heap grew by %d bytes for a capped read, want at most 64 MiB", grew)
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

// leakPrintHelperCommand orphans a grandchild holding stdout open (recorded
// in a pid file), then prints text and exits at once: Wait can only unblock
// via WaitDelay, but the output already received must be kept.
func leakPrintHelperCommand(pidFile, text string) []string {
	return []string{"leakprint", pidFile, text}
}

func spamHelperCommand(bytes int) []string { return []string{"spam", strconv.Itoa(bytes)} }

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
	case "holdpid":
		if len(args) < 3 {
			os.Exit(2)
		}
		if err := os.WriteFile(args[1], []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
			os.Exit(2)
		}
		duration, err := time.ParseDuration(args[2])
		if err != nil {
			os.Exit(2)
		}
		time.Sleep(duration)
	case "leakprint":
		if len(args) < 3 {
			os.Exit(2)
		}
		// Same orphaned-grandchild setup as "leak", but the parent prints
		// before exiting so there is output to keep.
		grandchild := exec.Command(os.Args[0],
			"-test.run=TestStatuslineSleepHelper", "--", "holdpid", args[1], "30s")
		grandchild.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
		grandchild.Stdout = os.Stdout
		if err := grandchild.Start(); err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString(args[2] + "\n")
		os.Exit(0)
	case "spam":
		remaining, err := strconv.Atoi(args[1])
		if err != nil {
			os.Exit(2)
		}
		chunk := []byte(strings.Repeat("x", 4096) + "\n")
		for remaining > 0 {
			n := min(remaining, len(chunk))
			if _, err := os.Stdout.Write(chunk[:n]); err != nil {
				break
			}
			remaining -= n
		}
	}
	os.Exit(0)
}
