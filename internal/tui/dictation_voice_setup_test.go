package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/dictation"
)

// voiceSetupController builds a controller whose transcription backend works,
// with fakes for the capture-program and microphone checks: no test may
// depend on programs installed on the machine, on its locale, or on a
// microphone.
func voiceSetupController() dictationController {
	d := batchOnlyController()
	d.lang = "pt"
	d.lookPath = func(string) (string, error) { return "/usr/bin/arecord", nil }
	d.micCheck = func(context.Context) (dictation.MicCheck, error) {
		return dictation.MicCheck{Peak: 0.5}, nil
	}
	return d
}

// voiceSetupFailBuild marks the backend as missing (a missing local model),
// the prelude to the first-use panel.
func voiceSetupFailBuild(d dictationController, downloadRoot string) dictationController {
	d.downloadRoot = downloadRoot
	d.build = func(config.STTConfig, bool) (Transcriber, bool, error) {
		return nil, false, &dictation.SetupError{Tool: "local STT model", Hint: "set stt.localModelPath"}
	}
	return d
}

// defaultVoiceVariant returns the curated model /voice offers by default.
func defaultVoiceVariant(t *testing.T) dictation.ModelVariant {
	t.Helper()
	v, ok := dictation.DefaultVariantFor("pt")
	if !ok {
		t.Fatal("DefaultVariantFor(pt) should return the default model")
	}
	return v
}

// openVoicePanel runs /voice against a missing model and returns the open
// first-use panel.
func openVoicePanel(t *testing.T, d dictationController) model {
	t.Helper()
	m := model{dictation: d}
	next, _ := m.toggleVoiceMode()
	if next.picker == nil || !next.picker.voiceFirstUse {
		t.Fatalf("expected the first-use panel, got picker %+v", next.picker)
	}
	return next
}

// choosePickerRow selects the picker's highlighted row (choosePicker returns
// tea.Model like Update does).
func choosePickerRow(t *testing.T, m model, row int) (model, tea.Cmd) {
	t.Helper()
	m.picker.selected = row
	updated, cmd := m.choosePicker()
	next, ok := updated.(model)
	if !ok {
		t.Fatal("choosePicker should return a model")
	}
	return next, cmd
}

// 1. A missing capture program blocks /voice on Linux; macOS and Windows
// record through the system audio API, so the check passes there.
func TestVoiceSetupCaptureMissingLinux(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	d := voiceSetupController()
	d.platform = dictation.PlatformLinux
	d.userConfigPath = cfgPath
	d.lookPath = func(string) (string, error) { return "", errors.New("not found") }

	next, _ := model{dictation: d}.toggleVoiceMode()
	if next.dictation.voiceModeEnabled {
		t.Error("voice must stay off when the capture program is missing")
	}
	if !transcriptHasText(next, "Para usar o modo de voz, ") || !transcriptHasText(next, "sudo apt install alsa-utils") {
		t.Error("expected the Linux install hint in the notice")
	}
	if _, err := os.Stat(cfgPath); !os.IsNotExist(err) {
		t.Error("a refused /voice must not create a config file")
	}
}

func TestVoiceSetupCaptureSkippedOnNativePlatforms(t *testing.T) {
	for _, platform := range []dictation.Platform{dictation.PlatformDarwin, dictation.PlatformWindows} {
		t.Run(string(platform), func(t *testing.T) {
			d := voiceSetupController()
			d.platform = platform
			d.lookPath = func(string) (string, error) { return "", errors.New("not found") }
			next, _ := model{dictation: d}.toggleVoiceMode()
			if !next.dictation.voiceModeEnabled {
				t.Errorf("voice should turn on with no capture program on %s (built-in recording)", platform)
			}
		})
	}
}

// 2. Everything ready: ON, saved, usage + summary in the message, and the
// microphone check command yields the working-microphone notice.
func TestVoiceSetupReadyTurnsOn(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	variant := defaultVoiceVariant(t)
	d := voiceSetupController()
	d.userConfigPath = cfgPath
	d.cfg.LocalModelPath = filepath.Join("/models", variant.DirName)

	next, cmd := model{dictation: d}.toggleVoiceMode()
	if !next.dictation.voiceModeEnabled {
		t.Fatal("voice should turn on when everything is ready")
	}
	for _, want := range []string{
		"Modo de voz ligado — " + voiceCaptureUsage + ".",
		"Idioma: português",
		"Modelo: Local · " + variant.Label + ".",
		"Rode /voice de novo para desligar.",
	} {
		if !transcriptHasText(next, want) {
			t.Errorf("turn-on notice missing %q", want)
		}
	}
	voiceMode, _, _ := readVoicePersistFile(t, cfgPath)
	if voiceMode == nil || !*voiceMode {
		t.Error("turning on should persist stt.voiceMode:true")
	}
	if cmd == nil {
		t.Fatal("expected the microphone check command")
	}
	msg, ok := cmd().(sttMicCheckedMsg)
	if !ok {
		t.Fatalf("mic command returned %T, want sttMicCheckedMsg", cmd())
	}
	after := next.handleSTTMicChecked(msg)
	if after.transientNotice.text != "Microfone funcionando. Segure Espaço e fale." {
		t.Errorf("mic ok notice = %q", after.transientNotice.text)
	}
}

