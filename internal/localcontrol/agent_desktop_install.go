package localcontrol

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
	"github.com/quebragalho-dev/quebragalho-cli/internal/filelock"
)

const (
	// AgentDesktopVersion é a versão fixada do binário agent-desktop instalado pelo QG.
	AgentDesktopVersion = "v1.3.1"

	defaultAgentDesktopBaseURL           = "https://github.com/vercel-labs/agent-desktop/releases/download/" + AgentDesktopVersion
	defaultAgentDesktopMaxBytes          = 64 << 20  // 64 MiB (tar.gz comprimido)
	defaultAgentDesktopMaxExtractedBytes = 256 << 20 // 256 MiB (conteúdo extraído)
	defaultAgentDesktopTimeout           = 2 * time.Minute

	agentDesktopLockFileName     = ".agent-desktop.lock"
	agentDesktopLockPollInterval = 20 * time.Millisecond
	agentDesktopInstalledMarker  = ".qg-complete"
	agentDesktopMaxSymlinks      = 8
)

// AgentDesktopAsset descreve o pacote oficial do agent-desktop para um par GOOS/GOARCH.
type AgentDesktopAsset struct {
	GOOS            string
	GOARCH          string
	AssetName       string
	SHA256          string
	ExecutableName  string
	CompanionBinary string
}

type agentDesktopPlatform struct {
	goos   string
	goarch string
}

// agentDesktopAssets mapeia os 6 alvos suportados pelo QG para os pacotes e hashes
// SHA256 oficiais da release v1.3.1 do vercel-labs/agent-desktop.
var agentDesktopAssets = map[agentDesktopPlatform]AgentDesktopAsset{
	{goos: "darwin", goarch: "arm64"}: {
		GOOS:            "darwin",
		GOARCH:          "arm64",
		AssetName:       "agent-desktop-v1.3.1-aarch64-apple-darwin.tar.gz",
		SHA256:          "9b7b7cfc74892eab46c2c85d6cb7dc4863bf8dab0a222cfcfb8ae4604c69c86c",
		ExecutableName:  "agent-desktop",
		CompanionBinary: "agent-desktop-macos-helper",
	},
	{goos: "darwin", goarch: "amd64"}: {
		GOOS:            "darwin",
		GOARCH:          "amd64",
		AssetName:       "agent-desktop-v1.3.1-x86_64-apple-darwin.tar.gz",
		SHA256:          "469da10cfb68928c58caff88b92b30526e0ca895ac06bc86b7f533b1d9b309b3",
		ExecutableName:  "agent-desktop",
		CompanionBinary: "agent-desktop-macos-helper",
	},
	{goos: "linux", goarch: "arm64"}: {
		GOOS:            "linux",
		GOARCH:          "arm64",
		AssetName:       "agent-desktop-v1.3.1-aarch64-unknown-linux-gnu.tar.gz",
		SHA256:          "438990a703fbeb19775596c341c3701435e80adbfa1b2f64b82ff17567d5573b",
		ExecutableName:  "agent-desktop",
		CompanionBinary: "agent-desktop-linux-clipboard-owner",
	},
	{goos: "linux", goarch: "amd64"}: {
		GOOS:            "linux",
		GOARCH:          "amd64",
		AssetName:       "agent-desktop-v1.3.1-x86_64-unknown-linux-gnu.tar.gz",
		SHA256:          "ca77b6b75034eeb12e537dd8c0a4f2a3424119253f38b7e6bd79ba0d2fef1264",
		ExecutableName:  "agent-desktop",
		CompanionBinary: "agent-desktop-linux-clipboard-owner",
	},
	{goos: "windows", goarch: "arm64"}: {
		GOOS:            "windows",
		GOARCH:          "arm64",
		AssetName:       "agent-desktop-v1.3.1-aarch64-pc-windows-msvc.tar.gz",
		SHA256:          "805f1f61890f9983fceaf38cda94a37a049c379435d65607db5cedd22c8ef97b",
		ExecutableName:  "agent-desktop.exe",
		CompanionBinary: "",
	},
	{goos: "windows", goarch: "amd64"}: {
		GOOS:            "windows",
		GOARCH:          "amd64",
		AssetName:       "agent-desktop-v1.3.1-x86_64-pc-windows-msvc.tar.gz",
		SHA256:          "6ee4b1e078f4e78792e79f16e32e4a78b9919464aa3c55629b7e27ed53a08c13",
		ExecutableName:  "agent-desktop.exe",
		CompanionBinary: "",
	},
}

