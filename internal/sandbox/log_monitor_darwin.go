//go:build darwin

package sandbox

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"sync"
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

// DenialMonitor tails `log stream` for seatbelt denials carrying a unique tag and
// collects them so a caller can report what the sandbox blocked. It is
// best-effort: if `log` cannot start it yields nothing and the command is
// unaffected.
//
// NOTE: attribution is best-effort. A violation that fires before the
// stream attaches, or a tag line the query never delivers, captures nothing.
// It never affects whether the command runs.
type DenialMonitor struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	blocks []string
}

// StartDenialMonitor begins streaming sandbox denials tagged with tag. An empty
// tag (monitoring disabled) yields a no-op monitor.
//
// The predicate matches both the tagged message lines and every Sandbox
// violation line: the `with message` tag the profile emits does NOT ride on
// the violation line itself (it arrives as its own log line right after), so
// filtering by tag alone would drop every violation before attribution.
func StartDenialMonitor(ctx context.Context, tag string) *DenialMonitor {
	monitor := &DenialMonitor{done: make(chan struct{})}
	if strings.TrimSpace(tag) == "" {
		close(monitor.done)
		return monitor
	}
	streamCtx, cancel := context.WithCancel(ctx)
	monitor.cancel = cancel
	monitor.cmd = exec.CommandContext(streamCtx, "log", "stream", "--style", "compact",
		"--predicate", `(eventMessage CONTAINS "`+tag+`") OR (eventMessage CONTAINS "Sandbox:" AND eventMessage CONTAINS "deny")`)
	stdout, err := monitor.cmd.StdoutPipe()
	if err != nil {
		cancel()
		close(monitor.done)
		return monitor
	}
	if err := monitor.cmd.Start(); err != nil {
		cancel()
		close(monitor.done)
		return monitor
	}
	go func() {
		defer close(monitor.done)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		attributor := newDenialAttributor(tag)
		for scanner.Scan() {
			if msg, ok := attributor.observe(scanner.Text()); ok {
				monitor.record(msg)
			}
		}
		// Reap the `log stream` child once stdout is drained; without Wait the
		// cancelled process lingers as a zombie across repeated runs. Safe here —
		// all reads from the pipe are complete.
		_ = monitor.cmd.Wait()
	}()
	return monitor
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

// denialAttributor pairs kernel Sandbox violation lines with the `with
// message` tag line the seatbelt profile emits right after each denial. The
// tag never rides on the violation line itself, so single-line matching can
// never fire; attribution is positional (latest pending violation wins) and
// best-effort, exactly like the monitor itself.
type denialAttributor struct {
	tag     string
	pending string
}

func newDenialAttributor(tag string) *denialAttributor {
	return &denialAttributor{tag: tag}
}

// observe feeds one log line; it returns the attributed violation, if any.
func (a *denialAttributor) observe(line string) (string, bool) {
	if msg, ok := parseSandboxDenyLine(line, a.tag); ok {
		a.pending = ""
		return msg, true
	}
	if msg, ok := parseSandboxViolation(line); ok {
		a.pending = msg
		return "", false
	}
	if a.tag != "" && a.pending != "" && strings.Contains(line, a.tag) {
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
	if tag != "" && !strings.Contains(line, tag) {
		return "", false
	}
	if tag != "" {
		msg = strings.ReplaceAll(msg, tag, "")
	}
	return strings.Join(strings.Fields(msg), " "), true
}
