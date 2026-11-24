package imageinput

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego/objc"
)

// Estes testes leem um NSPasteboard com nome próprio, criado para cada caso:
// nunca a área de transferência geral da pessoa. Só rodam no macOS.

// requireAppKit carrega o AppKit antes de qualquer chamada Objective-C do teste.
// Sem ele, as classes são nil e a primeira mensagem derruba o teste.
func requireAppKit(t *testing.T) {
	t.Helper()
	if err := loadAppKit(); err != nil {
		t.Skipf("AppKit indisponível: %v", err)
	}
}

// namedPasteboard cria um pasteboard de teste e o solta ao fim do teste.
func namedPasteboard(t *testing.T) objc.ID {
	t.Helper()
	requireAppKit(t)
	name := fmt.Sprintf("qg-test-%d", time.Now().UnixNano())
	var pb objc.ID
	withAutoreleasePool(func() {
		pb = objc.ID(objc.GetClass("NSPasteboard")).Send(objc.RegisterName("pasteboardWithName:"), nsStringFrom(name))
		pb = pb.Send(objc.RegisterName("retain"))
	})
	t.Cleanup(func() {
		withAutoreleasePool(func() {
			pb.Send(objc.RegisterName("releaseGlobally"))
			pb.Send(objc.RegisterName("release"))
		})
	})
	return pb
}

// nsDataFrom cria um NSData com cópia dos bytes.
func nsDataFrom(data []byte) objc.ID {
	var ptr unsafe.Pointer
	if len(data) > 0 {
		ptr = unsafe.Pointer(&data[0])
	}
	return objc.ID(objc.GetClass("NSData")).Send(objc.RegisterName("dataWithBytes:length:"), ptr, uint(len(data)))
}

// fillPasteboard limpa o pasteboard e declara um único tipo com os bytes dados.
// Com data nil, só declara o tipo, sem conteúdo.
func fillPasteboard(pb objc.ID, typ string, data []byte) {
	withAutoreleasePool(func() {
		pb.Send(objc.RegisterName("clearContents"))
		types := objc.ID(objc.GetClass("NSArray")).Send(objc.RegisterName("arrayWithObject:"), nsStringFrom(typ))
		pb.Send(objc.RegisterName("declareTypes:owner:"), types, objc.ID(0))
		if data != nil {
			pb.Send(objc.RegisterName("setData:forType:"), nsDataFrom(data), nsStringFrom(typ))
		}
	})
}

// fillPasteboardWithFile escreve um NSURL de arquivo (ou de outro esquema, se
// urlString não começar com file:) no pasteboard, como o Finder faz.
func fillPasteboardWithFile(pb objc.ID, path string, isFile bool) {
	withAutoreleasePool(func() {
		pb.Send(objc.RegisterName("clearContents"))
		var url objc.ID
		if isFile {
			url = objc.ID(objc.GetClass("NSURL")).Send(objc.RegisterName("fileURLWithPath:"), nsStringFrom(path))
		} else {
			url = objc.ID(objc.GetClass("NSURL")).Send(objc.RegisterName("URLWithString:"), nsStringFrom(path))
		}
		pb.Send(objc.RegisterName("writeObjects:"), objc.ID(objc.GetClass("NSArray")).Send(objc.RegisterName("arrayWithObject:"), url))
	})
}

// addToPasteboard acrescenta um tipo com dados ao pasteboard já preenchido.
func addToPasteboard(pb objc.ID, typ string, data []byte) {
	withAutoreleasePool(func() {
		pb.Send(objc.RegisterName("setData:forType:"), nsDataFrom(data), nsStringFrom(typ))
	})
}

