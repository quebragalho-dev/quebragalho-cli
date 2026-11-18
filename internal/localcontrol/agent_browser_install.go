package localcontrol

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	AgentBrowserVersion = "v0.43.0"

	defaultAgentBrowserBaseURL  = "https://github.com/vercel-labs/agent-browser/releases/download/" + AgentBrowserVersion
	defaultAgentBrowserMaxBytes = 64 << 20 // 64 MiB
	defaultAgentBrowserTimeout  = 2 * time.Minute

	agentBrowserLockFileName = ".agent-browser.lock"
)

// AgentBrowserAsset descreve o binário oficial do agent-browser para um par GOOS/GOARCH.
type AgentBrowserAsset struct {
	AssetName      string
	SHA256         string
	ExecutableName string
}

var agentBrowserAssets = map[[2]string]AgentBrowserAsset{
	{"darwin", "arm64"}:  {"agent-browser-darwin-arm64", "05338a8eeddf0b3f9ebf0c87a6c21aedc10685608c68618e1a8483c43eae9c3f", "agent-browser"},
	{"darwin", "amd64"}:  {"agent-browser-darwin-x64", "49a5ab27297213c1bc0005701a6a4542399abfc9d2cb7520ab519ebb94b8b13d", "agent-browser"},
	{"linux", "arm64"}:   {"agent-browser-linux-arm64", "34be55c2b92d8d199fb9d1d62a905ee5b6ac5b1f3e8c24e5b20597f370d905e5", "agent-browser"},
	{"linux", "amd64"}:   {"agent-browser-linux-x64", "8e69c9fb662514c9cf36e4911e94e913742ba066e55102486350e049dd847612", "agent-browser"},
	{"windows", "amd64"}: {"agent-browser-win32-x64.exe", "ebcef5ebea93c7ccf0fe3f8c18eee908ae9174dfef8e52a3dda6f24a59f630dc", "agent-browser.exe"},
	{"windows", "arm64"}: {"agent-browser-win32-x64.exe", "ebcef5ebea93c7ccf0fe3f8c18eee908ae9174dfef8e52a3dda6f24a59f630dc", "agent-browser.exe"},
}

// AgentBrowserInstallOptions configura a instalação/reaproveitamento do agent-browser.
type AgentBrowserInstallOptions struct {
	Root       string
	HTTPClient *http.Client
	BaseURL    string
	GOOS       string
	GOARCH     string

	expectedSHA256 string
	maxBytes       int64
	timeout        time.Duration
}

// AgentBrowserAssetFor devolve os metadados do asset fixado para goos/goarch.
func AgentBrowserAssetFor(goos, goarch string) (AgentBrowserAsset, bool) {
	asset, ok := agentBrowserAssets[[2]string{strings.TrimSpace(goos), strings.TrimSpace(goarch)}]
	return asset, ok
}

// DefaultAgentBrowserRoot devolve a pasta padrão do QG para o agent-browser baixado,
// na mesma raiz de configuração usada pelo agent-desktop (<config do qg>/tools/agent-browser).
func DefaultAgentBrowserRoot() (string, error) {
	return defaultQGToolRoot("agent-browser")
}

// EnsureAgentBrowser garante que o agent-browser na versão fixada esteja instalado
// em opts.Root (ou DefaultAgentBrowserRoot) e devolve o caminho absoluto do executável.
func EnsureAgentBrowser(ctx context.Context, opts AgentBrowserInstallOptions) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	goos := cmp.Or(strings.TrimSpace(opts.GOOS), runtime.GOOS)
	goarch := cmp.Or(strings.TrimSpace(opts.GOARCH), runtime.GOARCH)
	spec, ok := AgentBrowserAssetFor(goos, goarch)
	if !ok {
		return "", fmt.Errorf("o controle do navegador não tem suporte para %s/%s", goos, goarch)
	}

	root := strings.TrimSpace(opts.Root)
	if root == "" {
		var err error
		if root, err = DefaultAgentBrowserRoot(); err != nil {
			return "", err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolver pasta de instalação do agent-browser: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	installDir := filepath.Join(root, fmt.Sprintf("%s-%s-%s", AgentBrowserVersion, goos, goarch))
	execPath := filepath.Join(installDir, spec.ExecutableName)
	if isToolInstalled(installDir, goos, spec.ExecutableName) {
		return execPath, nil
	}

	downloadURL := strings.TrimRight(cmp.Or(strings.TrimSpace(opts.BaseURL), defaultAgentBrowserBaseURL), "/") + "/" + spec.AssetName
	if err := requireSecureReleaseURL(downloadURL, "agent-browser"); err != nil {
		return "", err
	}

	timeout := opts.timeout
	if timeout <= 0 {
		timeout = defaultAgentBrowserTimeout
	}
	dlCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("criar pasta de instalação do agent-browser: %w", err)
	}
	releaseLock, err := acquireToolInstallLock(dlCtx, filepath.Join(root, agentBrowserLockFileName), "agent-browser")
	if err != nil {
		return "", err
	}

	err = installAgentBrowserLocked(dlCtx, opts, root, installDir, goos, downloadURL, spec)
	if lockErr := releaseLock(); lockErr != nil && err == nil {
		err = fmt.Errorf("liberar trava de instalação do agent-browser: %w", lockErr)
	}
	if err != nil {
		return "", err
	}
	return execPath, nil
}

func installAgentBrowserLocked(ctx context.Context, opts AgentBrowserInstallOptions, root, installDir, goos, downloadURL string, spec AgentBrowserAsset) error {
	if isToolInstalled(installDir, goos, spec.ExecutableName) {
		return nil
	}
	maxBytes := opts.maxBytes
	if maxBytes <= 0 {
		maxBytes = defaultAgentBrowserMaxBytes
	}
	expectedSHA := cmp.Or(strings.TrimSpace(opts.expectedSHA256), spec.SHA256)
	binaryBytes, err := fetchAndVerifyReleaseAsset(ctx, opts.HTTPClient, downloadURL, spec.AssetName, expectedSHA, "agent-browser", maxBytes)
	if err != nil {
		return err
	}

	stageDir, err := os.MkdirTemp(root, ".agent-browser-stage-*")
	if err != nil {
		return fmt.Errorf("criar pasta temporária de instalação: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stageDir)
		}
	}()

	if err := writeExecutableBinary(filepath.Join(stageDir, spec.ExecutableName), binaryBytes); err != nil {
		return err
	}
	if err := verifyInstalledBinaries(stageDir, goos, spec.ExecutableName); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stageDir, toolInstalledMarker), []byte(AgentBrowserVersion+"\n"), 0o644); err != nil {
		return fmt.Errorf("gravar marcador de instalação: %w", err)
	}
	if err := publishInstalledDir(stageDir, installDir); err != nil {
		return err
	}
	committed = true
	return nil
}

func writeExecutableBinary(destPath string, data []byte) error {
	out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o755)
	if err != nil {
		return fmt.Errorf("criar executável %q: %w", filepath.Base(destPath), err)
	}
	if runtime.GOOS != "windows" {
		if err := out.Chmod(0o755); err != nil {
			_ = out.Close()
			return fmt.Errorf("ajustar permissões de %q: %w", filepath.Base(destPath), err)
		}
	}
	n, writeErr := out.Write(data)
	if writeErr == nil && n != len(data) {
		writeErr = fmt.Errorf("escrita incompleta (%d/%d bytes)", n, len(data))
	}
	if err := errors.Join(writeErr, out.Sync(), out.Close()); err != nil {
		return fmt.Errorf("gravar executável %q: %w", filepath.Base(destPath), err)
	}
	return nil
}
