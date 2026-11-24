package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
	"github.com/quebragalho-dev/quebragalho-cli/internal/termimage"
	"github.com/quebragalho-dev/quebragalho-cli/internal/tools"
)

// attachment_open_test.go covers clicking a composer attachment chip or
// thumbnail. A plain click opens the original file in the OS viewer; when the
// original is gone an image falls back to a copy of its bytes, and discarding
// attachments never removes the user's own file.

// recordAttachmentOpens swaps the OS opener for a recorder and returns the
// slice that collects every path handed to it.
func recordAttachmentOpens(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	restore := fileLinkOpener
	fileLinkOpener = func(path string) error {
		opened = append(opened, path)
		return nil
	}
	t.Cleanup(func() { fileLinkOpener = restore })
	return &opened
}

// attachmentClickModel builds a vision-capable alt-screen model whose clipboard
// copies land in the test's own temp dir, never in the developer's config.
func attachmentClickModel(t *testing.T, root string) model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stubClipboardContent(t, clipboardStub{})
	m := newModel(context.Background(), Options{Cwd: root, ModelName: "muse-spark-1.3-contributor"})
	m.width, m.height = 100, 30
	m.altScreen = true
	m.headerPrinted = true
	return m
}

func writeAttachmentPNG(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, previewImageBlock(t).Data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// clickComposer sends a plain left click at (column, row) inside the composer,
// measured from the content origin, and returns the model plus its command.
func clickComposer(t *testing.T, m model, column, row int) (model, tea.Cmd) {
	t.Helper()
	return clickComposerWithMod(t, m, column, row, 0)
}

func clickComposerWithMod(t *testing.T, m model, column, row int, mod tea.KeyMod) (model, tea.Cmd) {
	t.Helper()
	x, y := composerChipCell(t, m, column, row)
	updated, cmd := m.Update(tea.MouseClickMsg(tea.Mouse{Button: tea.MouseLeft, X: x, Y: y, Mod: mod}))
	return updated.(model), cmd
}

// composerChipCell maps a (column, row) offset inside the composer's content
// area to absolute screen coordinates, using the same frame as the hit test.
func composerChipCell(t *testing.T, m model, column, row int) (int, int) {
	t.Helper()
	width := m.chatColumnWidth()
	frame := m.scrollableTranscriptFrame(m.pinnedTitleBar(width), m.footerView(width))
	if frame.composerRect.height <= row {
		t.Fatalf("composer rect too short for row %d: %#v", row, frame.composerRect)
	}
	return frame.composerRect.x + 2 + column, frame.composerRect.y + row
}

// clickChipAndRun clicks and, when a command comes back, runs it and feeds its
// message to Update, as the Bubble Tea loop would.
func clickChipAndRun(t *testing.T, m model, column, row int) model {
	t.Helper()
	next, cmd := clickComposer(t, m, column, row)
	if cmd == nil {
		return next
	}
	msg := execCmd(cmd)
	if msg == nil {
		return next
	}
	updated, _ := next.Update(msg)
	return updated.(model)
}

func updateModel(t *testing.T, m model, msg tea.Msg) model {
	t.Helper()
	updated, _ := m.Update(msg)
	next, ok := updated.(model)
	if !ok {
		t.Fatalf("Update devolveu %T, want model", updated)
	}
	return next
}

func wantOpened(t *testing.T, opened *[]string, want string) {
	t.Helper()
	if len(*opened) != 1 || (*opened)[0] != want {
		t.Fatalf("opener got %q, want exactly [%q]", *opened, want)
	}
}

func wantNothingOpened(t *testing.T, opened *[]string) {
	t.Helper()
	if len(*opened) != 0 {
		t.Fatalf("opener should not run, got %q", *opened)
	}
}

func TestClickOnImageChipOpensOriginalFileWithoutTemp(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(png)

	// "[Image #1]" starts at column 0 of the chip row.
	m = clickChipAndRun(t, m, 3, 1)

	wantOpened(t, opened, png)
	if len(m.pendingImageTemps) != 1 || m.pendingImageTemps[0] != "" {
		t.Fatalf("o arquivo original nunca pode entrar em pendingImageTemps: %q", m.pendingImageTemps)
	}
}

func TestClickOnSecondChipAmongSeveralOpensItsOwnFile(t *testing.T) {
	root := t.TempDir()
	first := writeAttachmentPNG(t, filepath.Join(root, "primeiro.png"))
	second := writeAttachmentPNG(t, filepath.Join(root, "segundo.png"))
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(first)
	m, _ = m.handleImageCommand(second)

	// "[Image #1] [Image #2]": the second chip starts at column 11.
	m = clickChipAndRun(t, m, 12, 1)
	wantOpened(t, opened, second)

	// Column 10 is the separator between the chips: it opens nothing.
	if _, cmd := clickComposer(t, m, 10, 1); cmd != nil {
		t.Fatal("clique no espaço entre chips não deve gerar comando")
	}
	wantOpened(t, opened, second)
}

func TestClickOnVideoChipOpensOriginalGIFNotItsFrames(t *testing.T) {
	root := t.TempDir()
	gifPath := writeTinyGIF(t, root, "animado.gif", 5)
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(gifPath)
	if len(m.pendingImages) != 5 {
		t.Fatalf("setup: GIF de 5 quadros deveria gerar 5 imagens, veio %d", len(m.pendingImages))
	}

	// "[GIF #1 · 5 quadros]" is the only chip.
	m = clickChipAndRun(t, m, 3, 1)
	wantOpened(t, opened, gifPath)
}

func TestClickOnDocumentChipOpensOriginalPDF(t *testing.T) {
	root := t.TempDir()
	pdf := writeTestPDF(t, root, "spec.pdf", "Design spec body text")
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(pdf)
	if len(m.pendingDocuments) != 1 {
		t.Fatalf("setup: PDF deveria virar documento pendente, veio %d", len(m.pendingDocuments))
	}

	m = clickChipAndRun(t, m, 3, 1)
	wantOpened(t, opened, pdf)
}

func TestClickOnChipRowInNarrowTerminalOpensTruncatedChip(t *testing.T) {
	root := t.TempDir()
	paths := []string{
		writeAttachmentPNG(t, filepath.Join(root, "a.png")),
		writeAttachmentPNG(t, filepath.Join(root, "b.png")),
		writeAttachmentPNG(t, filepath.Join(root, "c.png")),
	}
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m.width = 30
	for _, p := range paths {
		m, _ = m.handleImageCommand(p)
	}

	innerWidth := m.chatColumnWidth() - 4
	// The row is cut at the composer edge. Its last visible cell belongs to
	// the chip that is still partly shown, which must still open.
	m = clickChipAndRun(t, m, innerWidth-1, 1)
	wantOpened(t, opened, paths[2])

	*opened = nil
	if _, cmd := clickComposer(t, m, innerWidth, 1); cmd != nil {
		t.Fatal("clique depois da borda visível do chip não deve abrir nada")
	}
	wantNothingOpened(t, opened)
}

func TestClickBesideChipsOnEmptyChipRowDoesNothing(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(png)

	// "[Image #1]" ends at column 9; column 20 is empty space on that row.
	if _, cmd := clickComposer(t, m, 20, 1); cmd != nil {
		t.Fatal("clique ao lado dos chips não deve gerar comando")
	}
	wantNothingOpened(t, opened)
}

func TestThumbnailClickOpensStagedClipboardCopy(t *testing.T) {
	root := t.TempDir()
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m.attachmentRenderers = []*termimage.ImageRenderer{termimage.NewImageRenderer(termimage.ImageSupport{Protocol: termimage.ImageProtocolKitty})}
	block := previewImageBlock(t)
	m = m.attachClipboardImage(block.Data, "image/png")
	if !m.attachmentThumbnailVisible(m.chatColumnWidth()) {
		t.Fatal("setup: a miniatura deveria estar visível")
	}
	if len(m.pendingImageTemps) != 1 || m.pendingImageTemps[0] == "" {
		t.Fatalf("setup: colagem deveria ter temporário, veio %q", m.pendingImageTemps)
	}

	// Thumbnail slot 0 occupies columns 2..11 of rows 1..4.
	m = clickChipAndRun(t, m, 5, 2)
	wantOpened(t, opened, m.pendingImageTemps[0])
}

func TestThumbnailClickOnVideoFrameOpensOriginalVideo(t *testing.T) {
	root := t.TempDir()
	gifPath := writeTinyGIF(t, root, "animado.gif", 5)
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m.attachmentRenderers = []*termimage.ImageRenderer{termimage.NewImageRenderer(termimage.ImageSupport{Protocol: termimage.ImageProtocolKitty})}
	m, _ = m.handleImageCommand(gifPath)
	if !m.attachmentThumbnailVisible(m.chatColumnWidth()) {
		t.Fatal("setup: a miniatura do primeiro quadro deveria estar visível")
	}

	m = clickChipAndRun(t, m, 5, 2)
	wantOpened(t, opened, gifPath)
}

func TestThumbnailModeTextRowDoesNotOpenSingleImage(t *testing.T) {
	root := t.TempDir()
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m.attachmentRenderers = []*termimage.ImageRenderer{termimage.NewImageRenderer(termimage.ImageSupport{Protocol: termimage.ImageProtocolKitty})}
	m = m.attachClipboardImage(previewImageBlock(t).Data, "image/png")
	m.input.SetValue("hello world")

	// With one thumbnail and no supplemental chip row, the row right under the
	// thumbnails is the text line and must keep the cursor behaviour.
	if _, cmd := clickComposer(t, m, 3, attachmentPreviewRows+1); cmd != nil {
		t.Fatal("linha de texto abaixo das miniaturas não deve abrir anexo")
	}
	wantNothingOpened(t, opened)
}

func TestVanishedOriginalImageFallsBackToBytesCopy(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	original, err := os.ReadFile(png)
	if err != nil {
		t.Fatal(err)
	}
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(png)
	if err := os.Remove(png); err != nil {
		t.Fatal(err)
	}

	m = clickChipAndRun(t, m, 3, 1)
	if len(*opened) != 1 {
		t.Fatalf("imagem sem original deveria abrir a cópia dos bytes, abriu %q", *opened)
	}
	copyPath := (*opened)[0]
	if copyPath == png {
		t.Fatal("não deve abrir o caminho original que não existe mais")
	}
	got, err := os.ReadFile(copyPath)
	if err != nil || string(got) != string(original) {
		t.Fatalf("cópia dos bytes não confere com a imagem anexada: err=%v", err)
	}
	if len(m.pendingImageTemps) != 1 || m.pendingImageTemps[0] != copyPath {
		t.Fatalf("cópia recriada deve ser registrada em pendingImageTemps, veio %q", m.pendingImageTemps)
	}
	if _, err := os.Stat(png); !os.IsNotExist(err) {
		t.Fatal("o original apagado pelo usuário não pode ser recriado")
	}

	m, _ = m.handleImageCommand("clear")
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatal("cópia recriada deve ser apagada no descarte")
	}
}

