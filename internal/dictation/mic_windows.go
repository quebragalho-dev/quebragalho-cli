//go:build windows

package dictation

import (
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native microphone capture on Windows through the winmm waveIn API, with no
// cgo. The default device (WAVE_MAPPER) opens with CALLBACK_EVENT: a worker
// goroutine waits on the event and copies finished buffers into a bounded
// FIFO. Buffers and headers stay pinned for the life of the stream.

const (
	windowsWaveMapper    = 0xFFFFFFFF
	windowsCallbackEvent = 0x00050000
	windowsWhdrDone      = 0x1
	windowsBufCount      = 4
)

var (
	winmmDLL = windows.NewLazySystemDLL("winmm.dll")

	winmmGetNumDevs      = winmmDLL.NewProc("waveInGetNumDevs")
	winmmOpen            = winmmDLL.NewProc("waveInOpen")
	winmmPrepareHeader   = winmmDLL.NewProc("waveInPrepareHeader")
	winmmUnprepareHeader = winmmDLL.NewProc("waveInUnprepareHeader")
	winmmAddBuffer       = winmmDLL.NewProc("waveInAddBuffer")
	winmmStart           = winmmDLL.NewProc("waveInStart")
	winmmReset           = winmmDLL.NewProc("waveInReset")
	winmmClose           = winmmDLL.NewProc("waveInClose")
)

// waveFormatEx mirrors WAVEFORMATEX for 16-bit mono PCM.
type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	Size           uint16
}

// waveHeader mirrors WAVEHDR. It must stay 48 bytes on amd64
// (TestWaveHeaderSize pins the layout).
type waveHeader struct {
	Data          uintptr
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

func winmmStatus(call string, r1 uintptr) error {
	if r1 != 0 {
		return fmt.Errorf("%s failed with code %d", call, r1)
	}
	return nil
}

// windowsMicStream is one open Windows microphone.
type windowsMicStream struct {
	handle uintptr // HWAVEIN
	event  windows.Handle
	bufs   [][]byte
	hdrs   []waveHeader
	pinner runtime.Pinner
	audio  *micBuffer

	closing atomic.Bool
	wg      sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

func (s *windowsMicStream) Read(p []byte) (int, error) {
	return s.audio.read(p)
}

func (s *windowsMicStream) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.shutdown()
	})
	return s.closeErr
}

func (s *windowsMicStream) shutdown() error {
	s.closing.Store(true)
	r1, _, _ := winmmReset.Call(s.handle)
	first := winmmStatus("waveInReset", r1)
	s.wg.Wait()
	for i := range s.hdrs {
		r1, _, _ := winmmUnprepareHeader.Call(s.handle, uintptr(unsafe.Pointer(&s.hdrs[i])), uintptr(unsafe.Sizeof(s.hdrs[i])))
		if err := winmmStatus("waveInUnprepareHeader", r1); err != nil && first == nil {
			first = err
		}
	}
	r1, _, _ = winmmClose.Call(s.handle)
	if err := winmmStatus("waveInClose", r1); err != nil && first == nil {
		first = err
	}
	_ = windows.CloseHandle(s.event)
	s.pinner.Unpin()
	s.audio.close()
	return first
}

// addBufferLocked hands one header back to the device. The caller must have
// checked closing; the worker is the only caller.
func (s *windowsMicStream) addBuffer(i int) {
	r1, _, _ := winmmAddBuffer.Call(s.handle, uintptr(unsafe.Pointer(&s.hdrs[i])), uintptr(unsafe.Sizeof(s.hdrs[i])))
	_ = winmmStatus("waveInAddBuffer", r1)
}

