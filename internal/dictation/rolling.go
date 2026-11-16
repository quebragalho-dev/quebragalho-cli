package dictation

import (
	"context"
	"encoding/binary"
	"strings"
	"time"
)

// RollingOptions tunes the simulated live transcription. Zero values pick the
// defaults.
type RollingOptions struct {
	SampleRate int           // of the PCM16 mono chunks; default DefaultSampleRate
	Interval   time.Duration // minimum time between the starts of two partial runs; default 1s
	MinAudio   time.Duration // audio needed before the first partial run; default 1s
}

func (o RollingOptions) withDefaults() RollingOptions {
	if o.SampleRate <= 0 {
		o.SampleRate = DefaultSampleRate
	}
	if o.Interval <= 0 {
		o.Interval = time.Second
	}
	if o.MinAudio <= 0 {
		o.MinAudio = time.Second
	}
	return o
}

// rollingTranscriber gives live text to a transcriber that only handles whole
// clips: while audio arrives it transcribes everything recorded so far again
// and reports each result as the current partial text.
type rollingTranscriber struct {
	inner      Transcriber
	sampleRate int
	interval   time.Duration
	minAudio   time.Duration
	// now is the clock for run scheduling (run starts, durations, duty
	// cycle). Tests replace it with a fake; production uses time.Now.
	now func() time.Time
}

// NewRollingTranscriber gives live text to a transcriber that only handles
// whole clips: while audio arrives it transcribes everything recorded so far
// again and reports each result as the current partial text.
func NewRollingTranscriber(inner Transcriber, opts RollingOptions) Transcriber {
	opts = opts.withDefaults()
	return &rollingTranscriber{
		inner:      inner,
		sampleRate: opts.SampleRate,
		interval:   opts.Interval,
		minAudio:   opts.MinAudio,
		now:        time.Now,
	}
}

// Transcribe returns the final text for one recorded clip, delegating to the
// wrapped transcriber.
func (r *rollingTranscriber) Transcribe(ctx context.Context, audio []byte) (string, error) {
	return r.inner.Transcribe(ctx, audio)
}

// SampleRate reports the capture rate the rolling transcriber works with, so
// the recorder can be built to match.
func (r *rollingTranscriber) SampleRate() int {
	return r.sampleRate
}

// minAudioBytes converts the MinAudio duration into a PCM16 byte threshold.
func (r *rollingTranscriber) minAudioBytes() int {
	return int(int64(r.minAudio) * int64(r.sampleRate) * 2 / int64(time.Second))
}

type rollingResult struct {
	text string
	err  error
}