// 3. Unknown language: Modelo without Idioma.
func TestVoiceSetupNoLanguageOmitsIdioma(t *testing.T) {
	d := batchOnlyController() // lang "" and no fakes: both checks skipped
	next, _ := model{dictation: d}.toggleVoiceMode()
	if !next.dictation.voiceModeEnabled {
		t.Fatal("voice should turn on")
	}
	if !transcriptHasText(next, "Modelo: Local (sem modelo).") {
		t.Error("expected the model-only summary")
	}
	if transcriptHasText(next, "Idioma:") {
		t.Error("unknown language must not show an Idioma summary")
	}
}

// 4. Missing model with a default: the first-use panel with its three rows.
func TestVoiceSetupFirstUsePanel(t *testing.T) {
	variant := defaultVoiceVariant(t)
	next := openVoicePanel(t, voiceSetupFailBuild(voiceSetupController(), t.TempDir()))

	if next.picker.title != "Ditado por voz" {
		t.Errorf("panel title = %q", next.picker.title)
	}
	for _, want := range []string{
		"Para ditar, o QG baixa uma vez o reconhecimento de fala.",
		"sua voz não é enviada a lugar nenhum.",
	} {
		if !strings.Contains(next.picker.header, want) {
			t.Errorf("panel header missing %q", want)
		}
	}
	if len(next.picker.items) != 3 {
		t.Fatalf("panel has %d rows, want 3", len(next.picker.items))
	}
	wantRow1 := "Baixar agora — " + variant.Label
	if !strings.HasPrefix(next.picker.items[0].Label, wantRow1) || !strings.Contains(next.picker.items[0].Label, "MB") {
		t.Errorf("row 1 = %q, want %q with the size", next.picker.items[0].Label, wantRow1)
	}
	if next.picker.items[0].Meta != variant.Description {
		t.Errorf("row 1 description = %q, want %q", next.picker.items[0].Meta, variant.Description)
	}
	if next.picker.items[1].Label != "Escolher outro modelo…" {
		t.Errorf("row 2 = %q", next.picker.items[1].Label)
	}
	if next.picker.items[2].Label != "Agora não" {
		t.Errorf("row 3 = %q", next.picker.items[2].Label)
	}
	if next.dictation.voiceModeEnabled {
		t.Error("voice must stay off while the panel is open")
	}
	if !next.dictation.voiceAfterSetup {
		t.Error("opening the panel should arm the auto-enable")
	}
}

// 5. Panel row 1 with nothing on disk starts the download, like a chooser row.
func TestVoiceSetupPanelRow1StartsDownload(t *testing.T) {
	next := openVoicePanel(t, voiceSetupFailBuild(voiceSetupController(), t.TempDir()))
	after, cmd := choosePickerRow(t, next, 0)
	if !after.dictation.downloading {
		t.Error("row 1 should start the download")
	}
	if cmd == nil {
		t.Error("expected a download command")
	}
	if after.picker != nil {
		t.Error("the panel should close once the download starts")
	}
	if !after.dictation.voiceAfterSetup {
		t.Error("the auto-enable should stay armed during the download")
	}
}

// 6. Panel row 1 with everything on disk turns voice on through the installed
// path.
func TestVoiceSetupPanelRow1InstalledTurnsOn(t *testing.T) {
	variant := defaultVoiceVariant(t)
	root := t.TempDir()
	stageInstalledModel(t, root, variant)
	next := openVoicePanel(t, voiceSetupFailBuild(voiceSetupController(), root))

	if !strings.Contains(next.picker.items[0].Label, "já baixado") {
		t.Fatalf("row 1 = %q, want the already-downloaded wording", next.picker.items[0].Label)
	}
	after, _ := choosePickerRow(t, next, 0)
	if !after.dictation.voiceModeEnabled {
		t.Error("voice should turn on right away through the installed path")
	}
	if after.dictation.voiceAfterSetup {
		t.Error("the auto-enable should disarm once voice is on")
	}
	if !transcriptHasText(after, variant.Label+" pronto. Modo de voz ligado — ") {
		t.Error("expected the ready prefix in the turn-on notice")
	}
}

