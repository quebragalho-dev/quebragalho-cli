//go:build darwin

package imageinput

import (
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// Leitura nativa da área de transferência no macOS: NSPasteboard direto no
// processo, pelo runtime Objective-C (purego, sem cgo). Substitui o osascript,
// que custava 50–100 ms por Ctrl+V. A regra de conteúdo é a de
// darwinClipboardContentScript: arquivo copiado suprime a imagem, a não ser
// que quem pede seja a leitura explícita de imagem; PNG segue direto; TIFF,
// JPEG e GIF passam pelo NSBitmapImageRep.
//
// Chamadas do AppKit que lançam exceção Objective-C não podem ser pegas em Go,
// então nenhuma mensagem é enviada a nil, e cada resultado é checado antes do
// próximo passo.

// nsBitmapImageFileTypePNG é o valor de NSBitmapImageFileTypePNG.
const nsBitmapImageFileTypePNG = 4

// darwinImageTypes são os tipos de imagem na ordem de preferência.
var darwinImageTypes = []string{"public.png", "public.tiff", "public.jpeg", "com.compuserve.gif"}

var (
	appKitOnce sync.Once
	appKitErr  error

	objcAutoreleasePoolPush func() uintptr
	objcAutoreleasePoolPop  func(pool uintptr)
)

// loadAppKit abre o AppKit uma única vez. Sem ele não há NSPasteboard no
// processo, e quem chama cai no osascript.
func loadAppKit() error {
	appKitOnce.Do(func() {
		if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_LAZY|purego.RTLD_GLOBAL); err != nil {
			appKitErr = fmt.Errorf("abrindo AppKit: %w", err)
			return
		}
		lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			appKitErr = fmt.Errorf("abrindo libobjc: %w", err)
			return
		}
		purego.RegisterLibFunc(&objcAutoreleasePoolPush, lib, "objc_autoreleasePoolPush")
		purego.RegisterLibFunc(&objcAutoreleasePoolPop, lib, "objc_autoreleasePoolPop")
		for _, name := range []string{"NSPasteboard", "NSURL", "NSArray", "NSDictionary", "NSString", "NSData", "NSBitmapImageRep"} {
			if objc.GetClass(name) == 0 {
				appKitErr = fmt.Errorf("classe %s ausente", name)
				return
			}
		}
	})
	return appKitErr
}

// withAutoreleasePool roda fn numa thread fixa dentro de um pool de
// autorelease. Os objetos devolvidos pelo AppKit morrem ao sair do pool, então
// fn copia para a memória do Go tudo o que quer guardar antes de voltar.
func withAutoreleasePool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pool := objcAutoreleasePoolPush()
	defer objcAutoreleasePoolPop(pool)
	fn()
}

// readClipboardContentNative lê a área de transferência geral sob o prazo
// compartilhado com as outras leituras.
func readClipboardContentNative(imageWithFiles bool) (ClipboardContent, error) {
	if err := loadAppKit(); err != nil {
		return ClipboardContent{}, errNativeClipboardUnavailable
	}
	return runClipboardRead(func() (ClipboardContent, error) {
		return readPasteboardContent(imageWithFiles, func() objc.ID {
			return objc.ID(objc.GetClass("NSPasteboard")).Send(objc.RegisterName("generalPasteboard"))
		})
	})
}

// readPasteboardContent aplica a regra do paste ao pasteboard que `pasteboard`
// devolve. A função é chamada dentro do pool, então o pasteboard pode ser um
// nomeado (usado pelos testes) sem tocar a área de transferência da pessoa.
func readPasteboardContent(imageWithFiles bool, pasteboard func() objc.ID) (content ClipboardContent, err error) {
	if err := loadAppKit(); err != nil {
		return ClipboardContent{}, errNativeClipboardUnavailable
	}
	withAutoreleasePool(func() {
		content, err = readPasteboardContentInPool(imageWithFiles, pasteboard())
	})
	return content, err
}

