package dictation

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// stubClock is a manually advanced clock so run scheduling is deterministic.
type stubClock struct {
	mu sync.Mutex
	t  time.Time
}

func newStubClock() *stubClock {
	return &stubClock{t: time.Unix(0, 0)}
}

func (c *stubClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stubClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// pcmBytes returns ms milliseconds of PCM16 mono silence at the given rate.
func pcmBytes(rate, ms int) []byte {
	return make([]byte, rate*ms*2/1000)
}

type partialCall struct {
	text  string
	final bool
}

// partialLog collects onPartial calls; arrived signals each one.
type partialLog struct {
	mu      sync.Mutex
	calls   []partialCall
	arrived chan partialCall
}

func newPartialLog() *partialLog {
	return &partialLog{arrived: make(chan partialCall, 32)}
}

func (l *partialLog) cb(text string, final bool) {
	c := partialCall{text: text, final: final}
	l.mu.Lock()
	l.calls = append(l.calls, c)
	l.mu.Unlock()
	select {
	case l.arrived <- c:
	default:
	}
}

func (l *partialLog) texts() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, 0, len(l.calls))
	for _, c := range l.calls {
		out = append(out, c.text)
	}
	return out
}

func (l *partialLog) allFinalFalse() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.calls {
		if c.final {
			return false
		}
	}
	return true
}

// scriptedTranscriber records every Transcribe call's audio and answers from
// behave (n is the 1-based call index).
type scriptedTranscriber struct {
	mu     sync.Mutex
	calls  [][]byte
	stamps []time.Time
	called chan int
	clock  *stubClock
	behave func(n int, ctx context.Context) (string, error)
}

func newScripted(clock *stubClock, behave func(n int, ctx context.Context) (string, error)) *scriptedTranscriber {
	return &scriptedTranscriber{called: make(chan int, 128), clock: clock, behave: behave}
}

func (f *scriptedTranscriber) Transcribe(ctx context.Context, audio []byte) (string, error) {
	cp := append([]byte(nil), audio...)
	f.mu.Lock()
	f.calls = append(f.calls, cp)
	if f.clock != nil {
		f.stamps = append(f.stamps, f.clock.now())
	}
	n := len(f.calls)
	f.mu.Unlock()
	select {
	case f.called <- n:
	default:
	}
	if f.behave != nil {
		return f.behave(n, ctx)
	}
	return "", nil
}

func (f *scriptedTranscriber) StreamTranscribe(context.Context, <-chan []byte, func(string, bool)) (string, error) {
	return "", ErrStreamingUnsupported
}

func (f *scriptedTranscriber) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// dataLen returns the WAV payload length of the n-th (1-based) call.
func (f *scriptedTranscriber) dataLen(t *testing.T, n int) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	return parseWAVDataLen(t, f.calls[n-1])
}

func newTestRolling(inner Transcriber, opts RollingOptions, clock *stubClock) Transcriber {
	rt := NewRollingTranscriber(inner, opts).(*rollingTranscriber)
	rt.now = clock.now
	return rt
}

type streamOutcome struct {
	text string
	err  error
}

func runStream(tr Transcriber, ctx context.Context, chunks chan []byte, log *partialLog) <-chan streamOutcome {
	done := make(chan streamOutcome, 1)
	go func() {
		text, err := tr.StreamTranscribe(ctx, chunks, log.cb)
		done <- streamOutcome{text: text, err: err}
	}()
	return done
}

// feedUntilCall sends chunks (advancing the clock before each one) until the
// fake records call want, proving the run started. The channel must be
// unbuffered so every send rendezvouses with one loop iteration: the clock
// then only advances in lockstep with what the loop evaluated, and a trigger
// consumed while a run is in flight is simply retried on the next send.
//
// Gosched yields every iteration so an instantly-returning fake's run
// goroutine gets a timeslice: a tight unbuffered ping-pong between this loop
// and StreamTranscribe would otherwise never free a P for it on a loaded
// machine, starving the completion it must report. Correctness still comes
// from the channel signals, never from timing.
func feedUntilCall(t *testing.T, fake *scriptedTranscriber, chunks chan []byte, clock *stubClock, want int) {
	t.Helper()
	chunk := pcmBytes(1000, 100)
	for i := 0; i < 500; i++ {
		select {
		case n := <-fake.called:
			if n == want {
				return
			}
			if n > want {
				t.Fatalf("call = %d, skipped %d", n, want)
			}
		default:
		}
		clock.advance(100 * time.Millisecond)
		chunks <- chunk
		runtime.Gosched()
	}
	t.Fatalf("run %d never started", want)
}

