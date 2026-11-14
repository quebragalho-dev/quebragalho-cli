package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// slPumpTicks pumps for d (or until done reports true) and counts the idle
// statusline ticks delivered meanwhile.
func slPumpTicks(p *slPump, d time.Duration, done func() bool) int {
	ticks := 0
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if done != nil && done() {
			break
		}
		select {
		case msg := <-p.msgs:
			if _, ok := msg.(statuslineTickMsg); ok {
				ticks++
			}
			p.deliver(msg)
		case <-time.After(20 * time.Millisecond):
		}
	}
	return ticks
}

// A run longer than refreshMs must not drive a tick storm: the re-arm delay
// never drops to zero or below while the run is in flight.
func TestStatusLineAcceptance_SlowScriptKeepsTicksBounded(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	command := writeScript(t, env, "lento.sh", "cat >/dev/null\nsleep 1.5\nprintf 'SL-LENTO\\n'")
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 1000, timeoutMS: 3000})
	p := startSLPump(t, newSLModel(t, env, nil), 120, 40)
	if ticks := slPumpTicks(p, 4*time.Second, nil); ticks > 8 {
		t.Errorf("a 1.5s script with refreshMs 1s drove %d idle ticks in 4s; want a bounded chain, not a storm", ticks)
	}
}

// Finished turns never arm extra tick chains: the idle tick rate after many
// turns matches the rate before them.
func TestStatusLineAcceptance_TurnsDoNotMultiplyTickChains(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	command := writeScript(t, env, "rapido.sh", "cat >/dev/null\nprintf 'SL-RAPIDO\\n'")
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 1000, timeoutMS: 8000})
	p := startSLPump(t, newSLChattyModel(t, env, 3), 120, 40)
	slPumpTicks(p, time.Second, nil)
	before := slPumpTicks(p, 3*time.Second, nil)
	for n := 1; n <= 30; n++ {
		p.submit(fmt.Sprintf("turno %d", n))
		marker := fmt.Sprintf("FIM-DO-TURNO-%d", n)
		slPumpTicks(p, 8*time.Second, func() bool {
			current, ok := p.m.(model)
			return ok && !current.pending && strings.Contains(plainFrame(p.view()), marker)
		})
	}
	if after := slPumpTicks(p, 3*time.Second, nil); after > before+2 {
		t.Errorf("idle ticks in 3s went from %d to %d after 30 turns; turns must not arm extra tick chains", before, after)
	}
}

// The idle re-arm reads the injected clock: 300ms after the last run start
// on m.now, the next tick comes 700ms later, not a full refreshMs.
func TestStatuslineTickRearmUsesInjectedClock(t *testing.T) {
	m := limeTestModel()
	m.width, m.height = 100, 30
	m.statuslineCfg = config.StatusLineConfig{
		Lines:     []config.StatusLineLine{{Left: []string{"mode"}}},
		Command:   &config.StatusLineCommand{Command: "echo hi"},
		RefreshMs: 1000,
	}
	fake := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return fake }
	m.statuslineCmdStartedAt = fake.Add(-300 * time.Millisecond)
	_, cmd := m.Update(statuslineTickMsg{})
	start := time.Now()
	msg := execCmd(cmd)
	took := time.Since(start)
	if _, ok := msg.(statuslineTickMsg); !ok {
		t.Fatalf("tick re-armed nothing: got %T", msg)
	}
	if took < 600*time.Millisecond || took > 900*time.Millisecond {
		t.Errorf("next tick after %v; the injected clock leaves 700ms of the 1s window", took.Round(10*time.Millisecond))
	}
}

