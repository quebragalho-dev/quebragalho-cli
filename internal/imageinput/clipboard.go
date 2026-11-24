package imageinput

import (
	"bytes"
	"context"
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

// ReadClipboardImage returns the raw image bytes and media type from the OS
// clipboard, or (nil, "", nil) when the clipboard has no image. Called when
// text clipboard is empty (the user pasted a screenshot). The media type is
// sniffed from the bytes, not trusted from the clipboard.
// ErrClipboardImageUnreadable reports that the clipboard holds an image the host
// could not extract. It is deliberately distinct from "no image on the
// clipboard", which is an ordinary no-op: this one must be shown, not
// swallowed. The sentence is already complete and in Portuguese, so callers
// display it as-is.
var ErrClipboardImageUnreadable = errors.New(
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

// readClipboardImageDarwin uses osascript to check for and read a clipboard
// image. Returns (nil, nil) when no image is present.
// darwinClipboardInfo reports what the pasteboard is currently offering, and
// darwinClipboardExtract pulls the PNG bytes out of it.
//
// Both are vars so the two-step logic below can be driven in a test. The step
// that matters, "the clipboard holds an image and nothing here can extract it",
// only happens when the first succeeds and the second fails, which cannot be
// arranged by stubbing the caller: a test that substitutes ReadClipboardImage
// asserts the plumbing around this function rather than the decision inside it,
// and stays green with that decision reverted.
var (
	darwinClipboardInfo = func() (string, error) {
		out, err := runClipboardStdout("sh", "-c", `osascript -e 'clipboard info'`)
		return string(out), err
	}
	darwinClipboardExtract = readClipboardImageDarwinJXA
)

// darwinClipboardImageScript extracts a screenshot from the pasteboard as PNG
// via JXA, the same bridge darwinClipboardFileScript uses for copied files.
// osascript ships with macOS, so this path works on a stock Mac — pngpaste is
// Homebrew-only and the python3 from Homebrew has no PyObjC, so both old
// helpers failed on every unmodified Mac.
//
// The PNG goes to a file instead of stdout because osascript output is not
// binary-safe. The destination arrives in argv, never interpolated into the
// script: a temp path can contain quotes and backslashes.
const darwinClipboardImageScript = `ObjC.import('AppKit');
function nilObj(x) { return !x || x.isNil(); }
function run(argv) {
	var path = argv[0];
	if (!path) { throw new Error('destino ausente'); }
	var pb = $.NSPasteboard.generalPasteboard;
	var data = pb.dataForType($.NSPasteboardTypePNG);
	if (nilObj(data)) {
		// O print do macOS chega como TIFF: converte para PNG antes de gravar.
		var tiff = pb.dataForType($.NSPasteboardTypeTIFF);
		if (nilObj(tiff)) { throw new Error('sem imagem no pasteboard'); }
		var rep = $.NSBitmapImageRep.imageRepWithData(tiff);
		if (nilObj(rep)) { throw new Error('TIFF ilegivel'); }
		data = rep.representationUsingTypeProperties($.NSBitmapImageFileTypePNG, $({}));
		if (nilObj(data)) { throw new Error('conversao para PNG falhou'); }
	}
	if (!data.writeToFileAtomically(path, true)) { throw new Error('falha ao gravar'); }
	return 0;
}`

// darwinClipboardImageRunner executa o JXA acima com o destino em argv. Var
// para teste hermético: no Linux um stub grava bytes falsos no caminho em vez
// de subir o osascript. O prazo é o mesmo do resto do clipboard.
var darwinClipboardImageRunner = func(script, destPath string) error {
	_, err := runClipboardStdout("osascript", "-l", "JavaScript", "-e", script, destPath)
	return err
}

// readClipboardImageDarwinJXA runs darwinClipboardImageScript against a fresh
// file in the private clipboard dir (screenshots can carry sensitive pixels,
// so not the shared temp dir) and reads the bytes back. The script replaces
// the file, so the bytes come from the read, not from the empty placeholder.
func readClipboardImageDarwinJXA() ([]byte, error) {
	dir, err := ClipboardTempDir()
	if err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(dir, "clipboard-*.png")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	if err := darwinClipboardImageRunner(darwinClipboardImageScript, tmpPath); err != nil {
		return nil, err
	}
	return os.ReadFile(tmpPath)
}

// darwinClipboardOffersImage reports whether the pasteboard info line names an
// image class.
func darwinClipboardOffersImage(info string) bool {
	for _, class := range []string{"PNG", "JPEG", "TIFF", "GIF"} {
		if strings.Contains(info, class) {
			return true
		}
	}
	return false
}

func readClipboardImageDarwin() ([]byte, error) {
	info, err := darwinClipboardInfo()
	if err != nil {
		if IsClipboardTimeout(err) {
			return nil, err
		}
		return nil, nil
	}
	if !darwinClipboardOffersImage(info) {
		return nil, nil
	}
	// Past this point the clipboard is KNOWN to hold an image, because the probe
	// above said so. A failure here is therefore not "no image", it is "there is
	// an image and nothing on this host can extract it". Reporting that as
	// absence is what left users staring at a paste that silently did nothing.
	data, err := darwinClipboardExtract()
	if err != nil {
		if IsClipboardTimeout(err) {
			return nil, err
		}
		return nil, ErrClipboardImageUnreadable
	}
	if len(data) == 0 {
		return nil, ErrClipboardImageUnreadable
	}
	return data, nil
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
