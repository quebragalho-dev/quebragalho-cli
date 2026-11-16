package tui

import (
	tea "charm.land/bubbletea/v2"
)

// programEventFilter is the single event filter of the main TUI program.
func programEventFilter(goos string) func(tea.Model, tea.Msg) tea.Msg {
	mouse := mouseEventFilter()
	return func(current tea.Model, msg tea.Msg) tea.Msg {
		if goos == "windows" && loneModifierNUL(current, msg) {
			return nil
		}
		return mouse(current, msg)
	}
}

// loneModifierNUL reports the ctrl+space key press a Windows console produces
// for a lone Ctrl, Shift or Alt press. In VT input mode the console delivers
// the modifier as a key-down record with character 0, and the input decoder
// reads that NUL as ctrl+space. Left alone it types a space into the composer
// and cancels the armed Ctrl+C exit. A real Ctrl+Space arrives the same way,
// so voice mode, whose capture chord it is, keeps the key.
func loneModifierNUL(current tea.Model, msg tea.Msg) bool {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok || !voiceCaptureKey(press) {
		return false
	}
	m, ok := current.(model)
	return !ok || !m.dictation.voiceModeEnabled
}
