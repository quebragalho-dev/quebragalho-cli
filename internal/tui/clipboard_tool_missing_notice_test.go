package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/imageinput"
)

// O aviso de ferramenta ausente sai como uma frase só: sem o prefixo "Falha ao
// ler a imagem da área de transferência:" repetindo o que a própria mensagem
// já diz.
func TestClipboardToolMissingNoticeHasNoPrefix(t *testing.T) {
	stubClipboardContent(t, clipboardStub{err: imageinput.ErrClipboardToolMissing})

	m := newModel(context.Background(), Options{Cwd: t.TempDir()})
	_, cmd := m.Update(testKeyCtrl('v'))
	if cmd == nil {
		t.Fatal("a sonda do clipboard não rodou")
	}
	msg := execCmd(cmd)
	if msg == nil {
		t.Fatal("a sonda não emitiu mensagem")
	}
	updated, _ := m.Update(msg)

	want := "Para colar imagens no Linux, instale wl-clipboard (Wayland) ou xclip (X11)."
	if got := lastTranscriptText(updated.(model)); got != want {
		t.Fatalf("aviso = %q, want %q", got, want)
	}
	if strings.Contains(lastTranscriptText(updated.(model)), "Falha ao ler") {
		t.Fatal("aviso ainda prefixado: a frase fica redundante")
	}
}

// Os erros reais (que não são instrução de instalação) continuam prefixados,
// para o usuário saber qual leitura falhou.
func TestClipboardRealErrorKeepsPrefix(t *testing.T) {
	stubClipboardContent(t, clipboardStub{err: errClipboardStub{"bytes corrompidos"}})

	m := newModel(context.Background(), Options{Cwd: t.TempDir()})
	_, cmd := m.Update(testKeyCtrl('v'))
	msg := execCmd(cmd)
	updated, _ := m.Update(msg)

	want := "Falha ao ler a imagem da área de transferência: bytes corrompidos"
	if got := lastTranscriptText(updated.(model)); got != want {
		t.Fatalf("aviso = %q, want %q", got, want)
	}
}

type errClipboardStub struct{ msg string }

func (e errClipboardStub) Error() string { return e.msg }
