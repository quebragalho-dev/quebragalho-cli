//go:build darwin

package dictation

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Native microphone capture on macOS through AudioToolbox's AudioQueue input,
// with no cgo (purego). One stream per open: the callback routes through an
// integer handle, never a Go pointer.

// audioStreamBasicDescription mirrors AudioStreamBasicDescription for 16-bit
// mono PCM.
type audioStreamBasicDescription struct {
	SampleRate       float64
	FormatID         uint32
	FormatFlags      uint32
	BytesPerPacket   uint32
	FramesPerPacket  uint32
	BytesPerFrame    uint32
	ChannelsPerFrame uint32
	BitsPerChannel   uint32
	Reserved         uint32
}

// audioQueueBuffer mirrors the AudioQueueBuffer header: capacity, data
// pointer, and the currently filled byte count.
type audioQueueBuffer struct {
	Capacity  uint32
	_         uint32
	Data      unsafe.Pointer
	ByteSize  uint32
	_         uint32
	UserData  unsafe.Pointer
	DescCap   uint32
	_         uint32
	Descs     unsafe.Pointer
	DescCount uint32
}

const (
	darwinFormatLinearPCM  = 0x6C70636D // 'lpcm'
	darwinFormatIsSigned   = 4
	darwinFormatIsPacked   = 8
	darwinInputBufferCount = 3
)

var (
	darwinLoadOnce sync.Once
	darwinLoadErr  error

	darwinNewInput    func(format *audioStreamBasicDescription, cb uintptr, user uintptr, runLoop uintptr, mode uintptr, flags uint32, out *uintptr) int32
	darwinAllocBuffer func(q uintptr, size uint32, out *uintptr) int32
	darwinEnqueue     func(q uintptr, buf uintptr, n uint32, descs uintptr) int32
	darwinStart       func(q uintptr, ts uintptr) int32
	darwinStop        func(q uintptr, immediate bool) int32
	darwinDispose     func(q uintptr, immediate bool) int32
)

// loadDarwinAudioToolbox opens the framework once per process. A failed
// Dlopen or a missing symbol is an error from openNativeMic, never a panic.
func loadDarwinAudioToolbox() error {
	darwinLoadOnce.Do(func() {
		lib, err := purego.Dlopen("/System/Library/Frameworks/AudioToolbox.framework/AudioToolbox", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			darwinLoadErr = fmt.Errorf("opening AudioToolbox: %w", err)
			return
		}
		funcs := []struct {
			name string
			fn   any
		}{
			{"AudioQueueNewInput", &darwinNewInput},
			{"AudioQueueAllocateBuffer", &darwinAllocBuffer},
			{"AudioQueueEnqueueBuffer", &darwinEnqueue},
			{"AudioQueueStart", &darwinStart},
			{"AudioQueueStop", &darwinStop},
			{"AudioQueueDispose", &darwinDispose},
		}
		for _, f := range funcs {
			addr, err := purego.Dlsym(lib, f.name)
			if err != nil {
				darwinLoadErr = fmt.Errorf("loading %s: %w", f.name, err)
				return
			}
			purego.RegisterFunc(f.fn, addr)
		}
	})
	return darwinLoadErr
}

func darwinStatus(call string, st int32) error {
	if st != 0 {
		return fmt.Errorf("%s failed with status %d", call, st)
	}
	return nil
}

// darwinStreams routes the single process-wide input callback to its stream.
// The AudioQueue user-data slot carries the integer handle, never a Go
// pointer. Entries are removed on Close so no callback touches a stream after
// Close returns.
var (
	darwinStreams      sync.Map // uintptr(handle) -> *darwinMicStream
	darwinNextHandle   atomic.Uintptr
	darwinCallbackOnce sync.Once
	darwinCallbackFn   uintptr
)

func darwinInputCallback() uintptr {
	darwinCallbackOnce.Do(func() {
		darwinCallbackFn = purego.NewCallback(darwinInputProc)
	})
	return darwinCallbackFn
}

// darwinInputProc is the AudioQueueInputCallback: copy the bytes, re-enqueue
// the buffer, never block. The buffer arrives as a pointer so no
// uintptr-to-pointer conversion is needed.
func darwinInputProc(user, aq uintptr, buf *audioQueueBuffer, _ uintptr, _ uint32, _ uintptr) {
	v, ok := darwinStreams.Load(user)
	if !ok {
		return
	}
	s := v.(*darwinMicStream)
	if s.stopping.Load() {
		return
	}
	if buf.ByteSize > 0 {
		s.audio.push(unsafe.Slice((*byte)(buf.Data), buf.ByteSize))
	}
	// Re-enqueue so capture continues; a failure here (queue stopping) just
	// ends this buffer's cycle. Never blocks.
	_ = darwinStatus("AudioQueueEnqueueBuffer", darwinEnqueue(aq, uintptr(unsafe.Pointer(buf)), 0, 0))
}

// darwinMicStream is one open macOS microphone.
type darwinMicStream struct {
	handle   uintptr
	queue    uintptr
	audio    *micBuffer
	stopping atomic.Bool

	closeOnce sync.Once
	closeErr  error
}

func (s *darwinMicStream) Read(p []byte) (int, error) {
	return s.audio.read(p)
}

func (s *darwinMicStream) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.shutdown()
	})
	return s.closeErr
}

func (s *darwinMicStream) shutdown() error {
	s.stopping.Store(true)
	// Remove first so no callback touches the stream after Close returns;
	// in-flight callbacks see stopping or a closed buffer and drop out.
	darwinStreams.Delete(s.handle)
	var first error
	if err := darwinStatus("AudioQueueStop", darwinStop(s.queue, true)); err != nil && first == nil {
		first = err
	}
	if err := darwinStatus("AudioQueueDispose", darwinDispose(s.queue, true)); err != nil && first == nil {
		first = err
	}
	s.audio.close()
	return first
}

func openNativeMic(sampleRate int) (micStream, error) {
	if err := loadDarwinAudioToolbox(); err != nil {
		return nil, err
	}
	format := audioStreamBasicDescription{
		SampleRate:       float64(sampleRate),
		FormatID:         darwinFormatLinearPCM,
		FormatFlags:      darwinFormatIsSigned | darwinFormatIsPacked,
		BytesPerPacket:   2,
		FramesPerPacket:  1,
		BytesPerFrame:    2,
		ChannelsPerFrame: 1,
		BitsPerChannel:   16,
	}
	handle := darwinNextHandle.Add(1)
	s := &darwinMicStream{handle: handle, audio: newMicBuffer(micBufferCapBytes(sampleRate, micBufferCapHold))}
	darwinStreams.Store(handle, s)
	var q uintptr
	if err := darwinStatus("AudioQueueNewInput", darwinNewInput(&format, darwinInputCallback(), handle, 0, 0, 0, &q)); err != nil {
		darwinStreams.Delete(handle)
		return nil, err
	}
	s.queue = q
	// 50ms buffers, matching the recorder's chunk size.
	bufSize := uint32(sampleRate * 2 / int(time.Second/chunkInterval))
	for i := 0; i < darwinInputBufferCount; i++ {
		var buf uintptr
		if err := darwinStatus("AudioQueueAllocateBuffer", darwinAllocBuffer(q, bufSize, &buf)); err != nil {
			_ = s.Close()
			return nil, err
		}
		if err := darwinStatus("AudioQueueEnqueueBuffer", darwinEnqueue(q, buf, 0, 0)); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	if err := darwinStatus("AudioQueueStart", darwinStart(q, 0)); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}
