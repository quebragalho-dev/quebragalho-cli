package imageinput

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/qgruntime"
)

// defaultClipboardTimeout limita cada chamada a uma ferramenta de clipboard.
// Sem prazo, um wl-paste travado no GNOME/Wayland deixava o Ctrl+V mudo para
// sempre — e cada nova tentativa empilhava mais um processo pendurado. O piso é
// 5 s porque o PowerShell frio do Windows leva 2–3 s só para subir.
const defaultClipboardTimeout = 5 * time.Second

// clipboardWaitDelay é a folga depois do kill para o Wait parar de esperar os
// pipes herdados (o neto de um `sh -c osascript` pode segurar o stdout).
const clipboardWaitDelay = 500 * time.Millisecond

// clipboardTimeout é o prazo efetivo. Var para o teste exercitar o estouro sem
// esperar os 5 s reais.
var clipboardTimeout = defaultClipboardTimeout

// ClipboardTimeoutError reporta que a ferramenta de clipboard não respondeu
// dentro do prazo. É acionável ("tente de novo"), então não pode virar silêncio
// como "clipboard vazio".
type ClipboardTimeoutError struct {
	Tool string
}

func (e *ClipboardTimeoutError) Error() string {
	return "A área de transferência não respondeu (" + e.Tool + "); tente de novo."
}

// IsClipboardTimeout reports whether err came from the clipboard deadline.
func IsClipboardTimeout(err error) bool {
	var timeoutErr *ClipboardTimeoutError
	return errors.As(err, &timeoutErr)
}

// clipboardNotice é uma frase pronta em português para o usuário. Ter um tipo
// próprio deixa claro que a mensagem é exibida como está, sem prefixo.
type clipboardNotice string

func (n clipboardNotice) Error() string { return string(n) }

// ReadClipboardImage returns the raw image bytes and media type from the OS
// clipboard, or (nil, "", nil) when the clipboard has no image. Called when
// text clipboard is empty (the user pasted a screenshot). The media type is
// sniffed from the bytes, not trusted from the clipboard.
// ErrClipboardImageUnreadable reports that the clipboard holds an image the host
// could not extract. It is deliberately distinct from "no image on the
// clipboard", which is an ordinary no-op: this one must be shown, not
// swallowed. The sentence is already complete and in Portuguese, so callers
// display it as-is.
var ErrClipboardImageUnreadable error = clipboardNotice(
	"A área de transferência tem uma imagem, mas o qg não conseguiu lê-la.")

func ReadClipboardImage() ([]byte, string, error) {
	data, err := readClipboardImageBytes()
	if err != nil {
		return nil, "", err
	}
	if data == nil {
		return nil, "", nil
	}
	// Sniff the media type from the bytes — don't trust the clipboard's claim.
	sniffLen := len(data)
	if sniffLen > 512 {
		sniffLen = 512
	}
	mediaType := qgruntime.NormalizeImageMediaType(http.DetectContentType(data[:sniffLen]))
	if mediaType == "" {
		return nil, "", fmt.Errorf("clipboard image is not a supported type (allowed: png, jpeg, gif, webp)")
	}
	return data, mediaType, nil
}

// readClipboardImageBytes calls the platform-specific clipboard tool to extract
// image bytes. Returns (nil, nil) when no image is present.
func readClipboardImageBytes() ([]byte, error) {
	switch runtime.GOOS {
	case "windows":
		return readClipboardImageWindows()
	case "darwin":
		return readClipboardImageDarwin()
	case "linux":
		return readClipboardImageLinux()
	default:
		return nil, nil
	}
}