// A tick armed before a turn-forced run is not discarded and starts no
// extra run: the forced run already refreshed, so the stale tick only
// re-arms the chain for the time left in refreshMs since the forced run
// started — and the next tick then runs on that phase.
func TestStatuslineStaleTickRearmsFromForcedRun(t *testing.T) {
	m := limeTestModel()
	m.width, m.height = 100, 30
	m.statuslineCfg = config.StatusLineConfig{
		Lines:     []config.StatusLineLine{{Left: []string{"mode"}}},
		Command:   &config.StatusLineCommand{Command: "echo hi"},
		RefreshMs: 1000,
	}
	fake := time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return fake }
	gen := m.statuslineTickGen

	// Startup tick starts run 1.
	started, _ := m.Update(statuslineTickMsg{gen: gen})
	m = started.(model)
	if !m.statuslineCmdInFlight || m.statuslineCmdSeq != 1 {
		t.Fatalf("startup tick started no run: inFlight=%v seq=%d", m.statuslineCmdInFlight, m.statuslineCmdSeq)
	}
	// Turn lands 250ms later and forces run 2 right away.
	m.now = func() time.Time { return fake.Add(250 * time.Millisecond) }
	completed, _ := m.applyStatuslineCmdMsg(statuslineCmdMsg{seq: 1})
	m = completed
	if m.statuslineCmdInFlight {
		t.Fatal("run 1 never completed")
	}
	forced, _ := m.startStatuslineRun(true)
	m = forced
	if !m.statuslineCmdInFlight || m.statuslineCmdSeq != 2 {
		t.Fatalf("turn forced no run: inFlight=%v seq=%d", m.statuslineCmdInFlight, m.statuslineCmdSeq)
	}
	done, _ := m.applyStatuslineCmdMsg(statuslineCmdMsg{seq: 2})
	m = done

	// The tick armed before the forced run arrives at t0+1000. It must start
	// no run (750ms < refreshMs since the forced start) and re-arm for the
	// remaining 250ms — not a full refreshMs, never an early run.
	m.now = func() time.Time { return fake.Add(1000 * time.Millisecond) }
	staled, staleCmd := m.Update(statuslineTickMsg{gen: gen})
	m = staled.(model)
	if m.statuslineCmdInFlight || m.statuslineCmdSeq != 2 {
		t.Fatalf("stale tick started an early run: inFlight=%v seq=%d", m.statuslineCmdInFlight, m.statuslineCmdSeq)
	}
	if staleCmd == nil {
		t.Fatal("stale tick ended the idle chain instead of re-arming it")
	}
	start := time.Now()
	msg, ok := execCmd(staleCmd).(statuslineTickMsg)
	took := time.Since(start)
	if !ok {
		t.Fatalf("stale tick re-armed nothing: got %T", msg)
	}
	if msg.gen != gen {
		t.Fatalf("re-armed tick gen = %d, want the live generation %d", msg.gen, gen)
	}
	if took < 150*time.Millisecond || took > 800*time.Millisecond {
		t.Errorf("stale tick re-armed after %v, want the ~250ms left in refreshMs since the forced run", took.Round(10*time.Millisecond))
	}

	// The re-armed tick fires on the forced phase and runs.
	m.now = func() time.Time { return fake.Add(1250 * time.Millisecond) }
	ticked, _ := m.Update(msg)
	if !ticked.(model).statuslineCmdInFlight {
		t.Fatal("re-armed tick started no run on the forced phase")
	}
}

// A1 in /statusline: a three-line fields-only proposal keeps the live
// command, so the footer after Enter gives the last field line to the
// script. The preview must not promise that line silently: it either shows
// the script line or warns that the field line is hidden.
func TestStatusLineAcceptance_FieldsOnlyPreviewMatchesFooterWithLiveCommand(t *testing.T) {
	requireSh(t)
	env := newSLEnv(t)
	command := writeScript(t, env, "vivo.sh", "printf 'SL-VIVO\\n'")
	writeUserStatusLine(t, env, slLayout{lines: []slLine{{left: []string{"mode"}}}, command: command, refreshMS: 1000, timeoutMS: 8000})
	var staged string
	provider := slProposer(t, env, slLayout{lines: slThreeFieldLines(), refreshMS: 1000, timeoutMS: 8000}, &staged)
	p := startSLPump(t, newSLModel(t, env, provider), 120, 40)
	if frame, ok := p.waitFor(5*time.Second, func(f string) bool { return strings.Contains(slFooter(plainFrame(f)), "SL-VIVO") }); !ok {
		t.Fatalf("the live command never reached the footer:\n%s", slFooter(plainFrame(frame)))
	}
	p.submit("/statusline três linhas de campos")
	frame, ok := p.waitFor(8*time.Second, containsPlain("Prévia com os dados desta sessão"))
	if !ok {
		t.Fatalf("no preview (staging=%q):\n%s", staged, plainFrame(frame))
	}
	preview := plainFrame(frame)
	preview = preview[strings.Index(preview, "Prévia com os dados desta sessão"):]
	if strings.Contains(preview, "~/workspace") && !strings.Contains(preview, "ocult") {
		t.Errorf("the preview shows the third field line, which the footer hides for the live script, and does not warn:\n%s", preview)
	}
	p.key(tea.KeyEnter)
	after, _ := p.waitFor(4*time.Second, func(f string) bool { return strings.Contains(slFooter(plainFrame(f)), "SL-VIVO") })
	if footer := slFooter(plainFrame(after)); strings.Contains(footer, "~/workspace") {
		t.Errorf("footer after Enter kept the third field line over the script:\n%s", footer)
	}
}
