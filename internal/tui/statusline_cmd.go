package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/execution"
	"github.com/quebragalho-dev/quebragalho-cli/internal/sessions"
)

// statuslineShellCommand builds the platform shell for an external statusline
// command: `sh -c` on Unix, `cmd /C` on Windows. A package var (not a
// parameter) so runner tests re-exec a helper instead of depending on a
// platform shell. It also applies stdin and env so stubs can extend the
// environment (production always passes the scrubbed allowlist).
var statuslineShellCommand = func(ctx context.Context, command, stdin string, env []string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = env
	return cmd
}

// statuslineCmdContext carries the context-window numbers for the stdin JSON.
// The object is always present; unknown numbers are null.
type statuslineCmdContext struct {
	Used   *int `json:"used"`
	Window *int `json:"window"`
	Pct    *int `json:"pct"`
}

// statuslineCmdPayload is the session snapshot piped to the external
// statusline command on stdin. Every key is always present — unknown values
// are null, never absent — and it carries display facts only: never keys,
// tokens, or any credential. hit_pct stays null until prompt-cache data
// exists (no fake zero).
type statuslineCmdPayload struct {
	SessionID string               `json:"session_id"`
	Cwd       string               `json:"cwd"`
	Model     string               `json:"model"`
	Provider  string               `json:"provider"`
	Effort    string               `json:"effort"`
	Context   statuslineCmdContext `json:"context"`
	Cache     statuslineCmdCache   `json:"cache"`
	CostBRL   *float64             `json:"cost_brl"`
	Branch    *string              `json:"branch"`
	Version   string               `json:"version"`
	Width     int                  `json:"width"`
}

// statuslineCmdCache carries the prompt-cache hit rate for the stdin JSON:
// the object is always present, hit_pct stays null until measured.
type statuslineCmdCache struct {
	HitPct *float64 `json:"hit_pct"`
}