// parseWAVDataLen validates a 44-byte RIFF header (PCM, mono, 16 bit) and
// returns the audio payload length.
func parseWAVDataLen(t *testing.T, wav []byte) int {
	t.Helper()
	if len(wav) < 44 {
		t.Fatalf("WAV too short: %d bytes", len(wav))
	}
	if string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" || string(wav[12:16]) != "fmt " {
		t.Fatalf("bad RIFF header: %q", wav[:16])
	}
	if got := binary.LittleEndian.Uint32(wav[16:20]); got != 16 {
		t.Fatalf("fmt chunk size = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint16(wav[20:22]); got != 1 {
		t.Fatalf("audio format = %d, want 1 (PCM)", got)
	}
	if got := binary.LittleEndian.Uint16(wav[22:24]); got != 1 {
		t.Fatalf("channels = %d, want 1 (mono)", got)
	}
	if got := binary.LittleEndian.Uint16(wav[34:36]); got != 16 {
		t.Fatalf("bits per sample = %d, want 16", got)
	}
	if string(wav[36:40]) != "data" {
		t.Fatalf("missing data marker: %q", wav[36:40])
	}
	dataLen := int(binary.LittleEndian.Uint32(wav[40:44]))
	if dataLen != len(wav)-44 {
		t.Fatalf("header data length = %d, actual payload = %d", dataLen, len(wav)-44)
	}
	return dataLen
}

func parseWAVRate(t *testing.T, wav []byte) int {
	t.Helper()
	if len(wav) < 44 {
		t.Fatalf("WAV too short: %d bytes", len(wav))
	}
	return int(binary.LittleEndian.Uint32(wav[24:28]))
}