// 7. Panel row 2 opens the full chooser (loading placeholder with the fetch
// command when uncached), keeping the flag; a model picked there and finished
// turns voice on.
func TestVoiceSetupPanelRow2OpensChooser(t *testing.T) {
	variant := defaultVoiceVariant(t)
	root := t.TempDir()
	stageInstalledModel(t, root, variant)
	next := openVoicePanel(t, voiceSetupFailBuild(voiceSetupController(), root))

	after, cmd := choosePickerRow(t, next, 1)
	if after.picker == nil || after.picker.kind != pickerSTTDownload || after.picker.voiceFirstUse {
		t.Fatalf("row 2 should open the full chooser, got %+v", after.picker)
	}
	if cmd == nil {
		t.Error("expected the model-list fetch command")
	}
	if !after.dictation.voiceAfterSetup {
		t.Error("the auto-enable should stay armed in the chooser")
	}
	// The fetched list resolves the default variant; finishing it turns voice on.
	filled := after.handleSTTModelsFetched(sttModelsFetchedMsg{variants: dictation.ModelVariants()})
	picked, _ := filled.handleSTTDownloadSelection(variant.ID)
	if !picked.dictation.voiceModeEnabled {
		t.Error("finishing the chooser install should turn voice on")
	}
}

// 8. Row 3, Esc and Ctrl+C on the panel stay off, disarm, and download
// nothing; a later manual download keeps today's text and stays off.
func TestVoiceSetupPanelRow3ClosesWithoutDownload(t *testing.T) {
	next := openVoicePanel(t, voiceSetupFailBuild(voiceSetupController(), t.TempDir()))
	after, _ := choosePickerRow(t, next, 2)
	if after.dictation.voiceModeEnabled || after.dictation.voiceAfterSetup || after.dictation.downloading {
		t.Error("row 3 should close with voice off, disarmed, and nothing downloading")
	}
	// A later /stt-model download that succeeds shows today's text and stays off.
	comp := dictation.EngineComponents{BinaryPath: "/x/bin", ModelPath: "/x/model"}
	done, _ := after.handleDictationDownloaded(dictationDownloadedMsg{components: comp})
	if done.dictation.voiceModeEnabled {
		t.Error("a manual download must leave voice off")
	}
	if !strings.Contains(done.transientNotice.text, "Rode /voice") {
		t.Errorf("manual download notice = %q, want today's text", done.transientNotice.text)
	}
}

func TestVoiceSetupPanelEscClearsFlag(t *testing.T) {
	m := newModel(t.Context(), Options{})
	m.dictation = voiceSetupFailBuild(voiceSetupController(), t.TempDir())
	m.dictation.voiceAfterSetup = true
	variant := defaultVoiceVariant(t)
	m.picker = newVoiceFirstUsePicker(variant, m.dictation.downloadRoot, false)

	updated, _ := m.Update(testKey(tea.KeyEsc))
	next := updated.(model)
	if next.picker != nil {
		t.Error("Esc should close the panel")
	}
	if next.dictation.voiceAfterSetup || next.dictation.voiceModeEnabled || next.dictation.downloading {
		t.Error("Esc on the panel should leave voice off and disarmed")
	}
}

func TestVoiceSetupPanelCtrlCClearsFlag(t *testing.T) {
	m := newModel(t.Context(), Options{})
	m.dictation = voiceSetupFailBuild(voiceSetupController(), t.TempDir())
	m.dictation.voiceAfterSetup = true
	variant := defaultVoiceVariant(t)
	m.picker = newVoiceFirstUsePicker(variant, m.dictation.downloadRoot, false)

	updated, _ := m.Update(testKeyCtrl('c'))
	next := updated.(model)
	if next.picker != nil {
		t.Error("Ctrl+C should close the panel")
	}
	if next.dictation.voiceAfterSetup || next.dictation.voiceModeEnabled {
		t.Error("Ctrl+C on the panel should leave voice off and disarmed")
	}
	if next.exitConfirmActive {
		t.Error("Ctrl+C on the panel must not arm the exit confirmation")
	}
}

func TestVoiceSetupChooserEscClearsFlag(t *testing.T) {
	m := newModel(t.Context(), Options{})
	m.dictation = voiceSetupController()
	m.dictation.voiceAfterSetup = true
	m.dictation.downloadRoot = t.TempDir()
	m.picker = newSTTDownloadPickerFrom(dictation.ModelVariants(), false, m.dictation.downloadRoot, false, "", "pt")

	updated, _ := m.Update(testKey(tea.KeyEsc))
	next := updated.(model)
	if next.picker != nil || next.dictation.voiceAfterSetup {
		t.Error("Esc on the chooser should close it and disarm the auto-enable")
	}
}