// readClipboardImageWindows uses PowerShell to check for and read a clipboard
// image. The image is saved as PNG to a temp file, read back, and the temp file
// deleted. Returns (nil, nil) when no image is on the clipboard.
func readClipboardImageWindows() ([]byte, error) {
	// Check if the clipboard contains an image.
	check := `Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; [System.Windows.Forms.Clipboard]::ContainsImage()`
	out, err := runClipboardStdout("powershell", "-NoProfile", "-Command", check)
	if err != nil {
		if IsClipboardTimeout(err) {
			return nil, err
		}
		return nil, nil // clipboard not available, treat as no image
	}
	if strings.TrimSpace(string(out)) != "True" {
		return nil, nil
	}
	// Save the clipboard image as PNG to a temp file, then read the bytes.
	// PowerShell stdout can't reliably emit raw binary — $ms.ToArray() prints
	// a .NET byte array as space-separated text, not raw bytes. A temp file
	// is the correct binary-safe path.
	tmpFile, err := os.CreateTemp("", "qg-clipboard-*.png")
	if err != nil {
		return nil, nil
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	// Stay on -Command: -File is subject to script execution policy (default
	// Restricted on Windows clients) and Windows PowerShell decodes BOM-less
	// UTF-8 .ps1 files as ANSI. Doubling single quotes is all the escaping the
	// single-quoted PowerShell literal needs.
	escapedTmpPath := strings.ReplaceAll(tmpPath, "'", "''")
	script := `Add-Type -AssemblyName System.Windows.Forms; Add-Type -AssemblyName System.Drawing; $img = [System.Windows.Forms.Clipboard]::GetImage(); if ($img -ne $null) { $img.Save('` + escapedTmpPath + `', [System.Drawing.Imaging.ImageFormat]::Png) }`
	if _, err := runClipboardStdout("powershell", "-NoProfile", "-Command", script); err != nil {
		if IsClipboardTimeout(err) {
			return nil, err
		}
		return nil, nil
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil || len(data) == 0 {
		return nil, nil
	}
	return data, nil
}

// readClipboardImageDarwin lê a imagem pela mesma execução única do paste. Aqui
// arquivos copiados não suprimem a imagem: quem chama pede imagem de propósito.
func readClipboardImageDarwin() ([]byte, error) {
	content, err := readClipboardContentDarwin(true)
	if err != nil {
		return nil, err
	}
	return content.Image, nil
}

// darwinClipboardContentScript faz a leitura inteira do paste numa única
// execução do osascript. argv[0] é o destino do PNG; argv[1] é "1" quando a
// imagem deve ser lida mesmo com arquivos copiados, "0" no paste normal.
//
// Lista os NSURL de arquivo copiados. Se há arquivo, a imagem não é lida: o
// Finder acrescenta ao pasteboard um TIFF derivado do ícone do arquivo, e o
// pasteboard o declara como imagem, então a lista de tipos sozinha não separa o
// print do ícone. O arquivo é a fonte. Sem arquivo, decide pelos tipos
// declarados (pb.types): PNG segue direto; TIFF, JPEG e GIF passam pelo
// NSBitmapImageRep. A resposta é JSON, então nomes com quebra de linha, aspas
// ou acento não se confundem com o resultado. Sem `clipboard info`, que
// converte o print em todos os formatos antes de responder.
const darwinClipboardContentScript = `ObjC.import('AppKit');
function nilObj(x) { return !x || x.isNil(); }
function run(argv) {
	var dest = argv[0];
	if (!dest) { throw new Error('destino ausente'); }
	var imageWithFiles = argv[1] === '1';
	var pb = $.NSPasteboard.generalPasteboard;
	var out = { files: [], image: false, written: false };
	var items = pb.readObjectsForClassesOptions($([$.NSURL]), $({}));
	if (items) { for (var i = 0; i < items.count; i++) { var u = items.objectAtIndex(i); if (u.isFileURL) out.files.push(u.path.js); } }
	if (out.files.length > 0 && !imageWithFiles) { return JSON.stringify(out); }
	var imageTypes = ['public.png', 'public.tiff', 'public.jpeg', 'com.compuserve.gif'];
	var offered = null;
	for (var k = 0; k < imageTypes.length && offered === null; k++) {
		if (pb.types.containsObject(imageTypes[k])) { offered = imageTypes[k]; }
	}
	if (offered !== null) {
		out.image = true;
		var data = pb.dataForType(offered);
		if (!nilObj(data)) {
			var png = data;
			if (offered !== 'public.png') {
				var rep = $.NSBitmapImageRep.imageRepWithData(data);
				png = nilObj(rep) ? null : rep.representationUsingTypeProperties($.NSBitmapImageFileTypePNG, $({}));
			}
			if (!nilObj(png)) { out.written = png.writeToFileAtomically(dest, true); }
		}
	}
	return JSON.stringify(out);
}`

// darwinClipboardContentRunner executa o script único com os argumentos em argv.
// Var para teste hermético: stubs devolvem a resposta sem subir o osascript.
var darwinClipboardContentRunner = func(script string, args ...string) ([]byte, error) {
	return runClipboardStdout("osascript", append([]string{"-l", "JavaScript", "-e", script}, args...)...)
}

// darwinClipboardReport é a resposta JSON de darwinClipboardContentScript.
type darwinClipboardReport struct {
	Files   []string `json:"files"`
	Image   bool     `json:"image"`
	Written bool     `json:"written"`
}

// errNativeClipboardUnavailable diz que a leitura nativa não pôde rodar (o
// AppKit não carregou ou a chamada caiu): quem chama usa o osascript de reserva.
var errNativeClipboardUnavailable = errors.New("leitura nativa da área de transferência indisponível")

// clipboardNativeReader é a leitura nativa do macOS (NSPasteboard no processo).
// Var para o teste trocar por um falso; fora do macOS é sempre indisponível.
var clipboardNativeReader = readClipboardContentNative

// readClipboardContentDarwin lê arquivos e imagem do paste. Primeiro tenta o
// NSPasteboard no próprio processo, que não sobe nenhum subprocesso; só se ele
// não está disponível cai no osascript de reserva.
func readClipboardContentDarwin(imageWithFiles bool) (ClipboardContent, error) {
	content, err := clipboardNativeReader(imageWithFiles)
	if errors.Is(err, errNativeClipboardUnavailable) {
		return readClipboardContentOsascript(imageWithFiles)
	}
	return content, err
}

// nativeReadInFlight é o único slot de leitura nativa em curso. Uma leitura
// presa (o app dono do clipboard não responde) segura o slot até devolver; as
// chamadas seguintes esperam o slot até o prazo e então falham, sem empilhar
// goroutines a cada Ctrl+V.
var nativeReadInFlight = make(chan struct{}, 1)

// nativeClipboardTool nomeia a leitura nativa na mensagem de prazo. É o nome
// que a pessoa reconhece, não o da API.
const nativeClipboardTool = "macOS"

// runClipboardRead roda uma leitura nativa sob o prazo das ferramentas externas,
// contado desde a chamada. Espera o slot dentro desse prazo, para que um Ctrl+V
// seguido de outro (com a primeira leitura ainda em curso) não se acuse de
// travado. Nunca inicia leitura depois do prazo. Uma leitura presa segura o
// slot até devolver, então nunca há duas em curso: quem espera o slot até o fim
// do prazo recebe *ClipboardTimeoutError. Um pânico vira
// errNativeClipboardUnavailable, para não derrubar a TUI.
func runClipboardRead(read func() (ClipboardContent, error)) (ClipboardContent, error) {
	timer := time.NewTimer(clipboardTimeout)
	defer timer.Stop()
	select {
	case nativeReadInFlight <- struct{}{}:
	case <-timer.C:
		return ClipboardContent{}, &ClipboardTimeoutError{Tool: nativeClipboardTool}
	}
	// O slot pode ter ficado livre no mesmo instante em que o prazo venceu; nesse
	// caso a escolha do select é aleatória, então a checagem barra a leitura.
	select {
	case <-timer.C:
		<-nativeReadInFlight
		return ClipboardContent{}, &ClipboardTimeoutError{Tool: nativeClipboardTool}
	default:
	}
	type result struct {
		content ClipboardContent
		err     error
	}
	done := make(chan result, 1)
	go func() {
		defer func() { <-nativeReadInFlight }()
		defer func() {
			if recover() != nil {
				done <- result{err: errNativeClipboardUnavailable}
			}
		}()
		content, err := read()
		done <- result{content: content, err: err}
	}()
	select {
	case r := <-done:
		return r.content, r.err
	case <-timer.C:
		return ClipboardContent{}, &ClipboardTimeoutError{Tool: nativeClipboardTool}
	}
}

// imageContentFromBytes valida os bytes de uma imagem do clipboard: vazio ou
// tipo que não é PNG/JPEG/GIF/WebP é ErrClipboardImageUnreadable. O tipo vem do
// próprio conteúdo, não da declaração do clipboard.
func imageContentFromBytes(data []byte) (ClipboardContent, error) {
	if len(data) == 0 {
		return ClipboardContent{}, ErrClipboardImageUnreadable
	}
	mediaType := qgruntime.NormalizeImageMediaType(http.DetectContentType(data))
	if mediaType == "" {
		return ClipboardContent{}, ErrClipboardImageUnreadable
	}
	return ClipboardContent{Image: data, MediaType: mediaType}, nil
}

// readClipboardContentOsascript é a leitura de reserva: um osascript por paste.
// Silêncio para texto, vazio e falha do osascript; timeout sobe; imagem que
// existe mas não foi gravada vira ErrClipboardImageUnreadable.
func readClipboardContentOsascript(imageWithFiles bool) (ClipboardContent, error) {
	dir, err := ClipboardTempDir()
	if err != nil {
		return ClipboardContent{}, err
	}
	tmp, err := os.CreateTemp(dir, "clipboard-*.png")
	if err != nil {
		return ClipboardContent{}, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	flag := "0"
	if imageWithFiles {
		flag = "1"
	}
	out, err := darwinClipboardContentRunner(darwinClipboardContentScript, tmpPath, flag)
	if err != nil {
		if IsClipboardTimeout(err) {
			return ClipboardContent{}, err
		}
		return ClipboardContent{}, nil
	}
	var report darwinClipboardReport
	if err := json.Unmarshal(out, &report); err != nil {
		return ClipboardContent{}, nil
	}
	content := ClipboardContent{Files: report.Files}
	if !report.Image {
		return content, nil
	}
	// O probe disse que há imagem: falhar daqui em diante é "não consegui ler",
	// nunca "clipboard vazio".
	if !report.Written {
		return ClipboardContent{}, ErrClipboardImageUnreadable
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return ClipboardContent{}, ErrClipboardImageUnreadable
	}
	img, err := imageContentFromBytes(data)
	if err != nil {
		return ClipboardContent{}, err
	}
	img.Files = content.Files
	return img, nil
}

// readClipboardImageLinux tries wl-paste (Wayland) then xclip (X11) to read
// clipboard image bytes. Returns (nil, nil) when no image or no tool available.
//
// The MIME type comes from the clipboard (wl-paste --list-types / xclip
// TARGETS), so it is NEVER interpolated into a shell — every command runs via
// exec.Command(prog, args...) with the type passed as a discrete argument.
// (A hostile clipboard offerer could otherwise register a target like
// "image/png; rm -rf ~" that passes the "image/" prefix check.)
func readClipboardImageLinux() ([]byte, error) {
	// Try Wayland first.
	if types, err := runClipboardStdout("wl-paste", "--list-types"); err == nil {
		for _, t := range imageMIMETypes(types) {
			data, err := runClipboardStdout("wl-paste", "--type", t)
			if err == nil && len(data) > 0 {
				return data, nil
			}
			if IsClipboardTimeout(err) {
				return nil, err
			}
		}
	} else if IsClipboardTimeout(err) {
		// Travou: o próximo helper só repetiria a espera.
		return nil, err
	}
	// Fall back to X11 xclip.
	if types, err := runClipboardStdout("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"); err == nil {
		for _, t := range imageMIMETypes(types) {
			data, err := runClipboardStdout("xclip", "-selection", "clipboard", "-t", t, "-o")
			if err == nil && len(data) > 0 {
				return data, nil
			}
			if IsClipboardTimeout(err) {
				return nil, err
			}
		}
	} else if IsClipboardTimeout(err) {
		return nil, err
	}
	return nil, nil
}

// runClipboardStdout runs a clipboard helper under the clipboard deadline and
// returns only its stdout. Stderr is discarded (the helpers are noisy when the
// clipboard is empty or the tool is missing); a missing tool surfaces as the
// command error, treated as "no image" by the callers.
//
// On expiry the process is killed (CommandContext) and a *ClipboardTimeoutError
// comes back, so callers can tell "stuck" from "absent" instead of going mute.
func runClipboardStdout(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// Sem WaitDelay, um neto que herdou o stdout segura o Wait mesmo depois do
	// kill, e a TUI voltaria a travar.
	cmd.WaitDelay = clipboardWaitDelay
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, &ClipboardTimeoutError{Tool: name}
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// imageMIMETypes extracts the "image/*" lines from a newline-separated type
// list, each safe to pass as a discrete argument (no shell).
func imageMIMETypes(list []byte) []string {
	var out []string
	for _, line := range strings.Split(string(list), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "image/") {
			out = append(out, line)
		}
	}
	return out
}
