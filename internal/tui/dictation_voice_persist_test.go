package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/dictation"
)

// voicePersistFactory is the transcriber factory voice-mode tests run with: it
// makes dictation "available" without exec'ing a real capture tool.
func voicePersistFactory(config.STTConfig, bool) (Transcriber, bool, error) {
	return fakeTUITranscriber{}, false, nil
}

func TestVoiceModeRestoresFromSavedConfig(t *testing.T) {
	m := newModel(t.Context(), Options{
		STT:                       config.STTConfig{VoiceMode: true},
		BuildDictationTranscriber: voicePersistFactory,
	})
	if !m.dictation.voiceModeEnabled {
		t.Fatal("voice mode should start on when stt.voiceMode is saved")
	}
	if m.voiceModeIndicator() == "" {
		t.Error("voice mode indicator should render when the mode starts on")
	}
}

func TestVoiceModeStaysOffWithoutFactory(t *testing.T) {
	m := newModel(t.Context(), Options{
		STT: config.STTConfig{VoiceMode: true},
	})
	if m.dictation.voiceModeEnabled {
		t.Error("voice mode must stay off without a transcriber factory: nothing to turn on")
	}
}

func TestVoiceModeDefaultsOff(t *testing.T) {
	m := newModel(t.Context(), Options{BuildDictationTranscriber: voicePersistFactory})
	if m.dictation.voiceModeEnabled {
		t.Error("voice mode should default off when nothing is saved")
	}
}

func readVoicePersistFile(t *testing.T, path string) (voiceMode *bool, theme string, raw string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var doc struct {
		Preferences struct {
			Theme string `json:"theme"`
		} `json:"preferences"`
		STT map[string]json.RawMessage `json:"stt"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	theme = doc.Preferences.Theme
	if rawVoice, ok := doc.STT["voiceMode"]; ok {
		var enabled bool
		if err := json.Unmarshal(rawVoice, &enabled); err != nil {
			t.Fatalf("stt.voiceMode is not a bool: %v", err)
		}
		voiceMode = &enabled
	}
	return voiceMode, theme, string(data)
}

// voiceTestFakes wires the capture-program and microphone fakes a model built
// through newDictationController needs: CI machines have no microphone and may
// lack the capture program, and /voice must not depend on either.
func voiceTestFakes(m model) model {
	m.dictation.lookPath = func(string) (string, error) { return "/usr/bin/arecord", nil }
	m.dictation.micCheck = func(context.Context) (dictation.MicCheck, error) {
		return dictation.MicCheck{}, nil
	}
	return m
}

func TestToggleVoiceModeSavesToUserConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"preferences":{"theme":"dark"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := voiceTestFakes(newModel(t.Context(), Options{UserConfigPath: path, BuildDictationTranscriber: voicePersistFactory}))

	on, _ := m.toggleVoiceMode()
	if !on.dictation.voiceModeEnabled {
		t.Fatal("first /voice should enable voice mode")
	}
	voiceMode, theme, _ := readVoicePersistFile(t, path)
	if voiceMode == nil || !*voiceMode {
		t.Error("toggling on should write stt.voiceMode:true to the user config")
	}
	if theme != "dark" {
		t.Errorf("toggling on must keep the theme, got %q", theme)
	}

	off, _ := on.toggleVoiceMode()
	if off.dictation.voiceModeEnabled {
		t.Fatal("second /voice should disable voice mode")
	}
	voiceMode, theme, raw := readVoicePersistFile(t, path)
	if voiceMode != nil {
		t.Errorf("toggling off should drop the voiceMode key, got file %s", raw)
	}
	if theme != "dark" {
		t.Errorf("toggling off must keep the theme, got %q", theme)
	}
}

func TestToggleVoiceModeSaveFailureStillFlips(t *testing.T) {
	dir := t.TempDir()
	m := voiceTestFakes(newModel(t.Context(), Options{UserConfigPath: dir, BuildDictationTranscriber: voicePersistFactory}))
	// A directory is not writable as a config file, so the save must fail.
	on, _ := m.toggleVoiceMode()
	if !on.dictation.voiceModeEnabled {
		t.Fatal("voice mode must still flip on when the save fails")
	}
	if !transcriptHasText(on, "Não foi possível salvar na configuração") {
		t.Error("save failure should surface a durable transcript notice")
	}
	if on.transientNotice.text != "" && strings.Contains(on.transientNotice.text, "Modo de voz ligado") {
		t.Error("save failure must use the durable notice, not the transient one")
	}
}

func TestToggleVoiceModeWithoutConfigPathStaysInMemory(t *testing.T) {
	m := voiceTestFakes(newModel(t.Context(), Options{BuildDictationTranscriber: voicePersistFactory}))
	if m.dictation.userConfigPath != "" {
		t.Fatalf("test setup: expected no config path, got %q", m.dictation.userConfigPath)
	}
	on, _ := m.toggleVoiceMode()
	if !on.dictation.voiceModeEnabled {
		t.Fatal("toggling without a config path should still flip in memory")
	}
	if transcriptHasText(on, "Não foi possível salvar") {
		t.Error("no config path means nothing to save and no error to report")
	}
}

func TestToggleVoiceModeUnavailableWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	m := newModel(t.Context(), Options{UserConfigPath: path})
	if m.dictation.available() {
		t.Fatal("test setup: dictation should be unavailable without a factory")
	}
	next, _ := m.toggleVoiceMode()
	if next.dictation.voiceModeEnabled {
		t.Error("voice mode must not enable when dictation is unavailable")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("refusing /voice must not create a config file")
	}
}
