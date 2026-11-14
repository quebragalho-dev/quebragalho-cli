package tui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// The exit tests run qg in a child process (this test binary, re-run as
// TestStatusLineAcceptance_ExitHelper) so that leaving qg is a real process
// exit: a statusline script the exit did not kill outlives it.
const (
	slExitModeEnv = "QG_TEST_STATUSLINE_EXIT_MODE"
	slExitDirEnv  = "QG_TEST_STATUSLINE_EXIT_DIR"
	slExitWarning = "a última linha de campos cede quando o script imprimir"
)

// slExitScript parks a statusline run until something kills it, recording its
// pid and the pid of the sleep it waits on.
func slExitScript(dir string) string {
	return `cat >/dev/null
d='` + dir + `'
echo $$ > "$d/pid.tmp" && mv "$d/pid.tmp" "$d/pid"
sleep 30 &
echo $! > "$d/child.tmp" && mv "$d/child.tmp" "$d/child"
wait`
}

func slExitPID(dir, name string) int {
	raw, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	return pid
}

func slExitWaitPID(dir, name string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pid := slExitPID(dir, name); pid > 1 {
			return pid
		}
		time.Sleep(25 * time.Millisecond)
	}
	return 0
}

// slExitOptions resolves the hermetic config the way the CLI launch does
// (same steps as newSLModel) and returns the launch options.
func slExitOptions(t *testing.T, env slEnv, setup SetupOptions) Options {
	t.Helper()
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
	return Options{
		Cwd:               env.workspace,
		Version:           "9.9.9-accept",
		UserConfigPath:    userConfigPath,
		ProjectConfigPath: opts.ProjectConfigPath,
		StatusLine:        resolved.StatusLine,
		ProviderName:      "accept-gw",
		ModelName:         "accept-model",
		ProviderProfile: config.ProviderProfile{
			Name:      "accept-gw",
			Model:     "accept-model",
			APIKey:    slProfileSentinel,
			APIKeyEnv: "ACCEPT_GATEWAY_KEY",
		},
		Provider:        &slProvider{},
		Registry:        tools.NewRegistry(),
		ReasoningEffort: "high",
		AltScreen:       true,
		Setup:           setup,
	}
}

// TestStatusLineAcceptance_ExitHelper is the qg process of the exit tests.
// "chat" and "setup" press Ctrl+C on that screen and exit the instant the
// model hands back tea.Quit, so the exit owes nothing to Bubble Tea's
// shutdown; "tty" runs the real shell (Run) on a terminal until a signal.
func TestStatusLineAcceptance_ExitHelper(t *testing.T) {
	mode := os.Getenv(slExitModeEnv)
	if mode == "" {
		t.Skip("qg process of the statusline exit tests")
	}
	dir := os.Getenv(slExitDirEnv)
	env := newSLEnv(t)
	command := writeScript(t, env, "exit.sh", slExitScript(dir))
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 60000, timeoutMS: 60000})
	var setup SetupOptions
	if mode == "setup" {
		setup = SetupOptions{
			Visible:    true,
			Required:   true,
			ConfigPath: env.userConfig,
			Providers:  []SetupProviderOption{{ID: "qg", Name: "Quebra Galho", DefaultModel: "accept-model", EnvVar: "QUEBRAGALHO_API_KEY", RequiresAuth: true}},
		}
	}
	options := slExitOptions(t, env, setup)
	if mode == "tty" {
		if err := os.WriteFile(filepath.Join(dir, "qg"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Exit(Run(context.Background(), options))
	}
	p := startSLPump(t, newModel(context.Background(), options), 100, 30)
	if _, ok := p.waitFor(15*time.Second, func(string) bool { return slExitPID(dir, "child") > 1 }); !ok {
		t.Fatal("the statusline script never started")
	}
	ctrlC := tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl})
	if mode == "chat" {
		p.deliver(ctrlC) // the first Ctrl+C only arms the exit confirmation
	}
	_, cmd := p.m.Update(ctrlC)
	if cmd == nil {
		t.Fatalf("Ctrl+C on the %s screen did not quit", mode)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("Ctrl+C on the %s screen did not hand back tea.Quit", mode)
	}
	os.Exit(0)
}

// slExitHelper prepares the qg process in mode, on a terminal when tty is
// set. Its cleanup kills qg, the script and its sleep, even on a red run.
func slExitHelper(t *testing.T, mode, dir string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	args := []string{os.Args[0], "-test.run=^TestStatusLineAcceptance_ExitHelper$"}
	if mode == "tty" {
		script, err := exec.LookPath("script")
		if err != nil {
			t.Skip("no script(1) to give qg a terminal")
		}
		switch runtime.GOOS {
		case "darwin":
			args = append([]string{script, "-q", "/dev/null"}, args...)
		case "linux":
			quoted := make([]string, len(args))
			for i, arg := range args {
				quoted[i] = "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
			}
			args = []string{script, "-qec", strings.Join(quoted, " "), "/dev/null"}
		default:
			t.Skipf("no terminal for qg on %s", runtime.GOOS)
		}
	}
	cmd := exec.Command(args[0], args[1:]...)
	// The race runtime holds an exiting process for a second, long enough
	// for a late kill to land; the exit must not get that grace.
	cmd.Env = append(os.Environ(), slExitModeEnv+"="+mode, slExitDirEnv+"="+dir, "TMPDIR="+tmp,
		"GORACE="+strings.TrimSpace(os.Getenv("GORACE")+" atexit_sleep_ms=0"))
	if mode != "tty" {
		// One P: a kill left to another goroutine never runs between
		// tea.Quit and the exit, so only a kill done by then counts.
		cmd.Env = append(cmd.Env, "GOMAXPROCS=1")
	}
	out := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if mode == "tty" {
		keyboard, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = keyboard.Close() })
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		for _, name := range []string{"qg", "pid", "child"} {
			if pid := slExitPID(dir, name); statuslineTestProcessAlive(pid) {
				if proc, err := os.FindProcess(pid); err == nil {
					_ = proc.Kill()
				}
			}
		}
	})
	return cmd, out
}

