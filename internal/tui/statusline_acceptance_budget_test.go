package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// Run budget of the external statusline command and the footer line it is
// owed. Runs are observed from outside: the script appends one line per
// run to a log, and the pump stamps each line when it first appears.

// slRefreshSlack is how far an idle run may trail its refreshMs deadline
// (pump polling, process start, a loaded machine).
const slRefreshSlack = 300 * time.Millisecond

// slRunLog stamps each line a statusline script appends, one per run.
type slRunLog struct {
	path string
	at   []time.Time
	vals []string
}

func (l *slRunLog) poll() {
	lines := fileLines(l.path)
	for len(l.at) < len(lines) {
		l.vals = append(l.vals, lines[len(l.at)])
		l.at = append(l.at, time.Now())
	}
}

func (l *slRunLog) pumpFor(p *slPump, d time.Duration) {
	p.waitFor(d, func(string) bool { l.poll(); return false })
}

func (l *slRunLog) pumpUntil(p *slPump, runs int, timeout time.Duration) bool {
	_, ok := p.waitFor(timeout, func(string) bool { l.poll(); return len(l.at) >= runs })
	return ok
}

// slChattyProvider answers the prompt "turno n" with many text deltas, a
// closing marker "FIM-DO-TURNO-n" and one usage report of n*1000+321 input
// tokens. Side calls (a session title, say) get a bare reply.
type slChattyProvider struct {
	deltas int
}

var slTurnPrompt = regexp.MustCompile(`^turno (\d+)$`)

func (p *slChattyProvider) StreamCompletion(_ context.Context, request qgruntime.CompletionRequest) (<-chan qgruntime.StreamEvent, error) {
	turn := 0
	if n := len(request.Messages); n > 0 && request.Messages[n-1].Role == qgruntime.MessageRoleUser {
		if match := slTurnPrompt.FindStringSubmatch(strings.TrimSpace(request.Messages[n-1].Content)); match != nil {
			turn, _ = strconv.Atoi(match[1])
		}
	}
	ch := make(chan qgruntime.StreamEvent, p.deltas+3)
	if turn == 0 {
		ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventText, Content: "ok"}
	} else {
		for i := 0; i < p.deltas; i++ {
			ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventText, Content: "x "}
		}
		ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventText, Content: fmt.Sprintf("\nFIM-DO-TURNO-%d", turn)}
		ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventUsage, Usage: qgruntime.Usage{InputTokens: turn*1000 + 321, OutputTokens: 7}}
	}
	ch <- qgruntime.StreamEvent{Type: qgruntime.StreamEventDone}
	close(ch)
	return ch, nil
}

func newSLChattyModel(t *testing.T, env slEnv, deltas int) model {
	t.Helper()
	m := newSLModel(t, env, nil)
	m.provider = &slChattyProvider{deltas: deltas}
	return m
}

// slSubmitTurn sends a prompt and pumps until turn n has landed, returning
// when the pump saw it land.
func slSubmitTurn(t *testing.T, p *slPump, runs *slRunLog, n int) time.Time {
	t.Helper()
	p.submit(fmt.Sprintf("turno %d", n))
	marker := fmt.Sprintf("FIM-DO-TURNO-%d", n)
	frame, ok := p.waitFor(8*time.Second, func(frame string) bool {
		if runs != nil {
			runs.poll()
		}
		current, isModel := p.m.(model)
		return isModel && !current.pending && strings.Contains(plainFrame(frame), marker)
	})
	if !ok {
		t.Fatalf("turn %d never landed:\n%s", n, plainFrame(frame))
	}
	return time.Now()
}

func slNonEmptyLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func slThreeFieldLines() []slLine {
	return []slLine{{left: []string{"mode"}}, {left: []string{"branch"}}, {left: []string{"cwd"}}}
}

// A1. With a command configured, its output (or its error) always keeps one
// of the three footer lines. The fields get the rest, and the last field
// line gives way first.
func TestStatusLineAcceptance_CommandKeepsALineUnderThreeFieldLines(t *testing.T) {
	requireSh(t)
	for _, tc := range []struct{ name, body, want string }{
		{"output", "printf 'SL-A1-OUT\\n'", "SL-A1-OUT"},
		{"error", "exit 1", "comando falhou"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSLEnv(t)
			command := writeScript(t, env, "a1.sh", tc.body)
			writeUserStatusLine(t, env, slLayout{lines: slThreeFieldLines(), command: command, refreshMS: 1000, timeoutMS: 8000})
			p := startSLPump(t, newSLModel(t, env, nil), 120, 40)
			frame, ok := p.waitFor(5*time.Second, func(frame string) bool {
				return strings.Contains(slFooter(plainFrame(frame)), tc.want)
			})
			footer := slFooter(plainFrame(frame))
			if !ok {
				t.Fatalf("with three field lines the command %s never reached the footer (want %q):\n%s", tc.name, tc.want, footer)
			}
			if rows := slNonEmptyLines(footer); len(rows) > 3 {
				t.Errorf("footer has %d lines, want at most 3:\n%s", len(rows), footer)
			}
			if !strings.Contains(footer, "automático") || !strings.Contains(footer, slBranch) {
				t.Errorf("the first two field lines must stay:\n%s", footer)
			}
			if strings.Contains(footer, "~/workspace") {
				t.Errorf("the last field line must give way to the command:\n%s", footer)
			}
		})
	}
}

