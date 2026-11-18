package imageinput

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// O Finder grava «class furl» E texto (só o nome do arquivo) no pasteboard, e
// o AppleScript antigo lia `the clipboard` como texto — `as alias` estourava
// -1700 e o Ctrl+V silenciava. O leitor agora usa JXA lendo NSURL direto, sem
// checagem prévia de `clipboard info`. Estes testes dirigem o runner injetado:
// várias linhas com espaço e acento viram caminhos exatos; texto puro sai
// vazio com exit 0; erro do runner é silêncio, não erro.
func TestReadClipboardFilesDarwinJXA(t *testing.T) {
	restore := func() func() {
		runner := darwinClipboardFileRunner
		return func() { darwinClipboardFileRunner = runner }
	}()
	t.Cleanup(restore)

	t.Run("vários arquivos com espaço e acento", func(t *testing.T) {
		var gotArgs []string
		darwinClipboardFileRunner = func(script string) ([]byte, error) {
			gotArgs = []string{"osascript", "-l", "JavaScript", "-e", script}
			return []byte("/Users/g/Captura de Tela 2026.png\n/Users/g/Relatório mensal.pdf\n"), nil
		}
		paths, err := readClipboardFilesDarwin()
		if err != nil {
			t.Fatalf("err = %v, want none", err)
		}
		want := []string{"/Users/g/Captura de Tela 2026.png", "/Users/g/Relatório mensal.pdf"}
		if !reflect.DeepEqual(paths, want) {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
		if len(gotArgs) != 5 || gotArgs[0] != "osascript" || gotArgs[1] != "-l" || gotArgs[2] != "JavaScript" {
			t.Fatalf("comando montado errado: %#v", gotArgs)
		}
		if !strings.Contains(gotArgs[4], "readObjectsForClasses") || !strings.Contains(gotArgs[4], "isFileURL") {
			t.Fatalf("script não lê NSURL do pasteboard: %q", gotArgs[4])
		}
	})

	t.Run("clipboard sem arquivo sai vazio", func(t *testing.T) {
		darwinClipboardFileRunner = func(script string) ([]byte, error) {
			return []byte(""), nil
		}
		paths, err := readClipboardFilesDarwin()
		if err != nil || len(paths) != 0 {
			t.Fatalf("paths=%#v err=%v, want silêncio", paths, err)
		}
	})

	t.Run("erro do runner é silêncio", func(t *testing.T) {
		darwinClipboardFileRunner = func(script string) ([]byte, error) {
			return nil, errors.New("exit status 1")
		}
		paths, err := readClipboardFilesDarwin()
		if err != nil || len(paths) != 0 {
			t.Fatalf("paths=%#v err=%v, want silêncio", paths, err)
		}
	})
}
