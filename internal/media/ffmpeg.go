package media

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

// FFmpegVersion é o build estático fixado para todas as plataformas com
// pacote próprio: ffmpeg 8.1.3 (build n8.1.3-27-g8dca1586f4 do BtbN,
// variante LGPL — sem componentes GPL).
const FFmpegVersion = "n8.1.3-27-g8dca1586f4"

// ffmpegReleaseTag é o tag do GitHub onde os assets fixados moram. Os builds
// do BtbN são rolantes no tag "latest"; os snapshots mensais (autobuild-*)
// guardam os assets versionados, então o pin é tag + nome do asset + SHA-256.
const ffmpegReleaseTag = "autobuild-2026-10-31-12-59"

const defaultFFmpegBaseURL = "https://github.com/BtbN/FFmpeg-Builds/releases/download/" + ffmpegReleaseTag

// defaultFFmpegMaxArchiveBytes limita o download do pacote (o maior asset
// fixado tem ~168 MB; a folga cobre metadados do container).
const defaultFFmpegMaxArchiveBytes = 256 << 20

const defaultFFmpegTimeout = 10 * time.Minute

const ffmpegInstalledMarker = ".qg-installed"

// FFmpegAsset descreve o pacote estático oficial do ffmpeg para um par
// GOOS/GOARCH. macOS não tem pacote: os builds do evermeet.cx não têm URL
// versionada nem hash publicado, então no darwin vale só o ffmpeg do sistema.
type FFmpegAsset struct {
	FileName string
	SHA256   string
	Size     int64
}

var ffmpegAssets = map[[2]string]FFmpegAsset{
	{"linux", "amd64"}: {
		FileName: "ffmpeg-" + FFmpegVersion + "-linux64-lgpl-8.1.tar.xz",
		SHA256:   "a64f3171e6ad1f7d358d28890a907ee478c331d1177ec696a93418f79ac3f1f3",
		Size:     135188284,
	},
	{"linux", "arm64"}: {
		FileName: "ffmpeg-" + FFmpegVersion + "-linuxarm64-lgpl-8.1.tar.xz",
		SHA256:   "2ea30086a637e3f0ad0f49a1b38db34121422b85e93cc82df2e01d9ee5200809",
		Size:     115803692,
	},
	{"windows", "amd64"}: {
		FileName: "ffmpeg-" + FFmpegVersion + "-win64-lgpl-8.1.zip",
		SHA256:   "d4c2a42673917b4a97104a3e9f082729a9730796b98664e37c0b7eba6c6c613d",
		Size:     167778744,
	},
	{"windows", "arm64"}: {
		FileName: "ffmpeg-" + FFmpegVersion + "-winarm64-lgpl-8.1.zip",
		SHA256:   "c918142d3920f6b15282739dd523885be0586cbd8684f400acea9d4a8c97002f",
		Size:     115921422,
	},
}

// FFmpegAssetFor devolve os metadados do pacote fixado para goos/goarch.
// macOS (e qualquer outro par sem pacote) devolve ok=false: lá vale só o
// ffmpeg do sistema (brew install ffmpeg).
func FFmpegAssetFor(goos, goarch string) (FFmpegAsset, bool) {
	asset, ok := ffmpegAssets[[2]string{strings.TrimSpace(goos), strings.TrimSpace(goarch)}]
	return asset, ok
}

// FFmpegMissingHint diz, em português, como instalar o ffmpeg na plataforma
// pedida. A orientação é só a daquele sistema — listar os três de uma vez
// confundia quem só precisa da sua — e o download gerenciado só aparece quando
// existe pacote fixado (FFmpegAssetFor), o que exclui o macOS. A frase já vem
// com o verbo da ação ("Instale com:", "Baixe com:") para quem a concatena só
// precisar do contexto e do ponto final.
func FFmpegMissingHint(goos, goarch string) string {
	_, downloadable := FFmpegAssetFor(goos, goarch)
	switch strings.TrimSpace(goos) {
	case "darwin":
		return "Instale com: brew install ffmpeg"
	case "windows":
		if downloadable {
			return "Baixe com: qg ffmpeg baixar (download único, ~115–170 MB) ou instale com: winget install Gyan.FFmpeg"
		}
		return "Instale com: winget install Gyan.FFmpeg"
	case "linux":
		if downloadable {
			return "Baixe com: qg ffmpeg baixar (download único, ~115–135 MB) ou instale com: sudo apt install ffmpeg"
		}
		return "Instale com: sudo apt install ffmpeg"
	default:
		if downloadable {
			return "Baixe com: qg ffmpeg baixar (download único)"
		}
		return "Instale o ffmpeg manualmente"
	}
}

