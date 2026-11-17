package localcontrol

import (
	"archive/tar"
	"bytes"
	"cmp"
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
	AgentDesktopVersion = "v1.3.1"

	defaultAgentDesktopBaseURL           = "https://github.com/vercel-labs/agent-desktop/releases/download/" + AgentDesktopVersion
	defaultAgentDesktopMaxBytes          = 64 << 20  // 64 MiB (tar.gz comprimido)
	defaultAgentDesktopMaxExtractedBytes = 256 << 20 // 256 MiB (conteúdo extraído)
	defaultAgentDesktopTimeout           = 2 * time.Minute

	agentDesktopLockFileName     = ".agent-desktop.lock"
	agentDesktopLockPollInterval = 20 * time.Millisecond
	agentDesktopInstalledMarker  = ".qg-complete"
)

// AgentDesktopAsset descreve o pacote oficial do agent-desktop para um par GOOS/GOARCH.
type AgentDesktopAsset struct {
	AssetName       string
	SHA256          string
	ExecutableName  string
	CompanionBinary string
}

var agentDesktopAssets = map[[2]string]AgentDesktopAsset{
	{"darwin", "arm64"}:  {"agent-desktop-v1.3.1-aarch64-apple-darwin.tar.gz", "9b7b7cfc74892eab46c2c85d6cb7dc4863bf8dab0a222cfcfb8ae4604c69c86c", "agent-desktop", "agent-desktop-macos-helper"},
	{"darwin", "amd64"}:  {"agent-desktop-v1.3.1-x86_64-apple-darwin.tar.gz", "469da10cfb68928c58caff88b92b30526e0ca895ac06bc86b7f533b1d9b309b3", "agent-desktop", "agent-desktop-macos-helper"},
	{"linux", "arm64"}:   {"agent-desktop-v1.3.1-aarch64-unknown-linux-gnu.tar.gz", "438990a703fbeb19775596c341c3701435e80adbfa1b2f64b82ff17567d5573b", "agent-desktop", "agent-desktop-linux-clipboard-owner"},
	{"linux", "amd64"}:   {"agent-desktop-v1.3.1-x86_64-unknown-linux-gnu.tar.gz", "ca77b6b75034eeb12e537dd8c0a4f2a3424119253f38b7e6bd79ba0d2fef1264", "agent-desktop", "agent-desktop-linux-clipboard-owner"},
	{"windows", "arm64"}: {"agent-desktop-v1.3.1-aarch64-pc-windows-msvc.tar.gz", "805f1f61890f9983fceaf38cda94a37a049c379435d65607db5cedd22c8ef97b", "agent-desktop.exe", ""},
	{"windows", "amd64"}: {"agent-desktop-v1.3.1-x86_64-pc-windows-msvc.tar.gz", "6ee4b1e078f4e78792e79f16e32e4a78b9919464aa3c55629b7e27ed53a08c13", "agent-desktop.exe", ""},
}

// AgentDesktopInstallOptions configura a instalação/reaproveitamento do agent-desktop.
type AgentDesktopInstallOptions struct {
	Root       string
	HTTPClient *http.Client
	BaseURL    string
	GOOS       string
	GOARCH     string

	expectedSHA256 string
	maxBytes       int64
	timeout        time.Duration
}

// AgentDesktopAssetFor devolve os metadados do asset fixado para goos/goarch.
func AgentDesktopAssetFor(goos, goarch string) (AgentDesktopAsset, bool) {
	asset, ok := agentDesktopAssets[[2]string{strings.TrimSpace(goos), strings.TrimSpace(goarch)}]
	return asset, ok
}

