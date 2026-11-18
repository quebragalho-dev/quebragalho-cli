package imageinput

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// ErrClipboardToolMissing informa, em português, que o host Linux não tem
// ferramenta de clipboard. É distinto de "clipboard vazio" (silencioso): aqui
// o usuário precisa instalar algo, então o silêncio seria a resposta errada.
//
// Nomeia os pacotes de cada servidor gráfico porque o qg não sabe em qual a
// sessão roda; instalar os dois cobre Wayland e X11.
var ErrClipboardToolMissing = errors.New(
	"não foi possível ler a área de transferência: instale wl-clipboard (Wayland) ou xclip (X11)")

// VideoExtensions é o ponto de extensão para anexos de vídeo: o engenheiro de
// vídeo implementa o pipeline; o paste do clipboard apenas classifica aqui para
// recusar com aviso em vez de cair no pipeline de imagem. As chaves incluem o
// ponto e a comparação é case-insensitive via IsVideoPath.
var VideoExtensions = map[string]bool{
	".mp4": true, ".mov": true, ".webm": true, ".mkv": true,
	".m4v": true, ".avi": true,
}

// IsVideoPath reports whether path names a video file (by extension only —
// classification for the paste refusal notice, not a content sniff).
func IsVideoPath(path string) bool {
	return VideoExtensions[strings.ToLower(filepath.Ext(strings.TrimSpace(path)))]
}

// ClipboardFileURLToPath converts a file:// URI from the clipboard
// (text/uri-list on Linux) to a local path, decoding %XX escapes. It reports
// false for anything that is not a file URI (http(s), plain text, empty), so
// callers only translate what the clipboard really offers as files.
func ClipboardFileURLToPath(uri string) (string, bool) {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return "", false
	}
	rest, ok := strings.CutPrefix(uri, "file://")
	if !ok {
		return "", false
	}
	rest = strings.TrimPrefix(rest, "localhost")
	decoded, err := url.PathUnescape(rest)
	if err != nil {
		return "", false
	}
	if decoded == "" || !filepath.IsAbs(decoded) {
		return "", false
	}
	return decoded, true
}

// ClipboardTempDir returns the private qg clipboard directory
// (<config>/qg/clipboard, mode 0700), creating it on first use. Clipboard
// screenshots can carry sensitive pixels, so they never land in the shared
// system temp dir. Callers resolve it through config.UserConfigDir so tests
// can redirect it with XDG_CONFIG_HOME/HOME instead of touching the real home.
func ClipboardTempDir() (string, error) {
	base, err := config.UserConfigDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "qg", "clipboard")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// WriteClipboardTemp saves clipboard image bytes to a 0600 file inside the
