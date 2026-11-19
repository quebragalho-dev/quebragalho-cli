package tui

import (
	"context"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/imageinput"
)

// Quando a ferramenta de clipboard trava (wl-paste no GNOME/Wayland), o aviso
// tem que ser a frase do prazo — sem o prefixo "Falha ao ler a imagem..." em
// cima, que só empurrava a informação útil para o fim da linha.
func TestClipboardTimeoutNoticeReachesTranscript(t *testing.T) {
	stubClipboardContent(t, clipboardStub{
		err: &imageinput.ClipboardTimeoutError{Tool: "wl-paste"},
	})

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

	want := "A área de transferência não respondeu (wl-paste); tente de novo."
	if got := lastTranscriptText(updated.(model)); got != want {
		t.Fatalf("aviso = %q, want %q", got, want)
	}
}
