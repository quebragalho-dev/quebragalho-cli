package imageinput

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Colar no macOS faz uma única execução do osascript: lista os arquivos
// copiados e, sem arquivo, grava o PNG da imagem no destino. Não se chama
// `clipboard info`, que converte o print em todos os formatos antes de responder
// (0,70 s para um print de 27 MB). O runner é injetado, então estes testes rodam
// em qualquer SO e nunca tocam o clipboard real.

const darwinTestPNG = "\x89PNG\r\n\x1a\n" + "png-rest"

// hermeticClipboardHome isola o diretório privado do clipboard (0700) do home real.
func hermeticClipboardHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
}

// stubDarwinContent troca o runner único. reply recebe o destino (argv[0]) e
// devolve a saída do script; o destino e o modo observados voltam para os asserts.
func stubDarwinContent(t *testing.T, reply func(dest string) (string, error)) (calls *int, gotDest, gotFlag *string) {
	t.Helper()
	hermeticClipboardHome(t)
	// A leitura nativa lê a área de transferência geral: aqui ela fica
	// indisponível, para o osascript falso ser o caminho testado.
	prevNative := clipboardNativeReader
	t.Cleanup(func() { clipboardNativeReader = prevNative })
	clipboardNativeReader = func(bool) (ClipboardContent, error) {
		return ClipboardContent{}, errNativeClipboardUnavailable
	}
	prev := darwinClipboardContentRunner
	t.Cleanup(func() { darwinClipboardContentRunner = prev })
	calls, gotDest, gotFlag = new(int), new(string), new(string)
	darwinClipboardContentRunner = func(script string, args ...string) ([]byte, error) {
		*calls++
		*gotDest = args[0]
		*gotFlag = args[1]
		out, err := reply(args[0])
		return []byte(out), err
	}
	return calls, gotDest, gotFlag
}

