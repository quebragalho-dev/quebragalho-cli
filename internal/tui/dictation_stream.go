package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/dictation"
)

// sttPartialMsg carries an incremental streaming transcript into the TUI. It is
// injected from the streaming goroutine via runtimeMessageSink — the same
// mechanism agent text deltas use (§6). text is the cumulative best transcript
// so far (not a delta); final marks a settled segment.
type sttPartialMsg struct {
	sessionID int64
	text      string
	final     bool
}

// startStreamingDictation begins continuous capture and launches the streaming
// transcription command. StartStreaming spawns the capture process
// synchronously, so a failure here is immediate; on success we are already
// recording (no separate "started" round-trip).
func (m model) startStreamingDictation() (model, tea.Cmd) {
	chunks, stop, err := m.dictation.recorder.StartStreaming()
	if err != nil {
		sid := m.dictation.sessionID
		_ = m.dictation.transition(DictEvStartedErr, dictArgs{sessionID: sid})
		return m.appendSystemNotice(dictationErrorText(err)), nil
	}
	_ = m.dictation.transition(DictEvStartedOK, dictArgs{sessionID: m.dictation.sessionID, streamStop: stop})

	sink := m.runtimeMessageSink
	ctx := m.dictation.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	transcriber := m.dictation.transcriber
	submit := m.dictation.cfg.AutoSubmitEnabled()
	sessionID := m.dictation.sessionID

	// Tap the audio: compute a mic level per chunk for the live waveform, then
	// forward the chunk to the transcriber. A small buffer keeps the tap from
	// stalling capture if the transcriber briefly lags.
	tapped := make(chan []byte, 16)
	go func() {
		defer close(tapped)
		for chunk := range chunks {
			if sink != nil {
				sink(sttLevelMsg{level: dictation.ChunkLevel(chunk)})
			}
			// Also select on ctx.Done so this goroutine can't block forever on the
			// send if StreamTranscribe exits early (e.g. a sherpa startup failure) and
			// stops draining `tapped` — the session's cancel unblocks and drains it.
			select {
			case tapped <- chunk:
			case <-ctx.Done():
				return
			}
		}
	}()

	streamCmd := func() tea.Msg {
		onPartial := func(text string, final bool) {
			if sink != nil {
				sink(sttPartialMsg{sessionID: sessionID, text: text, final: final})
			}
		}
		text, err := transcriber.StreamTranscribe(ctx, tapped, onPartial)
		return dictationTranscribedMsg{sessionID: sessionID, text: text, err: err, submit: submit, streaming: true}
	}
	// Streaming drives the waveform from real levels (no synthetic tick needed).
	return m, streamCmd
}

// handleDictationPartial renders a cumulative partial transcript into the
// composer, replacing the previously-rendered live region wholesale so the text
// builds up in place as the user keeps talking.
func (m model) handleDictationPartial(msg sttPartialMsg) model {
	if msg.sessionID != m.dictation.sessionID {
		return m
	}
	// Ignore stragglers that arrive after the session ended (cancel/final).
	if m.dictation.phase != dictRecording && m.dictation.phase != dictTranscribing {
		return m
	}
	m.applyStreamingText(msg.text)
	return m
}