// DefaultAgentDesktopRoot devolve a pasta padrão do QG para o agent-desktop baixado,
// na mesma raiz de configuração usada pelo motor de ditado (<config do qg>/tools/agent-desktop).
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
	goos := cmp.Or(strings.TrimSpace(opts.GOOS), runtime.GOOS)
	goarch := cmp.Or(strings.TrimSpace(opts.GOARCH), runtime.GOARCH)
	spec, ok := AgentDesktopAssetFor(goos, goarch)
	if !ok {
		return "", fmt.Errorf("o controle do computador não tem suporte para %s/%s", goos, goarch)
	}

	root := strings.TrimSpace(opts.Root)
	if root == "" {
		var err error
		if root, err = DefaultAgentDesktopRoot(); err != nil {
			return "", err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolver pasta de instalação do agent-desktop: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	installDir := filepath.Join(root, fmt.Sprintf("%s-%s-%s", AgentDesktopVersion, goos, goarch))
	execPath := filepath.Join(installDir, spec.ExecutableName)
	if isAgentDesktopInstalled(installDir, goos, spec) {
		return execPath, nil
	}

	downloadURL := strings.TrimRight(cmp.Or(strings.TrimSpace(opts.BaseURL), defaultAgentDesktopBaseURL), "/") + "/" + spec.AssetName
	if err := requireSecureAgentDesktopURL(downloadURL); err != nil {
		return "", err
	}

	timeout := opts.timeout
	if timeout <= 0 {
		timeout = defaultAgentDesktopTimeout
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

	err = installAgentDesktopLocked(dlCtx, opts, root, installDir, goos, downloadURL, spec)
	if lockErr := releaseLock(); lockErr != nil && err == nil {
		err = fmt.Errorf("liberar trava de instalação do agent-desktop: %w", lockErr)
	}
	if err != nil {
		return "", err
	}
	return execPath, nil
}

func installAgentDesktopLocked(ctx context.Context, opts AgentDesktopInstallOptions, root, installDir, goos, downloadURL string, spec AgentDesktopAsset) error {
	if isAgentDesktopInstalled(installDir, goos, spec) {
		return nil
	}
	maxBytes := opts.maxBytes
	if maxBytes <= 0 {
		maxBytes = defaultAgentDesktopMaxBytes
	}
	expectedSHA := cmp.Or(strings.TrimSpace(opts.expectedSHA256), spec.SHA256)
	archiveBytes, err := fetchAndVerifyAgentDesktop(ctx, opts.HTTPClient, downloadURL, spec.AssetName, expectedSHA, maxBytes)
	if err != nil {
		return err
	}

	stageDir, err := os.MkdirTemp(root, ".agent-desktop-stage-*")
	if err != nil {
		return fmt.Errorf("criar pasta temporária de extração: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stageDir)
		}
	}()

	if err := extractAgentDesktopTarGz(bytes.NewReader(archiveBytes), stageDir, spec); err != nil {
		return err
	}
	if err := verifyAgentDesktopTree(stageDir, goos, spec); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stageDir, agentDesktopInstalledMarker), []byte(AgentDesktopVersion+"\n"), 0o644); err != nil {
		return fmt.Errorf("gravar marcador de instalação: %w", err)
	}
	if err := publishAgentDesktopDir(stageDir, installDir); err != nil {
		return err
	}
	committed = true
	return nil
}

func isAgentDesktopInstalled(installDir, goos string, spec AgentDesktopAsset) bool {
	info, err := os.Stat(filepath.Join(installDir, agentDesktopInstalledMarker))
	return err == nil && info.Mode().IsRegular() && verifyAgentDesktopTree(installDir, goos, spec) == nil
}

func verifyAgentDesktopTree(dir, goos string, spec AgentDesktopAsset) error {
	needExecBit := runtime.GOOS != "windows" && goos != "windows"
	for _, name := range []string{spec.ExecutableName, spec.CompanionBinary} {
		if name == "" {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("binário %s ausente após extração: %w", name, err)
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 {
			return fmt.Errorf("binário %s inválido", name)
		}
		if needExecBit && info.Mode()&0o111 == 0 {
			return fmt.Errorf("binário %s sem permissão de execução", name)
		}
	}
	return nil
}

func publishAgentDesktopDir(stageDir, installDir string) error {
	if _, err := os.Lstat(installDir); errors.Is(err, os.ErrNotExist) {
		return os.Rename(stageDir, installDir)
	} else if err != nil {
		return err
	}
	backupDir := stageDir + ".old"
	_ = os.RemoveAll(backupDir)
	if err := os.Rename(installDir, backupDir); err != nil {
		return err
	}
	if err := os.Rename(stageDir, installDir); err != nil {
		return errors.Join(err, os.Rename(backupDir, installDir))
	}
	return os.RemoveAll(backupDir)
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
			return func() error { return errors.Join(filelock.Unlock(file), file.Close()) }, nil
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, fmt.Errorf("aguardar trava de instalação do agent-desktop: %w", ctx.Err())
		case <-time.After(agentDesktopLockPollInterval):
		}
	}
}

