package openai

import "testing"

// Table test fixing all state x event combos for the stream terminal FSM.
func TestStreamPhaseTable(t *testing.T) {
	type expectation struct {
		next streamPhase
		emit bool
		ok   bool
	}
	cases := map[streamPhase]map[streamEvent]expectation{
		streamStreaming: {
			streamEvFinish:    {streamFinished, false, true},
			streamEvCleanEnd:  {streamDoneOK, true, true},
			streamEvError:     {streamDoneErr, true, true},
			streamEvTruncated: {streamTruncated, true, true},
		},
		streamFinished: {
			streamEvFinish:    {streamFinished, false, true},
			streamEvCleanEnd:  {streamDoneOK, true, true},
			streamEvError:     {streamDoneErr, true, true},
			streamEvTruncated: {streamFinished, false, false},
		},
		streamDoneOK: {
			streamEvFinish:    {streamDoneOK, false, false},
			streamEvCleanEnd:  {streamDoneOK, false, false},
			streamEvError:     {streamDoneOK, false, false},
			streamEvTruncated: {streamDoneOK, false, false},
		},
		streamDoneErr: {
			streamEvFinish:    {streamDoneErr, false, false},
			streamEvCleanEnd:  {streamDoneErr, false, false},
			streamEvError:     {streamDoneErr, false, false},
			streamEvTruncated: {streamDoneErr, false, false},
		},
		streamTruncated: {
			streamEvFinish:    {streamTruncated, false, false},
			streamEvCleanEnd:  {streamTruncated, false, false},
			streamEvError:     {streamTruncated, false, false},
			streamEvTruncated: {streamTruncated, false, false},
		},
	}
	phases := []streamPhase{streamStreaming, streamFinished, streamDoneOK, streamDoneErr, streamTruncated}
	events := []streamEvent{streamEvFinish, streamEvCleanEnd, streamEvError, streamEvTruncated}
	for _, start := range phases {
		for _, ev := range events {
			want, defined := cases[start][ev]
			if !defined {
				t.Fatalf("missing expectation for %s x %s", start, ev)
			}
			s := newToolState(false)
			s.phase = start
			emit, ok := s.transition(ev, "")
			if ok != want.ok {
				t.Errorf("%s x %s: ok=%v, want %v", start, ev, ok, want.ok)
			}
			if emit != want.emit {
				t.Errorf("%s x %s: emit=%v, want %v", start, ev, emit, want.emit)
			}
			if s.phase != want.next {
				t.Errorf("%s x %s: phase=%s, want %s", start, ev, s.phase, want.next)
			}
		}
	}
}

func TestStreamPhaseString(t *testing.T) {
	if streamStreaming.String() != "streaming" || streamFinished.String() != "finished" {
		t.Fatalf("phase strings = %q %q", streamStreaming, streamFinished)
	}
	if streamDoneOK.String() != "doneOK" || streamDoneErr.String() != "doneErr" || streamTruncated.String() != "truncated" {
		t.Fatalf("terminal strings = %q %q %q", streamDoneOK, streamDoneErr, streamTruncated)
	}
	if streamEvFinish.String() != "finish" || streamEvCleanEnd.String() != "cleanEnd" {
		t.Fatalf("event strings = %q %q", streamEvFinish, streamEvCleanEnd)
	}
}

func TestStreamFinishRecordsReason(t *testing.T) {
	s := newToolState(false)
	if _, ok := s.transition(streamEvFinish, "length"); !ok {
		t.Fatal("finish must be legal from streaming")
	}
	if !s.isTerminated() || s.isDone() {
		t.Fatalf("after finish: terminated=%v done=%v, want true/false", s.isTerminated(), s.isDone())
	}
	if s.finishReason != "length" {
		t.Fatalf("finishReason = %q, want length", s.finishReason)
	}
	if emit, _ := s.transition(streamEvCleanEnd, ""); !emit || !s.isDone() {
		t.Fatal("cleanEnd after finish must emit Done and be done")
	}
}

func TestStreamTerminalRefusesSecond(t *testing.T) {
	s := newToolState(false)
	if emit, _ := s.transition(streamEvError, ""); !emit {
		t.Fatal("first error must emit")
	}
	if emit, ok := s.transition(streamEvCleanEnd, ""); emit || ok {
		t.Fatalf("second terminal must be refused, got emit=%v ok=%v phase=%s", emit, ok, s.phase)
	}
	if s.phase != streamDoneErr {
		t.Fatalf("phase = %s, want doneErr kept", s.phase)
	}
}
