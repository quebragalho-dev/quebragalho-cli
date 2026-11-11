package tui

import (
	"strings"
	"testing"
	"time"
)

// While the agent waits before a model-call retry, the working line shows the
// summarized error, a live countdown and the attempt counter.
func TestRetryWaitShowsCountdownOnWorkingLine(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := model{now: func() time.Time { return base }}
	m.width = 100
	m.pending = true
	m.activeRunID = 3

	updated, _ := m.Update(agentRetryWaitMsg{
		runID: 3, attempt: 4, max: 10,
		wait:    72 * time.Second,
		summary: "upstream 502",
	})
	m = updated.(model)
	if m.retryWait == nil {
		t.Fatal("retry wait state must be set by agentRetryWaitMsg")
	}

	line := plainRender(t, m.workingStatusLine())
	for _, want := range []string{"upstream 502", "tentando novamente", "1m 12s", "4/10"} {
		if !strings.Contains(line, want) {
			t.Fatalf("working line must show %q, got %q", want, line)
		}
	}

	// The countdown is live: 70s later it reads 2s.
	m.now = func() time.Time { return base.Add(70 * time.Second) }
	if line := plainRender(t, m.workingStatusLine()); !strings.Contains(line, "2s") {
		t.Fatalf("countdown must advance with the clock, got %q", line)
	}

	// The run settling clears the wait state.
	updated, _ = m.Update(agentRetryClearMsg{runID: 3})
	m = updated.(model)
	if m.retryWait != nil {
		t.Fatal("retry wait state must clear when the attempt settles")
	}
	if line := plainRender(t, m.workingStatusLine()); strings.Contains(line, "tentando novamente") {
		t.Fatalf("working line must drop the retry segment after clear, got %q", line)
	}
}

func TestFormatRetryCountdown(t *testing.T) {
	cases := map[time.Duration]string{
		0:                "0s",
		5 * time.Second:  "5s",
		72 * time.Second: "1m 12s",
		5 * time.Minute:  "5m 00s",
		-3 * time.Second: "0s",
	}
	for d, want := range cases {
		if got := formatRetryCountdown(d); got != want {
			t.Errorf("formatRetryCountdown(%v) = %q, want %q", d, got, want)
		}
	}
}
