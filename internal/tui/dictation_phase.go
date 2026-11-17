package tui

// dictationPhase names the live dictation session.
//
//	Text diagram:
//	  Idle -> Starting : start (recorder/transcriber armed)
//	  Starting -> Recording : startedOK (or streaming ready)
//	  Starting -> Recording -> Transcribing : startedOK consumes deferred voiceStop
//	  Starting -> Idle : startedErr (full reset)
//	  Starting -> Starting : voiceDefer (release during startup, remembers stop)
//	  Recording -> Transcribing : stop (clears spaceHeld)
//	  Recording/Transcribing -> Idle : transcribed (full reset)
//	  Starting/Recording/Transcribing/Idle -> Idle : cancel (full reset)
//	Only transition() writes phase/sessionID and the session twins
//	(voiceStopPending/spaceHeld/regionStart/regionEnd/regionAnchor);
//	stale async results (wrong sessionID) are refused keeping state.

import (
	"context"
	"fmt"
)

func (p dictationPhase) String() string {
	switch p {
	case dictIdle:
		return "idle"
	case dictStarting:
		return "starting"
	case dictRecording:
		return "recording"
	case dictTranscribing:
		return "transcribing"
	default:
		return "unknown"
	}
}

type DictEvent int

const (
	DictEvStart DictEvent = iota
	DictEvStartedOK
	DictEvStartedErr
	DictEvStop
	DictEvTranscribed
	DictEvCancel
	DictEvVoiceDefer
)

func (e DictEvent) String() string {
	switch e {
	case DictEvStart:
		return "start"
	case DictEvStartedOK:
		return "startedOK"
	case DictEvStartedErr:
		return "startedErr"
	case DictEvStop:
		return "stop"
	case DictEvTranscribed:
		return "transcribed"
	case DictEvCancel:
		return "cancel"
	case DictEvVoiceDefer:
		return "voiceDefer"
	default:
		return "unknown"
	}
}

// dictLegal is the single table of legal (phase x event) dictation transitions.
var dictLegal = map[dictationPhase]map[DictEvent]bool{
	dictIdle: {
		DictEvStart:       true,
		DictEvCancel:      true,
		DictEvTranscribed: true,
	},
	dictStarting: {
		DictEvStartedOK:   true,
		DictEvStartedErr:  true,
		DictEvCancel:      true,
		DictEvTranscribed: true,
		DictEvVoiceDefer:  true,
	},
	dictRecording: {
		DictEvStop:        true,
		DictEvCancel:      true,
		DictEvTranscribed: true,
	},
	dictTranscribing: {
		DictEvCancel:      true,
		DictEvTranscribed: true,
	},
}

func dictEventLegal(phase dictationPhase, ev DictEvent) bool {
	return dictLegal[phase][ev]
}

// dictArgs carries the optional payload for a dictation transition.
type dictArgs struct {
	recorder    Recorder
	transcriber Transcriber
	ctx         context.Context
	cancel      context.CancelFunc
	streaming   bool
	streamStop  func() error
	spaceHeld   bool
	voiceStop   bool
	sessionID   int64
}

// dictFullReset drops the whole session by construction: the phase returns to
// idle, the sessionID bumps so stale async results are discarded, and every
// session twin (handles, live region, waveform, voice flags) is zeroed
// together. Capability fields (voiceModeEnabled, eventTypes*, downloading,
// config) are untouched.
func (d *dictationController) dictFullReset() {
	d.sessionID++
	d.phase = dictIdle
	d.recorder = nil
	d.transcriber = nil
	d.ctx = nil
	d.cancel = nil
	d.streaming = false
	d.streamStop = nil
	d.regionActive = false
	d.regionStart = 0
	d.regionEnd = 0
	d.regionPrefix = ""
	d.regionAnchor = ""
	d.waveBars = nil
	d.waveTick = 0
	d.spaceHeld = false
	d.voiceStopPending = false
}

// transition is the single writer for the dictation session state. It refuses
// illegal (phase x event) pairs and stale async completions with an error
// while keeping state (never panics in production).
func (d *dictationController) transition(ev DictEvent, a dictArgs) error {
	if !dictEventLegal(d.phase, ev) {
		return fmt.Errorf("illegal dictation transition %s in %s", ev, d.phase)
	}
	switch ev {
	case DictEvStart:
		d.recorder = a.recorder
		d.transcriber = a.transcriber
		d.ctx = a.ctx
		d.cancel = a.cancel
		d.streaming = a.streaming
		d.spaceHeld = a.spaceHeld
		d.voiceStopPending = a.voiceStop
		d.phase = dictStarting
	case DictEvStartedOK:
		if a.sessionID != d.sessionID {
			return fmt.Errorf("stale dictation started for session %d in %d", a.sessionID, d.sessionID)
		}
		if a.streamStop != nil {
			d.streamStop = a.streamStop
		}
		if d.voiceStopPending {
			d.voiceStopPending = false
			d.phase = dictTranscribing
		} else {
			d.phase = dictRecording
		}
	case DictEvStartedErr:
		if a.sessionID != d.sessionID {
			return fmt.Errorf("stale dictation start error for session %d in %d", a.sessionID, d.sessionID)
		}
		d.dictFullReset()
	case DictEvStop:
		d.spaceHeld = false
		d.voiceStopPending = false
		d.phase = dictTranscribing
	case DictEvTranscribed:
		if a.sessionID != d.sessionID {
			return fmt.Errorf("stale dictation transcript for session %d in %d", a.sessionID, d.sessionID)
		}
		d.dictFullReset()
	case DictEvCancel:
		d.dictFullReset()
	case DictEvVoiceDefer:
		d.spaceHeld = false
		d.voiceStopPending = true
	}
	return nil
}
