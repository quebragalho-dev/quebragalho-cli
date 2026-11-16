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

func TestProgramEventFilterKeepsCtrlSpaceForVoiceModeOnWindows(t *testing.T) {
	m := newModel(context.Background(), Options{ProviderName: "tokenrouter"})
	m.dictation.voiceModeEnabled = true

	if got := programEventFilter("windows")(m, loneModifierNULPress()); got == nil {
		t.Fatal("voice mode lost its Ctrl+Space capture chord on windows")
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