func TestOriginalFilePreservedAfterOpenAndDiscard(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(png)

	m = clickChipAndRun(t, m, 3, 1)
	wantOpened(t, opened, png)
	_, _ = m.handleImageCommand("clear")

	if _, err := os.Stat(png); err != nil {
		t.Fatalf("arquivo original do usuário deve sobreviver ao descarte: %v", err)
	}
}

func TestVanishedClipboardTempIsRecreatedAndDiscarded(t *testing.T) {
	root := t.TempDir()
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	block := previewImageBlock(t)
	m = m.attachClipboardImage(block.Data, "image/png")
	temp := m.pendingImageTemps[0]
	if err := os.Remove(temp); err != nil {
		t.Fatal(err)
	}

	m = clickChipAndRun(t, m, 3, 1)
	if len(*opened) != 1 {
		t.Fatalf("temporário apagado deveria ser regravado e aberto, abriu %q", *opened)
	}
	copyPath := (*opened)[0]
	got, err := os.ReadFile(copyPath)
	if err != nil || string(got) != string(block.Data) {
		t.Fatalf("temporário regravado deve ter os bytes colados: err=%v", err)
	}
	if m.pendingImageTemps[0] != copyPath {
		t.Fatalf("temporário regravado deve ser registrado, veio %q", m.pendingImageTemps)
	}

	m, _ = m.handleImageCommand("clear")
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatal("temporário regravado deve ser apagado no descarte")
	}
}