func (s *windowsMicStream) loop() {
	defer s.wg.Done()
	for {
		if s.closing.Load() {
			return
		}
		// Poll with a short timeout so Close (which sets closing and resets
		// the device) never waits on a silent microphone.
		_, _ = windows.WaitForSingleObject(s.event, 100)
		if s.closing.Load() {
			return
		}
		for i := range s.hdrs {
			if s.hdrs[i].Flags&windowsWhdrDone == 0 {
				continue
			}
			s.hdrs[i].Flags &^= windowsWhdrDone
			n := s.hdrs[i].BytesRecorded
			if n > 0 && !s.closing.Load() {
				if int(n) > len(s.bufs[i]) {
					n = uint32(len(s.bufs[i]))
				}
				s.audio.push(s.bufs[i][:n])
			}
			if !s.closing.Load() {
				s.hdrs[i].BytesRecorded = 0
				s.addBuffer(i)
			}
		}
	}
}

func openNativeMic(sampleRate int) (micStream, error) {
	r1, _, _ := winmmGetNumDevs.Call()
	if r1 == 0 {
		return nil, fmt.Errorf("nenhum microfone encontrado")
	}
	format := waveFormatEx{
		FormatTag:      1, // PCM
		Channels:       1,
		SamplesPerSec:  uint32(sampleRate),
		AvgBytesPerSec: uint32(sampleRate * 2),
		BlockAlign:     2,
		BitsPerSample:  16,
		Size:           0,
	}
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateEvent failed: %w", err)
	}
	s := &windowsMicStream{event: event, audio: newMicBuffer(micBufferCapBytes(sampleRate, micBufferCapHold))}
	var handle uintptr
	r1, _, _ = winmmOpen.Call(
		uintptr(unsafe.Pointer(&handle)),
		windowsWaveMapper,
		uintptr(unsafe.Pointer(&format)),
		uintptr(event),
		0,
		windowsCallbackEvent,
	)
	if err := winmmStatus("waveInOpen", r1); err != nil {
		_ = windows.CloseHandle(event)
		return nil, err
	}
	s.handle = handle
	// 50ms buffers, matching the recorder's chunk size.
	bufSize := sampleRate * 2 / int(time.Second/chunkInterval)
	if bufSize <= 0 {
		bufSize = 1
	}
	s.bufs = make([][]byte, windowsBufCount)
	s.hdrs = make([]waveHeader, windowsBufCount)
	prepared := 0
	fail := func(err error) (micStream, error) {
		for i := 0; i < prepared; i++ {
			r1, _, _ := winmmUnprepareHeader.Call(s.handle, uintptr(unsafe.Pointer(&s.hdrs[i])), uintptr(unsafe.Sizeof(s.hdrs[i])))
			_ = winmmStatus("waveInUnprepareHeader", r1)
		}
		r1, _, _ := winmmClose.Call(s.handle)
		_ = winmmStatus("waveInClose", r1)
		_ = windows.CloseHandle(s.event)
		s.pinner.Unpin()
		return nil, err
	}
	for i := range s.bufs {
		s.bufs[i] = make([]byte, bufSize)
		s.pinner.Pin(&s.bufs[i][0])
		s.pinner.Pin(&s.hdrs[i])
		s.hdrs[i] = waveHeader{
			Data:         uintptr(unsafe.Pointer(&s.bufs[i][0])),
			BufferLength: uint32(bufSize),
		}
		r1, _, _ := winmmPrepareHeader.Call(s.handle, uintptr(unsafe.Pointer(&s.hdrs[i])), uintptr(unsafe.Sizeof(s.hdrs[i])))
		if err := winmmStatus("waveInPrepareHeader", r1); err != nil {
			return fail(err)
		}
		prepared++
		r1, _, _ = winmmAddBuffer.Call(s.handle, uintptr(unsafe.Pointer(&s.hdrs[i])), uintptr(unsafe.Sizeof(s.hdrs[i])))
		if err := winmmStatus("waveInAddBuffer", r1); err != nil {
			return fail(err)
		}
	}
	r1, _, _ = winmmStart.Call(s.handle)
	if err := winmmStatus("waveInStart", r1); err != nil {
		return fail(err)
	}
	s.wg.Add(1)
	go s.loop()
	return s, nil
}