func reportReply(t *testing.T, r darwinClipboardReport, writePNG bool) func(string) (string, error) {
	return func(dest string) (string, error) {
		if writePNG {
			if err := os.WriteFile(dest, []byte(darwinTestPNG), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(b), nil
	}
}

func TestReadClipboardContentDarwinSingleRunWithoutClipboardInfo(t *testing.T) {
	calls, gotDest, gotFlag := stubDarwinContent(t, reportReply(t, darwinClipboardReport{}, false))

	if _, err := readClipboardContentDarwin(false); err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if *calls != 1 {
		t.Fatalf("runner chamado %d vezes, want 1", *calls)
	}
	if *gotFlag != "0" {
		t.Fatalf("modo = %q, want \"0\" no paste normal (arquivo suprime imagem)", *gotFlag)
	}
	if *gotDest == "" || strings.Contains(darwinClipboardContentScript, *gotDest) {
		t.Fatal("destino deve ir em argv, nunca interpolado no script")
	}
	if dir := filepath.Dir(*gotDest); dir != mustClipboardTempDir(t) {
		t.Fatalf("destino em %q, want a pasta privada %q", dir, mustClipboardTempDir(t))
	}
}

func TestDarwinClipboardContentScriptShape(t *testing.T) {
	for _, want := range []string{
		"ObjC.import('AppKit')",
		"function run(argv)",
		"readObjectsForClassesOptions",
		"isFileURL",
		"$.NSPasteboard.generalPasteboard",
		"pb.types.containsObject",
		"'public.png'",
		"'public.tiff'",
		"'public.jpeg'",
		"'com.compuserve.gif'",
		"$.NSBitmapImageRep.imageRepWithData",
		"$.NSBitmapImageFileTypePNG",
		"writeToFileAtomically(dest, true)",
		"JSON.stringify(out)",
		// Arquivo copiado suprime a imagem, a não ser que o modo peça.
		"if (out.files.length > 0 && !imageWithFiles) { return JSON.stringify(out); }",
	} {
		if !strings.Contains(darwinClipboardContentScript, want) {
			t.Fatalf("script não contém %q:\n%s", want, darwinClipboardContentScript)
		}
	}
	// A regra de arquivo tem que vir antes da leitura de tipos de imagem.
	if strings.Index(darwinClipboardContentScript, "out.files.length > 0") > strings.Index(darwinClipboardContentScript, "imageTypes =") {
		t.Fatal("a regra de arquivo precisa vir antes da leitura da imagem")
	}
	for _, unwanted := range []string{"clipboard info", "pngpaste", "python3", "AppKit.NSPasteboard"} {
		if strings.Contains(darwinClipboardContentScript, unwanted) {
			t.Fatalf("script depende de %q (não existe num Mac de fábrica ou é o caminho lento)", unwanted)
		}
	}
}

func TestReadClipboardContentDarwinCases(t *testing.T) {
	timeout := &ClipboardTimeoutError{Tool: "osascript"}

	cases := []struct {
		name     string
		report   darwinClipboardReport
		writePNG bool   // o script gravou bytes PNG no destino
		raw      string // resposta crua, no lugar do JSON
		runErr   error
		want     ClipboardContent
		wantErr  error
	}{
		{
			name: "texto ou vazio é silêncio",
			want: ClipboardContent{},
		},
		{
			name: "arquivos copiados sem imagem",
			report: darwinClipboardReport{
				Files: []string{"/Users/g/Captura de Tela 2026.png", "/Users/g/\"Relatório\"\nlinha.pdf"},
			},
			want: ClipboardContent{Files: []string{"/Users/g/Captura de Tela 2026.png", "/Users/g/\"Relatório\"\nlinha.pdf"}},
		},
		{
			name:     "imagem sozinha",
			report:   darwinClipboardReport{Image: true, Written: true},
			writePNG: true,
			want:     ClipboardContent{Image: []byte(darwinTestPNG), MediaType: "image/png"},
		},
		{
			name:    "imagem sem gravação é ilegível",
			report:  darwinClipboardReport{Image: true, Written: false},
			wantErr: ErrClipboardImageUnreadable,
		},
		{
			name:    "gravação sem bytes é ilegível",
			report:  darwinClipboardReport{Image: true, Written: true},
			wantErr: ErrClipboardImageUnreadable,
		},
		{
			name:   "osascript quebrado é silêncio",
			runErr: errors.New("exit status 1"),
			want:   ClipboardContent{},
		},
		{
			name:    "osascript travado é timeout",
			runErr:  timeout,
			wantErr: timeout,
		},
		{
			name: "resposta ilegível é silêncio",
			raw:  "não é json",
			want: ClipboardContent{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, gotDest, _ := stubDarwinContent(t, func(dest string) (string, error) {
				if tc.writePNG {
					if err := os.WriteFile(dest, []byte(darwinTestPNG), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if tc.runErr != nil {
					return "", tc.runErr
				}
				if tc.raw != "" {
					return tc.raw, nil
				}
				return reportReplyJSON(t, tc.report), nil
			})

			got, err := readClipboardContentDarwin(false)
			if *calls != 1 {
				t.Fatalf("runner chamado %d vezes, want 1", *calls)
			}
			if _, statErr := os.Stat(*gotDest); !os.IsNotExist(statErr) {
				t.Fatalf("temporário %q não foi apagado (err=%v)", *gotDest, statErr)
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func reportReplyJSON(t *testing.T, r darwinClipboardReport) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ReadClipboardImage pede imagem de propósito: com arquivo copiado ela continua
// sendo lida (modo "1"), e a ilegível ainda é erro acionável.
func TestReadClipboardImageDarwinKeepsImageWithFiles(t *testing.T) {
	calls, _, gotFlag := stubDarwinContent(t, func(dest string) (string, error) {
		if err := os.WriteFile(dest, []byte(darwinTestPNG), 0o600); err != nil {
			t.Fatal(err)
		}
		return reportReplyJSON(t, darwinClipboardReport{Files: []string{"/a.pdf"}, Image: true, Written: true}), nil
	})

	data, err := readClipboardImageDarwin()
	if err != nil {
		t.Fatalf("err = %v, want none", err)
	}
	if string(data) != darwinTestPNG {
		t.Fatalf("data = %q, want a imagem mesmo com arquivo copiado", data)
	}
	if *calls != 1 || *gotFlag != "1" {
		t.Fatalf("calls=%d modo=%q, want 1 chamada com modo \"1\"", *calls, *gotFlag)
	}
}

func TestReadClipboardImageDarwinUnreadableIsActionable(t *testing.T) {
	stubDarwinContent(t, reportReply(t, darwinClipboardReport{Image: true, Written: false}, false))

	if _, err := readClipboardImageDarwin(); !errors.Is(err, ErrClipboardImageUnreadable) {
		t.Fatalf("err = %v, want ErrClipboardImageUnreadable", err)
	}
}

// Lista de arquivos com espaço, acento, aspas e quebra de linha; sem imagem.
func TestReadClipboardFilesDarwin(t *testing.T) {
	t.Run("vários arquivos com espaço e acento", func(t *testing.T) {
		stubDarwinContent(t, reportReply(t, darwinClipboardReport{
			Files: []string{"/Users/g/Captura de Tela 2026.png", "/Users/g/Relatório mensal.pdf"},
		}, false))
		paths, err := readClipboardFilesDarwin()
		if err != nil {
			t.Fatalf("err = %v, want none", err)
		}
		want := []string{"/Users/g/Captura de Tela 2026.png", "/Users/g/Relatório mensal.pdf"}
		if !reflect.DeepEqual(paths, want) {
			t.Fatalf("paths = %#v, want %#v", paths, want)
		}
	})

	t.Run("sem arquivo sai vazio", func(t *testing.T) {
		stubDarwinContent(t, reportReply(t, darwinClipboardReport{}, false))
		if paths, err := readClipboardFilesDarwin(); err != nil || len(paths) != 0 {
			t.Fatalf("paths=%#v err=%v, want silêncio", paths, err)
		}
	})

	t.Run("imagem ilegível não vira erro de arquivo", func(t *testing.T) {
		stubDarwinContent(t, reportReply(t, darwinClipboardReport{Image: true, Written: false}, false))
		if paths, err := readClipboardFilesDarwin(); err != nil || len(paths) != 0 {
			t.Fatalf("paths=%#v err=%v, want silêncio", paths, err)
		}
	})

	t.Run("erro do runner é silêncio", func(t *testing.T) {
		stubDarwinContent(t, func(string) (string, error) { return "", errors.New("exit status 1") })
		if paths, err := readClipboardFilesDarwin(); err != nil || len(paths) != 0 {
			t.Fatalf("paths=%#v err=%v, want silêncio", paths, err)
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