func readPasteboardContentInPool(imageWithFiles bool, pb objc.ID) (ClipboardContent, error) {
	if pb == 0 {
		return ClipboardContent{}, errNativeClipboardUnavailable
	}
	var files []string
	items := pb.Send(objc.RegisterName("readObjectsForClasses:options:"),
		objc.ID(objc.GetClass("NSArray")).Send(objc.RegisterName("arrayWithObject:"), objc.ID(objc.GetClass("NSURL"))),
		objc.ID(objc.GetClass("NSDictionary")).Send(objc.RegisterName("dictionary")))
	if items != 0 {
		n := objc.Send[uint](items, objc.RegisterName("count"))
		for i := uint(0); i < n; i++ {
			u := items.Send(objc.RegisterName("objectAtIndex:"), i)
			if u == 0 || !objc.Send[bool](u, objc.RegisterName("isFileURL")) {
				continue
			}
			// Um NSURL de arquivo sem caminho não é arquivo copiado: sem texto, não entra.
			path := nsStringGo(u.Send(objc.RegisterName("path")))
			if path == "" {
				continue
			}
			files = append(files, path)
		}
	}
	if len(files) > 0 && !imageWithFiles {
		return ClipboardContent{Files: files}, nil
	}

	offered := ""
	if types := pb.Send(objc.RegisterName("types")); types != 0 {
		for _, t := range darwinImageTypes {
			if objc.Send[bool](types, objc.RegisterName("containsObject:"), nsStringFrom(t)) {
				offered = t
				break
			}
		}
	}
	if offered == "" {
		return ClipboardContent{Files: files}, nil
	}

	png := pb.Send(objc.RegisterName("dataForType:"), nsStringFrom(offered))
	if png != 0 && offered != "public.png" {
		png = pngFromBitmapData(png)
	}
	if png == 0 {
		// A imagem foi declarada, mas não há bytes legíveis: é erro acionável.
		return ClipboardContent{}, ErrClipboardImageUnreadable
	}
	img, err := imageContentFromBytes(nsDataCopy(png))
	if err != nil {
		return ClipboardContent{}, err
	}
	img.Files = files
	return img, nil
}

// pngFromBitmapData converte TIFF/JPEG/GIF em PNG pelo NSBitmapImageRep. Zero
// quando o AppKit não consegue decodificar.
func pngFromBitmapData(data objc.ID) objc.ID {
	rep := objc.ID(objc.GetClass("NSBitmapImageRep")).Send(objc.RegisterName("imageRepWithData:"), data)
	if rep == 0 {
		return 0
	}
	return rep.Send(objc.RegisterName("representationUsingType:properties:"),
		uint(nsBitmapImageFileTypePNG),
		objc.ID(objc.GetClass("NSDictionary")).Send(objc.RegisterName("dictionary")))
}

// nsDataCopy copia o conteúdo do NSData para a memória do Go. Não guarda o
// ponteiro do AppKit, que morre com o pool.
func nsDataCopy(data objc.ID) []byte {
	n := objc.Send[uint](data, objc.RegisterName("length"))
	if n == 0 {
		return nil
	}
	buf := make([]byte, n)
	data.Send(objc.RegisterName("getBytes:length:"), unsafe.Pointer(&buf[0]), n)
	return buf
}

// nsStringFrom cria um NSString a partir de texto Go (copiado pelo AppKit).
func nsStringFrom(s string) objc.ID {
	cstr := append([]byte(s), 0)
	return objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), unsafe.Pointer(&cstr[0]))
}

// nsStringGo lê um NSString como texto Go, pelo UTF8String, sem guardar o
// ponteiro do AppKit.
func nsStringGo(s objc.ID) string {
	if s == 0 {
		return ""
	}
	cstr := objc.Send[*byte](s, objc.RegisterName("UTF8String"))
	if cstr == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(cstr), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(cstr, n))
}
