//go:build darwin

package sandbox

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// sandboxLogRingSize caps how many distinct denial lines a monitor retains, so a
// chatty run cannot grow memory without bound.
const sandboxLogRingSize = 64

// sandboxLogNoise lists daemons whose denials are background chatter, not caused
// by the agent's command; they are filtered out of the reported blocks.
var sandboxLogNoise = []string{
	"mDNSResponder",
	"com.apple.diagnosticd",
	"com.apple.analyticsd",
}

// denialMonitorConnectTimeout bounds the wait for the `log stream` feed to
// attach. The stream prints its header once the predicate is live; a command
// started after the header cannot lose denials to attach lag. Past the limit
// the monitor degrades to a no-op rather than trapping the command.
const denialMonitorConnectTimeout = 300 * time.Millisecond

// denialMonitorWarmupDelay gives the first `log stream` in a fresh process
// time beyond its header for the predicate to catch kernel sandbox reports.
// The header proves the stream attached, but a cold predicate still drops the
// first command's tag (cold miss): the network denial runs, fails with a raw
// DNS/EPERM error, and no permission prompt appears. A short sleep on the
// first monitor only (subsequent streams in the same process are already
// warm) fixes it without slowing every command.
const denialMonitorWarmupDelay = 200 * time.Millisecond

var denialMonitorWarmup sync.Once

// denialMonitorTagGrace bounds the wait for a late tag line after a failed
// command. It runs only on failure; successes never wait. AwaitTag blocks on
// a channel closed by markTagSeen with a timer for this grace, so a miss
// waits the full grace but never beyond it (no polling overshoot). A hit
// during the command returns immediately, so the grace only costs failing
// commands whose tag lands late (cold stream, loaded host). 200 ms covers the
// DNS-path (mDNSResponder) denial that missed 25 ms under TUI load; warm TCP
// hits still return in microseconds.
//
// A var rather than a const so timing tests can widen it under load (with
// margins that still kill the interesting mutants) and restore it after;
// production code never assigns it.
var denialMonitorTagGrace = 200 * time.Millisecond

// DenialMonitor tails `log stream` for seatbelt denials carrying a unique tag and
// collects them so a caller can report what the sandbox blocked. It is
// best-effort: if `log` cannot start, or the stream does not attach in time,
// it yields nothing and the command is unaffected.
type DenialMonitor struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
	// tagCh closes when markTagSeen records a tag hit, so AwaitTag wakes the
	// moment the tag lands instead of polling for it.
	tagCh chan struct{}

	mu        sync.Mutex
	blocks    []string
	tagSeen   bool
	connected bool
}

// StartDenialMonitor begins streaming sandbox denials tagged with tag: every
// Sandbox violation plus the tagged message lines, paired positionally. An
// empty tag (monitoring disabled) yields a no-op monitor. This is the
// explicit opt-in feed (Policy.MonitorDenials); production auto-monitoring
// uses StartNetworkDenialMonitor.
func StartDenialMonitor(ctx context.Context, tag string) *DenialMonitor {
	if strings.TrimSpace(tag) == "" {
		monitor := &DenialMonitor{done: make(chan struct{}), tagCh: make(chan struct{})}
		close(monitor.done)
		return monitor
	}
	attributor := newDenialAttributor(tag)
	monitor := startMonitor(ctx, tag,
		`(eventMessage CONTAINS "`+tag+`-net" OR eventMessage CONTAINS "`+tag+` " OR eventMessage ENDSWITH "`+tag+`") OR (eventMessage CONTAINS "Sandbox:" AND eventMessage CONTAINS "deny")`,
		func(monitor *DenialMonitor, line string) {
			if isLogStreamHeader(line) {
				return
			}
			if containsTagToken(line, tag) {
				monitor.markTagSeen()
			}
			if msg, ok := attributor.observe(line); ok {
				monitor.record(msg)
			}
		})
	return monitor
}

// StartNetworkDenialMonitor begins streaming only the network-only tag lines
// of one run (see sandboxNetworkDenialTag). A hit proves a network denial of
// this exact command, so no violation pairing is needed and the predicate
// stays off the system-wide Sandbox firehose. An empty tag yields a no-op.
func StartNetworkDenialMonitor(ctx context.Context, tag string) *DenialMonitor {
	if strings.TrimSpace(tag) == "" {
		monitor := &DenialMonitor{done: make(chan struct{}), tagCh: make(chan struct{})}
		close(monitor.done)
		return monitor
	}
	netTag := sandboxNetworkDenialTag(tag)
	monitor := startMonitor(ctx, tag,
		`eventMessage CONTAINS "`+netTag+`"`,
		func(monitor *DenialMonitor, line string) {
			if block, ok := narrowObserve(line, netTag); ok {
				monitor.markTagSeen()
				monitor.record(block)
			}
		})
	return monitor
}

