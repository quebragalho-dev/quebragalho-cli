package cli

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/media"
)

// runFFmpegCommand backs `qg ffmpeg`: status do binário usado para extrair
// quadros de vídeo (sistema ou gerenciado) e download do build estático
// fixado. A TUI nunca baixa sozinha: ela aponta para cá.
func runFFmpegCommand(args []string, stdout io.Writer, stderr io.Writer) int {
	subcommand := "status"
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		subcommand = rest[0]
		rest = rest[1:]
	}
	var root, baseURL string
	var help bool
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "-h", "--help", "help":
			help = true
		case "--root":
			if i+1 >= len(rest) {
				return writeUsageError(stderr, "--root precisa de um diretório")
			}
			root = rest[i+1]
			i++
		case "--base-url":
			if i+1 >= len(rest) {
				return writeUsageError(stderr, "--base-url precisa de uma URL")
			}
			baseURL = rest[i+1]
			i++
		default:
			if strings.HasPrefix(rest[i], "-") {
				return writeUsageError(stderr, "opção desconhecida para qg ffmpeg: "+rest[i])
			}
			return writeUsageError(stderr, "uso: qg ffmpeg [status|baixar]")
		}
	}
	if help {
		if err := writeFFmpegHelp(stdout); err != nil {
			return exitCrash
		}
		return exitSuccess
	}
	switch subcommand {
	case "status":
		return runFFmpegStatus(stdout, stderr)
	case "baixar":
		return runFFmpegDownload(root, baseURL, stdout, stderr)
	default:
		return writeUsageError(stderr, "uso: qg ffmpeg [status|baixar]")
	}
}

func runFFmpegStatus(stdout io.Writer, stderr io.Writer) int {
	ffmpegPath, ffprobePath, ok := media.ResolveFFmpeg()
	if !ok {
		_, _ = fmt.Fprintln(stderr, "ffmpeg não encontrado. "+ffmpegMissingHint()+".")
		return exitCrash
	}
	version := ffmpegVersionLine(ffmpegPath)
	if version == "" {
		version = "(versão desconhecida)"
	}
	_, _ = fmt.Fprintf(stdout, "ffmpeg: %s\n%s\n", ffmpegPath, version)
	if ffprobePath != "" {
		_, _ = fmt.Fprintf(stdout, "ffprobe: %s\n", ffprobePath)
	}
	return exitSuccess
}

func ffmpegVersionLine(ffmpegPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ffmpegPath, "-version").Output()
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return first
}

// ffmpegStatusPlatformSeam fixa o GOOS/GOARCH do aviso de `qg ffmpeg status`
// nos testes (vazio = plataforma do host, o único caminho da produção). Sem
// ele, o teste da orientação dependeria do host que roda a suíte.
var ffmpegStatusPlatformSeam struct {
	goos   string
	goarch string
}

// ffmpegMissingHint devolve a orientação de instalação do sistema atual.
func ffmpegMissingHint() string {
	goos, goarch := ffmpegStatusPlatformSeam.goos, ffmpegStatusPlatformSeam.goarch
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return media.FFmpegMissingHint(goos, goarch)
}

// ffmpegPlatformSeam fixa o GOOS/GOARCH do `qg ffmpeg baixar` nos testes
// (vazio = plataforma do host, o único caminho da produção). Sem ele, o
// teste do bundle adulterado dependeria do host: no darwin (sem pacote
// fixado) ele falharia com "sem suporte" em vez de exercer a SHA-256.
var ffmpegPlatformSeam struct {
	goos   string
	goarch string
}

func runFFmpegDownload(root, baseURL string, stdout io.Writer, stderr io.Writer) int {
	opts := media.FFmpegInstallOptions{Root: root, BaseURL: baseURL, GOOS: ffmpegPlatformSeam.goos, GOARCH: ffmpegPlatformSeam.goarch}
	if strings.TrimSpace(opts.Root) == "" {
		defaultRoot, err := media.DefaultFFmpegRoot()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "ffmpeg: "+err.Error())
			return exitCrash
		}
		opts.Root = defaultRoot
	}
	_, _ = fmt.Fprintf(stdout, "Baixando ffmpeg %s (download único, ~115–170 MB conforme a plataforma)...\n", media.FFmpegVersion)
	ctx := context.Background()
	ffmpegPath, ffprobePath, err := media.EnsureFFmpeg(ctx, opts)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "ffmpeg: "+err.Error())
		return exitCrash
	}
	_, _ = fmt.Fprintf(stdout, "ffmpeg pronto em %s\nffprobe em %s\n", ffmpegPath, ffprobePath)
	return exitSuccess
}

func writeFFmpegHelp(w io.Writer) error {
	_, err := fmt.Fprintf(w, `Uso:
  qg ffmpeg [status|baixar] [--root DIR] [--base-url URL]

Comandos:
  status   Mostra o ffmpeg usado para extrair quadros de vídeo
           (do sistema ou o build gerenciado) e sua versão
  baixar   Baixa o build estático fixado (~115–170 MB, uma vez só),
           com SHA-256 conferido: %s do BtbN (variante LGPL)

Opções:
      --root DIR     Pasta de instalação gerenciada (padrão: <config do qg>/tools/ffmpeg)
      --base-url URL Espelho alternativo do release do upstream (padrão: github.com/BtbN)
`, media.FFmpegVersion)
	return err
}
