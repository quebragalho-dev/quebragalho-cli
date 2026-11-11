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

// A discarded attempt's partial text vanishes from the live response: the
// retry streams the answer whole, so keeping the partial would show it twice.
func TestDiscardedPartialVanishesFromLiveText(t *testing.T) {
	base := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	m := model{now: func() time.Time { return base }}
	m.width = 100
	m.pending = true
	m.activeRunID = 3

	updated, _ := m.Update(agentTextMsg{runID: 3, delta: "parte A"})
	m = updated.(model)
	if got := m.streamingTextString(); got != "parte A" {
		t.Fatalf("live text = %q, want the streamed partial", got)
	}

	updated, _ = m.Update(agentRetryWaitMsg{
		runID: 3, attempt: 2, max: 10,
		wait:      5 * time.Second,
		summary:   "upstream 502",
		discarded: true,
	})
	m = updated.(model)
	if got := m.streamingTextString(); got != "" {
		t.Fatalf("discarded partial must vanish from the live text, got %q", got)
	}
	if line := plainRender(t, m.interimBlock(96)); strings.Contains(line, "parte A") {
		t.Fatalf("interim block must not show the discarded partial:\n%s", line)
	}

	updated, _ = m.Update(agentTextMsg{runID: 3, delta: "resposta completa"})
	m = updated.(model)
	updated, _ = m.Update(agentResponseMsg{runID: 3, rows: []transcriptRow{
		{kind: rowAssistant, text: "resposta completa", final: true},
	}})
	m = updated.(model)

	complete, partial := 0, 0
	for _, row := range m.transcript {
		complete += strings.Count(row.text, "resposta completa")
		partial += strings.Count(row.text, "parte A")
	}
	if complete != 1 {
		t.Fatalf("transcript must show the final answer once, got %d", complete)
	}
	if partial != 0 {
		t.Fatalf("transcript must not contain the discarded partial, got %d", partial)
	}
	if strings.Contains(m.streamingTextString(), "parte A") {
		t.Fatalf("live text must not retain the discarded partial after settle")
	}
}