// DefaultFFmpegRoot devolve a pasta padrão do QG para o ffmpeg baixado:
// <config do qg>/tools/ffmpeg, a mesma raiz dos outros componentes gerenciados.
func DefaultFFmpegRoot() (string, error) {
	userConfigDir, err := config.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolver pasta de configuração do QG: %w", err)
	}
	return filepath.Join(userConfigDir, "qg", "tools", "ffmpeg"), nil
}

// FFmpegInstallOptions configura a instalação/reaproveitamento do ffmpeg.
type FFmpegInstallOptions struct {
	Root       string
	HTTPClient *http.Client
	BaseURL    string
	GOOS       string
	GOARCH     string

	ExpectedSHA256 string
	MaxBytes       int64
	Timeout        time.Duration
}

// managedFFmpegDir é o layout instalado: root/<versão>-<goos>-<goarch>/ com
// ffmpeg(+ffprobe) e o marcador de instalação.
func managedFFmpegDir(root, goos, goarch string) string {
	return filepath.Join(root, fmt.Sprintf("%s-%s-%s", FFmpegVersion, goos, goarch))
}

// ffmpegExeName é o nome do binário dentro e fora do pacote.
func ffmpegExeName(goos string) string {
	if goos == "windows" {
		return "ffmpeg.exe"
	}
	return "ffmpeg"
}

// ffprobeExeName é o nome do binário de probe dentro e fora do pacote.
func ffprobeExeName(goos string) string {
	if goos == "windows" {
		return "ffprobe.exe"
	}
	return "ffprobe"
}

// FFmpegManagedPaths devolve os caminhos dos binários dentro de um root
// gerenciado, sem verificar existência.
func FFmpegManagedPaths(root, goos, goarch string) (ffmpegPath, ffprobePath string) {
	dir := managedFFmpegDir(root, goos, goarch)
	return filepath.Join(dir, ffmpegExeName(goos)), filepath.Join(dir, ffprobeExeName(goos))
}

// managedFFmpegInstalled diz se o root já tem o ffmpeg desta versão com os
// dois binários executáveis.
func managedFFmpegInstalled(root, goos, goarch string) bool {
	dir := managedFFmpegDir(root, goos, goarch)
	marker, err := os.ReadFile(filepath.Join(dir, ffmpegInstalledMarker))
	if err != nil || strings.TrimSpace(string(marker)) != FFmpegVersion {
		return false
	}
	ffmpegPath, ffprobePath := FFmpegManagedPaths(root, goos, goarch)
	return isExecutableFile(ffmpegPath) && isExecutableFile(ffprobePath)
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

// LookPathFFmpeg procura ffmpeg (e ffprobe, opcional) no PATH do sistema —
// argumentos diretos, sem shell, então caminhos com espaço ou acento passam
// intactos no Windows. Devolve ok=false quando não há ffmpeg no PATH.
// VideoExtensions são os contêineres de vídeo anexáveis (quadros via ffmpeg).
var VideoExtensions = map[string]bool{
	".mov": true, ".mp4": true, ".webm": true, ".mkv": true, ".m4v": true,
}

// IsVideoPath diz se o caminho é um vídeo anexável pela extensão.
func IsVideoPath(path string) bool {
	return VideoExtensions[strings.ToLower(filepath.Ext(strings.TrimSpace(path)))]
}

func LookPathFFmpeg() (ffmpegPath, ffprobePath string, ok bool) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		return "", "", false
	}
	ffprobePath, _ = exec.LookPath("ffprobe")
	return ffmpegPath, ffprobePath, true
}

// ResolveFFmpeg devolve o ffmpeg do sistema ou o gerenciado já instalado,
// sem nunca baixar nada. É o que o anexo de vídeo usa; baixar é decisão explícita do operador (qg ffmpeg baixar).
func ResolveFFmpeg() (ffmpegPath, ffprobePath string, ok bool) {
	if ffmpegPath, ffprobePath, ok := LookPathFFmpeg(); ok {
		return ffmpegPath, ffprobePath, true
	}
	root, err := DefaultFFmpegRoot()
	if err != nil {
		return "", "", false
	}
	goos, goarch := runtime.GOOS, runtime.GOARCH
	if !managedFFmpegInstalled(root, goos, goarch) {
		return "", "", false
	}
	ffmpegPath, ffprobePath = FFmpegManagedPaths(root, goos, goarch)
	return ffmpegPath, ffprobePath, true
}