// statuslineStdin builds the JSON snapshot for one command run.
func (m model) statuslineStdin(width int) []byte {
	effort := strings.TrimSpace(string(m.reasoningEffort))
	if effort == "" {
		effort = "auto"
	}
	payload := statuslineCmdPayload{
		SessionID: strings.TrimSpace(m.activeSession.SessionID),
		Cwd:       strings.TrimSpace(m.cwd),
		Model:     strings.TrimSpace(m.modelName),
		Provider:  strings.TrimSpace(m.providerName),
		Effort:    effort,
		Version:   strings.TrimSpace(m.appVersion),
		Width:     width,
	}
	if branch := strings.TrimSpace(m.gitBranch); branch != "" {
		payload.Branch = &branch
	}
	if used, ok := m.contextMeterUse(); ok {
		payload.Context.Used = &used
		if window := m.apiContextWindow(); window > 0 {
			payload.Context.Window = &window
			pct := contextMeterFill(used, window)
			payload.Context.Pct = &pct
		}
	}
	if total, ok := m.sessionCostBRLRat(); ok {
		asFloat, _ := new(big.Rat).Quo(total, big.NewRat(1, 1)).Float64()
		payload.CostBRL = &asFloat
	}
	// The session prompt-cache hit on the 0-100 scale of context.pct,
	// rendered as the same integer the footer shows; null until the first
	// call lands in the journal (never a fake zero).
	if data := m.cacheStatusData(); data.hasData {
		hitPct := float64(cacheHitPercent(data.hit))
		payload.Cache.HitPct = &hitPct
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{}`)
	}
	return encoded
}

// sessionCostBRLRat is the numeric core behind sessionCostBRL: the summed BRL
// call costs, false when nothing priced in BRL exists yet.
func (m model) sessionCostBRLRat() (*big.Rat, bool) {
	total := new(big.Rat)
	found := false
	for _, event := range m.sessionEvents {
		if event.Type != sessions.EventUsage {
			continue
		}
		var call struct {
			Amount   string `json:"cost_exact"`
			Currency string `json:"cost_currency"`
		}
		if json.Unmarshal(event.Payload, &call) != nil || call.Currency != "BRL" {
			continue
		}
		amount, ok := new(big.Rat).SetString(strings.TrimSpace(call.Amount))
		if !ok || amount.Sign() < 0 {
			continue
		}
		total.Add(total, amount)
		found = true
	}
	if !found {
		return nil, false
	}
	return total, true
}

// statuslineChildEnv builds the child's allowlist environment. The allowlist
// is the whole policy: only these names pass, everything else (including any
// credential-like variable) stays out.
func statuslineChildEnv(columns, lines int) []string {
	return statuslineChildEnvFor(runtime.GOOS, os.Environ(), columns, lines)
}

// statuslineChildEnvFor is the pure, testable core: the allowlist match is
// exact on Unix and case-insensitive on Windows (where "Path" carries the
// canonical casing). environ is KEY=VALUE lines, as from os.Environ.
func statuslineChildEnvFor(goos string, environ []string, columns, lines int) []string {
	keep := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"TERM": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true,
		"TZ": true, "NO_COLOR": true, "CLICOLOR": true,
	}
	if goos == "windows" {
		keep["SystemRoot"] = true
		keep["TEMP"] = true
		keep["TMP"] = true
	} else {
		keep["TMPDIR"] = true
		keep["SHELL"] = true
	}
	var env []string
	for _, kv := range environ {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		matched := keep[name]
		if !matched && goos == "windows" {
			for allowed := range keep {
				if strings.EqualFold(name, allowed) {
					matched = true
					break
				}
			}
		}
		if !matched {
			continue
		}
		env = append(env, kv)
	}
	if columns > 0 {
		env = append(env, "COLUMNS="+strconv.Itoa(columns))
	}
	if lines > 0 {
		env = append(env, "LINES="+strconv.Itoa(lines))
	}
	return env
}

// sanitizeStatuslineOutput keeps displayable text: SGR colors survive, every
// other escape (OSC links, cursor moves, charset shifts) and C0 control byte
// is removed. Returns at most three lines capped in memory; the render path
// re-truncates to the live width so resizes re-cut the cached lines.
func sanitizeStatuslineOutput(output string) []string {
	normalized := strings.ReplaceAll(output, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	raw := strings.Split(strings.TrimRight(normalized, "\n"), "\n")
	var lines []string
	for _, line := range raw {
		if len(lines) >= maxStatusLineRenderLines {
			break
		}
		lines = append(lines, fitStyledLine(stripNonSGREscapes(line), statuslineSanitizeWidth))
	}
	return lines
}

// stripNonSGREscapes removes every escape sequence except SGR colors
// (`ESC [ params m`) plus stray C0 controls (keeping tab, which terminals
// render). SGR survives so scripts can color their segments like CC's.
func stripNonSGREscapes(line string) string {
	var out strings.Builder
	rest := line
	for len(rest) > 0 {
		i := strings.IndexByte(rest, '\x1b')
		if i < 0 {
			out.WriteString(stripControls(rest, true))
			break
		}
		out.WriteString(stripControls(rest[:i], true))
		seq, advance := splitEscape(rest[i:])
		if isSGREscape(seq) {
			out.WriteString(seq)
		}
		rest = rest[i+advance:]
	}
	return out.String()
}

// splitEscape splits one escape sequence (ESC plus its terminator) from the
// front of s, returning the sequence and its byte length. Unknown forms are
// consumed conservatively to their final byte so nothing leaks through.
func splitEscape(s string) (string, int) {
	if len(s) < 2 || s[0] != '\x1b' {
		return "", 1
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			if (s[i] >= '@' && s[i] <= 'Z') || (s[i] >= 'a' && s[i] <= 'z') {
				return s[:i+1], i + 1
			}
		}
		return s, len(s)
	case ']', 'P', 'X', '^', '_':
		for i := 2; i < len(s); i++ {
			if s[i] == '\a' {
				return s[:i+1], i + 1
			}
			if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
				return s[:i+2], i + 2
			}
		}
		return s, len(s)
	case '(', ')', '#':
		if len(s) >= 3 {
			return s[:3], 3
		}
		return s, len(s)
	default:
		return s[:2], 2
	}
}

// isSGREscape reports whether seq is a color/style SGR sequence.
func isSGREscape(seq string) bool {
	if len(seq) < 3 || seq[0] != '\x1b' || seq[1] != '[' || seq[len(seq)-1] != 'm' {
		return false
	}
	for _, c := range seq[2 : len(seq)-1] {
		if (c < '0' || c > '9') && c != ';' {
			return false
		}
	}
	return true
}

// stripControls drops C0 controls, DEL, and the C1 block (U+0080–U+009F:
// single-byte CSI and friends must not survive as live escapes); tabs
// survive when keepTab. Newlines never reach here (lines are split first).
func stripControls(s string, keepTab bool) string {
	var out strings.Builder
	for _, r := range s {
		if r == '\n' {
			continue
		}
		if r == '\t' && keepTab {
			out.WriteRune(r)
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// statuslineCmdMsg carries one finished external command run back to the
// model. Empty lines with an empty note means "no output"; a note means the
// run failed and the last good value stays on screen.
type statuslineCmdMsg struct {
	seq     int
	lines   []string
	errNote string
}

// execStatuslineRun runs one external statusline command synchronously with a
// timeout kill: the caller must already be off the UI goroutine (tea.Cmd).
func execStatuslineRun(ctx context.Context, command, stdin string, env []string, timeout time.Duration) statuslineCmdMsg {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := statuslineShellCommand(runCtx, command, stdin, env)
	statuslineHardenCmd(cmd)
	// The pipe and its copier belong to os/exec: on Cancel it closes the
	// parent ends after WaitDelay even when an escaped grandchild (own group)
	// still holds them open. A self-owned io.Copy here would never reach
	// Wait and would stall past the timeout instead.
	kept := &cappedStatuslineBuffer{cap: maxStatuslineCmdOutputBytes}
	cmd.Stdout = kept
	// Stderr carries no statusline content; dropping it (instead of the
	// default capture buffer) keeps a chatty script from growing memory.
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return statuslineCmdMsg{errNote: "statusline: comando falhou (erro de execução)"}
	}
	waitErr := cmd.Wait()
	text := strings.TrimRight(string(kept.bytes), "\n")
	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		timeoutLabel := timeout.Truncate(time.Second)
		if timeoutLabel <= 0 {
			timeoutLabel = timeout
		}
		return statuslineCmdMsg{errNote: "statusline: comando estourou o tempo (" + timeoutLabel.String() + ")"}
	case waitErr != nil:
		// A failed Wait can leave backgrounded children behind: a nonzero
		// exit returns an *ExitError with the group still alive, and an
		// orphaned grandchild only unblocks Wait via WaitDelay. Kill the
		// group on every error return so no orphan survives per refresh;
		// the output already received is still good, so keep it.
		if cmd.Process != nil {
			_ = execution.KillProcessGroup(cmd.Process.Pid)
		}
		if errors.Is(waitErr, exec.ErrWaitDelay) && text != "" {
			return statuslineCmdMsg{lines: sanitizeStatuslineOutput(text)}
		}
		return statuslineCmdMsg{errNote: "statusline: comando falhou (" + shortCmdError(waitErr) + ")"}
	default:
		return statuslineCmdMsg{lines: sanitizeStatuslineOutput(text)}
	}
}

// statuslineTreeKillGrace bounds how long Wait blocks for the I/O pipes to
// drain after the tree kill. Var (not const) so tests can shorten it.
var statuslineTreeKillGrace = 500 * time.Millisecond

// maxStatuslineCmdOutputBytes caps one command's stdout in memory; the excess
// is discarded, never rendered.
const maxStatuslineCmdOutputBytes = 64 * 1024

// cappedStatuslineBuffer keeps the head of a stream up to cap, discarding the
// rest while still draining (so a finite flood exits instead of blocking on a
// full pipe).
type cappedStatuslineBuffer struct {
	bytes []byte
	cap   int
}

func (w *cappedStatuslineBuffer) Write(p []byte) (int, error) {
	if len(w.bytes) < w.cap {
		w.bytes = append(w.bytes, p[:min(len(p), w.cap-len(w.bytes))]...)
	}
	return len(p), nil
}

// statuslineHardenCmd makes one statusline run killable as a single unit, so
// a timeout cannot leak backgrounded children: the child leads its own
// process group (Unix) and Cancel signals the whole tree (taskkill /T on
// Windows), mirroring tools.hardenProcessLifetime. WaitDelay is the backstop
// because the pipe and its copier belong to os/exec (cmd.Stdout above): after
// Cancel it closes the parent ends after the grace period even when an
// escaped grandchild in its own group still holds them open.
func statuslineHardenCmd(cmd *exec.Cmd) {
	execution.ConfigureProcessGroup(cmd)
	cmd.WaitDelay = statuslineTreeKillGrace
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Launch-time invariant (ConfigureProcessGroup above), not
		// rediscovery: the leader may already have exited while a
		// grandchild holds the pipes, and rediscovery then misses the
		// group it configured.
		return execution.KillProcessGroup(cmd.Process.Pid)
	}
}

// statuslineSanitizeWidth bounds cached command lines in memory; the render
// path re-truncates to the live width.
const statuslineSanitizeWidth = 1024

// shortCmdError keeps the failure mark discreet: exit status only, never the
// command text or output (which could echo back session facts).
func shortCmdError(err error) string {
	if exit, ok := err.(*exec.ExitError); ok {
		return "saída " + strconv.Itoa(exit.ExitCode())
	}
	return "erro de execução"
}

// statuslineCommandActive reports whether an external command run is
// configured at all.
func (m model) statuslineCommandActive() bool {
	return m.statuslineCfg.IsCustom() &&
		m.statuslineCfg.Command != nil &&
		strings.TrimSpace(m.statuslineCfg.Command.Command) != ""
}

// statuslineTickMsg re-arms the idle refresh chain and opportunistically
// starts a stale run. gen is the chain generation: apply/reset bumps it, so
// a tick armed under the old cadence is discarded instead of delaying the
// new one.
type statuslineTickMsg struct{ gen int }

// statuslineTickCmdAfter schedules one idle refresh tick after d, so the
// chain can re-arm with the time left since the last run started instead of
// its own phase.
func statuslineTickCmdAfter(d time.Duration, gen int) tea.Cmd {
	return tea.Tick(d,
		func(time.Time) tea.Msg { return statuslineTickMsg{gen: gen} })
}

// statuslineInitCmd kicks the first refresh tick immediately at startup.
func statuslineInitCmd(gen int) tea.Cmd {
	return func() tea.Msg { return statuslineTickMsg{gen: gen} }
}

// pollStatuslineCommand starts a command run when one is due after a state
// change (a finished call changed the context/cost numbers). It runs on the
// Update goroutine; the idle tick chain (see the statuslineTickMsg case in
// updateModel) covers quiet periods. It never runs on the default path.
func (m model) pollStatuslineCommand(msg tea.Msg) (model, tea.Cmd) {
	if !m.statuslineCommandActive() {
		return m, nil
	}
	if inner, ok := msg.(agentResponseMsg); ok && inner.runID == m.runID {
		// A finished turn refreshes the command right away, skipping the
		// refresh window: the model just changed the context/cost numbers.
		// updateModel already zeroed activeRunID, so match the last-started
		// run instead. At most one run stays in flight.
		return m.startStatuslineRun(true)
	}
	return m, nil
}

// startStatuslineRun starts one command run: immediately when forced (a
// finished turn just changed the numbers), otherwise once the refresh window
// elapsed since the last start. At most one run is ever in flight: a forced
// trigger during a flight only sets statuslineRefreshDue, consumed when the
// in-flight run completes; anything else stays a no-op.
func (m model) startStatuslineRun(force bool) (model, tea.Cmd) {
	if m.statuslineCmdInFlight {
		if force {
			m.statuslineRefreshDue = true
		}
		return m, nil
	}
	// The first run starts immediately; later runs wait out the refresh window
	// unless a finished turn forces one now.
	refresh := time.Duration(m.statuslineCfg.EffectiveRefreshMs()) * time.Millisecond
	if !force && !m.statuslineCmdStartedAt.IsZero() && m.now().Sub(m.statuslineCmdStartedAt) < refresh {
		return m, nil
	}
	m.statuslineCmdInFlight = true
	m.statuslineCmdStartedAt = m.now()
	m.statuslineCmdSeq++
	seq := m.statuslineCmdSeq
	cfg := m.statuslineCfg
	stdin := string(m.statuslineStdin(m.width))
	env := statuslineChildEnv(m.width, m.height)
	timeout := time.Duration(cfg.EffectiveTimeoutMs()) * time.Millisecond
	// Cancellable by apply/reset/quit: the cancel fires the same tree kill
	// the timeout relies on, so the old process never overlaps the new
	// cadence. The exit path also waits for the runner (bounded) so os.Exit
	// right after Run cannot slip ahead of the kill.
	runCtx, cancel := context.WithCancel(context.Background())
	m.statuslineCmdCancel = cancel
	done := make(chan struct{})
	m.statuslineCmdDone = done
	return m, func() tea.Msg {
		defer close(done)
		msg := execStatuslineRun(runCtx, cfg.Command.Command, stdin, env, timeout)
		msg.seq = seq
		return msg
	}
}

// cancelStatuslineRun kills a run still in flight (apply/reset switch the
// cadence underneath it): the context cancel fires the same tree kill the
// timeout relies on, so no orphan survives. The late result is dropped by
// the sequence check, and a refresh queued during the flight is dropped too,
// so the new cadence starts clean instead of firing one stale extra run.
func (m model) cancelStatuslineRun() model {
	if m.statuslineCmdCancel != nil {
		m.statuslineCmdCancel()
		m.statuslineCmdCancel = nil
	}
	m.statuslineCmdInFlight = false
	m.statuslineRefreshDue = false
	return m
}

// statuslineExitWait caps the synchronous kill on process exit. The runner
// usually returns in milliseconds once cancelled; the cap only backstops a
// stuck pipe drain so the exit never hangs.
const statuslineExitWait = 2 * time.Second

// killStatuslineSync cancels an in-flight run and waits (bounded) for the
// runner to return, so a process exit right after Run cannot orphan the
// script: the tree kill is issued before Wait unblocks, and its SIGKILL
// cannot be caught. No-op when nothing ever ran.
func (m model) killStatuslineSync() {
	m = m.cancelStatuslineRun()
	done := m.statuslineCmdDone
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(statuslineExitWait):
	}
}

// applyStatuslineCmdMsg folds a finished run: output replaces the cache and
// clears the error mark; a failure keeps the last good value and marks
// discreetly. Stale runs (a newer one started) are dropped. A turn-end
// refresh queued during the flight runs now.
func (m model) applyStatuslineCmdMsg(msg statuslineCmdMsg) (model, tea.Cmd) {
	if msg.seq != m.statuslineCmdSeq {
		return m, nil
	}
	m.statuslineCmdInFlight = false
	m.statuslineCmdCancel = nil
	if msg.errNote != "" {
		m.statuslineCmdErr = msg.errNote
	} else {
		m.statuslineCmdOutput = strings.Join(msg.lines, "\n")
		m.statuslineCmdErr = ""
	}
	if m.statuslineRefreshDue && m.statuslineCommandActive() {
		m.statuslineRefreshDue = false
		return m.startStatuslineRun(true)
	}
	return m, nil
}