// AgentDesktopInstallOptions configura a instalação/reaproveitamento do agent-desktop.
type AgentDesktopInstallOptions struct {
	// Root é a pasta base onde o QG instala versões do agent-desktop.
	// Quando vazio, usa DefaultAgentDesktopRoot().
	Root string
	// HTTPClient permite injetar um cliente HTTP em testes (ex.: httptest TLS).
	HTTPClient *http.Client
	// BaseURL substitui a URL base do release em testes (https, ou http apenas em loopback).
	BaseURL string
	// GOOS substitui runtime.GOOS em testes.
	GOOS string
	// GOARCH substitui runtime.GOARCH em testes.
	GOARCH string

	// Campos internos para testes determinísticos sem enfraquecer a API pública.
	expectedSHA256 string
	maxBytes       int64
	timeout        time.Duration
}

// AgentDesktopAssetFor devolve os metadados do asset fixado para goos/goarch.
func AgentDesktopAssetFor(goos, goarch string) (AgentDesktopAsset, bool) {
	asset, ok := agentDesktopAssets[agentDesktopPlatform{
		goos:   strings.TrimSpace(goos),
		goarch: strings.TrimSpace(goarch),
	}]
	return asset, ok
}

// DefaultAgentDesktopRoot devolve a pasta padrão do QG para o agent-desktop baixado,
// na mesma raiz de configuração do usuário usada pelo motor de ditado (<config do qg>/tools/agent-desktop).
func DefaultAgentDesktopRoot() (string, error) {
	userConfigDir, err := config.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolver pasta de configuração do QG: %w", err)
	}
	return filepath.Join(userConfigDir, "qg", "tools", "agent-desktop"), nil
}

// EnsureAgentDesktop garante que o agent-desktop na versão fixada esteja instalado
// em opts.Root (ou DefaultAgentDesktopRoot) e devolve o caminho absoluto do executável.
func EnsureAgentDesktop(ctx context.Context, opts AgentDesktopInstallOptions) (string, error) {
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

	spec, ok := AgentDesktopAssetFor(goos, goarch)
	if !ok {
		return "", fmt.Errorf("o controle do computador não tem suporte para %s/%s", goos, goarch)
	}

	root := strings.TrimSpace(opts.Root)
	if root == "" {
		defaultRoot, err := DefaultAgentDesktopRoot()
		if err != nil {
			return "", err
		}
		root = defaultRoot
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolver pasta de instalação do agent-desktop: %w", err)
	}
	root = absRoot

	if err := ctx.Err(); err != nil {
		return "", err
	}

	installDir := filepath.Join(root, fmt.Sprintf("%s-%s-%s", AgentDesktopVersion, goos, goarch))
	execPath := filepath.Join(installDir, spec.ExecutableName)
	if isAgentDesktopInstalled(installDir, spec) {
		return execPath, nil
	}

	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		baseURL = defaultAgentDesktopBaseURL
	}
	downloadURL := strings.TrimRight(baseURL, "/") + "/" + spec.AssetName
	if err := requireSecureAgentDesktopURL(downloadURL); err != nil {
		return "", err
	}

	timeout := opts.timeout
	if timeout <= 0 {
		timeout = defaultAgentDesktopTimeout
	}
	maxBytes := opts.maxBytes
	if maxBytes <= 0 {
		maxBytes = defaultAgentDesktopMaxBytes
	}
	expectedSHA := spec.SHA256
	if override := strings.TrimSpace(opts.expectedSHA256); override != "" {
		expectedSHA = override
	}

	dlCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("criar pasta de instalação do agent-desktop: %w", err)
	}

	releaseLock, err := acquireAgentDesktopLock(dlCtx, filepath.Join(root, agentDesktopLockFileName))
	if err != nil {
		return "", err
	}
	unlocked := false
	defer func() {
		if !unlocked {
			_ = releaseLock()
		}
	}()

	// Revalida sob a trava exclusiva: outro processo/goroutine pode ter concluído
	// a instalação enquanto aguardávamos.
	if isAgentDesktopInstalled(installDir, spec) {
		unlocked = true
		if err := releaseLock(); err != nil {
			return "", fmt.Errorf("liberar trava de instalação do agent-desktop: %w", err)
		}
		return execPath, nil
	}

	archiveFile, err := os.CreateTemp(root, ".agent-desktop-dl-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("criar arquivo temporário de download: %w", err)
	}
	archivePath := archiveFile.Name()
	defer func() {
		_ = os.Remove(archivePath)
	}()

	if err := downloadAndVerifyAgentDesktop(dlCtx, opts.HTTPClient, downloadURL, spec.AssetName, expectedSHA, maxBytes, archiveFile); err != nil {
		return "", err
	}

	stageDir, err := os.MkdirTemp(root, ".agent-desktop-stage-*")
	if err != nil {
		return "", fmt.Errorf("criar pasta temporária de extração: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stageDir)
		}
	}()

	if err := extractAgentDesktopTarGz(archivePath, stageDir); err != nil {
		return "", err
	}
	if err := verifyAgentDesktopTree(stageDir, spec); err != nil {
		return "", err
	}
	if err := writeAgentDesktopMarker(stageDir); err != nil {
		return "", err
	}
	if err := publishAgentDesktopDir(stageDir, installDir); err != nil {
		return "", err
	}
	committed = true

	unlocked = true
	if err := releaseLock(); err != nil {
		return "", fmt.Errorf("liberar trava de instalação do agent-desktop: %w", err)
	}
	return execPath, nil
}