// EnsureFFmpeg garante o ffmpeg da versão fixada: reaproveita a instalação
// gerenciada quando válida, senão baixa o pacote do upstream, confere o
// SHA-256 fixo e extrai ffmpeg+ffprobe. Devolve os dois caminhos absolutos.
func EnsureFFmpeg(ctx context.Context, opts FFmpegInstallOptions) (string, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	goos := strings.TrimSpace(opts.GOOS)
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := strings.TrimSpace(opts.GOARCH)
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	spec, ok := FFmpegAssetFor(goos, goarch)
	if !ok {
		return "", "", fmt.Errorf("ffmpeg automático não tem suporte para %s/%s; instale manualmente (no macOS: brew install ffmpeg)", goos, goarch)
	}

	root := strings.TrimSpace(opts.Root)
	if root == "" {
		var err error
		if root, err = DefaultFFmpegRoot(); err != nil {
			return "", "", err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolver pasta de instalação do ffmpeg: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}

	if managedFFmpegInstalled(root, goos, goarch) {
		ffmpegPath, ffprobePath := FFmpegManagedPaths(root, goos, goarch)
		return ffmpegPath, ffprobePath, nil
	}

	baseURL := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultFFmpegBaseURL
	}
	downloadURL := baseURL + "/" + spec.FileName
	if err := requireSecureFFmpegURL(downloadURL); err != nil {
		return "", "", err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultFFmpegTimeout
	}
	dlCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", "", fmt.Errorf("criar pasta de instalação do ffmpeg: %w", err)
	}

	expectedSHA := strings.TrimSpace(opts.ExpectedSHA256)
	if expectedSHA == "" {
		expectedSHA = spec.SHA256
	}
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultFFmpegMaxArchiveBytes
	}
	archive, err := fetchAndVerify(dlCtx, opts.HTTPClient, downloadURL, spec.FileName, expectedSHA, maxBytes)
	if err != nil {
		return "", "", err
	}

	stageDir, err := os.MkdirTemp(root, ".ffmpeg-stage-*")
	if err != nil {
		return "", "", fmt.Errorf("criar pasta temporária de instalação: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stageDir)
		}
	}()

	if err := extractFFmpegArchive(archive, spec.FileName, stageDir, goos); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(stageDir, ffmpegInstalledMarker), []byte(FFmpegVersion+"\n"), 0o644); err != nil {
		return "", "", fmt.Errorf("gravar marcador de instalação: %w", err)
	}
	installDir := managedFFmpegDir(root, goos, goarch)
	_ = os.RemoveAll(installDir)
	if err := os.Rename(stageDir, installDir); err != nil {
		return "", "", fmt.Errorf("publicar ffmpeg instalado: %w", err)
	}
	committed = true

	ffmpegPath, ffprobePath := FFmpegManagedPaths(root, goos, goarch)
	if !isExecutableFile(ffmpegPath) || !isExecutableFile(ffprobePath) {
		return "", "", fmt.Errorf("pacote %s não trouxe ffmpeg+ffprobe executáveis", spec.FileName)
	}
	return ffmpegPath, ffprobePath, nil
}

// requireSecureFFmpegURL exige https, liberando http só para loopback
// (testes herméticos com httptest).
func requireSecureFFmpegURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL inválida para download do ffmpeg")
	}
	host := parsed.Hostname()
	if parsed.Host == "" || host == "" {
		return fmt.Errorf("URL sem host para download do ffmpeg: %s", parsed.Redacted())
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		if addr, err := netip.ParseAddr(host); strings.EqualFold(host, "localhost") || (err == nil && addr.Unmap().IsLoopback()) {
			return nil
		}
		return fmt.Errorf("URL insegura recusada para download do ffmpeg (%s): http só é aceito para loopback em testes", parsed.Redacted())
	default:
		return fmt.Errorf("esquema de URL não suportado para download do ffmpeg: %s", parsed.Redacted())
	}
}