// narrowObserve classifies one stream line for the network-only tag. It
// returns ("", false) for the stream header and for lines without the tag; a
// tag hit records a fixed network block, since the tag line itself proves the
// denial and the violation text never rides it.
func narrowObserve(line, netTag string) (string, bool) {
	if isLogStreamHeader(line) {
		return "", false
	}
	if netTag == "" || !containsTagToken(line, netTag) {
		return "", false
	}
	return "Sandbox: deny network (network access blocked by sandbox)", true
}

// isTagChar reports bytes that continue a denial tag token. Tags are
// alphanumerics plus '-'/'_' ("qg-sandbox-denied-v1-<pid>-<seq>" and the
// "-net" network form), so any of those right after a tag means a longer tag.
func isTagChar(c byte) bool {
	return c == '-' || c == '_' ||
		('0' <= c && c <= '9') ||
		('a' <= c && c <= 'z') ||
		('A' <= c && c <= 'Z')
}

// containsTagToken reports whether tag occurs in line as a whole token: at a
// token boundary, followed by end-of-line, a non-tag byte, or the "-net"
// network suffix (itself boundary-terminated) that carries a run's base tag on
// a network-denial line. A bare prefix of a longer tag ("...-1" inside
// "...-10-net") is not a hit, so concurrent runs with adjacent sequence
// numbers cannot ingest each other's denials.
func containsTagToken(line, tag string) bool {
	if tag == "" {
		return false
	}
	for start := 0; start+len(tag) <= len(line); {
		idx := strings.Index(line[start:], tag)
		if idx < 0 {
			return false
		}
		pos := start + idx
		end := pos + len(tag)
		if pos > 0 && isTagChar(line[pos-1]) {
			start = pos + 1
			continue
		}
		if end == len(line) {
			return true
		}
		if rest := line[end:]; strings.HasPrefix(rest, "-net") {
			if after := end + len("-net"); after == len(line) || !isTagChar(line[after]) {
				return true
			}
		} else if !isTagChar(line[end]) {
			return true
		}
		start = pos + 1
	}
	return false
}

// isLogStreamHeader reports the `log stream` startup line. It carries the
// predicate text, so it trivially contains every tag without proving any
// denial.
func isLogStreamHeader(line string) bool {
	return strings.Contains(line, "Filtering the log data using")
}

// startMonitor spawns `log stream` and waits for its first output line (the
// header, printed once the predicate is live) before releasing the caller to
// run the command. Past the timeout it degrades to a disconnected monitor
// rather than delaying the command.
func startMonitor(ctx context.Context, tag, predicate string, handle func(*DenialMonitor, string)) *DenialMonitor {
	monitor := &DenialMonitor{done: make(chan struct{}), tagCh: make(chan struct{})}
	streamCtx, cancel := context.WithCancel(ctx)
	monitor.cancel = cancel
	monitor.cmd = exec.CommandContext(streamCtx, "log", "stream", "--style", "compact",
		"--predicate", predicate)
	stdout, err := monitor.cmd.StdoutPipe()
	if err != nil {
		monitor.shutdown()
		return monitor
	}
	if err := monitor.cmd.Start(); err != nil {
		monitor.shutdown()
		return monitor
	}
	reader := bufio.NewReader(stdout)
	firstCh := make(chan firstLineResult, 1)
	go func() {
		line, err := reader.ReadString('\n')
		firstCh <- firstLineResult{line: line, err: err}
	}()
	select {
	case first := <-firstCh:
		if first.err != nil {
			monitor.shutdown()
			return monitor
		}
		monitor.connected = true
		handle(monitor, first.line)
		go monitor.scan(reader, handle)
		denialMonitorWarmup.Do(func() { time.Sleep(denialMonitorWarmupDelay) })
		return monitor
	case <-time.After(denialMonitorConnectTimeout):
		monitor.shutdown()
		return monitor
	}
}

type firstLineResult struct {
	line string
	err  error
}

func (monitor *DenialMonitor) scan(reader *bufio.Reader, handle func(*DenialMonitor, string)) {
	defer close(monitor.done)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		handle(monitor, scanner.Text())
	}
	// Reap the `log stream` child once stdout is drained; without Wait the
	// cancelled process lingers as a zombie across repeated runs. Safe here —
	// all reads from the pipe are complete.
	_ = monitor.cmd.Wait()
}

// shutdown cancels the stream, reaps it, and releases Stop. Only for paths
// where no scan goroutine owns the Wait.
func (monitor *DenialMonitor) shutdown() {
	if monitor.cancel != nil {
		monitor.cancel()
	}
	if monitor.cmd != nil && monitor.cmd.Process != nil {
		_ = monitor.cmd.Wait()
	}
	close(monitor.done)
}

func (monitor *DenialMonitor) record(msg string) {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	for _, existing := range monitor.blocks {
		if existing == msg {
			return // dedupe repeats of the same denial
		}
	}
	monitor.blocks = append(monitor.blocks, msg)
	if len(monitor.blocks) > sandboxLogRingSize {
		monitor.blocks = monitor.blocks[len(monitor.blocks)-sandboxLogRingSize:]
	}
}

