package dictation

import (
	"sync"
	"time"
)

// warmMicTTL is how long a warmed microphone waits to be used before it is
// closed again: longer than the hold that warms it, short enough that an
// abandoned hold releases the device quickly.
const warmMicTTL = 3 * time.Second

// warmMic is the microphone opened ahead of a recording. The stream buffers
// the audio it captures meanwhile, so the recording that takes it over starts
// with what was said just before it began instead of losing the first words
// to the device's start-up time.
var warmMic struct {
	mu     sync.Mutex
	stream micStream
	rate   int
	gen    int
}

// WarmMicrophone opens the native microphone ahead of a recording the user is
// about to start (the Space hold is under way). The next recording at the
// same sample rate takes the stream over; otherwise it closes by itself after
// warmMicTTL. It does nothing where recordings do not use the native
// microphone, and a failed open is ignored: the recording opens its own.
func WarmMicrophone(opts RecorderOptions) {
	open := opts.openMic
	if open == nil {
		open = openNativeMic
	}
	opts = opts.withDefaults()
	if !opts.useNativeMic() {
		return
	}
	warmMic.mu.Lock()
	defer warmMic.mu.Unlock()
	if warmMic.stream != nil {
		return
	}
	stream, err := open(opts.SampleRate)
	if err != nil {
		return
	}
	warmMic.stream, warmMic.rate = stream, opts.SampleRate
	warmMic.gen++
	gen := warmMic.gen
	time.AfterFunc(warmMicTTL, func() {
		warmMic.mu.Lock()
		defer warmMic.mu.Unlock()
		if warmMic.stream != nil && warmMic.gen == gen {
			_ = warmMic.stream.Close()
			warmMic.stream = nil
		}
	})
}

// openMicPreferWarm hands over the warmed microphone when one is waiting at
// this sample rate, else opens the device.
func openMicPreferWarm(sampleRate int) (micStream, error) {
	warmMic.mu.Lock()
	stream, rate := warmMic.stream, warmMic.rate
	warmMic.stream = nil
	warmMic.mu.Unlock()
	if stream != nil {
		if rate == sampleRate {
			return stream, nil
		}
		_ = stream.Close()
	}
	return openNativeMic(sampleRate)
}
