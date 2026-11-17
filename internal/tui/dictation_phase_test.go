package tui

import (
	"testing"
)

// Exhaustive phase x event table: pins exactly which transitions are legal.
// Must match dictLegal; any new phase or event has to update both.
func TestDictationPhaseTable(t *testing.T) {
	phases := []dictationPhase{dictIdle, dictStarting, dictRecording, dictTranscribing}
	events := []DictEvent{DictEvStart, DictEvStartedOK, DictEvStartedErr, DictEvStop, DictEvTranscribed, DictEvCancel, DictEvVoiceDefer}

	want := map[dictationPhase]map[DictEvent]bool{
		dictIdle: {
			DictEvStart: true, DictEvCancel: true, DictEvTranscribed: true,
		},
		dictStarting: {
			DictEvStartedOK: true, DictEvStartedErr: true, DictEvCancel: true,
			DictEvTranscribed: true, DictEvVoiceDefer: true,
		},
		dictRecording: {
			DictEvStop: true, DictEvCancel: true, DictEvTranscribed: true,
		},
		dictTranscribing: {
			DictEvCancel: true, DictEvTranscribed: true,
		},
	}

	for _, ph := range phases {
		for _, ev := range events {
			legal := want[ph][ev]
			d := dictationController{sessionID: 5, phase: ph}
			// Seed twins so reset events prove they zero together.
			if legal && (ev == DictEvCancel || ev == DictEvTranscribed || ev == DictEvStartedErr) {
				d.voiceStopPending = true
				d.spaceHeld = true
				d.regionStart = 3
				d.regionEnd = 9
				d.regionAnchor = "seed"
				d.regionActive = true
			}
			args := dictArgs{sessionID: 5}
			if ev == DictEvStart {
				args = dictArgs{}
			}
			err := d.transition(ev, args)
			if dictEventLegal(ph, ev) != legal {
				t.Errorf("dictLegal[%v][%v] = %v, want %v", ph, ev, dictEventLegal(ph, ev), legal)
			}
			if legal != (err == nil) {
				t.Errorf("phase %v event %v: legal=%v but err=%v", ph, ev, legal, err)
				continue
			}
			if !legal {
				if d.phase != ph || d.sessionID != 5 {
					t.Errorf("illegal %v in %v must keep state, got phase=%v session=%d", ev, ph, d.phase, d.sessionID)
				}
				continue
			}
			if ev == DictEvCancel || ev == DictEvTranscribed || ev == DictEvStartedErr {
				if d.phase != dictIdle {
					t.Errorf("%v in %v: want idle, got %v", ev, ph, d.phase)
				}
				if d.voiceStopPending || d.spaceHeld || d.regionStart != 0 || d.regionEnd != 0 || d.regionAnchor != "" || d.regionActive {
					t.Errorf("%v in %v: twins not zeroed: %+v", ev, ph, d)
				}
			}
		}
	}
}

// Stale async completions keep state even when the phase would allow them.
func TestDictationPhaseStaleGuard(t *testing.T) {
	d := dictationController{sessionID: 9, phase: dictStarting}
	if err := d.transition(DictEvStartedOK, dictArgs{sessionID: 8}); err == nil {
		t.Fatal("stale StartedOK must be refused")
	}
	if d.phase != dictStarting || d.sessionID != 9 {
		t.Fatalf("stale StartedOK changed state: %+v", d)
	}
	d2 := dictationController{sessionID: 9, phase: dictRecording}
	if err := d2.transition(DictEvTranscribed, dictArgs{sessionID: 8}); err == nil {
		t.Fatal("stale Transcribed must be refused")
	}
	if d2.phase != dictRecording {
		t.Fatalf("stale Transcribed changed state: %+v", d2)
	}
}

// Deferred voice stop: release in Starting lands straight in Transcribing on
// StartedOK, firing the transcribe command from the handler.
func TestDictationPhaseDeferredVoiceStop(t *testing.T) {
	d := dictationController{sessionID: 4, phase: dictStarting}
	if err := d.transition(DictEvVoiceDefer, dictArgs{}); err != nil {
		t.Fatal(err)
	}
	if !d.voiceStopPending || d.phase != dictStarting {
		t.Fatalf("voiceDefer must set flag staying in Starting: %+v", d)
	}
	if err := d.transition(DictEvStartedOK, dictArgs{sessionID: 4}); err != nil {
		t.Fatal(err)
	}
	if d.phase != dictTranscribing || d.voiceStopPending {
		t.Fatalf("StartedOK must consume deferred stop: %+v", d)
	}
}

// Start writes the voice twins from its args: the transition owns
// spaceHeld/voiceStopPending, so a dirty Idle is overwritten, not inherited.
func TestDictationPhaseStartWritesVoiceTwins(t *testing.T) {
	d := dictationController{sessionID: 4, phase: dictIdle, voiceStopPending: true}
	if err := d.transition(DictEvStart, dictArgs{spaceHeld: true, voiceStop: false}); err != nil {
		t.Fatal(err)
	}
	if d.phase != dictStarting || d.voiceStopPending || !d.spaceHeld {
		t.Fatalf("Start must write voice twins from args: %+v", d)
	}
	d2 := dictationController{sessionID: 4, phase: dictIdle, voiceStopPending: true, spaceHeld: true}
	if err := d2.transition(DictEvStart, dictArgs{}); err != nil {
		t.Fatal(err)
	}
	if d2.phase != dictStarting || d2.voiceStopPending || d2.spaceHeld {
		t.Fatalf("Start with zero args must clear dirty voice twins: %+v", d2)
	}
}
