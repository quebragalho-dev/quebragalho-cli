package imageinput

import (
	"os"
	"path/filepath"
	"testing"
)

// Converte URIs file:// da área de transferência (wl-paste/xclip text/uri-list)
// em caminhos locais, decodificando %XX. Sem isso, um arquivo copiado no
// gerenciador de arquivos chega como "file:///home/u/foto%20nova.png" e nunca
// resolve para um anexo.
func TestClipboardFileURLToPath(t *testing.T) {
	cases := []struct {
		name   string
		uri    string
		want   string
		wantOK bool
	}{
		{name: "uri simples", uri: "file:///home/u/foto.png", want: "/home/u/foto.png", wantOK: true},
		{name: "espaco escapado", uri: "file:///home/u/foto%20nova.png", want: "/home/u/foto nova.png", wantOK: true},
		{name: "localhost", uri: "file://localhost/home/u/foto.png", want: "/home/u/foto.png", wantOK: true},
		{name: "http nao e arquivo", uri: "https://example.com/foto.png", wantOK: false},
		{name: "texto puro nao e uri", uri: "/home/u/foto.png", wantOK: false},
		{name: "vazio", uri: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ClipboardFileURLToPath(tc.uri)
			if ok != tc.wantOK || got != tc.want {
				t.Fatalf("ClipboardFileURLToPath(%q) = (%q, %v), want (%q, %v)",
					tc.uri, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// Ponto de extensão para vídeo: o engenheiro de vídeo implementa o anexo;
// aqui apenas garantimos que a classificação existe e não confunde vídeo com
// imagem (que seguiria para o pipeline de visão e falharia de outro jeito).
func TestIsVideoPathExtensionPoint(t *testing.T) {
	for _, p := range []string{"clip.mp4", "CLIP.MOV", "a.webm", "a.mkv", "a.m4v", "a.avi"} {
		if !IsVideoPath(p) {
			t.Fatalf("IsVideoPath(%q) = false, want true (extensão de vídeo)", p)
		}
	}
	for _, p := range []string{"foto.png", "doc.pdf", "nota.txt", "foto.jpeg"} {
		if IsVideoPath(p) {
			t.Fatalf("IsVideoPath(%q) = true, want false", p)
		}
	}
}

// O temporário do clipboard vive em pasta privada do qg (0700), não no /tmp
// compartilhado: são bytes do usuário (prints podem conter dados sensíveis).
func TestWriteClipboardTempPrivateDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		t.Setenv("HOME", dir)
	}

	png := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00}
	path, err := WriteClipboardTemp(png, ".png")
	if err != nil {
		t.Fatalf("WriteClipboardTemp: %v", err)
	}
	defer os.Remove(path)

	if got := filepath.Dir(path); got != mustClipboardTempDir(t) {
		t.Fatalf("temporário fora da pasta privada do qg: %q", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("temporário com permissão aberta: %v (err=%v)", info, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != string(png) {
		t.Fatalf("conteúdo do temporário divergiu (err=%v)", err)
	}
}

func mustClipboardTempDir(t *testing.T) string {
	t.Helper()
	dir, err := ClipboardTempDir()
	if err != nil {
		t.Fatalf("ClipboardTempDir: %v", err)
	}
	return dir
}