// 9. No default model (unknown or uncovered language): the full chooser
// directly, with the pick-a-model notice and the flag armed.
func TestVoiceSetupNoDefaultOpensChooser(t *testing.T) {
	for _, lang := range []string{"", "xx"} {
		t.Run("lang="+lang, func(t *testing.T) {
			d := voiceSetupFailBuild(voiceSetupController(), t.TempDir())
			d.lang = lang
			next, _ := model{dictation: d}.toggleVoiceMode()
			if next.picker == nil || next.picker.kind != pickerSTTDownload || next.picker.voiceFirstUse {
				t.Fatalf("want the full chooser, got %+v", next.picker)
			}
			if !next.dictation.voiceAfterSetup {
				t.Error("the chooser should arm the auto-enable")
			}
			if !strings.Contains(next.transientNotice.text, "Escolha um modelo de ditado. O modo de voz liga sozinho quando ele estiver pronto.") {
				t.Errorf("chooser notice = %q", next.transientNotice.text)
			}
			if next.dictation.voiceModeEnabled {
				t.Error("voice must stay off until a model is ready")
			}
		})
	}
}

// 10. A download that succeeds with the flag turns voice on, saved and with
// the ready prefix plus the microphone check command.
func TestVoiceSetupDownloadSuccessEnables(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	variant := defaultVoiceVariant(t)
	d := voiceSetupController()
	d.userConfigPath = cfgPath
	d.downloading = true
	d.voiceAfterSetup = true
	d.cfg.LocalModelPath = filepath.Join("/models", variant.DirName)
	comp := dictation.EngineComponents{
		BinaryPath:       "/x/bin/sherpa-onnx-offline",
		OnlineBinaryPath: "/x/bin/sherpa-onnx",
		ModelPath:        filepath.Join("/models", variant.DirName),
	}

	next, cmd := model{dictation: d}.handleDictationDownloaded(dictationDownloadedMsg{components: comp, label: variant.Label})
	if !next.dictation.voiceModeEnabled {
		t.Fatal("a flagged download success should turn voice on")
	}
	if next.dictation.voiceAfterSetup {
		t.Error("the auto-enable should disarm once voice is on")
	}
	if next.dictation.downloading {
		t.Error("downloading should clear")
	}
	rows := next.transcript
	if len(rows) == 0 || !strings.HasPrefix(rows[len(rows)-1].text, variant.Label+" pronto. Modo de voz ligado — ") {
		t.Errorf("turn-on notice should start with the ready prefix, got %d rows", len(rows))
	}
	voiceMode, _, _ := readVoicePersistFile(t, cfgPath)
	if voiceMode == nil || !*voiceMode {
		t.Error("the ready path should save stt.voiceMode:true")
	}
	if cmd == nil {
		t.Error("expected the microphone check command")
	}
}

func TestVoiceSetupDownloadSuccessWithoutLabel(t *testing.T) {
	d := voiceSetupController()
	d.downloading = true
	d.voiceAfterSetup = true
	comp := dictation.EngineComponents{BinaryPath: "/x/bin", ModelPath: "/x/model"}

	next, _ := model{dictation: d}.handleDictationDownloaded(dictationDownloadedMsg{components: comp})
	if !next.dictation.voiceModeEnabled {
		t.Fatal("a flagged download success should turn voice on")
	}
	if !transcriptHasText(next, "Reconhecimento de fala pronto. Modo de voz ligado — ") {
		t.Error("missing-label downloads fall back to the generic ready prefix")
	}
}

// 11. A failed download stays off, disarms, and keeps today's failure notice.
func TestVoiceSetupDownloadFailureDisables(t *testing.T) {
	d := voiceSetupController()
	d.downloading = true
	d.voiceAfterSetup = true

	next, _ := model{dictation: d}.handleDictationDownloaded(dictationDownloadedMsg{err: errors.New("network down")})
	if next.dictation.voiceModeEnabled || next.dictation.voiceAfterSetup || next.dictation.downloading {
		t.Error("a failed download should stay off, disarmed, and idle")
	}
	if !transcriptHasText(next, "network down") {
		t.Error("expected today's failure notice")
	}
}

// 12. /voice during a download arms instead of starting a second one; the
// success then turns voice on.
func TestVoiceSetupDuringDownloadArms(t *testing.T) {
	d := voiceSetupController()
	d.downloading = true
	d.downloadStatus = "Parakeet v3 12% · 57/464 MB"

	next, cmd := model{dictation: d}.toggleVoiceMode()
	if cmd != nil {
		t.Error("/voice during a download should not start anything")
	}
	if !next.dictation.downloading {
		t.Error("the in-flight download must keep running")
	}
	if !next.dictation.voiceAfterSetup {
		t.Error("/voice during a download should arm the auto-enable")
	}
	if !strings.Contains(next.transientNotice.text, "O reconhecimento de fala ainda está baixando. O modo de voz liga sozinho quando terminar.") {
		t.Errorf("during-download notice = %q", next.transientNotice.text)
	}
}