func (m *model) applyStreamingText(text string) {
	state := m.currentComposerState()
	runes := []rune(state.text)
	if !m.dictation.regionActive {
		m.dictation.regionActive = true
		m.dictation.regionStart = state.cursor
		m.dictation.regionEnd = state.cursor
		m.dictation.regionPrefix = ""
		// Anchor the prefix text BEFORE the live region. If the user types or
		// pastes outside the region, the next partial can detect the change in
		// this prefix and shift [start,end) to stay aligned.
		m.dictation.regionAnchor = string(runes[:state.cursor])
		if needsLeadingSpace(state) {
			// Fold the separator into the region so a cancel removes it too.
			m.dictation.regionPrefix = " "
		}
	} else if m.dictation.regionStart < 0 || m.dictation.regionStart > len(runes) || m.dictation.regionEnd < m.dictation.regionStart || m.dictation.regionEnd > len(runes) {
		m.dictation.regionStart = state.cursor
		m.dictation.regionEnd = state.cursor
		m.dictation.regionPrefix = ""
		if needsLeadingSpace(state) {
			m.dictation.regionPrefix = " "
		}
		m.dictation.regionAnchor = string(runes[:state.cursor])
	} else {
		// Compare the prefix before the live region. If it changed (the user
		// typed/pasted there) shift [start,end) by the length delta so the
		// next partial's slice targets the right span. If the user edited
		// INSIDE the region, drop the live region and re-anchor at the
		// current cursor — we can't tell the partial from the user's text
		// after that point.
		prefix := string(runes[:m.dictation.regionStart])
		anchor := m.dictation.regionAnchor
		switch {
		case prefix == anchor:
			// External edit at or after the region — no shift needed.
		case len(prefix) > len(anchor) && strings.HasPrefix(prefix, anchor):
			// External edit inserted text just before the region (i.e. at
			// position regionStart, pushing the region right).
			delta := len([]rune(prefix)) - len([]rune(anchor))
			m.dictation.regionStart += delta
			m.dictation.regionEnd += delta
			m.dictation.regionAnchor = prefix
		case len(anchor) > len(prefix) && strings.HasPrefix(anchor, prefix):
			// External delete just before the region.
			delta := len([]rune(anchor)) - len([]rune(prefix))
			m.dictation.regionStart -= delta
			m.dictation.regionEnd -= delta
			m.dictation.regionAnchor = prefix
		default:
			// User edited inside or across the region — can't safely
			// overwrite. Drop the live region and re-anchor at the cursor
			// so the next partial inserts as a fresh span.
			m.dictation.regionStart = state.cursor
			m.dictation.regionEnd = state.cursor
			m.dictation.regionPrefix = ""
			m.dictation.regionAnchor = string(runes[:state.cursor])
		}
	}
	// Replace [regionStart, regionEnd) with prefix + the new cumulative text.
	rendered := m.dictation.regionPrefix + text
	cleared, nextPreviews := deleteComposerRangeWithPastePreviews(state, m.composerPastePreviews, m.dictation.regionStart, m.dictation.regionEnd)
	// Preserve the user's cursor if they moved it outside the live region
	// (e.g. to edit elsewhere in the composer) — yanking it to regionStart on
	// every partial would steal their focus mid-edit.
	outsideCursor := -1
	if state.cursor < m.dictation.regionStart {
		outsideCursor = state.cursor
	} else if state.cursor > m.dictation.regionEnd {
		outsideCursor = state.cursor - (m.dictation.regionEnd - m.dictation.regionStart) + len([]rune(rendered))
	}
	insertAt := m.dictation.regionStart
	cleared.cursor = insertAt
	nextPreviews = composerPastePreviewsAfterInsert(nextPreviews, insertAt, len([]rune(rendered)))
	updated := insertComposerText(cleared, rendered)
	if outsideCursor >= 0 {
		updated.cursor = outsideCursor
	}
	m.dictation.regionEnd = m.dictation.regionStart + len([]rune(rendered))
	m.setComposerState(updated)
	m.composerPastePreviews = nextPreviews
}

// commitDictationRegion keeps the streamed text in the composer and stops
// tracking it as a live region (used on successful completion — the final
// transcript equals the last partial already rendered).
func (m model) commitDictationRegion() model {
	m.dictation.regionActive = false
	return m
}

// discardDictationRegion removes the live streamed text from the composer (used
// on cancel — the user aborted, so the half-formed transcript is dropped).
func (m model) discardDictationRegion() model {
	if m.dictation.regionActive {
		state := m.currentComposerState()
		nextState, nextPreviews := deleteComposerRangeWithPastePreviews(state, m.composerPastePreviews, m.dictation.regionStart, m.dictation.regionEnd)
		m.setComposerState(nextState)
		m.composerPastePreviews = nextPreviews
		m.dictation.regionActive = false
	}
	return m
}