func TestOpenerReceivesExactPathWithSpacesAccentsAndLeadingDash(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "pasta com espaço e ação")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "-tela ção.png"
	absolute := writeAttachmentPNG(t, filepath.Join(dir, name))

	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(absolute)
	clickChipAndRun(t, m, 3, 1)
	wantOpened(t, opened, absolute)

	*opened = nil
	// A relative /image path resolves against the session cwd; the viewer must
	// still receive the absolute path, which cannot be read as an option.
	relative := attachmentClickModel(t, dir)
	relative, _ = relative.handleImageCommand(name)
	clickChipAndRun(t, relative, 3, 1)
	wantOpened(t, opened, filepath.Join(dir, name))
}

func TestMissingPDFShowsShortWarningInsteadOfOpening(t *testing.T) {
	root := t.TempDir()
	pdf := writeTestPDF(t, root, "spec.pdf", "Design spec body text")
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(pdf)
	if err := os.Remove(pdf); err != nil {
		t.Fatal(err)
	}

	m = clickChipAndRun(t, m, 3, 1)
	wantNothingOpened(t, opened)
	if text := lastTranscriptText(m); !strings.Contains(text, "Não foi possível abrir o PDF") || !strings.Contains(text, "original não existe mais") {
		t.Fatalf("aviso de PDF ausente em português, último aviso: %q", text)
	}
}