// A1 in /statusline: the preview of a proposal with three field lines and a
// script shows the script's dry-run output, as the footer will.
func TestStatusLineAcceptance_PreviewShowsScriptUnderThreeFieldLines(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	writeJSONFile(t, env.userConfig, slUserConfigBase())
	var staged string
	provider := slProposer(t, env, slLayout{
		lines:     slThreeFieldLines(),
		script:    slScript{name: "tres.sh", body: "printf 'SL-A1-PREVIEW\\n'"},
		refreshMS: 1000,
		timeoutMS: 8000,
	}, &staged)
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	p.submit("/statusline três linhas de campos e o meu script")
	frame, ok := p.waitFor(8*time.Second, containsPlain("Prévia com os dados desta sessão"))
	if !ok {
		t.Fatalf("no preview (staging=%q):\n%s", staged, plainFrame(frame))
	}
	if !strings.Contains(plainFrame(frame), "SL-A1-PREVIEW") {
		t.Errorf("the preview hides the script output under three field lines:\n%s", plainFrame(frame))
	}
}

// R2a. A finished turn causes at most one run, however many events it
// streams. A turn that lands while a run is in flight gets exactly one
// follow-up run, and that run sees the end-of-turn state.
func TestStatusLineAcceptance_TurnCausesOneRunWithEndOfTurnState(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	log := filepath.Join(env.root, "runs.log")
	gate := filepath.Join(env.root, "gate")
	// Each run logs context.used from its stdin. The first run holds until
	// the gate opens, so turn 1 lands while it is in flight.
	command := writeScript(t, env, "uso.sh", `s=$(cat)
u=$(printf '%s' "$s" | sed -n 's/.*"used":\([0-9a-z]*\).*/\1/p')
first=0
[ -f '`+log+`' ] || first=1
printf '%s\n' "$u" >> '`+log+`'
if [ "$first" = 1 ]; then while [ ! -f '`+gate+`' ]; do sleep 0.05; done; fi
printf 'SL-USED-%s\n' "$u"`)
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 60000, timeoutMS: 8000})
	p := startSLPump(t, newSLChattyModel(t, env, 200), 120, 40)
	runs := &slRunLog{path: log}
	if !runs.pumpUntil(p, 1, 5*time.Second) {
		t.Fatal("the startup run never started")
	}

	slSubmitTurn(t, p, runs, 1)
	if err := os.WriteFile(gate, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runs.pumpFor(p, 1500*time.Millisecond)
	if len(runs.vals) != 2 {
		t.Fatalf("turn 1 landed during a run: want the startup run plus exactly one follow-up, got %d runs %q", len(runs.vals), runs.vals)
	}
	if runs.vals[1] != "1321" {
		t.Errorf("the follow-up run saw context.used=%q, want the end-of-turn 1321", runs.vals[1])
	}
	if footer := slFooter(plainFrame(p.view())); !strings.Contains(footer, "SL-USED-1321") {
		t.Errorf("footer does not show the end-of-turn value:\n%s", footer)
	}

	slSubmitTurn(t, p, runs, 2)
	runs.pumpFor(p, 1500*time.Millisecond)
	if len(runs.vals) != 3 {
		t.Fatalf("turn 2 landed with no run in flight: want exactly one more run, got %d runs %q", len(runs.vals), runs.vals)
	}
	if runs.vals[2] != "2321" {
		t.Errorf("turn 2 run saw context.used=%q, want 2321", runs.vals[2])
	}
}

