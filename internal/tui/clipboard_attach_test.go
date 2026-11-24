package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/imageinput"
)

// Ctrl+V com imagem no clipboard anexa com prévia (chip), sem tocar no texto
// do composer. O leitor é injetável: aqui um PNG de 1px simulando um print.
func TestCtrlVPastesClipboardImageAsAttachment(t *testing.T) {
	dir := t.TempDir()
	stubClipboardContent(t, clipboardStub{image: testPNGBytes(), mediaType: "image/png"})

	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	m.input.SetValue("veja isso")
	updated, cmd := m.Update(testKeyCtrl('v'))
	next := updated.(model)

	msg := execCmd(cmd)
	content, ok := msg.(clipboardImageMsg)
	if !ok {
		t.Fatalf("Ctrl+V não emitiu a sonda do clipboard, veio %T", msg)
	}
	updated2, _ := next.Update(content)
	got := updated2.(model)

	if len(got.pendingImages) != 1 {
		t.Fatalf("pendingImages = %d, want 1", len(got.pendingImages))
	}
	if got.composerValue() != "veja isso" {
		t.Fatalf("composer foi alterado: %q", got.composerValue())
	}
	if chips := renderAttachmentChips(got.pendingImageLabels, got.pendingDocuments, got.pendingVideoInfos); !strings.Contains(chips, "[Image #1]") {
		t.Fatalf("sem chip de prévia: %q", chips)
	}
}

// Ctrl+V com arquivo(s) copiado(s) (Finder/Explorer/gerenciador Linux) anexa
// como o arrastar faz: imagem e PDF entram; vídeo ainda não é suportado e
// rende aviso em português (ponto de extensão para o engenheiro de vídeo);
// texto copiado continua vindo pela colagem normal, sem duplicar.
func TestCtrlVPastesClipboardFilesLikeDragDrop(t *testing.T) {
	dir := t.TempDir()
	img := writeTestPNG(t, dir, "print.png")
	stubClipboardContent(t, clipboardStub{files: []string{img}})

	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	updated, cmd := m.Update(testKeyCtrl('v'))
	msg := execCmd(cmd)
	updated2, _ := updated.(model).Update(msg.(clipboardImageMsg))
	got := updated2.(model)

	if len(got.pendingImages) != 1 {
		t.Fatalf("arquivo de imagem copiado não anexou: %d pendentes", len(got.pendingImages))
	}
}

// Arquivo de vídeo copiado no Finder/Explorer anexa como quadros, igual ao
// arrastar: precisa do ffmpeg no PATH (falso aqui).
func TestCtrlVPastesVideoAsFrames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAKE_FFMPEG_LOG", filepath.Join(dir, "argv.log"))
	writeFakeFFmpegTUI(t, dir, "ffmpeg")
	writeFakeFFmpegTUI(t, dir, "ffprobe")
	clip := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(clip, []byte("fake-vídeo"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubClipboardContent(t, clipboardStub{files: []string{clip}})

	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	updated, cmd := m.Update(testKeyCtrl('v'))
	msg := execCmd(cmd)
	updated2, attachCmd := updated.(model).Update(msg.(clipboardImageMsg))
	got := updated2.(model)
	if attachCmd != nil {
		if vmsg, ok := execCmd(attachCmd).(videoFramesMsg); ok {
			finished, _ := got.Update(vmsg)
			got = finished.(model)
		}
	}

	if len(got.pendingImages) == 0 {
		t.Fatalf("vídeo colado deveria anexar quadros, aviso: %q", lastTranscriptText(got))
	}
	if len(got.pendingVideoInfos) != 1 {
		t.Fatal("vídeo colado deveria gerar linha de contexto")
	}
}

// GIF animado copiado anexa quadros sem precisar de ffmpeg.
func TestCtrlVPastesAnimatedGIFAsFrames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	clip := writeTinyGIF(t, dir, "animado.gif", 4)
	stubClipboardContent(t, clipboardStub{files: []string{clip}})

	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	updated, cmd := m.Update(testKeyCtrl('v'))
	msg := execCmd(cmd)
	updated2, attachCmd := updated.(model).Update(msg.(clipboardImageMsg))
	got := updated2.(model)
	// The frames are staged synchronously; attachCmd can only carry previews.
	_ = attachCmd

	if len(got.pendingImages) == 0 {
		t.Fatalf("GIF animado colado deveria anexar quadros, aviso: %q", lastTranscriptText(got))
	}
}