func TestMissingGIFShowsShortWarningInsteadOfOpening(t *testing.T) {
	root := t.TempDir()
	gifPath := writeTinyGIF(t, root, "animado.gif", 5)
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(gifPath)
	if err := os.Remove(gifPath); err != nil {
		t.Fatal(err)
	}

	m = clickChipAndRun(t, m, 3, 1)
	wantNothingOpened(t, opened)
	if text := lastTranscriptText(m); !strings.Contains(text, "Não foi possível abrir o GIF") || !strings.Contains(text, "original não existe mais") {
		t.Fatalf("aviso de GIF ausente em português, último aviso: %q", text)
	}
}

func TestOpenerFailureShowsPortugueseNoticeWithoutBlockingUpdate(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	restore := fileLinkOpener
	fileLinkOpener = func(string) error { return errors.New("xdg-open não encontrado") }
	t.Cleanup(func() { fileLinkOpener = restore })

	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(png)
	next, cmd := clickComposer(t, m, 3, 1)
	if cmd == nil {
		t.Fatal("abrir o anexo deve rodar em tea.Cmd, não no Update")
	}
	msg := execCmd(cmd)
	if msg == nil {
		t.Fatal("falha do opener deveria chegar como mensagem para o Update")
	}
	final := updateModel(t, next, msg)
	if text := lastTranscriptText(final); !strings.Contains(text, "Não foi possível abrir o anexo") {
		t.Fatalf("falha ao abrir sem aviso em português, último aviso: %q", text)
	}
}

func TestAttachmentClickBlockedByOverlaysAndModifiers(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	opened := recordAttachmentOpens(t)

	blockers := map[string]func(*model){
		"help overlay": func(m *model) { m.helpOverlay = true },
		"permission":   func(m *model) { m.pendingPermission = &pendingPermissionPrompt{} },
	}
	for name, block := range blockers {
		m := attachmentClickModel(t, root)
		m, _ = m.handleImageCommand(png)
		// Take the chip's cell before the overlay covers the composer, then
		// click that same cell with the overlay up.
		x, y := composerChipCell(t, m, 3, 1)
		block(&m)
		if _, cmd := m.Update(tea.MouseClickMsg(tea.Mouse{Button: tea.MouseLeft, X: x, Y: y})); cmd != nil {
			t.Fatalf("%s: clique não pode abrir anexo por cima", name)
		}
	}
	wantNothingOpened(t, opened)

	// Ctrl+click belongs to the file-link modifier flow, not to attachments.
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(png)
	if _, cmd := clickComposerWithMod(t, m, 3, 1, tea.ModCtrl); cmd != nil {
		t.Fatal("Ctrl+clique não deve abrir o anexo")
	}
	wantNothingOpened(t, opened)
}