// R2b. The idle tick counts refreshMs from the start of the last run,
// whatever caused it: after a turn-forced run, no tick run comes earlier
// than refreshMs, and the next idle run comes at refreshMs, not later.
func TestStatusLineAcceptance_IdleTickCountsFromLastRun(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	log := filepath.Join(env.root, "runs.log")
	command := writeScript(t, env, "tick.sh", "cat >/dev/null\nprintf 'run\\n' >> '"+log+"'\nprintf 'SL-TICK\\n'")
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 1000, timeoutMS: 8000})
	p := startSLPump(t, newSLChattyModel(t, env, 5), 120, 40)
	runs := &slRunLog{path: log}
	if !runs.pumpUntil(p, 1, 5*time.Second) {
		t.Fatal("the startup run never started")
	}
	// The turn lands early in the idle window, so its run is forced.
	runs.pumpFor(p, 250*time.Millisecond)
	slSubmitTurn(t, p, runs, 1)
	if !runs.pumpUntil(p, 2, 3*time.Second) {
		t.Fatalf("the finished turn started no run: %d runs", len(runs.at))
	}
	forced := runs.at[1]
	runs.pumpUntil(p, 4, time.Until(forced.Add(3*time.Second)))
	if len(runs.at) < 3 {
		t.Fatalf("no idle run within 3s after the forced run; refreshMs is 1s")
	}
	refresh := time.Second
	gap := runs.at[2].Sub(forced)
	if gap < refresh-100*time.Millisecond {
		t.Errorf("a tick run came %v after the forced run; none may come before refreshMs (%v)", gap.Round(time.Millisecond), refresh)
	}
	if gap > refresh+slRefreshSlack {
		t.Errorf("the next idle run came %v after the forced run; the tick must count refreshMs (%v) from the last run start", gap.Round(time.Millisecond), refresh)
	}
	if len(runs.at) >= 4 {
		if next := runs.at[3].Sub(runs.at[2]); next < refresh-100*time.Millisecond || next > refresh+slRefreshSlack {
			t.Errorf("idle runs then came %v apart, want refreshMs (%v)", next.Round(time.Millisecond), refresh)
		}
	}
}

// R2c. With the default statusline (no command) no process is spawned for
// it, at startup, after a turn or while idle. A configured command through
// the same shim is the control that the shim sees spawns.
func TestStatusLineAcceptance_DefaultStatusLineSpawnsNoProcess(t *testing.T) {
	requireSh(t)
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "control"}[configured], func(t *testing.T) {
			env := newSLEnv(t)
			realSh, err := exec.LookPath("sh")
			if err != nil {
				t.Fatal(err)
			}
			shims := filepath.Join(env.root, "shims")
			spawned := filepath.Join(env.root, "spawned.log")
			if err := os.MkdirAll(shims, 0o700); err != nil {
				t.Fatal(err)
			}
			shim := "#!" + realSh + "\nprintf 'sh\\n' >> '" + spawned + "'\nexec '" + realSh + "' \"$@\"\n"
			if err := os.WriteFile(filepath.Join(shims, "sh"), []byte(shim), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", shims+string(os.PathListSeparator)+os.Getenv("PATH"))
			if configured {
				writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: "printf 'SL-C\\n'", refreshMS: 1000, timeoutMS: 8000})
			} else {
				writeJSONFile(t, env.userConfig, slUserConfigBase())
			}
			p := startSLPump(t, newSLChattyModel(t, env, 5), 120, 40)
			slSubmitTurn(t, p, nil, 1)
			p.runFor(2500 * time.Millisecond)
			n := len(fileLines(spawned))
			if configured && n == 0 {
				t.Fatal("control: the shim saw no statusline run, so it cannot see a spawn")
			}
			if !configured && n != 0 {
				t.Errorf("the default statusline spawned %d processes", n)
			}
		})
	}
}

// R2d. Idle, the bar keeps refreshing on its own every refreshMs, and a
// finished turn refreshes it right away.
func TestStatusLineAcceptance_IdleRefreshCadenceAndTurnRefresh(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	log := filepath.Join(env.root, "runs.log")
	command := writeScript(t, env, "idle.sh", "cat >/dev/null\nprintf 'run\\n' >> '"+log+"'\nprintf 'SL-IDLE\\n'")
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 1000, timeoutMS: 8000})
	p := startSLPump(t, newSLChattyModel(t, env, 5), 120, 40)
	runs := &slRunLog{path: log}
	if !runs.pumpUntil(p, 1, 5*time.Second) {
		t.Fatal("the startup run never started")
	}
	runs.pumpFor(p, 3300*time.Millisecond)
	if len(runs.at) < 4 {
		t.Fatalf("idle for 3.3s with refreshMs 1s: want at least 4 runs, got %d", len(runs.at))
	}
	refresh := time.Second
	for i := 1; i < len(runs.at); i++ {
		if gap := runs.at[i].Sub(runs.at[i-1]); gap < refresh-100*time.Millisecond || gap > refresh+slRefreshSlack {
			t.Errorf("idle runs %d and %d came %v apart, want refreshMs (%v)", i-1, i, gap.Round(time.Millisecond), refresh)
		}
	}
	landed := slSubmitTurn(t, p, runs, 1)
	refreshed := func() (time.Time, bool) {
		for _, at := range runs.at {
			if at.After(landed.Add(-100 * time.Millisecond)) {
				return at, true
			}
		}
		return time.Time{}, false
	}
	p.waitFor(3*time.Second, func(string) bool { runs.poll(); _, ok := refreshed(); return ok })
	at, ok := refreshed()
	if !ok {
		t.Fatal("the finished turn refreshed nothing")
	}
	if late := at.Sub(landed); late > 600*time.Millisecond {
		t.Errorf("the turn refresh came %v after the turn landed, want right away", late.Round(time.Millisecond))
	}
}