// 13. Without the flag nothing changes: the ready paths keep today's text
// and leave voice off (existing tests TestDownloadedBatchModelAppliedToConfig,
// TestDownloadedStreamingModelSetsStreamingOn and
// TestInstallEnablesStreamingDownloadPath pin the same behaviour).
func TestVoiceSetupManualDownloadKeepsTodayBehavior(t *testing.T) {
	d := voiceSetupController()
	d.downloading = true
	comp := dictation.EngineComponents{BinaryPath: "/x/bin", ModelPath: "/x/model"}

	next, _ := model{dictation: d}.handleDictationDownloaded(dictationDownloadedMsg{components: comp})
	if next.dictation.voiceModeEnabled {
		t.Error("an unflagged download must leave voice off")
	}
	if !strings.Contains(next.transientNotice.text, "Ditado local pronto. Rode /voice para começar a ditar.") {
		t.Errorf("unflagged download notice = %q", next.transientNotice.text)
	}

	installed, _ := model{dictation: installedController(t)}.applyInstalledModel(defaultVoiceVariant(t))
	if installed.dictation.voiceModeEnabled {
		t.Error("an unflagged installed-model apply must leave voice off")
	}
	if !strings.Contains(installed.transientNotice.text, "está pronto. Rode /voice para começar a ditar.") {
		t.Errorf("unflagged installed notice = %q", installed.transientNotice.text)
	}
}

// installedController stages the default model under a temp root so the
// already-installed path resolves without network.
func installedController(t *testing.T) dictationController {
	t.Helper()
	root := t.TempDir()
	stageInstalledModel(t, root, defaultVoiceVariant(t))
	d := voiceSetupController()
	d.downloadRoot = root
	return d
}

// 14. Any other build error (cloud provider without a key) stays off with the
// error text and no picker.
func TestVoiceSetupOtherBuildError(t *testing.T) {
	d := voiceSetupController()
	d.downloadRoot = t.TempDir()
	d.cfg.Provider = config.STTProviderGroq
	buildErr := errors.New("missing Groq key")
	d.build = func(config.STTConfig, bool) (Transcriber, bool, error) { return nil, false, buildErr }

	next, _ := model{dictation: d}.toggleVoiceMode()
	if next.dictation.voiceModeEnabled {
		t.Error("voice must stay off on a backend error")
	}
	if next.picker != nil {
		t.Error("a backend error must not open a picker")
	}
	if !transcriptHasText(next, "Falha no ditado: missing Groq key") {
		t.Error("expected the backend error text")
	}
}

// 15. Mismatch warning: a curated English-only local model with a Portuguese
// user turns on with the warning; a covering or manual model shows none.
func TestVoiceSetupMismatchWarning(t *testing.T) {
	englishOnly := dictation.ModelVariant{}
	for _, v := range dictation.ModelVariants() {
		if v.Language == "en" {
			englishOnly = v
			break
		}
	}
	if englishOnly.ID == "" {
		t.Fatal("need a curated English-only variant for the mismatch test")
	}
	covering := defaultVoiceVariant(t)

	for _, test := range []struct {
		name      string
		modelPath string
		wantWarn  bool
	}{
		{name: "english-only curated warns", modelPath: filepath.Join("/models", englishOnly.DirName), wantWarn: true},
		{name: "covering model is quiet", modelPath: filepath.Join("/models", covering.DirName), wantWarn: false},
		{name: "manual path is quiet", modelPath: "/models/custom-manual", wantWarn: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := voiceSetupController()
			d.micCheck = nil
			d.cfg.LocalModelPath = test.modelPath
			next, _ := model{dictation: d}.toggleVoiceMode()
			if !next.dictation.voiceModeEnabled {
				t.Fatal("voice should still turn on with a mismatch")
			}
			hasWarn := transcriptHasText(next, "Este modelo não entende português; para trocar, rode /stt-model.")
			if hasWarn != test.wantWarn {
				t.Errorf("mismatch warning present = %v, want %v", hasWarn, test.wantWarn)
			}
		})
	}
}

