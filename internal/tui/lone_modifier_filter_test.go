package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// loneModifierNULPress is the key press the input decoder builds from the NUL a
// Windows console reports for a lone Ctrl, Shift or Alt press.
func loneModifierNULPress() tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl})
}

func TestProgramEventFilterDropsLoneModifierNULOnWindows(t *testing.T) {
	m := newModel(context.Background(), Options{ProviderName: "tokenrouter"})
	filter := programEventFilter("windows")

	if got := filter(m, loneModifierNULPress()); got != nil {
		t.Fatalf("lone modifier NUL passed through on windows: %#v", got)
	}
	locked := tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl | tea.ModNumLock})
	if got := filter(m, locked); got != nil {
		t.Fatalf("lone modifier NUL with NumLock passed through on windows: %#v", got)
	}
}

func TestProgramEventFilterKeepsRealKeysOnWindows(t *testing.T) {
	m := newModel(context.Background(), Options{ProviderName: "tokenrouter"})
	filter := programEventFilter("windows")

	for name, msg := range map[string]tea.Msg{
		"space":         testKey(tea.KeySpace),
		"shift+space":   tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModShift, Text: " "}),
		"ctrl+c":        testKeyCtrl('c'),
		"ctrl+alt+spc":  tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl | tea.ModAlt}),
		"release":       tea.KeyReleaseMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl}),
		"letter":        testKeyText("a"),
		"window resize": tea.WindowSizeMsg{Width: 80, Height: 24},
	} {
		if got := filter(m, msg); got == nil {
			t.Errorf("%s was dropped on windows", name)
		}
	}
}

func TestProgramEventFilterDropsLoneModifierNULWithVoiceModeOnWindows(t *testing.T) {
	m := newModel(context.Background(), Options{ProviderName: "tokenrouter"})
	m.dictation.voiceModeEnabled = true

	if got := programEventFilter("windows")(m, loneModifierNULPress()); got != nil {
		t.Fatalf("lone modifier NUL passed through on windows with voice mode on: %#v", got)
	}
	locked := tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl | tea.ModNumLock | tea.ModCapsLock})
	if got := programEventFilter("windows")(m, locked); got != nil {
		t.Fatalf("lone modifier NUL with NumLock/CapsLock passed through on windows with voice mode on: %#v", got)
	}
}

func TestProgramEventFilterKeepsCtrlSpaceForVoiceModeOffWindows(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		m := newModel(context.Background(), Options{ProviderName: "tokenrouter"})
		m.dictation.voiceModeEnabled = true

		if got := programEventFilter(goos)(m, loneModifierNULPress()); got == nil {
			t.Errorf("ctrl+space was dropped on %s with voice mode on", goos)
		}
	}
}

// On Windows with voice mode on, the lone-modifier NUL must vanish before
// Update: typing after it leaves a clean composer and starts no recording.
func TestWindowsLoneModifierNULWithVoiceModeLeavesComposerAlone(t *testing.T) {
	m := newModel(context.Background(), Options{ProviderName: "tokenrouter"})
	m.dictation = batchOnlyController()
	m.dictation.voiceModeEnabled = true
	filter := programEventFilter("windows")

	var current tea.Model = m
	filtered := filter(current, loneModifierNULPress())
	if filtered != nil {
		t.Fatalf("lone modifier NUL passed through on windows with voice mode on: %#v", filtered)
	}

	updated, _ := current.Update(testKeyText("a"))
	got := updated.(model)
	if text := got.composerValue(); text != "a" {
		t.Fatalf("composer = %q, want %q", text, "a")
	}
	if got.dictation.phase != dictIdle {
		t.Fatalf("lone modifier NUL started a recording on windows: phase=%d", got.dictation.phase)
	}
}

func TestVoiceCaptureUsageForDescribesGesturesPerSystem(t *testing.T) {
	if got := voiceCaptureUsageFor("windows"); got != "segure Espaço por 2s para ditar" {
		t.Errorf("windows usage = %q", got)
	}
	for _, goos := range []string{"linux", "darwin"} {
		if got := voiceCaptureUsageFor(goos); got != "segure Espaço por 2s para ditar; Ctrl+Space também grava" {
			t.Errorf("%s usage = %q", goos, got)
		}
	}
}

func TestProgramEventFilterKeepsCtrlSpaceOffWindows(t *testing.T) {
	m := newModel(context.Background(), Options{ProviderName: "tokenrouter"})

	for _, goos := range []string{"linux", "darwin"} {
		if got := programEventFilter(goos)(m, loneModifierNULPress()); got == nil {
			t.Errorf("ctrl+space was dropped on %s", goos)
		}
	}
}

// A Windows console reports the Ctrl of each Ctrl+C as its own NUL first. That
// NUL must neither type a space into the composer nor cancel the armed exit.
func TestWindowsCtrlCExitsDespiteLoneModifierNUL(t *testing.T) {
	var current tea.Model = newModel(context.Background(), Options{ProviderName: "tokenrouter"})
	filter := programEventFilter("windows")

	for _, msg := range []tea.Msg{loneModifierNULPress(), testKeyCtrl('c'), loneModifierNULPress(), testKeyCtrl('c')} {
		filtered := filter(current, msg)
		if filtered == nil {
			continue
		}
		current, _ = current.Update(filtered)
	}

	got := current.(model)
	if text := got.composerValue(); text != "" {
		t.Fatalf("composer = %q, want empty", text)
	}
	if !got.exiting {
		t.Fatal("second Ctrl+C did not exit")
	}
}