// /edit restores the remembered attachments after a real submit. The restored
// file image must still open its original, so the submit snapshot and the /edit
// restore both have to carry the origin along with the bytes.
func TestEditRestoredFileImageOpensItsOriginal(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	opened := recordAttachmentOpens(t)
	provider := &fakeProvider{events: []qgruntime.StreamEvent{
		{Type: qgruntime.StreamEventText, Content: "ok"},
		{Type: qgruntime.StreamEventDone},
	}}
	m := newModel(context.Background(), Options{
		Cwd:          root,
		ProviderName: "openai",
		ModelName:    "muse-spark-1.3-contributor",
		Provider:     provider,
		Registry:     tools.NewRegistry(),
		SessionStore: testSessionStore(t),
	})
	m.agentOptions.OnText = func(string) {}
	m.captureRunImages = func([]qgruntime.ImageBlock) {}
	m.width, m.height = 100, 30
	m.altScreen = true
	m.headerPrinted = true
	stubClipboardContent(t, clipboardStub{})

	m.input.SetValue("/image tela.png")
	updated, _ := m.handleSubmit()
	m = updated.(model)
	m.input.SetValue("describe this")
	updated, cmd := m.handleSubmit()
	m = updated.(model)
	if cmd == nil || len(m.pendingImages) != 0 {
		t.Fatal("setup: envio deveria iniciar um turno e limpar os pendentes")
	}

	m.input.SetValue("/edit")
	updated, _ = m.handleSubmit()
	m = updated.(model)
	if len(m.pendingImages) != 1 {
		t.Fatalf("setup: /edit deveria restaurar a imagem, veio %d", len(m.pendingImages))
	}

	m = clickChipAndRun(t, m, 3, 1)
	wantOpened(t, opened, png)
}

// The staged-attachment slices describe the same entries, so they must agree in
// length after every kind of staging and every removal.
func wantAttachmentLockstep(t *testing.T, m model, step string) {
	t.Helper()
	n := len(m.pendingImages)
	if len(m.pendingImageLabels) != n || len(m.pendingImageTemps) != n || len(m.pendingImageOrigins) != n {
		t.Fatalf("%s: lockstep quebrado: imagens=%d rótulos=%d temps=%d origens=%d",
			step, n, len(m.pendingImageLabels), len(m.pendingImageTemps), len(m.pendingImageOrigins))
	}
}

func TestAttachmentSlicesStayInLockstep(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	gifPath := writeTinyGIF(t, root, "animado.gif", 3)
	pdf := writeTestPDF(t, root, "spec.pdf", "Design spec body text")
	recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)

	m, _ = m.handleImageCommand(png)
	wantAttachmentLockstep(t, m, "após imagem de arquivo")
	m = m.attachClipboardImage(previewImageBlock(t).Data, "image/png")
	wantAttachmentLockstep(t, m, "após colagem")
	m, _ = m.handleImageCommand(gifPath)
	wantAttachmentLockstep(t, m, "após GIF de três quadros")
	m, _ = m.handleImageCommand(pdf)
	wantAttachmentLockstep(t, m, "após PDF")

	m = clickChipAndRun(t, m, 3, 1)
	wantAttachmentLockstep(t, m, "após clique que abre")
	m, _ = m.removeLastAttachment()
	wantAttachmentLockstep(t, m, "após remover o PDF")
	m, _ = m.removeLastAttachment()
	wantAttachmentLockstep(t, m, "após remover o GIF (três quadros)")
	if len(m.pendingImages) != 2 {
		t.Fatalf("setup: depois de remover o GIF deveriam sobrar 2 imagens, veio %d", len(m.pendingImages))
	}
	m, _ = m.removeLastAttachment()
	wantAttachmentLockstep(t, m, "após remover a colagem")
	m, _ = m.handleImageCommand("clear")
	wantAttachmentLockstep(t, m, "após /image clear")
	if len(m.pendingImageOrigins) != 0 {
		t.Fatalf("/image clear deveria zerar as origens, veio %d", len(m.pendingImageOrigins))
	}
}

func TestNewSessionClearsStagedImageOrigins(t *testing.T) {
	root := t.TempDir()
	png := writeAttachmentPNG(t, filepath.Join(root, "tela.png"))
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(png)
	m.lastImages = m.pendingImages
	m.lastImageLabels = m.pendingImageLabels
	m.lastImageOrigins = m.pendingImageOrigins

	next := m.startNewSession()
	if len(next.pendingImageOrigins) != 0 || len(next.lastImageOrigins) != 0 {
		t.Fatalf("nova sessão deve zerar origens, veio pendentes=%d snapshot=%d",
			len(next.pendingImageOrigins), len(next.lastImageOrigins))
	}
	wantAttachmentLockstep(t, next, "após nova sessão")
}