// 16. Microphone results: ok, silent per platform, error, stale drops, and the
// nil check.
func TestVoiceSetupMicResults(t *testing.T) {
	voiceOn := func() model {
		m := model{dictation: voiceSetupController()}
		m.dictation.voiceModeEnabled = true
		return m
	}

	if got := voiceOn().handleSTTMicChecked(sttMicCheckedMsg{check: dictation.MicCheck{Peak: 0.5}}); got.transientNotice.text != "Microfone funcionando. Segure Espaço e fale." {
		t.Errorf("mic ok notice = %q", got.transientNotice.text)
	}

	silentWant := map[dictation.Platform]string{
		dictation.PlatformDarwin:  "Em Ajustes do Sistema > Privacidade e Segurança > Microfone",
		dictation.PlatformWindows: "Em Configurações > Privacidade e segurança > Microfone",
		dictation.PlatformLinux:   "Confira o microfone padrão do sistema",
	}
	for platform, want := range silentWant {
		m := voiceOn()
		m.dictation.platform = platform
		got := m.handleSTTMicChecked(sttMicCheckedMsg{check: dictation.MicCheck{Silent: true}})
		if !transcriptHasText(got, "O microfone não está entregando som. ") || !transcriptHasText(got, want) {
			t.Errorf("%s silent notice missing the platform sentence", platform)
		}
	}

	if got := voiceOn().handleSTTMicChecked(sttMicCheckedMsg{err: errors.New("no permission")}); !transcriptHasText(got, "Não foi possível testar o microfone: ") {
		t.Error("mic errors need the guidance notice")
	}

	before := voiceOn()
	rows := len(before.transcript)
	off := before
	off.dictation.voiceModeEnabled = false
	if got := off.handleSTTMicChecked(sttMicCheckedMsg{check: dictation.MicCheck{Peak: 0.5}}); len(got.transcript) != rows || got.transientNotice.text != "" {
		t.Error("a mic result after voice was turned off must be dropped")
	}
	recording := before
	recording.dictation.phase = dictRecording
	if got := recording.handleSTTMicChecked(sttMicCheckedMsg{check: dictation.MicCheck{Peak: 0.5}}); len(got.transcript) != rows || got.transientNotice.text != "" {
		t.Error("a mic result during a dictation must be dropped")
	}

	d := batchOnlyController() // micCheck nil
	_, cmd := model{dictation: d}.toggleVoiceMode()
	_ = cmd
	on, cmd := model{dictation: d}.toggleVoiceMode()
	if !on.dictation.voiceModeEnabled {
		t.Fatal("voice should turn on")
	}
	if cmd != nil {
		t.Error("a nil micCheck must return no command")
	}
}

// 17. Progress bar cells, percentage/MB text, legacy fallback, narrow width,
// and field reset.
func TestVoiceSetupProgressBarCells(t *testing.T) {
	full := renderDownloadBar(100, 100)
	if full != strings.Repeat("█", 20) {
		t.Errorf("100%% bar = %q", full)
	}
	if got := renderDownloadBar(0, 100); got != strings.Repeat("░", 20) {
		t.Errorf("0%% bar = %q", got)
	}
	if got := renderDownloadBar(999, 1000); got != strings.Repeat("█", 19)+strings.Repeat("░", 1) {
		t.Errorf("99.9%% bar = %q (want 19 filled cells)", got)
	}
	if got := renderDownloadBar(190<<20, 464<<20); got != strings.Repeat("█", 8)+strings.Repeat("░", 12) {
		t.Errorf("190/464 bar = %q (want 8 filled cells)", got)
	}
}

func TestVoiceSetupProgressText(t *testing.T) {
	if got := downloadProgressText("Parakeet v3", 190<<20, 464<<20, true); got != "Parakeet v3 "+strings.Repeat("█", 8)+strings.Repeat("░", 12)+" 41% · 190/464 MB" {
		t.Errorf("progress text = %q", got)
	}
	if got := downloadProgressText("Parakeet v3", 190<<20, 464<<20, false); strings.Contains(got, "█") || !strings.Contains(got, "Parakeet v3 41% · 190/464 MB") {
		t.Errorf("narrow progress text = %q", got)
	}
}