// slExitRequireDead fails when the script or its sleep is still alive two
// seconds after qg exited: nothing kills them later.
func slExitRequireDead(t *testing.T, dir, what string) {
	t.Helper()
	script, child := slExitPID(dir, "pid"), slExitPID(dir, "child")
	if script <= 1 || child <= 1 {
		t.Fatalf("%s: the statusline script never recorded its pids (script %d, sleep %d)", what, script, child)
	}
	deadline := time.Now().Add(2 * time.Second)
	for statuslineTestProcessAlive(script) || statuslineTestProcessAlive(child) {
		if time.Now().After(deadline) {
			t.Errorf("%s left the in-flight statusline script running: script pid %d alive=%v, its sleep pid %d alive=%v, 2s after qg exited",
				what, script, statuslineTestProcessAlive(script), child, statuslineTestProcessAlive(child))
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// Leaving qg (Ctrl+C twice in the chat, Ctrl+C on the setup screen) with a
// statusline run in flight kills the script before the exit returns: qg
// exits the instant the model hands back tea.Quit, and the script is dead.
func TestStatusLineAcceptance_ExitKillsInFlightScript(t *testing.T) {
	requireSh(t)
	for _, mode := range []string{"chat", "setup"} {
		t.Run(mode, func(t *testing.T) {
			for i := 1; i <= 3; i++ {
				dir := t.TempDir()
				cmd, out := slExitHelper(t, mode, dir)
				if err := cmd.Run(); err != nil {
					t.Fatalf("%s exit %d: qg did not leave through Ctrl+C: %v\n%s", mode, i, err, out)
				}
				slExitRequireDead(t, dir, mode+" exit "+strconv.Itoa(i))
			}
		})
	}
}

// SIGTERM and SIGHUP (the terminal closing) end qg with a statusline run in
// flight; the script dies with it instead of staying behind as an orphan.
func TestStatusLineAcceptance_SignalsKillInFlightScript(t *testing.T) {
	requireSh(t)
	for _, tc := range []struct {
		name   string
		signal syscall.Signal
	}{
		{"SIGTERM", syscall.SIGTERM},
		{"SIGHUP", syscall.SIGHUP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cmd, out := slExitHelper(t, "tty", dir)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			exited := make(chan struct{})
			go func() {
				_ = cmd.Wait()
				close(exited)
			}()
			qg := slExitWaitPID(dir, "qg", 15*time.Second)
			if qg <= 1 || slExitWaitPID(dir, "child", 15*time.Second) <= 1 {
				t.Fatalf("precondition: qg (pid %d) never had a statusline run in flight:\n%s", qg, out)
			}
			proc, err := os.FindProcess(qg)
			if err != nil {
				t.Fatal(err)
			}
			if err := proc.Signal(tc.signal); err != nil {
				t.Fatal(err)
			}
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Fatalf("qg did not exit within 10s of %s", tc.name)
			}
			slExitRequireDead(t, dir, tc.name)
		})
	}
}

// A fields-only proposal that keeps a live command which has printed
// nothing warns that the last field line gives way only when the fields fill
// all three lines; with two field lines the script line fits below them.
func TestStatusLineAcceptance_PreviewWarnsOnlyWhenFieldsFillThreeLines(t *testing.T) {
	requireSh(t)
	for _, tc := range []struct {
		name  string
		lines []slLine
		warn  bool
	}{
		{"two field lines", []slLine{{left: []string{"mode"}}, {left: []string{"branch"}}}, false},
		{"three field lines", slThreeFieldLines(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSLEnv(t)
			command := writeScript(t, env, "quiet.sh", "cat >/dev/null")
			writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 1000, timeoutMS: 8000})
			var staged string
			provider := slProposer(t, env, slLayout{lines: tc.lines, refreshMS: 1000, timeoutMS: 8000}, &staged)
			p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
			p.submit("/statusline campos sem script")
			frame, ok := p.waitFor(8*time.Second, containsPlain("Prévia com os dados desta sessão"))
			if !ok {
				t.Fatalf("no preview (staged=%q):\n%s", staged, plainFrame(frame))
			}
			plain := plainFrame(frame)
			preview := plain[strings.Index(plain, "Prévia com os dados desta sessão"):]
			if got := strings.Contains(preview, slExitWarning); got != tc.warn {
				t.Errorf("%s: warning shown=%v, want %v:\n%s", tc.name, got, tc.warn, preview)
			}
		})
	}
}
