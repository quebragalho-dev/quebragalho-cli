package imageinput

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Num Mac de fábrica colar print nunca funcionava: a extração chamava pngpaste
// (só Homebrew) e caiu num python3 sem PyObjC, então a TUI sempre dizia "no
// helper on this host can read it". O caminho agora é JXA via osascript, que
// acompanha o macOS. Estes testes dirigem o runner e o script sem Mac.
func TestReadClipboardImageDarwinJXA(t *testing.T) {
	restore := func() func() {
		runner := darwinClipboardImageRunner
		return func() { darwinClipboardImageRunner = runner }
	}()
	t.Cleanup(restore)

	hermeticClipboardHome(t)

	t.Run("script usa a ponte ObjC e converte TIFF em PNG", func(t *testing.T) {
		for _, want := range []string{
			"ObjC.import('AppKit')",
			"function run(argv)",
			"$.NSPasteboard.generalPasteboard",
			"pb.dataForType($.NSPasteboardTypePNG)",
			"pb.dataForType($.NSPasteboardTypeTIFF)",
			"$.NSBitmapImageRep.imageRepWithData",
			"$.NSBitmapImageFileTypePNG",
			"writeToFileAtomically(path, true)",
		} {
			if !strings.Contains(darwinClipboardImageScript, want) {
				t.Fatalf("script não contém %q:\n%s", want, darwinClipboardImageScript)
			}
		}
		// O Mac comum não tem nenhum dos dois: voltar a apoiar-se neles
		// reintroduz o bug que este arquivo cobre.
		for _, unwanted := range []string{"pngpaste", "python3", "AppKit.NSPasteboard"} {
			if strings.Contains(darwinClipboardImageScript, unwanted) {
				t.Fatalf("script ainda depende de %q (não existe num Mac de fábrica)", unwanted)
			}
		}
	})

	t.Run("destino vai em argv, não interpolado no script", func(t *testing.T) {
		var gotScript, gotPath string
		darwinClipboardImageRunner = func(script, destPath string) error {
			gotScript, gotPath = script, destPath
			// Osascript de verdade substituiria o arquivo; o stub grava os bytes.
			return os.WriteFile(destPath, []byte("png-bytes"), 0o600)
		}

		data, err := readClipboardImageDarwinJXA()
		if err != nil {
			t.Fatalf("err = %v, want none", err)
		}
		if string(data) != "png-bytes" {
			t.Fatalf("data = %q, want os bytes gravados pelo script", data)
		}
		if gotScript != darwinClipboardImageScript {
			t.Fatal("o runner não recebeu o script de extração")
		}
		if gotPath == "" {
			t.Fatal("caminho de destino vazio")
		}
		if strings.Contains(gotScript, gotPath) {
			t.Fatal("caminho interpolado no script: quebra com aspas/barra invertida")
		}
	})

	t.Run("temporário fica na pasta privada e é apagado", func(t *testing.T) {
		var gotPath string
		darwinClipboardImageRunner = func(script, destPath string) error {
			gotPath = destPath
			return os.WriteFile(destPath, []byte("png-bytes"), 0o600)
		}

		if _, err := readClipboardImageDarwinJXA(); err != nil {
			t.Fatalf("err = %v, want none", err)
		}
		if dir := filepath.Dir(gotPath); dir != mustClipboardTempDir(t) {
			t.Fatalf("temporário em %q, want %q (print não vai para o /tmp compartilhado)", dir, mustClipboardTempDir(t))
		}
		if _, err := os.Stat(gotPath); !os.IsNotExist(err) {
			t.Fatalf("temporário %q não foi apagado (err=%v)", gotPath, err)
		}
	})

	t.Run("erro do osascript sobe para o chamador", func(t *testing.T) {
		darwinClipboardImageRunner = func(script, destPath string) error {
			return errors.New("exit status 1")
		}
		if _, err := readClipboardImageDarwinJXA(); err == nil {
			t.Fatal("erro do runner virou silêncio")
		}
	})

	// O caso do bug: o runner "roda" mas não produz PNG. Isso tem que chegar
	// como "há imagem e não consegui ler", não como "clipboard vazio".
	t.Run("script sem saída vira imagem ilegível", func(t *testing.T) {
		info := darwinClipboardInfo
		t.Cleanup(func() { darwinClipboardInfo = info })
		darwinClipboardInfo = func() (string, error) { return `«class TIFF», «class PNGf»`, nil }
		darwinClipboardImageRunner = func(script, destPath string) error { return nil }

		if _, err := readClipboardImageDarwin(); !errors.Is(err, ErrClipboardImageUnreadable) {
			t.Fatalf("err = %v, want ErrClipboardImageUnreadable", err)
		}
	})
}

// A mensagem é a única coisa que o usuário vê quando a extração falha. Mandar
// instalar pngpaste/PyObjC era o defeito: num Mac de fábrica nenhum dos dois
// está disponível.
func TestClipboardImageUnreadableMessageIsPortugueseAndInstallsNothing(t *testing.T) {
	msg := ErrClipboardImageUnreadable.Error()
	if !strings.HasPrefix(msg, "A área de transferência tem uma imagem") {
		t.Fatalf("mensagem = %q, want a frase em português", msg)
	}
	for _, unwanted := range []string{"pngpaste", "pyobjc", "PyObjC", "install", "brew"} {
		if strings.Contains(msg, unwanted) {
			t.Fatalf("mensagem manda instalar %q: %q", unwanted, msg)
		}
	}
}

// Dir privado do clipboard (0700) via XDG_CONFIG_HOME/HOME, sem tocar no home
// real — o XDG sozinho não redireciona no macOS.
func hermeticClipboardHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}