// StreamTranscribe consumes PCM16 chunks and reports incremental results via
// onPartial(text, false) as decoding proceeds, returning the complete final
// transcript when the chunk channel closes. See NewRollingTranscriber.
func (r *rollingTranscriber) StreamTranscribe(ctx context.Context, chunks <-chan []byte, onPartial func(string, bool)) (string, error) {
	if onPartial == nil {
		onPartial = func(string, bool) {}
	}
	minBytes := r.minAudioBytes()
	var buf []byte
	lastReported := ""
	var lastStart time.Time
	var lastEnd time.Time
	var lastDur time.Duration
	var seen int // PCM bytes covered by the last started run's snapshot
	var haveRun bool
	var runCh chan rollingResult
	var runCancel context.CancelFunc

	shouldStart := func(now time.Time) bool {
		if runCh != nil {
			return false
		}
		if len(buf) < minBytes {
			return false
		}
		if len(buf) <= seen {
			return false
		}
		if !haveRun {
			return true
		}
		return !now.Before(lastStart.Add(r.interval)) && !now.Before(lastEnd.Add(lastDur))
	}

	startRun := func(snapshot []byte, started time.Time) {
		runCtx, cancel := context.WithCancel(ctx)
		runCancel = cancel
		ch := make(chan rollingResult, 1)
		runCh = ch
		lastStart = started
		seen = len(snapshot)
		haveRun = true
		go func() {
			text, err := r.inner.Transcribe(runCtx, wavFromPCM16(snapshot, r.sampleRate))
			ch <- rollingResult{text: text, err: err}
		}()
	}

	// finishRun folds a completed partial run into the schedule and reports
	// its text when it is worth showing. A failed run is ignored.
	finishRun := func(res rollingResult, now time.Time) {
		runCh = nil
		if runCancel != nil {
			runCancel()
			runCancel = nil
		}
		lastDur = now.Sub(lastStart)
		lastEnd = now
		if res.err != nil {
			return
		}
		if res.text == "" || res.text == lastReported {
			return
		}
		lastReported = res.text
		onPartial(res.text, false)
	}

	for {
		select {
		case <-ctx.Done():
			if runCancel != nil {
				runCancel()
				<-runCh // let the run observe the cancel; its result is discarded
				runCh = nil
				runCancel = nil
			}
			return lastReported, ctx.Err()
		case res := <-runCh:
			if ctx.Err() != nil {
				if runCancel != nil {
					runCancel()
					runCancel = nil
				}
				runCh = nil
				return lastReported, ctx.Err()
			}
			now := r.now()
			finishRun(res, now)
			if now = r.now(); shouldStart(now) {
				startRun(append([]byte(nil), buf...), now)
			}
		case chunk, ok := <-chunks:
			if !ok {
				if runCancel != nil {
					runCancel()
					<-runCh // the in-flight run is cancelled; its late result is discarded
					runCh = nil
					runCancel = nil
				}
				if len(buf) == 0 {
					return "", nil
				}
				text, err := r.inner.Transcribe(ctx, wavFromPCM16(buf, r.sampleRate))
				if err != nil {
					return lastReported, err
				}
				return text, nil
			}
			buf = append(buf, chunk...)
			if now := r.now(); shouldStart(now) {
				startRun(append([]byte(nil), buf...), now)
			}
		}
	}
}

// wavFromPCM16 wraps raw PCM16 mono samples in a 44-byte RIFF header so a
// whole-clip transcriber can decode the snapshot.
func wavFromPCM16(pcm []byte, sampleRate int) []byte {
	out := make([]byte, 44+len(pcm))
	copy(out, "RIFF")
	binary.LittleEndian.PutUint32(out[4:], uint32(36+len(pcm)))
	copy(out[8:], "WAVE")
	copy(out[12:], "fmt ")
	binary.LittleEndian.PutUint32(out[16:], 16)
	binary.LittleEndian.PutUint16(out[20:], 1) // PCM
	binary.LittleEndian.PutUint16(out[22:], 1) // mono
	binary.LittleEndian.PutUint32(out[24:], uint32(sampleRate))
	binary.LittleEndian.PutUint32(out[28:], uint32(sampleRate*2)) // byte rate: rate * 1ch * 16bit/8
	binary.LittleEndian.PutUint16(out[32:], 2)                    // block align: 1ch * 16bit/8
	binary.LittleEndian.PutUint16(out[34:], 16)                   // bits per sample
	copy(out[36:], "data")
	binary.LittleEndian.PutUint32(out[40:], uint32(len(pcm)))
	copy(out[44:], pcm)
	return out
}

// StreamingModelDir reports whether a local model directory holds a true
// streaming model. Directories QG installs are named "model-<id>", and a
// streaming model's id contains "streaming". Any other directory is a manual
// setup and keeps the streaming-server path it has today (true).
func StreamingModelDir(path string) bool {
	trimmed := strings.TrimRight(strings.TrimSpace(path), `/\`)
	if trimmed == "" {
		return true
	}
	base := trimmed
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if base == "" {
		return true
	}
	if !strings.HasPrefix(base, "model-") {
		return true
	}
	return strings.Contains(strings.TrimPrefix(base, "model-"), "streaming")
}
