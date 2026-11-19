package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/quebragalho-dev/quebragalho-cli/internal/imageinput"
)

// Alt+V faz o mesmo que Ctrl+V: sonda o clipboard por imagem. O Windows
// Terminal tem Ctrl+V ligado à ação "paste" do próprio terminal, então o qg
// nunca recebe essa tecla; com um print (só Bitmap) no clipboard o terminal não
// tem texto para colar e não manda nada, e o anexo nunca acontecia.
func TestAltVProbesClipboardForImage(t *testing.T) {
	original := readClipboardContent
	t.Cleanup(func() { readClipboardContent = original })
	readClipboardContent = func() (imageinput.ClipboardContent, error) {
		return imageinput.ClipboardContent{Image: []byte("png-bytes"), MediaType: "image/png"}, nil
	}

	m := newModel(context.Background(), Options{})
	_, cmd := m.Update(testKeyAlt('v'))
	if cmd == nil {
		t.Fatal("Alt+V não emitiu comando; a sonda do clipboard nunca rodou")
	}
	image, ok := execCmd(cmd).(clipboardImageMsg)
	if !ok {
		t.Fatal("o comando emitido não foi a sonda do clipboard")
	}
	if string(image.data) != "png-bytes" || image.mediaType != "image/png" {
		t.Fatalf("mensagem de imagem inesperada: %+v", image)
	}
}

// Alt+V não insere texto no composer: como o Ctrl+V, só sonda o clipboard.
func TestAltVDoesNotPasteTextIntoComposer(t *testing.T) {
	original := readClipboardContent
	t.Cleanup(func() { readClipboardContent = original })
	readClipboardContent = func() (imageinput.ClipboardContent, error) {
		return imageinput.ClipboardContent{}, nil
	}

	m := newModel(context.Background(), Options{})
	m.input.SetValue("hello")
	m.input.CursorEnd()

	updated, _ := m.Update(testKeyAlt('v'))
	if got := updated.(model).composerValue(); got != "hello" {
		t.Fatalf("composer após Alt+V = %q, want unchanged", got)
	}
}

// Alt+V não colide com outro atalho: nem com os reservados, nem com os padrões
// configuráveis, nem com os Alt+<letra> do composer (Alt+D/B/F).
func TestAltVHasNoKeybindingConflict(t *testing.T) {
	v := parseBinding("alt+v")
	for _, r := range reservedBindings {
		if v == r.binding {
			t.Fatalf("Alt+V conflita com atalho reservado (%s)", r.description)
		}
	}
	defaults := []parsedBinding{
		parseBinding("ctrl+o"), parseBinding("ctrl+e"), parseBinding("ctrl+t"),
		parseBinding("ctrl+p"), parseBinding("ctrl+b"),
	}
	for _, d := range defaults {
		if v == d {
			t.Fatalf("Alt+V conflita com padrão configurável %s", d.Label())
		}
	}
	// O composer trata Alt+D (apaga palavra à frente), Alt+B e Alt+F (anda por
	// palavra): Alt+V precisa continuar livre ali.
	for _, key := range []tea.KeyMsg{testKeyAlt('d'), testKeyAlt('b'), testKeyAlt('f')} {
		if keyText(key) == "v" {
			t.Fatalf("Alt+%s já é usado pelo composer", keyText(key))
		}
	}
}

// A tecla aparece na ajuda junto do Ctrl+V, dizendo qual serve no Windows
// Terminal.
func TestHelpOverlayDocumentsAltV(t *testing.T) {
	m := newModel(context.Background(), Options{})
	var row string
	for _, g := range m.buildKeybindingGroups() {
		for _, b := range g.bindings {
			if strings.Contains(b.keys, "Alt+V") {
				row = b.keys + " " + b.desc
			}
		}
	}
	if row == "" {
		t.Fatal("overlay de ajuda não documenta Alt+V")
	}
	if !strings.Contains(row, "Ctrl+V") {
		t.Fatalf("a linha do Alt+V deveria mostrar também o Ctrl+V: %q", row)
	}
	if !strings.Contains(strings.ToLower(row), "windows terminal") {
		t.Fatalf("a linha do Alt+V deveria dizer que é o atalho do Windows Terminal: %q", row)
	}
}
