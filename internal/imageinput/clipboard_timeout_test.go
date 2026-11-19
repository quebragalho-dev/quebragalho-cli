package imageinput

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// hungClipboardHelper escreve um helper falso que dorme bem além do prazo e só
// então grava o marcador: se o processo for morto no prazo, o marcador nunca
// aparece. O PATH mantém o resto do sistema para o `sh` achar sleep/touch — o
// falso vem primeiro, então é ele que o exec encontra.
func hungClipboardHelper(t *testing.T, name string) (marker string) {
	t.Helper()
	dir := t.TempDir()
	marker = filepath.Join(dir, "finished")
	body := "#!/bin/sh\nsleep 30\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	clipboardTimeout = 300 * time.Millisecond
	t.Cleanup(func() { clipboardTimeout = defaultClipboardTimeout })
	return marker
}

// No GNOME/Wayland o wl-paste pode travar para sempre (o offerer do clipboard
// nunca responde). Sem prazo, o Ctrl+V fica mudo e cada nova tentativa deixa
// mais um processo pendurado. O leitor agora tem prazo: estourou, o processo é
// morto e o usuário recebe um aviso em português em vez de silêncio.
func TestRunClipboardStdoutTimesOutOnHungHelper(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper falso é um script POSIX")
	}
	marker := hungClipboardHelper(t, "wl-paste")

	start := time.Now()
	_, err := runClipboardStdout("wl-paste", "--list-types")
	elapsed := time.Since(start)

	if elapsed > 10*time.Second {
		t.Fatalf("leitura do clipboard levou %s; o prazo não foi aplicado", elapsed)
	}
	var timeoutErr *ClipboardTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("err = %v, want ClipboardTimeoutError", err)
	}
	if timeoutErr.Tool != "wl-paste" {
		t.Fatalf("ferramenta = %q, want wl-paste", timeoutErr.Tool)
	}
	want := "A área de transferência não respondeu (wl-paste); tente de novo."
	if timeoutErr.Error() != want {
		t.Fatalf("mensagem = %q, want %q", timeoutErr.Error(), want)
	}

	// O processo tem que morrer junto: senão cada Ctrl+V deixa um pendurado.
	time.Sleep(time.Second)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("o helper continuou rodando depois do prazo: o processo não foi morto")
	}
}

// O prazo padrão precisa cobrir o PowerShell frio do Windows (2–3 s).
func TestClipboardTimeoutDefaultCoversColdPowerShell(t *testing.T) {
	if defaultClipboardTimeout < 5*time.Second {
		t.Fatalf("prazo padrão = %s, want >= 5s", defaultClipboardTimeout)
	}
}

// O estouro de uma ferramenta precisa chegar ao chamador como erro acionável,
// não como "não há imagem": quem cola um print quer saber que a leitura travou.
func TestReadClipboardImageLinuxSurfacesHelperTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper falso é um script POSIX")
	}
	hungClipboardHelper(t, "wl-paste")
	hungClipboardHelper(t, "xclip")

	_, err := readClipboardImageLinux()
	if !IsClipboardTimeout(err) {
		t.Fatalf("err = %v, want erro de prazo", err)
	}
	if want := "A área de transferência não respondeu (wl-paste); tente de novo."; err.Error() != want {
		t.Fatalf("mensagem = %q, want %q", err.Error(), want)
	}
}

// O mesmo vale para a lista de arquivos copiados (text/uri-list).
func TestReadClipboardFilesLinuxSurfacesHelperTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper falso é um script POSIX")
	}
	hungClipboardHelper(t, "wl-paste")
	hungClipboardHelper(t, "xclip")

	_, err := readClipboardFilesLinux()
	if !IsClipboardTimeout(err) {
		t.Fatalf("err = %v, want erro de prazo", err)
	}
}

// A leitura do macOS (JXA via osascript) também tem prazo.
func TestDarwinClipboardFileRunnerUsesDeadline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("helper falso é um script POSIX")
	}
	marker := hungClipboardHelper(t, "osascript")

	_, err := darwinClipboardFileRunner(darwinClipboardFileScript)
	if !IsClipboardTimeout(err) {
		t.Fatalf("err = %v, want erro de prazo", err)
	}
	time.Sleep(time.Second)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("o osascript continuou rodando depois do prazo: o processo não foi morto")
	}
}