func isAgentDesktopInstalled(installDir string, spec AgentDesktopAsset) bool {
	markerInfo, err := os.Stat(filepath.Join(installDir, agentDesktopInstalledMarker))
	if err != nil || !markerInfo.Mode().IsRegular() {
		return false
	}
	return verifyAgentDesktopTree(installDir, spec) == nil
}

func verifyAgentDesktopTree(dir string, spec AgentDesktopAsset) error {
	needExecBit := runtime.GOOS != "windows" && spec.GOOS != "windows"
	if err := checkInstalledBinary(filepath.Join(dir, spec.ExecutableName), needExecBit); err != nil {
		return fmt.Errorf("executável %s ausente ou inválido após extração: %w", spec.ExecutableName, err)
	}
	if spec.CompanionBinary != "" {
		if err := checkInstalledBinary(filepath.Join(dir, spec.CompanionBinary), needExecBit); err != nil {
			return fmt.Errorf("auxiliar %s ausente ou inválido ao lado do executável: %w", spec.CompanionBinary, err)
		}
	}
	return nil
}

func checkInstalledBinary(path string, needExecBit bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s não é um arquivo regular", path)
	}
	if info.Size() <= 0 {
		return fmt.Errorf("%s está vazio", path)
	}
	if needExecBit && info.Mode()&0o111 == 0 {
		return fmt.Errorf("%s não possui permissão de execução", path)
	}
	return nil
}

func writeAgentDesktopMarker(stageDir string) error {
	markerPath := filepath.Join(stageDir, agentDesktopInstalledMarker)
	file, err := os.OpenFile(markerPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("gravar marcador de instalação: %w", err)
	}
	if _, err := file.WriteString(AgentDesktopVersion + "\n"); err != nil {
		_ = file.Close()
		return fmt.Errorf("gravar marcador de instalação: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sincronizar marcador de instalação: %w", err)
	}
	return file.Close()
}

