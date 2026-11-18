package tui

import (
	"context"
	"strings"
	"testing"
)

// Uma colagem vazia (o terminal não tem texto quando o clipboard só tem imagem)
// sonda o clipboard e anexa a imagem com a prévia existente.
func TestEmptyBracketedPasteAttachesClipboardImage(t *testing.T) {
	dir := t.TempDir()
	writeTestPNG(t, dir, "photo.png")
	stubClipboardContent(t, clipboardStub{image: testPNGBytes(), mediaType: "image/png"})

	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	updated, cmd := m.Update(testPaste(""))
	if cmd == nil {
		t.Fatal("paste vazio deveria sondar o clipboard")
	}
	updated2, _ := updated.(model).Update(execCmd(cmd).(clipboardImageMsg))
	got := updated2.(model)

	if len(got.pendingImages) != 1 {
		t.Fatalf("paste vazio não anexou a imagem: %d pendentes", len(got.pendingImages))
	}
}

// Paste vazio com arquivos copiados anexa como o arrastar faz.
func TestEmptyBracketedPasteAttachesClipboardFiles(t *testing.T) {
	dir := t.TempDir()
	img := writeTestPNG(t, dir, "print.png")
	stubClipboardContent(t, clipboardStub{files: []string{img}})

	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	updated, cmd := m.Update(testPaste(""))
	if cmd == nil {
		t.Fatal("paste vazio deveria sondar o clipboard")
	}
	updated2, _ := updated.(model).Update(execCmd(cmd).(clipboardImageMsg))
	got := updated2.(model)

	if len(got.pendingImages) != 1 {
		t.Fatalf("paste vazio não anexou o arquivo copiado: %d pendentes", len(got.pendingImages))
	}
}

// Colar um caminho file:// (Ghostty e navegadores entregam assim) anexa como o
// arrastar faz, inclusive com %20 no nome. Texto comum continua sendo texto.
func TestFileURLPasteAttachesLikeDragDrop(t *testing.T) {
	dir := t.TempDir()
	img := writeTestPNG(t, dir, "foto nova.png")
	stubClipboardContent(t, clipboardStub{})

	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	updated, _ := m.Update(testPaste("file://" + strings.ReplaceAll(img, " ", "%20")))
	got := updated.(model)

	if len(got.pendingImages) != 1 {
		t.Fatalf("paste file:// não anexou: %d pendentes", len(got.pendingImages))
	}
	if got.composerValue() != "" {
		t.Fatalf("caminho file:// vazou para o composer: %q", got.composerValue())
	}
}

// file:// com host localhost também resolve; URI não-arquivo continua texto.
func TestFileURLPasteVariants(t *testing.T) {
	dir := t.TempDir()
	img := writeTestPNG(t, dir, "a.png")
	stubClipboardContent(t, clipboardStub{})

	m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	updated, _ := m.Update(testPaste("file://localhost" + img))
	if got := updated.(model); len(got.pendingImages) != 1 {
		t.Fatalf("file://localhost não anexou: %d pendentes", len(got.pendingImages))
	}

	m2 := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
	updated2, _ := m2.Update(testPaste("https://example.com/a.png"))
	if got := updated2.(model); len(got.pendingImages) != 0 {
		t.Fatal("URL https não deveria anexar")
	}
}