// private clipboard dir and returns its path. The caller tracks the path and
// removes the file when the attachment is discarded (Backspace, /image clear),
// consumed at submit, or the program quits.
func WriteClipboardTemp(data []byte, ext string) (string, error) {
	dir, err := ClipboardTempDir()
	if err != nil {
		return "", err
	}
	if ext == "" || ext[0] != '.' {
		ext = ".png"
	}
	f, err := os.CreateTemp(dir, "paste-*"+ext)
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// ReadClipboardFiles returns the local paths of files currently copied in the
// OS clipboard (a Finder/Explorer/file-manager copy), or nil when the
// clipboard holds no files. A missing Linux clipboard tool is actionable, so
// it surfaces ErrClipboardToolMissing instead of silence; every other lookup
// failure (no files, helper error) stays a silent nil so plain text pastes and
// stray Ctrl+V presses never nag.
func ReadClipboardFiles() ([]string, error) {
	switch runtime.GOOS {
	case "windows":
		return windowsClipboardFileList()
	case "darwin":
		return darwinClipboardFileURLs()
	case "linux":
		return linuxClipboardFileList()
	default:
		return nil, nil
	}
}

var (
	// Injetáveis para teste nos três SOs sem depender do clipboard real.
	darwinClipboardFileURLs  = readClipboardFilesDarwin
	windowsClipboardFileList = readClipboardFilesWindows
	linuxClipboardFileList   = readClipboardFilesLinux
)

// readClipboardFilesDarwin extracts copied-file paths via osascript: a Finder
// copy advertises «class furl» in `clipboard info`. osascript ships with macOS,
// so there is no missing-tool case here. Returns (nil, nil) when the clipboard
// holds no files.
func readClipboardFilesDarwin() ([]string, error) {
	info, err := darwinClipboardInfo()
	if err != nil {
		return nil, nil
	}
	if !strings.Contains(info, "furl") {
		return nil, nil
	}
	// Handles one file and many: `the clipboard` is a single alias for one
	// file, a list of aliases for several. `log` writes to stderr, so the
	// script returns the paths on stdout instead.
	script := `set c to the clipboard
if (class of c is list) then
  set out to ""
  repeat with f in c
    set out to out & (POSIX path of (f as alias)) & linefeed
  end repeat
else
  set out to (POSIX path of (c as alias)) & linefeed
end if
return out`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return nil, nil
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// readClipboardFilesWindows lists copied files via PowerShell: Get-Clipboard
// -Format FileDropList (PowerShell 5+, ships with Windows), falling back to
// the WinForms helper on hosts where the cmdlet is unavailable. Returns
// (nil, nil) when no files are copied.
func readClipboardFilesWindows() ([]string, error) {
	if out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-Clipboard -Format FileDropList) -join \"`n\"").Output(); err == nil {
		return splitClipboardLines(string(out)), nil
	}
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`Add-Type -AssemblyName System.Windows.Forms; $f = [System.Windows.Forms.Clipboard]::GetFileDropList(); if ($f -ne $null) { $f | ForEach-Object { $_.FullName } }`).Output()
	if err != nil {
		return nil, nil
	}
	return splitClipboardLines(string(out)), nil
}

// readClipboardFilesLinux lists copied files from text/uri-list (what file
// managers put on the clipboard): wl-paste on Wayland, xclip on X11. When
// neither helper exists the user must install one, so this returns
// ErrClipboardToolMissing instead of silence.
func readClipboardFilesLinux() ([]string, error) {
	_, wlErr := exec.LookPath("wl-paste")
	_, xclipErr := exec.LookPath("xclip")
	if wlErr != nil && xclipErr != nil {
		return nil, ErrClipboardToolMissing
	}
	if wlErr == nil {
		if types, err := runClipboardStdout("wl-paste", "--list-types"); err == nil {
			for _, line := range strings.Split(string(types), "\n") {
				if strings.TrimSpace(line) == "text/uri-list" {
					if data, err := runClipboardStdout("wl-paste", "--type", "text/uri-list"); err == nil {
						return parseClipboardURIList(data), nil
					}
				}
			}
		}
	}
	if xclipErr == nil {
		if types, err := runClipboardStdout("xclip", "-selection", "clipboard", "-t", "TARGETS", "-o"); err == nil {
			for _, line := range strings.Split(string(types), "\n") {
				if strings.TrimSpace(line) == "text/uri-list" {
					if data, err := runClipboardStdout("xclip", "-selection", "clipboard", "-t", "text/uri-list", "-o"); err == nil {
						return parseClipboardURIList(data), nil
					}
				}
			}
		}
	}
	return nil, nil
}

// parseClipboardURIList converts a text/uri-list payload to local paths,
// keeping only file:// entries that decode to absolute paths.
func parseClipboardURIList(data []byte) []string {
	var paths []string
	for _, line := range strings.Split(string(data), "\n") {
		if p, ok := ClipboardFileURLToPath(line); ok {
			paths = append(paths, p)
		}
	}
	return paths
}

func splitClipboardLines(out string) []string {
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if p := strings.TrimSpace(strings.TrimSuffix(line, "\r")); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}