// A file's content decides that it is an image at attach time, but its name is
// what the OS viewer acts on. An image with a launcher-like name must never be
// handed to the viewer: the staged bytes are copied out instead.
func TestImageWithLauncherExtensionOpensCopyNeverOriginal(t *testing.T) {
	root := t.TempDir()
	hta := writeAttachmentPNG(t, filepath.Join(root, "apresentacao.hta"))
	original, err := os.ReadFile(hta)
	if err != nil {
		t.Fatal(err)
	}
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(hta)
	clickChipAndRun(t, m, 3, 1)

	if len(*opened) != 1 {
		t.Fatalf("imagem com nome .hta deveria abrir uma cópia, abriu %q", *opened)
	}
	if (*opened)[0] == hta {
		t.Fatal("o original .hta nunca pode ser entregue ao sistema para abrir")
	}
	if filepath.Ext((*opened)[0]) != ".png" {
		t.Fatalf("a cópia deve ter extensão de imagem pelo tipo do anexo, veio %q", (*opened)[0])
	}
	got, err := os.ReadFile((*opened)[0])
	if err != nil || string(got) != string(original) {
		t.Fatalf("cópia dos bytes não confere: err=%v", err)
	}
}

// Extensions are matched case-insensitively, so an upper-case image name opens
// its original like any other image.
func TestUppercaseImageExtensionOpensOriginal(t *testing.T) {
	root := t.TempDir()
	upper := writeAttachmentPNG(t, filepath.Join(root, "TELA.PNG"))
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(upper)
	clickChipAndRun(t, m, 3, 1)
	wantOpened(t, opened, upper)
}

// A symlink named like an image but pointing at a command-style file must not
// open the target. The check follows the link, so the real name decides.
func TestSymlinkToCommandFileNeverOpensTarget(t *testing.T) {
	root := t.TempDir()
	target := writeAttachmentPNG(t, filepath.Join(root, "x.command"))
	link := filepath.Join(root, "foto.png")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks indisponíveis neste sistema: %v", err)
	}
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(link)
	clickChipAndRun(t, m, 3, 1)

	if len(*opened) != 1 {
		t.Fatalf("imagem por link deveria abrir uma cópia, abriu %q", *opened)
	}
	if (*opened)[0] == target || (*opened)[0] == link {
		t.Fatalf("o alvo do link não pode ser aberto: %q", (*opened)[0])
	}
	if filepath.Ext((*opened)[0]) != ".png" {
		t.Fatalf("a cópia deve ter extensão de imagem, veio %q", (*opened)[0])
	}
}

// A PDF is only opened by a .pdf name. A PDF stored under another name is still
// attached (by content), but a click warns instead of opening it.
func TestPDFWithoutPDFExtensionDoesNotOpen(t *testing.T) {
	root := t.TempDir()
	bin := writeTestPDF(t, root, "spec.bin", "Design spec body text")
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(bin)
	if len(m.pendingDocuments) != 1 {
		t.Fatalf("setup: PDF por conteúdo deveria anexar, veio %d", len(m.pendingDocuments))
	}
	m = clickChipAndRun(t, m, 3, 1)
	wantNothingOpened(t, opened)
	if text := lastTranscriptText(m); !strings.Contains(text, "a extensão do arquivo original não é aceita") {
		t.Fatalf("aviso de extensão ausente para PDF sem .pdf, último aviso: %q", text)
	}
}

// An animated GIF with a launcher-like name is attached by content, but the
// viewer only gets a .gif name; the click warns instead.
func TestAnimatedGIFWithLauncherExtensionDoesNotOpen(t *testing.T) {
	root := t.TempDir()
	gifPath := writeTinyGIF(t, root, "animado.hta", 5)
	opened := recordAttachmentOpens(t)
	m := attachmentClickModel(t, root)
	m, _ = m.handleImageCommand(gifPath)
	m = clickChipAndRun(t, m, 3, 1)
	wantNothingOpened(t, opened)
	if text := lastTranscriptText(m); !strings.Contains(text, "a extensão do arquivo original não é aceita") {
		t.Fatalf("aviso de extensão ausente para GIF, último aviso: %q", text)
	}
}