// fetchAndVerify baixa o asset com limite de tamanho e confere o SHA-256.
func fetchAndVerify(ctx context.Context, client *http.Client, downloadURL, assetName, expectedSHA256 string, maxBytes int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("criar requisição de download do %s: %w", assetName, err)
	}
	request.Header.Set("User-Agent", "qg/ffmpeg-installer")

	httpClient := client
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("baixar %s: %w", assetName, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("baixar %s: HTTP %d", assetName, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("baixar %s: tamanho (%d bytes) excede o limite de %d bytes", assetName, resp.ContentLength, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("ler download do %s: %w", assetName, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("baixar %s: arquivo excede o limite de %d bytes", assetName, maxBytes)
	}
	sum := sha256.Sum256(data)
	gotSHA := hex.EncodeToString(sum[:])
	wantLower := strings.ToLower(strings.TrimSpace(expectedSHA256))
	if len(gotSHA) != len(wantLower) || subtle.ConstantTimeCompare([]byte(gotSHA), []byte(wantLower)) != 1 {
		return nil, fmt.Errorf("verificação SHA-256 falhou para %s: esperado %s, obtido %s", assetName, wantLower, gotSHA)
	}
	return data, nil
}

// extractFFmpegArchive extrai ffmpeg+ffprobe do pacote (.zip ou .tar.xz).
func extractFFmpegArchive(archive []byte, fileName, destDir, goos string) error {
	lower := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return extractFFmpegZip(archive, destDir, goos)
	case strings.HasSuffix(lower, ".tar.xz"):
		return extractFFmpegTarXz(archive, destDir, goos)
	default:
		return fmt.Errorf("formato de pacote do ffmpeg não suportado: %s", fileName)
	}
}

// matchArchiveBinary diz se a entrada do pacote é um dos binários procurados,
// seja qual for o diretório de topo do pacote.
func matchArchiveBinary(entry, goos string) (string, bool) {
	normalized := strings.ToLower(filepath.ToSlash(entry))
	for _, want := range []string{"bin/" + ffmpegExeName(goos), "bin/" + ffprobeExeName(goos)} {
		if normalized == want || strings.HasSuffix(normalized, "/"+want) {
			return filepath.Base(want), true
		}
	}
	return "", false
}

func writeArchiveBinary(destDir, name string, data []byte) error {
	if int64(len(data)) > defaultFFmpegMaxArchiveBytes {
		return fmt.Errorf("binário %s no pacote excede o limite de %d bytes", name, defaultFFmpegMaxArchiveBytes)
	}
	destPath := filepath.Join(destDir, name)
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		return fmt.Errorf("criar executável %q: %w", name, err)
	}
	execErr := func() error {
		if runtime.GOOS != "windows" {
			if err := out.Chmod(0o755); err != nil {
				return fmt.Errorf("ajustar permissões de %q: %w", name, err)
			}
		}
		n, writeErr := out.Write(data)
		if writeErr == nil && n != len(data) {
			writeErr = fmt.Errorf("escrita incompleta (%d/%d bytes)", n, len(data))
		}
		if writeErr != nil {
			return fmt.Errorf("gravar executável %q: %w", name, writeErr)
		}
		return nil
	}()
	if syncErr := out.Sync(); syncErr != nil && execErr == nil {
		execErr = syncErr
	}
	if closeErr := out.Close(); closeErr != nil && execErr == nil {
		execErr = closeErr
	}
	if execErr != nil {
		return execErr
	}
	return nil
}

func extractFFmpegZip(archive []byte, destDir, goos string) error {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return fmt.Errorf("abrir pacote zip do ffmpeg: %w", err)
	}
	found := 0
	for _, entry := range reader.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		base, ok := matchArchiveBinary(entry.Name, goos)
		if !ok {
			continue
		}
		content, err := entry.Open()
		if err != nil {
			return fmt.Errorf("ler %s do pacote: %w", entry.Name, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(content, defaultFFmpegMaxArchiveBytes+1))
		_ = content.Close()
		if readErr != nil {
			return fmt.Errorf("ler %s do pacote: %w", entry.Name, readErr)
		}
		if writeErr := writeArchiveBinary(destDir, base, data); writeErr != nil {
			return writeErr
		}
		found++
	}
	if found != 2 {
		return fmt.Errorf("pacote zip não trouxe ffmpeg+ffprobe executáveis")
	}
	return nil
}

func extractFFmpegTarXz(archive []byte, destDir, goos string) error {
	xzReader, err := xz.NewReader(bytes.NewReader(archive))
	if err != nil {
		return fmt.Errorf("abrir pacote tar.xz do ffmpeg: %w", err)
	}
	tarReader := tar.NewReader(xzReader)
	found := 0
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("ler pacote tar.xz do ffmpeg: %w", err)
		}
		if header.FileInfo().IsDir() {
			continue
		}
		if header.Size > defaultFFmpegMaxArchiveBytes {
			return fmt.Errorf("entrada %s no pacote excede o limite", header.Name)
		}
		base, ok := matchArchiveBinary(header.Name, goos)
		if !ok {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tarReader, defaultFFmpegMaxArchiveBytes+1))
		if err != nil {
			return fmt.Errorf("ler %s do pacote: %w", header.Name, err)
		}
		if err := writeArchiveBinary(destDir, base, data); err != nil {
			return err
		}
		found++
	}
	if found != 2 {
		return fmt.Errorf("pacote tar.xz não trouxe ffmpeg+ffprobe executáveis")
	}
	return nil
}