func TestVoiceSetupProgressBarStatusLine(t *testing.T) {
	m := model{dictation: voiceSetupController()}
	m.dictation.downloading = true
	m.dictation.downloadStatus = "Parakeet v3 41% · 190/464 MB"

	legacy := plainRender(t, m.statusLine(160))
	if !strings.Contains(legacy, "Parakeet v3 41% · 190/464 MB") {
		t.Errorf("unknown-size status = %q, want today's text", legacy)
	}
	// A stale label with an unknown size still shows the text status.
	m.dictation.downloadLabel = "Motor"
	stale := plainRender(t, m.statusLine(160))
	if !strings.Contains(stale, "Parakeet v3 41% · 190/464 MB") {
		t.Errorf("stale-label status = %q, want today's text", stale)
	}
	m.dictation.downloadLabel = ""

	m.dictation.downloadLabel = "Parakeet v3"
	m.dictation.downloadDone = 190 << 20
	m.dictation.downloadTotal = 464 << 20
	wide := plainRender(t, m.statusLine(160))
	if !strings.Contains(wide, "⬇ Parakeet v3 "+strings.Repeat("█", 8)) || !strings.Contains(wide, "41% · 190/464 MB") {
		t.Errorf("wide status = %q, want the bar", wide)
	}

	// Width 60 still fits the short label with the bar: the bar only drops
	// when the line does not fit.
	narrow := plainRender(t, m.statusLine(60))
	for _, want := range []string{"⬇ Parakeet v3", "█", "41% · 190/464 MB"} {
		if !strings.Contains(narrow, want) {
			t.Errorf("fitting status = %q, want %q kept", narrow, want)
		}
	}
	if strings.Contains(narrow, "\n") {
		t.Errorf("fitting status = %q, must stay on one line", narrow)
	}

	// A longer label overflows width 60 with the bar and keeps the short form.
	m.dictation.downloadLabel = "Kroko (newest, fastest)"
	overflowing := plainRender(t, m.statusLine(60))
	if strings.Contains(overflowing, "█") {
		t.Errorf("overflowing status = %q, the bar should drop first", overflowing)
	}
	for _, want := range []string{"Kroko (newest, fastest)", "41%", "190/464 MB"} {
		if !strings.Contains(overflowing, want) {
			t.Errorf("overflowing status = %q, want %q kept", overflowing, want)
		}
	}
	if strings.Contains(overflowing, "\n") {
		t.Errorf("overflowing status = %q, must stay on one line", overflowing)
	}
}

func TestVoiceSetupProgressFieldsReset(t *testing.T) {
	d := voiceSetupController()
	d.downloading = true
	d.downloadLabel = "Motor"
	d.downloadDone = 10
	d.downloadTotal = 20

	ok, _ := model{dictation: d}.handleDictationDownloaded(dictationDownloadedMsg{})
	if ok.dictation.downloadLabel != "" || ok.dictation.downloadDone != 0 || ok.dictation.downloadTotal != 0 {
		t.Error("byte fields should reset on success")
	}
	failed, _ := model{dictation: d}.handleDictationDownloaded(dictationDownloadedMsg{err: errors.New("x")})
	if failed.dictation.downloadLabel != "" || failed.dictation.downloadDone != 0 || failed.dictation.downloadTotal != 0 {
		t.Error("byte fields should reset on failure")
	}
}

// TestVoiceSetupProgressBytesMessage wires ProgressBytes through the runtime
// message sink into the stored byte counts, mapping the engine label.
func TestVoiceSetupProgressBytesMessage(t *testing.T) {
	var delivered []tea.Msg
	m := model{dictation: voiceSetupController()}
	m.dictation.downloadRoot = t.TempDir()
	m.runtimeMessageSink = func(msg tea.Msg) { delivered = append(delivered, msg) }

	_, cmd := m.startVariantDownload(defaultVoiceVariant(t))
	if cmd == nil {
		t.Fatal("expected a download command")
	}
	// The command would hit the network; only the ProgressBytes wiring is
	// under test, exercised below through the handler.
	_ = cmd
	// Bytes arriving outside a download are dropped.
	quiet := m.handleDictationDownloadProgressBytes(sttDownloadProgressBytesMsg{label: "Motor", done: 1, total: 2})
	if quiet.dictation.downloadLabel != "" {
		t.Error("byte progress outside a download must be dropped")
	}
	m.dictation.downloading = true
	after := m.handleDictationDownloadProgressBytes(sttDownloadProgressBytesMsg{label: "Engine", done: 10 << 20, total: 22 << 20})
	if after.dictation.downloadLabel != "Motor" {
		t.Errorf("engine label = %q, the bar shows Motor", after.dictation.downloadLabel)
	}
	if after.dictation.downloadDone != 10<<20 || after.dictation.downloadTotal != 22<<20 {
		t.Error("byte counts should be stored verbatim")
	}
	steady := after.handleDictationDownloadProgressBytes(sttDownloadProgressBytesMsg{label: "Motor", done: 1, total: 2})
	if len(steady.transcript) != len(after.transcript) || steady.transientNotice.text != "" {
		t.Error("byte progress must not append transcript or transient rows")
	}
}

