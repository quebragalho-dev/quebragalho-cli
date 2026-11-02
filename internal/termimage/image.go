package termimage

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"sync"
	"time"
)

// State selects which frame sequence an Animation plays. Attachments only
// ever stage single-frame thumbnails, which rest on Idle.
type State string

const (
	Idle State = "idle"
)

// Animation is a named set of frame sequences shared by the inline image
// renderers (attachment thumbnails today). Frames are images; encoding to
// PNG happens lazily per frame and is cached.
type Animation struct {
	frames     map[State][]image.Image
	durations  map[State][]time.Duration
	loopStarts map[State]int
	pngMu      sync.Mutex
	pngCache   map[frameCacheKey][]byte
}

type frameCacheKey struct {
	state State
	index int
}

func (a *Animation) Frame(state State, phase int) image.Image {
	frame, _ := a.frame(state, phase)
	return frame
}

func (a *Animation) FrameDelay(state State, phase int) time.Duration {
	if a == nil {
		return 0
	}
	frames := a.frames[state]
	if len(frames) == 0 {
		state = Idle
		frames = a.frames[state]
	}
	if len(frames) == 0 {
		return 0
	}
	durations := a.durations[state]
	if len(durations) != len(frames) {
		return 0
	}
	return durations[a.frameIndex(state, phase, len(frames))]
}

func (a *Animation) frame(state State, phase int) (image.Image, frameCacheKey) {
	if a == nil {
		return nil, frameCacheKey{}
	}
	frames := a.frames[state]
	if len(frames) == 0 {
		state = Idle
		frames = a.frames[Idle]
	}
	if len(frames) == 0 {
		return nil, frameCacheKey{}
	}
	index := a.frameIndex(state, phase, len(frames))
	return frames[index], frameCacheKey{state: state, index: index}
}

func (a *Animation) frameIndex(state State, phase, frameCount int) int {
	if frameCount < 1 {
		return 0
	}
	if phase < 0 {
		phase = -phase
	}
	loopStart, configured := a.loopStarts[state]
	if !configured {
		return phase % frameCount
	}
	if phase < frameCount {
		return phase
	}
	if loopStart >= 0 && loopStart < frameCount {
		return loopStart + (phase-loopStart)%(frameCount-loopStart)
	}
	return frameCount - 1
}

func (a *Animation) framePNG(state State, phase int) ([]byte, frameCacheKey, error) {
	frame, key := a.frame(state, phase)
	if frame == nil {
		return nil, key, fmt.Errorf("image has no frames")
	}
	a.pngMu.Lock()
	defer a.pngMu.Unlock()
	if cached := a.pngCache[key]; len(cached) > 0 {
		return cached, key, nil
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, frame); err != nil {
		return nil, key, fmt.Errorf("encode image frame: %w", err)
	}
	if a.pngCache == nil {
		a.pngCache = make(map[frameCacheKey][]byte)
	}
	value := append([]byte(nil), encoded.Bytes()...)
	a.pngCache[key] = value
	return value, key, nil
}

// ThumbnailAnimation wraps a single decoded image as a still animation for
// the inline attachment preview.
func ThumbnailAnimation(imageValue image.Image) (*Animation, error) {
	if imageValue == nil || imageValue.Bounds().Empty() {
		return nil, fmt.Errorf("thumbnail image is empty")
	}
	return &Animation{frames: map[State][]image.Image{Idle: {imageValue}}}, nil
}