func publishAgentDesktopDir(stageDir, installDir string) error {
	_, err := os.Lstat(installDir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(stageDir, installDir); err != nil {
			return fmt.Errorf("publicar instalação do agent-desktop: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecionar instalação anterior do agent-desktop: %w", err)
	}

	backupDir := stageDir + ".old"
	_ = os.RemoveAll(backupDir)
	if err := os.Rename(installDir, backupDir); err != nil {
		return fmt.Errorf("afastar instalação incompleta do agent-desktop: %w", err)
	}
	if err := os.Rename(stageDir, installDir); err != nil {
		restoreErr := os.Rename(backupDir, installDir)
		return errors.Join(fmt.Errorf("publicar instalação do agent-desktop: %w", err), restoreErr)
	}
	if err := os.RemoveAll(backupDir); err != nil {
		return fmt.Errorf("remover instalação substituída do agent-desktop: %w", err)
	}
	return nil
}

func acquireAgentDesktopLock(ctx context.Context, lockPath string) (func() error, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("abrir trava de instalação do agent-desktop: %w", err)
	}
	for {
		locked, err := filelock.TryLockExclusive(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("travar instalação do agent-desktop: %w", err)
		}
		if locked {
			return func() error {
				unlockErr := filelock.Unlock(file)
				closeErr := file.Close()
				return errors.Join(unlockErr, closeErr)
			}, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("aguardar trava de instalação do agent-desktop: %w", ctx.Err())
		case <-time.After(agentDesktopLockPollInterval):
		}
	}
}

func downloadAndVerifyAgentDesktop(
	ctx context.Context,
	baseClient *http.Client,
	downloadURL string,
	assetName string,
	expectedSHA256 string,
	maxBytes int64,
	destFile *os.File,
) error {
	closed := false
	defer func() {
		if !closed {
			_ = destFile.Close()
		}
	}()

	client := agentDesktopHTTPClient(baseClient)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("criar requisição de download do %s: %w", assetName, err)
	}
	req.Header.Set("User-Agent", "qg/agent-desktop-installer")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("baixar %s: %w", assetName, err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("baixar %s: HTTP %d", assetName, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return fmt.Errorf("baixar %s: tamanho anunciado (%d bytes) excede o limite de %d bytes", assetName, resp.ContentLength, maxBytes)
	}

	hasher := sha256.New()
	limited := io.LimitReader(resp.Body, maxBytes+1)
	written, err := io.Copy(io.MultiWriter(destFile, hasher), limited)
	if err != nil {
		return fmt.Errorf("ler download do %s: %w", assetName, err)
	}
	if written > maxBytes {
		return fmt.Errorf("baixar %s: arquivo excede o limite de %d bytes", assetName, maxBytes)
	}
	if err := destFile.Sync(); err != nil {
		return fmt.Errorf("sincronizar arquivo baixado %s: %w", assetName, err)
	}
	closed = true
	if err := destFile.Close(); err != nil {
		return fmt.Errorf("fechar arquivo baixado %s: %w", assetName, err)
	}

	gotSHA := hex.EncodeToString(hasher.Sum(nil))
	wantLower := strings.ToLower(strings.TrimSpace(expectedSHA256))
	if len(gotSHA) != len(wantLower) || subtle.ConstantTimeCompare([]byte(gotSHA), []byte(wantLower)) != 1 {
		return fmt.Errorf("verificação SHA256 falhou para %s: esperado %s, obtido %s", assetName, wantLower, gotSHA)
	}
	return nil
}

func agentDesktopHTTPClient(base *http.Client) *http.Client {
	var cloned http.Client
	var prevRedirect func(req *http.Request, via []*http.Request) error
	if base != nil {
		cloned = *base
		prevRedirect = base.CheckRedirect
	}
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("redirecionamentos demais ao baixar o agent-desktop")
		}
		if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && !strings.EqualFold(req.URL.Scheme, "https") {
			return fmt.Errorf("redirecionamento inseguro recusado ao baixar o agent-desktop: %s", req.URL.Redacted())
		}
		if err := requireSecureAgentDesktopURL(req.URL.String()); err != nil {
			return err
		}
		if prevRedirect != nil {
			return prevRedirect(req, via)
		}
		return nil
	}
	return &cloned
}

func requireSecureAgentDesktopURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("URL inválida para download do agent-desktop")
	}
	host := parsed.Hostname()
	if parsed.Host == "" || host == "" {
		return fmt.Errorf("URL sem host para download do agent-desktop: %s", parsed.Redacted())
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		if isAgentDesktopLoopbackHost(host) {
			return nil
		}
		return fmt.Errorf("URL insegura recusada para download do agent-desktop (%s): http só é aceito para loopback em testes", parsed.Redacted())
	default:
		return fmt.Errorf("esquema de URL não suportado para download do agent-desktop: %s", parsed.Redacted())
	}
}

func isAgentDesktopLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return addr.Unmap().IsLoopback()
	}
	return false
}

func extractAgentDesktopTarGz(archivePath, destDir string) error {
	destRoot, err := os.OpenRoot(destDir)
	if err != nil {
		return fmt.Errorf("abrir raiz de extração: %w", err)
	}
	defer func() {
		_ = destRoot.Close()
	}()

	file, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("abrir pacote tar.gz: %w", err)
	}
	defer func() {
		_ = file.Close()
	}()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("ler cabeçalho gzip: %w", err)
	}
	defer func() {
		_ = gz.Close()
	}()

	tr := tar.NewReader(gz)
	var totalExtracted int64

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("ler entrada do tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader {
			continue
		}

		cleanName, err := cleanAgentDesktopTarPath(hdr.Name)
		if err != nil {
			return err
		}
		if cleanName == "." {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := destRoot.MkdirAll(cleanName, 0o755); err != nil {
				return fmt.Errorf("criar diretório %q na extração: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			parent := filepath.Dir(cleanName)
			if parent != "." {
				if err := destRoot.MkdirAll(parent, 0o755); err != nil {
					return fmt.Errorf("criar diretório pai %q na extração: %w", parent, err)
				}
			}
			remaining := defaultAgentDesktopMaxExtractedBytes - totalExtracted
			if remaining < 0 || hdr.Size > remaining {
				return fmt.Errorf("conteúdo extraído do agent-desktop excede o limite de %d bytes", defaultAgentDesktopMaxExtractedBytes)
			}
			n, err := writeExtractedAgentDesktopFile(destRoot, cleanName, io.LimitReader(tr, remaining+1), remaining, fs.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			totalExtracted += n
		case tar.TypeSymlink:
			parent := filepath.Dir(cleanName)
			if parent != "." {
				if err := destRoot.MkdirAll(parent, 0o755); err != nil {
					return fmt.Errorf("criar diretório pai %q para link simbólico: %w", parent, err)
				}
			}
			if err := validateAgentDesktopSymlink(destRoot, parent, hdr.Linkname); err != nil {
				return fmt.Errorf("link simbólico inválido no tar (%s -> %s): %w", hdr.Name, hdr.Linkname, err)
			}
			_ = destRoot.Remove(cleanName)
			if err := destRoot.Symlink(hdr.Linkname, cleanName); err != nil {
				return fmt.Errorf("criar link simbólico %q: %w", hdr.Name, err)
			}
		case tar.TypeLink:
			targetClean, err := cleanAgentDesktopTarPath(hdr.Linkname)
			if err != nil || targetClean == "." {
				return fmt.Errorf("hardlink inválido no tar (%s -> %s)", hdr.Name, hdr.Linkname)
			}
			if _, err := followAgentDesktopRoot(destRoot, targetClean, 0); err != nil {
				return fmt.Errorf("hardlink escapa da pasta de destino (%s -> %s): %w", hdr.Name, hdr.Linkname, err)
			}
			parent := filepath.Dir(cleanName)
			if parent != "." {
				if err := destRoot.MkdirAll(parent, 0o755); err != nil {
					return fmt.Errorf("criar diretório pai %q para hardlink: %w", parent, err)
				}
			}
			_ = destRoot.Remove(cleanName)
			if err := destRoot.Link(targetClean, cleanName); err != nil {
				return fmt.Errorf("criar hardlink %q: %w", hdr.Name, err)
			}
		default:
			return fmt.Errorf("tipo de entrada não suportado no tar para %q (%d)", hdr.Name, hdr.Typeflag)
		}
	}
}

func writeExtractedAgentDesktopFile(destRoot *os.Root, name string, src io.Reader, maxBytes int64, mode fs.FileMode) (int64, error) {
	perm := mode.Perm()
	if perm == 0 {
		perm = 0o644
	}
	if perm&0o111 != 0 {
		perm |= 0o700
	} else {
		perm |= 0o600
	}
	out, err := destRoot.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return 0, fmt.Errorf("criar arquivo extraído %q: %w", name, err)
	}
	if runtime.GOOS != "windows" {
		if err := out.Chmod(perm); err != nil {
			_ = out.Close()
			return 0, fmt.Errorf("ajustar permissões de %q: %w", name, err)
		}
	}
	n, copyErr := io.Copy(out, src)
	if copyErr == nil && n > maxBytes {
		_ = out.Close()
		return n, fmt.Errorf("conteúdo extraído de %q excede o limite permitido", name)
	}
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return n, fmt.Errorf("gravar arquivo extraído %q: %w", name, copyErr)
	}
	if syncErr != nil {
		return n, fmt.Errorf("sincronizar arquivo extraído %q: %w", name, syncErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("fechar arquivo extraído %q: %w", name, closeErr)
	}
	return n, nil
}

