package imageinput

import (
	"errors"
	"strings"
	"testing"
)

// Sem wl-clipboard/xclip o aviso saía duplicado:
// "não foi possível ler a área de transferência: instale wl-clipboard ...",
// e a TUI ainda prefixava "Falha ao ler a imagem...". Uma frase só basta.
func TestClipboardToolMissingMessageIsSingleSentence(t *testing.T) {
	got := ErrClipboardToolMissing.Error()
	want := "Para colar imagens no Linux, instale wl-clipboard (Wayland) ou xclip (X11)"
	if got != want {
		t.Fatalf("mensagem = %q, want %q", got, want)
	}
	if strings.Count(got, "instale") != 1 {
		t.Fatalf("mensagem com instrução repetida: %q", got)
	}
}

// A ferramenta ausente não pode ser confundida com prazo estourado nem com
// clipboard vazio: são três respostas diferentes para o usuário.
func TestClipboardToolMissingIsDistinctFromTimeout(t *testing.T) {
	if IsClipboardTimeout(ErrClipboardToolMissing) {
		t.Fatal("ferramenta ausente não é erro de prazo")
	}
	if !errors.Is(ErrClipboardToolMissing, ErrClipboardToolMissing) {
		t.Fatal("ErrClipboardToolMissing deveria casar consigo mesmo")
	}
}
