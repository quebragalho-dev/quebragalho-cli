package tui

import (
	"fmt"
	"strings"
	"time"
)

// retryWaitState tracks an in-flight model-call auto-retry wait so the working
// status line can show the error summary, the live countdown and the attempt
// counter while the agent goroutine sleeps between attempts.
type retryWaitState struct {
	attempt  int
	max      int
	summary  string
	deadline time.Time
}

// agentRetryWaitMsg reports a scheduled model-call retry from the agent
// goroutine (wired through Options.OnRetryWait in runAgentWithOptions).
type agentRetryWaitMsg struct {
	runID     int
	attempt   int
	max       int
	wait      time.Duration
	summary   string
	discarded bool
}

// agentRetryClearMsg settles a reported retry wait (the attempt finally ran or
// the budget ran out). The run settling (agentResponseMsg) clears it too.
type agentRetryClearMsg struct {
	runID int
}

// formatRetryCountdown renders a retry countdown compactly: "45s", "1m 12s".
func formatRetryCountdown(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm %02ds", int(d/time.Minute), int(d.Seconds())%60)
}

// retryWaitSegment renders the live retry line for the working status line:
// "upstream 502 · tentando novamente em 1m 12s… 4/10". Empty when no wait is
// in flight. The countdown derives from the model's clock, so the existing
// spinner tick keeps it advancing with no extra timer.
func (m model) retryWaitSegment() string {
	wait := m.retryWait
	if wait == nil || m.activeRunID == 0 {
		return ""
	}
	summary := strings.TrimSpace(wait.summary)
	if summary == "" {
		summary = "modelo"
	}
	return fmt.Sprintf("%s · tentando novamente em %s… %d/%d",
		summary, formatRetryCountdown(wait.deadline.Sub(m.now())), wait.attempt, wait.max)
}