// 18. The chooser puts the user's language first; unknown language keeps
// today's order.
func TestVoiceSetupChooserLanguageOrder(t *testing.T) {
	enOnly := dictation.ModelVariant{ID: "en-stream", Label: "EN", Bytes: 10 << 20, DirName: "model-en-stream", Streaming: true, Recommended: true, Language: "en"}
	coverPT := dictation.ModelVariant{ID: "multi-big", Label: "Multi", Bytes: 500 << 20, DirName: "model-multi-big", Recommended: true, Languages: []string{"pt", "es"}}
	noCover := dictation.ModelVariant{ID: "multi-nocover", Label: "Multi2", Bytes: 100 << 20, DirName: "model-multi-nocover", Recommended: true, Languages: []string{"ja"}}
	tiny := dictation.ModelVariant{ID: "tiny", Label: "Tiny", Bytes: 5 << 20, DirName: "model-tiny"}
	variants := []dictation.ModelVariant{enOnly, coverPT, noCover, tiny}
	root := t.TempDir()

	gotPT := newSTTDownloadPickerFrom(variants, false, root, false, "", "pt")
	var orderPT []string
	for _, item := range gotPT.items {
		orderPT = append(orderPT, item.Value)
	}
	wantPT := []string{"multi-big", "en-stream", "multi-nocover", "tiny"}
	if strings.Join(orderPT, ",") != strings.Join(wantPT, ",") {
		t.Errorf("pt order = %v, want %v", orderPT, wantPT)
	}

	gotUnknown := newSTTDownloadPickerFrom(variants, false, root, false, "", "")
	var orderUnknown []string
	for _, item := range gotUnknown.items {
		orderUnknown = append(orderUnknown, item.Value)
	}
	// Unknown language keeps today's order: recommended first, then
	// already-installed, then smallest first.
	wantUnknown := []string{"en-stream", "multi-nocover", "multi-big", "tiny"}
	if strings.Join(orderUnknown, ",") != strings.Join(wantUnknown, ",") {
		t.Errorf("unknown-language order = %v, want %v", orderUnknown, wantUnknown)
	}
}

// 19. Turning off runs no check and works with no microphone and no model.
func TestVoiceSetupOffRunsNoCheck(t *testing.T) {
	d := voiceSetupController()
	d.voiceModeEnabled = true
	d.lookPath = func(string) (string, error) { return "", errors.New("no arecord") }
	d.micCheck = func(context.Context) (dictation.MicCheck, error) { return dictation.MicCheck{}, errors.New("no mic") }
	d.build = func(config.STTConfig, bool) (Transcriber, bool, error) { return nil, false, errors.New("no model") }

	off, cmd := model{dictation: d}.toggleVoiceMode()
	if off.dictation.voiceModeEnabled {
		t.Error("voice should turn off")
	}
	if transcriptHasText(off, "Para usar o modo de voz") || transcriptHasText(off, "Não foi possível testar") {
		t.Error("turning off must run neither the capture nor the microphone check")
	}
	if cmd != nil {
		t.Errorf("turning off should return nil cmd, got %v", cmd)
	}
}

// 20. Width 80 render of the turn-on message. The message goes through the
// transcript (a transient notice would truncate it), and the whole text is
// one transcript row; the width-80 view shows its lead like every other
// system notice. renderSystemNote truncates long rows with an ellipsis, so
// the tail is only fully visible on wider terminals — shared rendering left
// unchanged for all notices (see the report).
func TestVoiceSetupTurnOnMessageWidth80(t *testing.T) {
	variant := defaultVoiceVariant(t)
	m := newModel(t.Context(), Options{BuildDictationTranscriber: voicePersistFactory})
	d := m.dictation
	d.lang = "pt"
	d.lookPath = func(string) (string, error) { return "/usr/bin/arecord", nil }
	d.micCheck = nil
	d.cfg.LocalModelPath = filepath.Join("/models", variant.DirName)
	m.dictation = d
	m.width = 80

	next, _ := m.toggleVoiceMode()
	if !next.dictation.voiceModeEnabled {
		t.Fatal("voice should turn on")
	}
	full := "Modo de voz ligado — " + voiceCaptureUsage + ". Idioma: português · Modelo: Local · " + variant.Label + ". Rode /voice de novo para desligar."
	found := false
	for _, row := range next.transcript {
		if strings.Contains(row.text, full) {
			found = true
		}
	}
	if !found {
		t.Error("the whole turn-on message should be one transcript row")
	}
	view := plainRender(t, next.View())
	if !strings.Contains(view, "Modo de voz ligado — segure Espaço") {
		t.Error("the turn-on lead should be on screen at width 80")
	}
}

// 21. No user-visible string in the package may mention docs/dictation.md (a
// file that does not exist): comments are fine, code strings are not.
func TestNoDocsDictationMDInUserStrings(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller should report this test file")
	}
	dir := filepath.Dir(file)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if strings.Contains(code, "docs/dictation.md") {
				t.Errorf("%s:%d mentions docs/dictation.md outside a comment — point at /stt-model or name the config key", name, i+1)
			}
		}
	}
}
