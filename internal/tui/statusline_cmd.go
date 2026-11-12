package tui

import (
	"context"
	"encoding/json"
	"math/big"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

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
type statuslineCmdContext struct {
	Used   int `json:"used"`
	Window int `json:"window,omitempty"`
	Pct    int `json:"pct,omitempty"`
}

// statuslineCmdPayload is the session snapshot piped to the external
// statusline command on stdin. It carries display facts only — never keys,
// tokens, or any credential. Cache stays nil until the Cofre item plugs in.
type statuslineCmdPayload struct {
	SessionID string                `json:"session_id"`
	Cwd       string                `json:"cwd"`
	Model     string                `json:"model"`
	Provider  string                `json:"provider"`
	Effort    string                `json:"effort"`
	Context   *statuslineCmdContext `json:"context,omitempty"`
	Cache     *statuslineCmdCache   `json:"cache,omitempty"`
	CostBRL   *float64              `json:"cost_brl,omitempty"`
	Branch    string                `json:"branch,omitempty"`
	Version   string                `json:"version"`
	Width     int                   `json:"width"`
}

// statuslineCmdCache is reserved for the Cofre item (prompt-cache hit rate);
// nil until it lands, so scripts see no fake zero.
type statuslineCmdCache struct {
	HitPct float64 `json:"hit_pct"`
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
		Branch:    strings.TrimSpace(m.gitBranch),
		Version:   strings.TrimSpace(m.appVersion),
		Width:     width,
	}
	if used, ok := m.contextMeterUse(); ok {
		ctx := &statuslineCmdContext{Used: used}
		if window := m.apiContextWindow(); window > 0 {
			ctx.Window = window
			ctx.Pct = contextMeterFill(used, window)
		}
		payload.Context = ctx
	}
	if total, ok := m.sessionCostBRLRat(); ok {
		asFloat, _ := new(big.Rat).Quo(total, big.NewRat(1, 1)).Float64()
		payload.CostBRL = &asFloat
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

// statuslineChildEnv builds the child's allowlist environment: locale and
// display basics in, every credential-like variable out. QG_* is dropped
// wholesale so a statusline script can never observe keys through the env.
func statuslineChildEnv(columns, lines int) []string {
	keep := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true,
		"TERM": true, "LANG": true, "LC_ALL": true, "LC_CTYPE": true,
		"TZ": true, "NO_COLOR": true, "CLICOLOR": true,
	}
	if runtime.GOOS == "windows" {
		keep["SystemRoot"] = true
		keep["TEMP"] = true
		keep["TMP"] = true
	} else {
		keep["TMPDIR"] = true
		keep["SHELL"] = true
	}
	var env []string
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "QG_") {
			continue
		}
		for _, banned := range []string{"KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL", "AUTH"} {
			if strings.Contains(upper, banned) {
				name = ""
				break
			}
		}
		if name == "" || !keep[name] {
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

// maxStatuslineCmdLines caps external command output: the footer stays a
// status line, not a transcript.
const maxStatuslineCmdLines = 3

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
		if len(lines) >= maxStatuslineCmdLines {
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

// stripControls drops C0 controls; tabs survive when keepTab. Newlines never
// reach here (lines are split first).
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
		if r < 0x20 || r == 0x7f {
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
	out, err := cmd.Output()
	text := strings.TrimRight(string(out), "\n")
	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		timeoutSecs := timeout.Truncate(time.Second)
		if timeoutSecs <= 0 {
			timeoutSecs = timeout
		}
		return statuslineCmdMsg{errNote: "statusline: comando estourou o tempo (" + timeoutSecs.String() + ")"}
	case err != nil:
		return statuslineCmdMsg{errNote: "statusline: comando falhou (" + shortCmdError(err) + ")"}
	default:
		return statuslineCmdMsg{lines: sanitizeStatuslineOutput(text)}
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
// starts a stale run.
type statuslineTickMsg struct{}

// statuslineTickCmd schedules one idle refresh tick.
func statuslineTickCmd(refreshMs int) tea.Cmd {
	return tea.Tick(time.Duration(refreshMs)*time.Millisecond,
		func(time.Time) tea.Msg { return statuslineTickMsg{} })
}

// statuslineInitCmd kicks the first refresh tick immediately at startup.
func statuslineInitCmd() tea.Cmd {
	return func() tea.Msg { return statuslineTickMsg{} }
}

// pollStatuslineCommand starts a command run when one is due after a state
// change (a finished call changed the context/cost numbers). It runs on the
// Update goroutine; the idle tick chain (see the statuslineTickMsg case in
// updateModel) covers quiet periods. It never runs on the default path.
func (m model) pollStatuslineCommand(msg tea.Msg) (model, tea.Cmd) {
	if !m.statuslineCommandActive() {
		return m, nil
	}
	if inner, ok := msg.(agentResponseMsg); ok && inner.runID == m.activeRunID {
		return m.maybeStartStatuslineRun()
	}
	return m, nil
}

// maybeStartStatuslineRun launches one run when the cache window elapsed and
// no run is in flight. At most one run is ever in flight: a second trigger
// while one runs is a no-op (no overlap, no queue).
func (m model) maybeStartStatuslineRun() (model, tea.Cmd) {
	if m.statuslineCmdInFlight {
		return m, nil
	}
	// The first run starts immediately; later runs wait out the refresh window.
	refresh := time.Duration(m.statuslineCfg.EffectiveRefreshMs()) * time.Millisecond
	if !m.statuslineCmdStartedAt.IsZero() && m.now().Sub(m.statuslineCmdStartedAt) < refresh {
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
	return m, func() tea.Msg {
		msg := execStatuslineRun(context.Background(), cfg.Command.Command, stdin, env, timeout)
		msg.seq = seq
		return msg
	}
}

// applyStatuslineCmdMsg folds a finished run: output replaces the cache and
// clears the error mark; a failure keeps the last good value and marks
// discreetly. Stale runs (a newer one started) are dropped.
func (m model) applyStatuslineCmdMsg(msg statuslineCmdMsg) model {
	if msg.seq != m.statuslineCmdSeq {
		return m
	}
	m.statuslineCmdInFlight = false
	if msg.errNote != "" {
		m.statuslineCmdErr = msg.errNote
		return m
	}
	m.statuslineCmdOutput = strings.Join(msg.lines, "\n")
	m.statuslineCmdErr = ""
	return m
}