// O temporário do print é apagado ao descartar o anexo (Backspace com entrada
// vazia remove o chip) — sem isso cada Ctrl+V deixaria um PNG para trás.
func TestClipboardImageTempRemovedOnDiscard(t *testing.T) {
	dir := t.TempDir()
	stub := stubClipboardContent(t, clipboardStub{image: testPNGBytes(), mediaType: "image/png"})

	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	updated, cmd := m.Update(testKeyCtrl('v'))
	updated2, _ := updated.(model).Update(execCmd(cmd).(clipboardImageMsg))
	got := updated2.(model)

	if len(stub.tempsWritten()) != 1 {
		t.Fatalf("attach não salvou temporário, veio %d", len(stub.tempsWritten()))
	}
	for _, p := range stub.tempsWritten() {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("temporário deveria existir após anexar: %v", err)
		}
	}
	// Backspace com composer vazio descarta o último anexo.
	updated3, _ := got.Update(testKeyBackspace())
	final := updated3.(model)
	if len(final.pendingImages) != 0 {
		t.Fatal("Backspace deveria descartar o anexo")
	}
	for _, p := range stub.tempsWritten() {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("temporário não apagado ao descartar: %q", p)
		}
	}
}

// Ctrl+V não conflita com atalhos existentes: nem com os reservados
// (cancelar, submit, navegação) nem com os padrões configuráveis.
func TestCtrlVHasNoKeybindingConflict(t *testing.T) {
	v := parseBinding("ctrl+v")
	for _, r := range reservedBindings {
		if v == r.binding {
			t.Fatalf("Ctrl+V conflita com atalho reservado (%s)", r.description)
		}
	}
	defaults := []parsedBinding{
		parseBinding("ctrl+o"), parseBinding("ctrl+e"), parseBinding("ctrl+t"),
		parseBinding("ctrl+p"), parseBinding("ctrl+b"),
	}
	for _, d := range defaults {
		if v == d {
			t.Fatalf("Ctrl+V conflita com padrão configurável %s", d.Label())
		}
	}
}

// A tecla aparece onde os atalhos aparecem (overlay do "?").
func TestHelpOverlayDocumentsCtrlV(t *testing.T) {
	m := newModel(context.Background(), Options{})
	found := false
	for _, g := range m.buildKeybindingGroups() {
		for _, b := range g.bindings {
			if strings.Contains(b.keys, "Ctrl+V") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("overlay de ajuda não documenta Ctrl+V")
	}
}

// clipboardStub injeta conteúdo da área de transferência nos testes.
type clipboardStub struct {
	image     []byte
	mediaType string
	files     []string
	err       error

	written []string
}

func (s *clipboardStub) tempsWritten() []string { return s.written }

// stubClipboardContent troca o leitor do clipboard pelo stub e restaura ao
// fim do teste. Também desvia a escrita do temporário para o dir do teste,
// mantendo os testes herméticos (nada toca no ~/.config real).
func stubClipboardContent(t *testing.T, stub clipboardStub) *clipboardStub {
	t.Helper()
	origContent, origSave := readClipboardContent, saveClipboardTempFile
	t.Cleanup(func() {
		readClipboardContent, saveClipboardTempFile = origContent, origSave
	})
	readClipboardContent = func() (imageinput.ClipboardContent, error) {
		return imageinput.ClipboardContent{
			Image:     stub.image,
			MediaType: stub.mediaType,
			Files:     stub.files,
		}, stub.err
	}
	saveClipboardTempFile = func(data []byte, ext string) (string, error) {
		f, err := os.CreateTemp(t.TempDir(), "qg-clipboard-*"+ext)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if _, err := f.Write(data); err != nil {
			return "", err
		}
		stub.written = append(stub.written, f.Name())
		return f.Name(), nil
	}
	return &stub
}

func testPNGBytes() []byte {
	return []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4,
		0x89, 0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0D, 0x0A, 0x2D, 0xB4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE,
		0x42, 0x60, 0x82,
	}
}

func testKeyBackspace() tea.KeyPressMsg {
	return testKey(tea.KeyBackspace)
}
