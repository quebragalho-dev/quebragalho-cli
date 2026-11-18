package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/quebragalho-dev/quebragalho-cli/internal/imageinput"
)

// Matriz do paste via leitor injetável: um único stub controla o que o SO
// oferece (imagem, arquivos, texto, vazio, ferramenta ausente) e cada linha
// prova o destino certo sem tocar no clipboard real.
func TestClipboardPasteMatrix(t *testing.T) {
	dir := t.TempDir()
	img := writeTestPNG(t, dir, "print.png")

	cases := []struct {
		name     string
		stub     clipboardStub
		via      string // "ctrlv" ou "paste-vazio" ou "texto"
		text     string
		wantImgs int
		wantText string
		wantNote string // fragmento esperado no transcript; "" = nenhum aviso novo
	}{
		{
			name: "imagem vira anexo", via: "ctrlv",
			stub:     clipboardStub{image: testPNGBytes(), mediaType: "image/png"},
			wantImgs: 1,
		},
		{
			name: "arquivos copiados anexam", via: "ctrlv",
			stub:     clipboardStub{files: []string{img}},
			wantImgs: 1,
		},
		{
			name: "texto continua texto", via: "texto", text: "olá mundo",
			stub:     clipboardStub{image: testPNGBytes(), mediaType: "image/png"},
			wantText: "olá mundo",
		},
		{
			name: "vazio é silencioso", via: "paste-vazio",
			stub: clipboardStub{},
		},
		{
			name: "ferramenta ausente avisa em português", via: "ctrlv",
			stub:     clipboardStub{err: imageinput.ErrClipboardToolMissing},
			wantNote: "wl-clipboard",
		},
		{
			name: "paste vazio com imagem anexa", via: "paste-vazio",
			stub:     clipboardStub{image: testPNGBytes(), mediaType: "image/png"},
			wantImgs: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubClipboardContent(t, tc.stub)
			m := newModel(context.Background(), Options{Cwd: dir, ModelName: "muse-spark-1.3-contributor"})
			before := len(m.transcript)

			var updated model
			switch tc.via {
			case "ctrlv":
				next, cmd := m.Update(testKeyCtrl('v'))
				msg := execCmd(cmd)
				if msg == nil {
					if tc.wantImgs > 0 || tc.wantNote != "" {
						t.Fatal("sonda não emitiu mensagem")
					}
					return
				}
				content, ok := msg.(clipboardImageMsg)
				if !ok {
					t.Fatalf("sonda emitiu %T", msg)
				}
				u2, _ := next.(model).Update(content)
				updated = u2.(model)
			case "paste-vazio":
				next, cmd := m.Update(testPaste(""))
				if cmd == nil {
					if tc.wantImgs > 0 || tc.wantNote != "" {
						t.Fatal("paste vazio deveria sondar")
					}
					return
				}
				msg := execCmd(cmd)
				if msg == nil {
					return // sonda silenciosa: nada a anexar
				}
				u2, _ := next.(model).Update(msg.(clipboardImageMsg))
				updated = u2.(model)
			case "texto":
				next, _ := m.Update(testPaste(tc.text))
				updated = next.(model)
			}

			if len(updated.pendingImages) != tc.wantImgs {
				t.Fatalf("anexos = %d, want %d", len(updated.pendingImages), tc.wantImgs)
			}
			if tc.wantText != "" && updated.composerValue() != tc.wantText {
				t.Fatalf("composer = %q, want %q", updated.composerValue(), tc.wantText)
			}
			if tc.wantNote != "" {
				found := false
				for _, row := range updated.transcript[before:] {
					if strings.Contains(row.text, tc.wantNote) {
						found = true
					}
				}
				if !found {
					t.Fatalf("sem aviso %q no transcript", tc.wantNote)
				}
			}
		})
	}
}