// TestRollingPartialsCumulativeAndOrdered drives 3 s of audio with the clock
// advanced accordingly: partials arrive cumulative and in order, snapshots
// grow, and the return value is the transcript of the whole audio.
func TestRollingPartialsCumulativeAndOrdered(t *testing.T) {
	clock := newStubClock()
	texts := map[int]string{1: "one", 2: "one two", 3: "one two three", 4: "one two three final"}
	fake := newScripted(clock, func(n int, _ context.Context) (string, error) { return texts[n], nil })
	tr := newTestRolling(fake, RollingOptions{}, clock)
	chunks := make(chan []byte, 64)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	for i := 0; i < 60; i++ {
		chunks <- pcmBytes(DefaultSampleRate, 50)
		clock.advance(50 * time.Millisecond)
		if (i+1)%20 == 0 {
			want := texts[(i+1)/20]
			select {
			case p := <-log.arrived:
				if p.text != want {
					t.Fatalf("partial %d = %q, want %q", (i+1)/20, p.text, want)
				}
				if p.final {
					t.Fatalf("rolling partials must use final=false")
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("timed out waiting for partial %d", (i+1)/20)
			}
		}
	}
	close(chunks)
	var out streamOutcome
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if out.err != nil {
		t.Fatalf("StreamTranscribe: %v", out.err)
	}
	if out.text != texts[4] {
		t.Errorf("final = %q, want %q (transcript of the whole audio)", out.text, texts[4])
	}
	if got := fake.count(); got != 4 {
		t.Fatalf("inner calls = %d, want 4 (3 partials + final)", got)
	}
	lens := []int{fake.dataLen(t, 1), fake.dataLen(t, 2), fake.dataLen(t, 3), fake.dataLen(t, 4)}
	if lens[0] != 32000 {
		t.Errorf("first snapshot = %d PCM bytes, want 32000 (1 s at MinAudio)", lens[0])
	}
	for i := 1; i < len(lens)-1; i++ {
		if lens[i] <= lens[i-1] {
			t.Errorf("snapshots must grow: %v", lens)
			break
		}
	}
	if lens[3] != 60*1600 {
		t.Errorf("final snapshot = %d PCM bytes, want %d (whole audio)", lens[3], 60*1600)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for i := 1; i < 3; i++ {
		if fake.stamps[i].Sub(fake.stamps[i-1]) < time.Second {
			t.Errorf("run starts %v apart, want at least the 1 s Interval", fake.stamps[i].Sub(fake.stamps[i-1]))
		}
	}
}

// TestRollingSnapshotsAreValidWAV checks every snapshot handed to the engine:
// PCM, mono, 16 bit, the configured rate, data length = the PCM so far.
func TestRollingSnapshotsAreValidWAV(t *testing.T) {
	clock := newStubClock()
	fake := newScripted(clock, func(n int, _ context.Context) (string, error) {
		return fmt.Sprintf("text-%d", n), nil
	})
	const rate = 8000
	tr := newTestRolling(fake, RollingOptions{SampleRate: rate, MinAudio: 100 * time.Millisecond, Interval: 50 * time.Millisecond}, clock)
	chunks := make(chan []byte, 8)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	sent := pcmBytes(rate, 100)
	chunks <- sent
	clock.advance(100 * time.Millisecond)
	select {
	case <-log.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the partial")
	}
	close(chunks)
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("StreamTranscribe: %v", out.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if got := fake.count(); got != 2 {
		t.Fatalf("inner calls = %d, want 2 (partial + final)", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for i, wav := range fake.calls {
		if r := parseWAVRate(t, wav); r != rate {
			t.Errorf("call %d rate = %d, want %d", i+1, r, rate)
		}
		dataLen := parseWAVDataLen(t, wav)
		if dataLen != len(sent) {
			t.Errorf("call %d data length = %d, want %d (PCM so far)", i+1, dataLen, len(sent))
		}
		if string(wav[44:]) != string(sent) {
			t.Errorf("call %d payload differs from the recorded PCM", i+1)
		}
		if got := binary.LittleEndian.Uint32(wav[28:32]); got != uint32(rate*2) {
			t.Errorf("call %d byte rate = %d, want %d", i+1, got, rate*2)
		}
		if got := binary.LittleEndian.Uint16(wav[32:34]); got != 2 {
			t.Errorf("call %d block align = %d, want 2", i+1, got)
		}
	}
}

// TestRollingOneRunAtATime blocks the first partial run while more audio
// arrives: no second call starts until the first returns, and the next run
// then sees all the audio accumulated meanwhile.
func TestRollingOneRunAtATime(t *testing.T) {
	clock := newStubClock()
	release := make(chan struct{})
	fake := newScripted(clock, func(n int, _ context.Context) (string, error) {
		if n == 1 {
			<-release
			return "first", nil
		}
		if n == 2 {
			return "second", nil
		}
		return "FINAL", nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: 10 * time.Millisecond}, clock)
	chunks := make(chan []byte, 8)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	chunk := pcmBytes(1000, 100)
	chunks <- chunk
	select {
	case n := <-fake.called:
		if n != 1 {
			t.Fatalf("first call = %d, want 1", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first partial run never started")
	}
	clock.advance(time.Second) // time passes while the run is blocked
	chunks <- chunk
	chunks <- chunk
	close(release) // let the first run finish
	select {
	case p := <-log.arrived:
		if p.text != "first" || p.final {
			t.Fatalf("partial = (%q, %v), want (\"first\", false)", p.text, p.final)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first partial never reported")
	}
	clock.advance(time.Second) // satisfy the duty cycle (the run took 1 s)
	chunks <- chunk
	select {
	case n := <-fake.called:
		if n != 2 {
			t.Fatalf("second call = %d, want 2", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second partial run never started")
	}
	select {
	case p := <-log.arrived:
		if p.text != "second" {
			t.Fatalf("partial = %q, want \"second\"", p.text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second partial never reported")
	}
	close(chunks)
	var out streamOutcome
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if out.err != nil || out.text != "FINAL" {
		t.Fatalf("final = (%q, %v), want (\"FINAL\", nil)", out.text, out.err)
	}
	if got := fake.count(); got != 3 {
		t.Fatalf("inner calls = %d, want 3 (2 partials + final)", got)
	}
	if got, want := fake.dataLen(t, 2), 4*len(chunk); got != want {
		t.Errorf("second snapshot = %d PCM bytes, want %d (all audio accumulated meanwhile)", got, want)
	}
}

// TestRollingDutyCycleHalvesEngineLoad: a partial run that took 3 s blocks the
// next run for 3 more seconds even with Interval at 1 s.
func TestRollingDutyCycleHalvesEngineLoad(t *testing.T) {
	clock := newStubClock()
	release := make(chan struct{})
	fake := newScripted(clock, func(n int, _ context.Context) (string, error) {
		if n == 1 {
			<-release
			return "one", nil
		}
		return "two", nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: time.Second}, clock)
	chunks := make(chan []byte, 8)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	chunk := pcmBytes(1000, 100)
	chunks <- chunk
	select {
	case <-fake.called:
	case <-time.After(10 * time.Second):
		t.Fatal("first partial run never started")
	}
	clock.advance(3 * time.Second) // the run takes 3 s
	close(release)
	select {
	case <-log.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("first partial never reported")
	}
	clock.advance(2 * time.Second) // Interval (1 s) satisfied, duty (3 s) not
	chunks <- chunk
	select {
	case n := <-fake.called:
		t.Fatalf("run %d started 2 s after a 3 s run: duty cycle violated", n)
	case <-time.After(200 * time.Millisecond):
	}
	clock.advance(time.Second) // 3 s after the run ended: duty satisfied
	chunks <- chunk
	select {
	case n := <-fake.called:
		if n != 2 {
			t.Fatalf("second call = %d, want 2", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("duty-satisfied run never started")
	}
	close(chunks)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if got := fake.count(); got != 3 {
		t.Fatalf("inner calls = %d, want 3 (2 partials + final)", got)
	}
	if got, want := fake.dataLen(t, 2), 3*len(chunk); got != want {
		t.Errorf("second snapshot = %d PCM bytes, want %d (all audio)", got, want)
	}
}

// TestRollingIntervalAndMinAudio: below MinAudio nothing runs; two starts are
// never closer than Interval.
func TestRollingIntervalAndMinAudio(t *testing.T) {
	clock := newStubClock()
	fake := newScripted(clock, func(n int, _ context.Context) (string, error) {
		return fmt.Sprintf("text-%d", n), nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: time.Second, Interval: time.Second}, clock)
	chunks := make(chan []byte, 8)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	chunks <- pcmBytes(1000, 500) // half of MinAudio: must not run
	clock.advance(500 * time.Millisecond)
	chunks <- pcmBytes(1000, 500) // 1 s total: first run starts here
	select {
	case <-fake.called:
	case <-time.After(10 * time.Second):
		t.Fatal("MinAudio run never started")
	}
	select {
	case <-log.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("first partial never reported")
	}
	if got := fake.dataLen(t, 1); got != 2000 {
		t.Fatalf("first snapshot = %d PCM bytes, want 2000 (MinAudio)", got)
	}
	clock.advance(500 * time.Millisecond) // Interval not yet satisfied
	chunks <- pcmBytes(1000, 500)
	clock.advance(500 * time.Millisecond) // Interval satisfied now
	chunks <- pcmBytes(1000, 500)
	select {
	case <-fake.called:
	case <-time.After(10 * time.Second):
		t.Fatal("Interval run never started")
	}
	close(chunks)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if got := fake.count(); got != 3 {
		t.Fatalf("inner calls = %d, want 3 (2 partials + final)", got)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if d := fake.stamps[1].Sub(fake.stamps[0]); d < time.Second {
		t.Errorf("run starts %v apart, want at least the 1 s Interval", d)
	}
}

// TestRollingNoNewAudioNoNewRun: an empty chunk advances nothing, so no new
// run starts; the next run still sees the real audio that arrived later.
func TestRollingNoNewAudioNoNewRun(t *testing.T) {
	clock := newStubClock()
	fake := newScripted(clock, func(n int, _ context.Context) (string, error) {
		return fmt.Sprintf("text-%d", n), nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: 10 * time.Millisecond}, clock)
	chunks := make(chan []byte, 8)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	chunk := pcmBytes(1000, 100)
	chunks <- chunk
	select {
	case <-fake.called:
	case <-time.After(10 * time.Second):
		t.Fatal("first run never started")
	}
	select {
	case <-log.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("first partial never reported")
	}
	clock.advance(time.Second) // past Interval and duty
	chunks <- nil              // no new audio: must not start a run
	chunks <- chunk            // real audio: starts the second run
	select {
	case n := <-fake.called:
		if n != 2 {
			t.Fatalf("second call = %d, want 2", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second run never started")
	}
	close(chunks)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if got := fake.count(); got != 3 {
		t.Fatalf("inner calls = %d, want 3 (2 partials + final)", got)
	}
	if first, second := fake.dataLen(t, 1), fake.dataLen(t, 2); second <= first {
		t.Errorf("second snapshot (%d) must exceed the first (%d)", second, first)
	}
	if got, want := fake.dataLen(t, 2), 2*len(chunk); got != want {
		t.Errorf("second snapshot = %d PCM bytes, want %d", got, want)
	}
}

// TestRollingDuplicateTextReportedOnce: the one-in-flight gate proves the
// second run completed before the third starts, so equal texts collapse.
func TestRollingDuplicateTextReportedOnce(t *testing.T) {
	clock := newStubClock()
	release := make(chan struct{})
	fake := newScripted(clock, func(n int, ctx context.Context) (string, error) {
		switch n {
		case 1, 2:
			return "same", nil
		case 3:
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-release:
				return "third", nil
			}
		}
		return "FINAL", nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: 10 * time.Millisecond}, clock)
	chunks := make(chan []byte)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	chunks <- pcmBytes(1000, 100)
	select {
	case <-log.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("first partial never reported")
	}
	select {
	case n := <-fake.called:
		if n != 1 {
			t.Fatalf("first call = %d, want 1", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first run never started")
	}
	feedUntilCall(t, fake, chunks, clock, 2)
	feedUntilCall(t, fake, chunks, clock, 3)
	if got := log.texts(); len(got) != 1 || got[0] != "same" {
		t.Fatalf("partials = %q, want exactly [\"same\"]", got)
	}
	if !log.allFinalFalse() {
		t.Fatal("rolling partials must use final=false")
	}
	close(chunks)
	var out streamOutcome
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if out.err != nil || out.text != "FINAL" {
		t.Fatalf("final = (%q, %v), want (\"FINAL\", nil)", out.text, out.err)
	}
	if got := log.texts(); len(got) != 1 {
		t.Fatalf("partials after return = %q, want exactly [\"same\"]", got)
	}
}

// TestRollingEmptyPartialNotReported: an empty partial never reaches onPartial.
func TestRollingEmptyPartialNotReported(t *testing.T) {
	clock := newStubClock()
	fake := newScripted(clock, func(n int, ctx context.Context) (string, error) {
		switch n {
		case 1:
			return "", nil
		case 2:
			return "hi", nil
		case 3:
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "FINAL", nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: 10 * time.Millisecond}, clock)
	chunks := make(chan []byte)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	chunks <- pcmBytes(1000, 100)
	feedUntilCall(t, fake, chunks, clock, 1)
	feedUntilCall(t, fake, chunks, clock, 2)
	feedUntilCall(t, fake, chunks, clock, 3)
	if got := log.texts(); len(got) != 1 || got[0] != "hi" {
		t.Fatalf("partials = %q, want exactly [\"hi\"]", got)
	}
	close(chunks)
	select {
	case out := <-done:
		if out.err != nil || out.text != "FINAL" {
			t.Fatalf("final = (%q, %v), want (\"FINAL\", nil)", out.text, out.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
}

// TestRollingEndCancelsInflightRun: closing the channel cancels the in-flight
// run through its context, discards its late result, and returns the
// transcript of the whole audio.
func TestRollingEndCancelsInflightRun(t *testing.T) {
	clock := newStubClock()
	release := make(chan struct{})
	sawCancel := make(chan struct{}, 1)
	total := 0
	fake := newScripted(clock, func(n int, ctx context.Context) (string, error) {
		if n == 1 {
			select {
			case <-release:
				return "LATE", nil
			case <-ctx.Done():
				select {
				case sawCancel <- struct{}{}:
				default:
				}
				return "LATE", nil
			}
		}
		return "FINAL", nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: 10 * time.Millisecond}, clock)
	chunks := make(chan []byte, 8)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	chunk := pcmBytes(1000, 100)
	for i := 0; i < 3; i++ {
		chunks <- chunk
		total += len(chunk)
	}
	select {
	case <-fake.called:
	case <-time.After(10 * time.Second):
		t.Fatal("partial run never started")
	}
	close(chunks) // end while a partial run is in flight
	var out streamOutcome
	select {
	case out = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if out.err != nil || out.text != "FINAL" {
		t.Fatalf("final = (%q, %v), want (\"FINAL\", nil)", out.text, out.err)
	}
	select {
	case <-sawCancel:
	default:
		t.Fatal("in-flight run was not cancelled at the end of the stream")
	}
	if got := fake.dataLen(t, 2); got != total {
		t.Errorf("final snapshot = %d PCM bytes, want %d (whole audio)", got, total)
	}
	if got := log.texts(); len(got) != 0 {
		t.Fatalf("partials = %q, want none (late result discarded, final not reported)", got)
	}
	// The discarded run finishing late must not report anything either.
	close(release)
	select {
	case p := <-log.arrived:
		t.Fatalf("late partial %q reported after return", p.text)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRollingNoAudioCallsNothing: an empty stream returns ("", nil) without
// touching the engine.
func TestRollingNoAudioCallsNothing(t *testing.T) {
	clock := newStubClock()
	fake := newScripted(clock, nil)
	tr := newTestRolling(fake, RollingOptions{}, clock)
	chunks := make(chan []byte)
	close(chunks)
	text, err := tr.StreamTranscribe(context.Background(), chunks, func(string, bool) {
		t.Error("onPartial must not be called for an empty stream")
	})
	if err != nil || text != "" {
		t.Fatalf("empty stream = (%q, %v), want (\"\", nil)", text, err)
	}
	if got := fake.count(); got != 0 {
		t.Fatalf("inner calls = %d, want 0", got)
	}
}

// TestRollingFinalFailureKeepsLastPartial: a failing final run returns the
// last partial text plus the error.
func TestRollingFinalFailureKeepsLastPartial(t *testing.T) {
	sentinel := errors.New("engine blew up")
	t.Run("with partial", func(t *testing.T) {
		clock := newStubClock()
		fake := newScripted(clock, func(n int, _ context.Context) (string, error) {
			if n == 1 {
				return "part", nil
			}
			return "", sentinel
		})
		tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: 10 * time.Millisecond}, clock)
		chunks := make(chan []byte, 8)
		log := newPartialLog()
		done := runStream(tr, context.Background(), chunks, log)

		chunks <- pcmBytes(1000, 100)
		select {
		case <-log.arrived:
		case <-time.After(10 * time.Second):
			t.Fatal("partial never reported")
		}
		close(chunks)
		select {
		case out := <-done:
			if !errors.Is(out.err, sentinel) {
				t.Fatalf("err = %v, want %v", out.err, sentinel)
			}
			if out.text != "part" {
				t.Errorf("text = %q, want the last partial %q", out.text, "part")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("StreamTranscribe did not return after close")
		}
	})
	t.Run("without partial", func(t *testing.T) {
		clock := newStubClock()
		fake := newScripted(clock, func(_ int, _ context.Context) (string, error) {
			return "", sentinel
		})
		// MinAudio far above the audio sent: no partial runs, only the final.
		tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: time.Hour, Interval: time.Second}, clock)
		chunks := make(chan []byte, 8)
		log := newPartialLog()
		done := runStream(tr, context.Background(), chunks, log)

		chunks <- pcmBytes(1000, 100)
		close(chunks)
		select {
		case out := <-done:
			if !errors.Is(out.err, sentinel) {
				t.Fatalf("err = %v, want %v", out.err, sentinel)
			}
			if out.text != "" {
				t.Errorf("text = %q, want \"\" (no partial was reported)", out.text)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("StreamTranscribe did not return after close")
		}
		if got := fake.count(); got != 1 {
			t.Fatalf("inner calls = %d, want exactly the final run", got)
		}
	})
}

// TestRollingPartialFailureIgnored: a failed partial run is ignored; later
// partials and the final still work.
func TestRollingPartialFailureIgnored(t *testing.T) {
	clock := newStubClock()
	boom := errors.New("partial exploded")
	fake := newScripted(clock, func(n int, _ context.Context) (string, error) {
		if n == 1 {
			return "", boom
		}
		return "ok", nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: 10 * time.Millisecond}, clock)
	// Unbuffered: every send rendezvouses with one loop iteration, so the
	// clock can never run away from what the loop has consumed.
	chunks := make(chan []byte)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	// The failed first run reports nothing, so there is no event proving its
	// completion was processed: keep feeding audio until the second run
	// starts (feedUntilCall retries triggers consumed while gated).
	feedUntilCall(t, fake, chunks, clock, 2)
	select {
	case p := <-log.arrived:
		if p.text != "ok" {
			t.Fatalf("partial = %q, want \"ok\"", p.text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second partial never reported")
	}
	close(chunks)
	select {
	case out := <-done:
		if out.err != nil || out.text != "ok" {
			t.Fatalf("final = (%q, %v), want (\"ok\", nil)", out.text, out.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if got := fake.count(); got != 3 {
		t.Fatalf("inner calls = %d, want 3 (failed partial, partial, final)", got)
	}
	if got := log.texts(); len(got) != 1 {
		t.Fatalf("partials = %q, want exactly [\"ok\"]", got)
	}
	if first, second := fake.dataLen(t, 1), fake.dataLen(t, 2); second <= first {
		t.Errorf("second snapshot (%d) must exceed the first (%d)", second, first)
	}
}

// TestRollingContextCancelStopsPromptly: cancelling ctx returns the last
// partial plus ctx.Err() and cancels the in-flight run.
func TestRollingContextCancelStopsPromptly(t *testing.T) {
	clock := newStubClock()
	release := make(chan struct{})
	sawCancel := make(chan struct{}, 1)
	fake := newScripted(clock, func(n int, ctx context.Context) (string, error) {
		if n == 1 {
			return "early", nil
		}
		select {
		case <-release:
			return "never", nil
		case <-ctx.Done():
			select {
			case sawCancel <- struct{}{}:
			default:
			}
			return "", ctx.Err()
		}
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: 10 * time.Millisecond}, clock)
	ctx, cancel := context.WithCancel(context.Background())
	chunks := make(chan []byte, 8)
	log := newPartialLog()
	done := runStream(tr, ctx, chunks, log)

	chunks <- pcmBytes(1000, 100)
	select {
	case <-log.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("first partial never reported")
	}
	select {
	case n := <-fake.called:
		if n != 1 {
			t.Fatalf("first call = %d, want 1", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first run never started")
	}
	clock.advance(time.Second)
	chunks <- pcmBytes(1000, 100)
	select {
	case n := <-fake.called:
		if n != 2 {
			t.Fatalf("second call = %d, want 2", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second run never started")
	}
	cancel()
	select {
	case out := <-done:
		if !errors.Is(out.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", out.err)
		}
		if out.text != "early" {
			t.Errorf("text = %q, want the last partial %q", out.text, "early")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not stop promptly on ctx cancel")
	}
	select {
	case <-sawCancel:
	default:
		t.Fatal("in-flight run was not cancelled")
	}
	if got := log.texts(); len(got) != 1 {
		t.Fatalf("partials = %q, want exactly [\"early\"]", got)
	}
}

// TestRollingConcurrentUse exercises chunks, partials and the end arriving
// from different goroutines under -race.
func TestRollingConcurrentUse(t *testing.T) {
	clock := newStubClock()
	var mu sync.Mutex
	runCount := 0
	fake := newScripted(clock, func(n int, _ context.Context) (string, error) {
		mu.Lock()
		runCount++
		mu.Unlock()
		return fmt.Sprintf("text-%d", n), nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 5 * time.Millisecond, Interval: time.Millisecond}, clock)
	chunks := make(chan []byte, 256)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	var wg sync.WaitGroup
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				chunks <- pcmBytes(1000, 5)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 400; i++ {
			clock.advance(5 * time.Millisecond)
		}
	}()
	wg.Wait()
	close(chunks)
	var out streamOutcome
	select {
	case out = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if out.err != nil {
		t.Fatalf("StreamTranscribe: %v", out.err)
	}
	if out.text == "" {
		t.Error("final text must not be empty after 1000 ms of audio")
	}
	if !log.allFinalFalse() {
		t.Error("rolling partials must use final=false")
	}
	mu.Lock()
	defer mu.Unlock()
	if runCount == 0 {
		t.Error("engine never ran")
	}
}

// TestStreamingModelDir maps installed directory names to streaming or not.
func TestStreamingModelDir(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/dl/model-streaming-zipformer-en-kroko-2025-08-06", true},
		{"/dl/model-nemo-parakeet-tdt-0.6b-v3-int8", false},
		{"/dl/model-nemo-streaming-fast-conformer-transducer-en-80ms-int8", true},
		{"/opt/my-model", true},
		{"", true},
		{"/dl/model-nemo-parakeet-tdt-0.6b-v3-int8/", false},
		{"/dl/model-streaming-zipformer-en-kroko-2025-08-06/", true},
	}
	for _, tc := range cases {
		if got := StreamingModelDir(tc.path); got != tc.want {
			t.Errorf("StreamingModelDir(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestRollingTranscribeDelegates: Transcribe passes through to the inner
// transcriber, and the rolling transcriber reports the configured rate.
func TestRollingTranscribeDelegates(t *testing.T) {
	audio := wavFixture()
	clock := newStubClock()
	fake := newScripted(clock, func(_ int, _ context.Context) (string, error) { return "delegated", nil })
	tr := newTestRolling(fake, RollingOptions{SampleRate: 8000}, clock)
	text, err := tr.Transcribe(context.Background(), audio)
	if err != nil || text != "delegated" {
		t.Fatalf("Transcribe = (%q, %v), want (\"delegated\", nil)", text, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 || string(fake.calls[0]) != string(audio) {
		t.Fatal("Transcribe must pass the audio through untouched")
	}
	if got := RequiredSampleRate(tr); got != 8000 {
		t.Errorf("RequiredSampleRate = %d, want the configured 8000", got)
	}
	if got := RequiredSampleRate(NewRollingTranscriber(fake, RollingOptions{})); got != DefaultSampleRate {
		t.Errorf("default RequiredSampleRate = %d, want %d", got, DefaultSampleRate)
	}
}

// TestRollingFinalFlagNeverSet: every partial the rolling transcriber emits
// uses final=false — the TUI replaces the live region wholesale and ignores
// the flag.
func TestRollingFinalFlagNeverSet(t *testing.T) {
	clock := newStubClock()
	fake := newScripted(clock, func(n int, _ context.Context) (string, error) {
		return fmt.Sprintf("v%d", n), nil
	})
	tr := newTestRolling(fake, RollingOptions{SampleRate: 1000, MinAudio: 10 * time.Millisecond, Interval: 10 * time.Millisecond}, clock)
	chunks := make(chan []byte, 8)
	log := newPartialLog()
	done := runStream(tr, context.Background(), chunks, log)

	chunks <- pcmBytes(1000, 100)
	select {
	case <-log.arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("partial never reported")
	}
	close(chunks)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("StreamTranscribe did not return after close")
	}
	if !log.allFinalFalse() {
		t.Fatalf("partials = %+v, want final=false on all of them", log.calls)
	}
}