func fetchAndVerifyAgentDesktop(ctx context.Context, baseClient *http.Client, downloadURL, assetName, expectedSHA256 string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("criar requisição de download do %s: %w", assetName, err)
	}
	req.Header.Set("User-Agent", "qg/agent-desktop-installer")

	resp, err := agentDesktopHTTPClient(baseClient).Do(req)
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
		return nil, fmt.Errorf("verificação SHA256 falhou para %s: esperado %s, obtido %s", assetName, wantLower, gotSHA)
	}
	return data, nil
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
		if addr, err := netip.ParseAddr(host); strings.EqualFold(host, "localhost") || (err == nil && addr.Unmap().IsLoopback()) {
			return nil
		}
		return fmt.Errorf("URL insegura recusada para download do agent-desktop (%s): http só é aceito para loopback em testes", parsed.Redacted())
	default:
		return fmt.Errorf("esquema de URL não suportado para download do agent-desktop: %s", parsed.Redacted())
	}
}

func extractAgentDesktopTarGz(archive io.Reader, destDir string, spec AgentDesktopAsset) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("ler cabeçalho gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	seen := map[string]bool{}
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
		baseName, err := parseAgentDesktopTarName(hdr.Name)
		if err != nil {
			return err
		}
		if baseName == "." || hdr.Typeflag == tar.TypeDir {
			continue
		}
		if hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("tipo de entrada não suportado no tar para %q (%d)", hdr.Name, hdr.Typeflag)
		}
		if baseName != spec.ExecutableName && (spec.CompanionBinary == "" || baseName != spec.CompanionBinary) {
			return fmt.Errorf("arquivo inesperado no tar do agent-desktop: %q", hdr.Name)
		}
		if seen[baseName] {
			return fmt.Errorf("arquivo duplicado no tar do agent-desktop: %q", baseName)
		}
		seen[baseName] = true

		remaining := defaultAgentDesktopMaxExtractedBytes - totalExtracted
		if remaining < 0 || hdr.Size > remaining {
			return fmt.Errorf("conteúdo extraído do agent-desktop excede o limite de %d bytes", defaultAgentDesktopMaxExtractedBytes)
		}
		perm := cmp.Or(fs.FileMode(hdr.Mode).Perm(), fs.FileMode(0o644))
		if perm&0o111 != 0 {
			perm |= 0o700
		} else {
			perm |= 0o600
		}
		out, err := os.OpenFile(filepath.Join(destDir, baseName), os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
		if err != nil {
			return fmt.Errorf("criar arquivo extraído %q: %w", baseName, err)
		}
		if runtime.GOOS != "windows" {
			if err := out.Chmod(perm); err != nil {
				_ = out.Close()
				return fmt.Errorf("ajustar permissões de %q: %w", baseName, err)
			}
		}
		n, copyErr := io.Copy(out, io.LimitReader(tr, remaining+1))
		if copyErr == nil && n > remaining {
			_ = out.Close()
			return fmt.Errorf("conteúdo extraído de %q excede o limite permitido", baseName)
		}
		if err := errors.Join(copyErr, out.Sync(), out.Close()); err != nil {
			return fmt.Errorf("gravar arquivo extraído %q: %w", baseName, err)
		}
		totalExtracted += n
	}
}

func parseAgentDesktopTarName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) ||
		strings.HasPrefix(name, "/") || strings.HasPrefix(name, "\\") || strings.Contains(name, ":") {
		return "", fmt.Errorf("entrada de tar inválida ou com caminho absoluto: %q", name)
	}
	var parts []string
	for _, part := range strings.Split(strings.ReplaceAll(name, "\\", "/"), "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", fmt.Errorf("entrada do tar com '..' não é permitida: %q", name)
		default:
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return ".", nil
	}
	if len(parts) > 2 {
		return "", fmt.Errorf("caminho inesperado no tar do agent-desktop: %q", name)
	}
	return parts[len(parts)-1], nil
}