// Stop ends the stream and returns the collected denial messages (possibly empty).
func (monitor *DenialMonitor) Stop() []string {
	if monitor.cancel != nil {
		monitor.cancel()
	}
	<-monitor.done
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return append([]string(nil), monitor.blocks...)
}

func (monitor *DenialMonitor) markTagSeen() {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	if monitor.tagSeen {
		return
	}
	monitor.tagSeen = true
	// tagCh is always set by startMonitor and the no-op constructors; the
	// guard only covers a zero-value monitor, which degrades to the timer
	// path in AwaitTag instead of panicking here.
	if monitor.tagCh != nil {
		close(monitor.tagCh)
	}
}

// AwaitTag waits up to denialMonitorTagGrace for a tag hit: a denial of this
// run that lands just after the command exits. It returns immediately when
// the stream never connected or a tag already landed, and otherwise blocks on
// the tag channel with a timer for the grace: a hit wakes it at once, a miss
// costs the full grace but never beyond it. Callers invoke it only for failed
// commands, so successes never wait.
func (monitor *DenialMonitor) AwaitTag() bool {
	monitor.mu.Lock()
	connected, seen := monitor.connected, monitor.tagSeen
	tagCh := monitor.tagCh
	monitor.mu.Unlock()
	if !connected || seen {
		return seen
	}
	select {
	case <-tagCh:
		return true
	case <-time.After(denialMonitorTagGrace):
		monitor.mu.Lock()
		defer monitor.mu.Unlock()
		return monitor.tagSeen
	}
}

// denialAttributor pairs kernel Sandbox violation lines with the `with
// message` tag line the seatbelt profile emits right after each denial. The
// tag never rides on the violation line itself, so single-line matching can
// never fire; attribution is positional (latest pending violation wins) and
// best-effort, exactly like the monitor itself. Every tag check is a
// whole-token match, so a run never pairs with a longer tag of another run.
type denialAttributor struct {
	tag     string
	pending string
}

func newDenialAttributor(tag string) *denialAttributor {
	return &denialAttributor{tag: tag}
}

// observe feeds one log line; it returns the attributed violation, if any.
// A violation line that itself carries this run's tag token attributes that
// line's own violation (hence its own pid), never the latest pending one;
// a bare tag token line attributes the pending violation it follows.
func (a *denialAttributor) observe(line string) (string, bool) {
	if msg, ok := parseSandboxDenyLine(line, a.tag); ok {
		a.pending = ""
		return msg, true
	}
	if msg, ok := parseSandboxViolation(line); ok {
		a.pending = msg
		return "", false
	}
	if a.tag != "" && a.pending != "" && containsTagToken(line, a.tag) {
		msg := a.pending
		a.pending = ""
		return msg, true
	}
	return "", false
}

// parseSandboxViolation extracts a human-readable denial from a `log stream`
// line carrying a Sandbox violation, without requiring any tag. Daemons on
// the noise list match by exact process name, so a denial ABOUT a noisy
// service (e.g. DNS via mDNSResponder) is kept while that daemon's own
// chatter is dropped. Lines without an attributable process (e.g. our own
// predicate echo) are dropped.
func parseSandboxViolation(line string) (string, bool) {
	daemon := sandboxDenyDaemon(line)
	if daemon == "" {
		return "", false
	}
	for _, noisy := range sandboxLogNoise {
		if daemon == noisy {
			return "", false
		}
	}
	if !strings.Contains(line, "Sandbox:") || !strings.Contains(line, "deny") {
		return "", false
	}
	message := line
	if idx := strings.Index(line, "Sandbox:"); idx >= 0 {
		message = line[idx:]
	}
	return strings.Join(strings.Fields(message), " "), true
}

// sandboxDenyDaemon returns the process name from a Sandbox violation line
// ("Sandbox: python3(87035) deny ..." -> "python3"), or "" when absent.
func sandboxDenyDaemon(line string) string {
	idx := strings.Index(line, "Sandbox: ")
	if idx < 0 {
		return ""
	}
	rest := line[idx+len("Sandbox: "):]
	end := strings.Index(rest, "(")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// parseSandboxDenyLine extracts a human-readable denial from a `log stream` line
// that is a Sandbox deny carrying tag. It returns ("", false) for non-denials,
// background-noise daemons, or lines missing the tag.
func parseSandboxDenyLine(line string, tag string) (string, bool) {
	msg, ok := parseSandboxViolation(line)
	if !ok {
		return "", false
	}
	if tag != "" && !containsTagToken(line, tag) {
		return "", false
	}
	if tag != "" {
		msg = strings.ReplaceAll(msg, tag, "")
	}
	return strings.Join(strings.Fields(msg), " "), true
}
