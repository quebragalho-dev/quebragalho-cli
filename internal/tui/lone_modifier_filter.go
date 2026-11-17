package tui

import (
	"runtime"

	tea "charm.land/bubbletea/v2"
)

// programEventFilter is the single event filter of the main TUI program.
func programEventFilter(goos string) func(tea.Model, tea.Msg) tea.Msg {
	mouse := mouseEventFilter()
	return func(current tea.Model, msg tea.Msg) tea.Msg {
		if goos == "windows" && loneModifierNUL(msg) {
			return nil
		}
		return mouse(current, msg)
	}
}

// loneModifierNUL reports the ctrl+space key press a Windows console produces
// for a lone Ctrl, Shift or Alt press. In VT input mode the console delivers
// the modifier as a key-down record with character 0, and the input decoder
// reads that NUL as ctrl+space. Left alone it types a space into the composer,
// cancels the armed Ctrl+C exit and, in voice mode, starts a recording on
// every Shift. A real Ctrl+Space arrives the same way and cannot be told
// apart, so on Windows Ctrl+Space is not a key QG uses: voice mode records
// with the Space hold only (see voiceCaptureUsageFor).
func loneModifierNUL(msg tea.Msg) bool {
	press, ok := msg.(tea.KeyPressMsg)
	return ok && voiceCaptureKey(press)
}

// voiceCaptureUsageFor describes the voice-mode gestures available on goos.
// Windows has no Ctrl+Space (see loneModifierNUL).
func voiceCaptureUsageFor(goos string) string {
	if goos == "windows" {
		return "segure Espaço por 2s para ditar"
	}
	return "segure Espaço por 2s para ditar; Ctrl+Space também grava"
}

var voiceCaptureUsage = voiceCaptureUsageFor(runtime.GOOS)
