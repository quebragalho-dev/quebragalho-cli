package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// slInflightScript is a statusline command that logs one line per run. While
// <dir>/slow exists, a run parks in a background sleep (its pid saved in
// <dir>/child.<pid>) until something kills it; otherwise it lasts 0.3s. Each
// run first lists the other runs still alive (zombies excluded), so a run
// started on top of another records the overlap itself.
func slInflightScript(dir string) string {
	return `cat >/dev/null
d='` + dir + `'
: > "$d/run.$$"
others=''
for f in "$d"/run.*; do
	p=${f##*.}
	[ "$p" = "$$" ] && continue
	s=$(ps -o stat= -p "$p" 2>/dev/null | tr -d ' ')
	case "$s" in
	''|Z*) ;;
	*) others="$others $p" ;;
	esac
done
if [ -e "$d/slow" ]; then
	sleep 30 &
	echo "$!" > "$d/child.$$"
	printf 'start %s slow alive:%s\n' "$$" "$others" >> "$d/runs.log"
	wait
else
	printf 'start %s fast alive:%s\n' "$$" "$others" >> "$d/runs.log"
	sleep 0.3
fi
printf 'SL-INFLIGHT\n'`
}

type slInflightRun struct {
	pid    int
	slow   bool
	others string
	at     time.Time
}

func slInflightRuns(log *slRunLog) []slInflightRun {
	var runs []slInflightRun
	for i, line := range log.vals {
		head, others, _ := strings.Cut(line, "alive:")
		fields := strings.Fields(head)
		if len(fields) != 3 || fields[0] != "start" {
			continue
		}
		pid, _ := strconv.Atoi(fields[1])
		runs = append(runs, slInflightRun{pid: pid, slow: fields[2] == "slow", others: strings.TrimSpace(others), at: log.at[i]})
	}
	return runs
}

// slKillInflight kills every script run and parked sleep the test recorded,
// so a red run never leaves processes behind.
func slKillInflight(dir string) {
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		var pid int
		switch name := entry.Name(); {
		case strings.HasPrefix(name, "run."):
			pid, _ = strconv.Atoi(strings.TrimPrefix(name, "run."))
		case strings.HasPrefix(name, "child."):
			raw, _ := os.ReadFile(filepath.Join(dir, name))
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		}
		if statuslineTestProcessAlive(pid) {
			if proc, err := os.FindProcess(pid); err == nil {
				_ = proc.Kill()
			}
		}
	}
}

// Apply and reset while a statusline run is in flight: the old run (and the
// sleep it parked) is killed, no two runs of the script are ever alive at
// once, and the cadence of the config left in place takes over (a new
// refreshMs after apply, none after reset).
func TestStatusLineAcceptance_ApplyAndResetKillInFlightRun(t *testing.T) {
	requireSh(t)
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"apply", "/statusline duas linhas e refresh de 1s"},
		{"reset", "/statusline reset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newSLEnv(t)
			dir := filepath.Join(env.root, "inflight")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "slow"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { slKillInflight(dir) })
			command := writeScript(t, env, "inflight.sh", slInflightScript(dir))
			writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 5000, timeoutMS: 20000})
			var staged string
			provider := slProposer(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}, {left: []string{"branch"}}}, refreshMS: 1000, timeoutMS: 20000}, &staged)
			p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
			log := &slRunLog{path: filepath.Join(dir, "runs.log")}
			if !log.pumpUntil(p, 1, 5*time.Second) {
				t.Fatal("the first statusline run never started")
			}
			old := slInflightRuns(log)[0]
			child := statuslineTestWaitPID(t, filepath.Join(dir, "child."+strconv.Itoa(old.pid)))
			if !old.slow || child == 0 || !statuslineTestProcessAlive(old.pid) || !statuslineTestProcessAlive(child) {
				t.Fatalf("precondition: the first run must be parked in flight (run %+v, sleep pid %d)", old, child)
			}
			if err := os.Remove(filepath.Join(dir, "slow")); err != nil {
				t.Fatal(err)
			}

			p.submit(tc.input)
			if tc.name == "apply" {
				frame, ok := p.waitFor(8*time.Second, func(f string) bool {
					log.poll()
					return strings.Contains(plainFrame(f), "Prévia com os dados desta sessão")
				})
				if !ok {
					t.Fatalf("no preview (staged=%q):\n%s", staged, plainFrame(frame))
				}
				p.key(tea.KeyEnter)
			}
			done := time.Now()
			log.pumpFor(p, 4*time.Second)

			runs := slInflightRuns(log)
			for _, run := range runs {
				if run.others != "" {
					t.Errorf("%s left two script runs alive at once: run pid %d started while pid(s) %s still ran", tc.name, run.pid, run.others)
				}
			}
			if statuslineTestProcessAlive(old.pid) || statuslineTestProcessAlive(child) {
				t.Errorf("%s did not kill the in-flight run: script pid %d alive=%v, its sleep pid %d alive=%v, 4s later",
					tc.name, old.pid, statuslineTestProcessAlive(old.pid), child, statuslineTestProcessAlive(child))
			}
			var after []time.Duration
			for _, run := range runs[1:] {
				after = append(after, run.at.Sub(done).Round(10*time.Millisecond))
			}
			if tc.name == "reset" {
				if len(after) != 0 {
					t.Errorf("reset must stop the cadence; runs started at %v after it", after)
				}
				return
			}
			if len(after) < 3 || after[0] > time.Second+slRefreshSlack {
				t.Fatalf("the refreshMs 1s cadence never took over after apply: runs at %v", after)
			}
			for i := 1; i < len(after); i++ {
				if gap := after[i] - after[i-1]; gap > time.Second+slRefreshSlack {
					t.Errorf("runs after apply drifted off the 1s cadence: %v (gap %v)", after, gap)
					break
				}
			}
		})
	}
}