func cleanAgentDesktopTarPath(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("entrada de tar inválida: %q", name)
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") || filepath.IsAbs(name) || strings.Contains(name, ":") {
		return "", fmt.Errorf("entrada do tar com caminho absoluto não é permitida: %q", name)
	}
	slashed := strings.ReplaceAll(name, "\\", "/")
	for _, part := range strings.Split(slashed, "/") {
		if part == ".." {
			return "", fmt.Errorf("entrada do tar com '..' não é permitida: %q", name)
		}
	}
	cleanName := filepath.Clean(filepath.FromSlash(slashed))
	if cleanName == "." {
		return ".", nil
	}
	if filepath.IsAbs(cleanName) || filepath.VolumeName(cleanName) != "" ||
		cleanName == ".." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("entrada do tar escapa da pasta de instalação: %q", name)
	}
	return cleanName, nil
}

func validateAgentDesktopSymlink(root *os.Root, parentRel, linkTarget string) error {
	if linkTarget == "" || strings.ContainsRune(linkTarget, 0) ||
		filepath.IsAbs(linkTarget) || strings.HasPrefix(linkTarget, "/") || strings.HasPrefix(linkTarget, "\\") ||
		filepath.VolumeName(linkTarget) != "" || strings.Contains(linkTarget, ":") {
		return fmt.Errorf("destino de link absoluto ou inválido: %q", linkTarget)
	}
	base := parentRel
	if base == "" {
		base = "."
	}
	resolvedBase, err := followAgentDesktopRoot(root, base, 0)
	if err != nil {
		return err
	}
	_, err = walkAgentDesktopRoot(root, resolvedBase, linkTarget, 0)
	return err
}

func followAgentDesktopRoot(root *os.Root, rel string, depth int) (string, error) {
	if rel == "" || rel == "." {
		return ".", nil
	}
	return walkAgentDesktopRoot(root, ".", rel, depth)
}

func walkAgentDesktopRoot(root *os.Root, base, target string, depth int) (string, error) {
	if depth > agentDesktopMaxSymlinks {
		return "", errors.New("cadeia de links simbólicos excede o limite")
	}
	current := base
	if current == "" {
		current = "."
	}
	for _, part := range strings.Split(strings.ReplaceAll(target, "\\", "/"), "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if current == "." {
				return "", errors.New("link escapa da pasta de instalação")
			}
			current = filepath.Dir(current)
			if current == "" {
				current = "."
			}
			continue
		}
		next := part
		if current != "." {
			next = filepath.Join(current, part)
		}
		info, err := root.Lstat(next)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				current = next
				continue
			}
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			current = next
			continue
		}
		linkDest, err := root.Readlink(next)
		if err != nil {
			return "", err
		}
		if linkDest == "" || strings.ContainsRune(linkDest, 0) ||
			filepath.IsAbs(linkDest) || strings.HasPrefix(linkDest, "/") || strings.HasPrefix(linkDest, "\\") ||
			filepath.VolumeName(linkDest) != "" || strings.Contains(linkDest, ":") {
			return "", fmt.Errorf("link simbólico %q aponta para destino absoluto: %q", next, linkDest)
		}
		followed, err := walkAgentDesktopRoot(root, current, linkDest, depth+1)
		if err != nil {
			return "", err
		}
		current = followed
	}
	return current, nil
}
