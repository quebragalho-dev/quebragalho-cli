package agent

import "testing"

// Table test fixing all state x event combos for the retrier FSM.
func TestRetrierPhaseTable(t *testing.T) {
	type expectation struct {
		next      retryPhase
		ok        bool
		wantClear bool
	}
	cases := map[retryPhase]map[retryEvent]expectation{
		retryIdle: {
			retryEvSchedule:     {retryWaiting, true, false},
			retryEvSucceed:      {retryIdle, true, false},
			retryEvNonRetryable: {retryIdle, true, false},
			retryEvExhausted:    {retryExhausted, true, false},
			retryEvCancel:       {retryIdle, true, false},
			retryEvSleepAbort:   {retryIdle, false, false},
		},
		retryWaiting: {
			retryEvSchedule:     {retryWaiting, true, false},
			retryEvSucceed:      {retryIdle, true, true},
			retryEvNonRetryable: {retryIdle, true, true},
			retryEvExhausted:    {retryExhausted, true, true},
			retryEvCancel:       {retryIdle, true, true},
			retryEvSleepAbort:   {retryIdle, true, true},
		},
		retryExhausted: {
			retryEvSchedule:     {retryExhausted, false, false},
			retryEvSucceed:      {retryExhausted, false, false},
			retryEvNonRetryable: {retryExhausted, false, false},
			retryEvExhausted:    {retryExhausted, false, false},
			retryEvCancel:       {retryExhausted, false, false},
			retryEvSleepAbort:   {retryExhausted, false, false},
		},
	}
	phases := []retryPhase{retryIdle, retryWaiting, retryExhausted}
	events := []retryEvent{retryEvSchedule, retryEvSucceed, retryEvNonRetryable, retryEvExhausted, retryEvCancel, retryEvSleepAbort}
	for _, start := range phases {
		for _, ev := range events {
			want, defined := cases[start][ev]
			if !defined {
				t.Fatalf("missing expectation for %s x %s", start, ev)
			}
			r := &modelCallRetrier{phase: start}
			clears := 0
			r.onWait = func(info RetryWaitInfo) {
				if !info.Active {
					clears++
				}
			}
			ok := r.transition(ev)
			if ok != want.ok {
				t.Errorf("%s x %s: ok=%v, want %v", start, ev, ok, want.ok)
			}
			if r.phase != want.next {
				t.Errorf("%s x %s: phase=%s, want %s", start, ev, r.phase, want.next)
			}
			gotClear := clears > 0
			if gotClear != want.wantClear {
				t.Errorf("%s x %s: clear=%v, want %v", start, ev, gotClear, want.wantClear)
			}
			if clears > 1 {
				t.Errorf("%s x %s: %d clears, want at most 1", start, ev, clears)
			}
		}
	}
}

func TestRetrierPhaseString(t *testing.T) {
	if retryIdle.String() != "idle" || retryWaiting.String() != "waiting" || retryExhausted.String() != "exhausted" {
		t.Fatalf("phase strings = %q %q %q", retryIdle, retryWaiting, retryExhausted)
	}
}