// encodePNG devolve um PNG 2x2 real, para o tipo PNG e para a conversão de TIFF.
func encodePNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// tiffFromPNG converte o PNG em TIFF pelo próprio AppKit, para montar o caso.
func tiffFromPNG(t *testing.T, pngBytes []byte) []byte {
	t.Helper()
	requireAppKit(t)
	var tiff []byte
	withAutoreleasePool(func() {
		rep := objc.ID(objc.GetClass("NSBitmapImageRep")).Send(objc.RegisterName("imageRepWithData:"), nsDataFrom(pngBytes))
		if rep == 0 {
			t.Fatal("NSBitmapImageRep não leu o PNG de teste")
		}
		data := rep.Send(objc.RegisterName("representationUsingType:properties:"),
			uint(0), // NSBitmapImageFileTypeTIFF
			objc.ID(objc.GetClass("NSDictionary")).Send(objc.RegisterName("dictionary")))
		if data == 0 {
			t.Fatal("AppKit não gerou TIFF do PNG de teste")
		}
		tiff = nsDataCopy(data)
	})
	return tiff
}

// Mesma regra de darwinClipboardContentScript, agora lida direto do pasteboard.
func TestReadPasteboardContentRules(t *testing.T) {
	requireAppKit(t)
	pngBytes := encodePNG(t)
	tiffBytes := tiffFromPNG(t, pngBytes)

	cases := []struct {
		name           string
		fill           func(pb objc.ID)
		imageWithFiles bool
		want           ClipboardContent
		wantErr        error
		anyPNG         bool // o TIFF vira PNG; o conteúdo exato não é o mesmo
	}{
		{
			name: "texto é silêncio",
			fill: func(pb objc.ID) { fillPasteboard(pb, "public.utf8-plain-text", []byte("oi")) },
			want: ClipboardContent{},
		},
		{
			name: "PNG segue direto",
			fill: func(pb objc.ID) { fillPasteboard(pb, "public.png", pngBytes) },
			want: ClipboardContent{Image: pngBytes, MediaType: "image/png"},
		},
		{
			name:   "TIFF vira PNG",
			fill:   func(pb objc.ID) { fillPasteboard(pb, "public.tiff", tiffBytes) },
			anyPNG: true,
		},
		{
			// O macOS devolve o caminho com o acento decomposto (NFD: "o" mais
			// U+0301). O nome vai em NFD aqui, como o Finder o entrega, para a
			// comparação ser exata; o produto não normaliza nada.
			name: "arquivo copiado suprime a imagem",
			fill: func(pb objc.ID) {
				fillPasteboardWithFile(pb, "/Users/g/Relatório mensal.pdf", true)
				addToPasteboard(pb, "public.png", pngBytes)
			},
			want: ClipboardContent{Files: []string{"/Users/g/Relatório mensal.pdf"}},
		},
		{
			name:           "arquivo copiado com imagem pedida",
			imageWithFiles: true,
			fill: func(pb objc.ID) {
				fillPasteboardWithFile(pb, "/Users/g/a.pdf", true)
				addToPasteboard(pb, "public.png", pngBytes)
			},
			want: ClipboardContent{Image: pngBytes, MediaType: "image/png", Files: []string{"/Users/g/a.pdf"}},
		},
		{
			name: "URL que não é arquivo é ignorada",
			fill: func(pb objc.ID) { fillPasteboardWithFile(pb, "https://exemplo.invalid/a.png", false) },
			want: ClipboardContent{},
		},
		{
			name:    "imagem declarada sem dados é ilegível",
			fill:    func(pb objc.ID) { fillPasteboard(pb, "public.png", nil) },
			wantErr: ErrClipboardImageUnreadable,
		},
		{
			name:    "imagem de zero bytes é ilegível",
			fill:    func(pb objc.ID) { fillPasteboard(pb, "public.png", []byte{}) },
			wantErr: ErrClipboardImageUnreadable,
		},
		{
			name:    "bytes que não são imagem são ilegíveis",
			fill:    func(pb objc.ID) { fillPasteboard(pb, "public.png", []byte("não é imagem")) },
			wantErr: ErrClipboardImageUnreadable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pb := namedPasteboard(t)
			tc.fill(pb)

			got, err := readPasteboardContent(tc.imageWithFiles, func() objc.ID { return pb })
			if tc.wantErr != nil {
				if err != tc.wantErr {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want none", err)
			}
			if tc.anyPNG {
				if got.MediaType != "image/png" || len(got.Image) < 8 || string(got.Image[:4]) != "\x89PNG" {
					t.Fatalf("TIFF não virou PNG: media=%q", got.MediaType)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
