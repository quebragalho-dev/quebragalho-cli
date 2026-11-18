package media

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ulikunitz/xz"
)

func TestFFmpegAssetForKnownPlatforms(t *testing.T) {
	for _, platform := range [][2]string{
		{"linux", "amd64"},
		{"linux", "arm64"},
		{"windows", "amd64"},
		{"windows", "arm64"},
	} {
		asset, ok := FFmpegAssetFor(platform[0], platform[1])
		if !ok {
			t.Fatalf("plataforma %s/%s deveria ter pacote fixado", platform[0], platform[1])
		}
		if asset.FileName == "" || asset.SHA256 == "" || asset.Size <= 0 {
			t.Fatalf("pacote %s/%s incompleto: %+v", platform[0], platform[1], asset)
		}
	}
}

func TestFFmpegAssetForDarwinIsSystemOnly(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		if _, ok := FFmpegAssetFor("darwin", arch); ok {
			t.Fatalf("darwin/%s não deveria ter pacote fixado (só o ffmpeg do sistema)", arch)
		}
	}
}

// fakeFFmpegZip monta um pacote zip mínimo com os dois binários (scripts
// falsos que ecoam os argumentos, para provar chamada sem shell).
func fakeFFmpegZip(t *testing.T, goos string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, name := range []string{"bin/" + ffmpegExeName(goos), "bin/" + ffprobeExeName(goos)} {
		entry, err := writer.Create("pkg-top/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("#!/usr/bin/env sh\necho fake-ffmpeg \"$@\"\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fakeFFmpegTarXz(t *testing.T, goos string) []byte {
	t.Helper()
	var tarBuf bytes.Buffer
	tarWriter := tar.NewWriter(&tarBuf)
	for _, name := range []string{"pkg-top/bin/" + ffmpegExeName(goos), "pkg-top/bin/" + ffprobeExeName(goos)} {
		body := []byte("#!/usr/bin/env sh\necho fake-ffmpeg \"$@\"\n")
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	xzWriter, err := xz.NewWriter(&out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xzWriter.Write(tarBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := xzWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func serveBytes(t *testing.T, data []byte) (string, *http.Client) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)
	return server.URL, server.Client()
}

func TestEnsureFFmpegDownloadsAndVerifiesZipBundle(t *testing.T) {
	archive := fakeFFmpegZip(t, "windows")
	baseURL, client := serveBytes(t, archive)
	root := t.TempDir()

	ffmpegPath, ffprobePath, err := EnsureFFmpeg(t.Context(), FFmpegInstallOptions{
		Root:           root,
		HTTPClient:     client,
		BaseURL:        baseURL,
		GOOS:           "windows",
		GOARCH:         "amd64",
		ExpectedSHA256: sha256Hex(archive),
	})
	if err != nil {
		t.Fatalf("EnsureFFmpeg falhou: %v", err)
	}
	if !strings.HasSuffix(ffmpegPath, "ffmpeg.exe") || !strings.HasSuffix(ffprobePath, "ffprobe.exe") {
		t.Fatalf("caminhos inesperados: %q %q", ffmpegPath, ffprobePath)
	}
	for _, path := range []string{ffmpegPath, ffprobePath} {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			t.Fatalf("binário ausente: %s (%v)", path, err)
		}
	}

	// Segunda chamada reaproveita sem baixar de novo: servidor fora do ar.
	second, _, err := EnsureFFmpeg(t.Context(), FFmpegInstallOptions{
		Root:           root,
		HTTPClient:     &http.Client{},
		BaseURL:        "http://127.0.0.1:1",
		GOOS:           "windows",
		GOARCH:         "amd64",
		ExpectedSHA256: "00",
	})
	if err != nil {
		t.Fatalf("reaproveitamento falhou: %v", err)
	}
	if second != ffmpegPath {
		t.Fatalf("reaproveitou %q, want %q", second, ffmpegPath)
	}
}

func TestEnsureFFmpegDownloadsAndVerifiesTarXzBundle(t *testing.T) {
	archive := fakeFFmpegTarXz(t, "linux")
	baseURL, client := serveBytes(t, archive)
	root := t.TempDir()

	ffmpegPath, ffprobePath, err := EnsureFFmpeg(t.Context(), FFmpegInstallOptions{
		Root:           root,
		HTTPClient:     client,
		BaseURL:        baseURL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExpectedSHA256: sha256Hex(archive),
	})
	if err != nil {
		t.Fatalf("EnsureFFmpeg falhou: %v", err)
	}
	for _, path := range []string{ffmpegPath, ffprobePath} {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("binário ausente ou sem +x: %s (%v)", path, err)
		}
	}
}

func TestEnsureFFmpegRejectsTamperedBundle(t *testing.T) {
	archive := fakeFFmpegZip(t, "windows")
	baseURL, client := serveBytes(t, archive)

	_, _, err := EnsureFFmpeg(t.Context(), FFmpegInstallOptions{
		Root:           t.TempDir(),
		HTTPClient:     client,
		BaseURL:        baseURL,
		GOOS:           "windows",
		GOARCH:         "amd64",
		ExpectedSHA256: strings.Repeat("0", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("hash adulterado deveria recusar com erro de SHA-256, foi: %v", err)
	}
}

func TestEnsureFFmpegRejectsPackageWithoutBinaries(t *testing.T) {
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	entry, err := writer.Create("pkg-top/README.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("sem binários"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	baseURL, client := serveBytes(t, buf.Bytes())

	_, _, err = EnsureFFmpeg(t.Context(), FFmpegInstallOptions{
		Root:           t.TempDir(),
		HTTPClient:     client,
		BaseURL:        baseURL,
		GOOS:           "windows",
		GOARCH:         "amd64",
		ExpectedSHA256: sha256Hex(buf.Bytes()),
	})
	if err == nil || !strings.Contains(err.Error(), "ffmpeg+ffprobe") {
		t.Fatalf("pacote sem binários deveria recusar, foi: %v", err)
	}
}

func TestEnsureFFmpegOnDarwinPointsToSystemInstall(t *testing.T) {
	_, _, err := EnsureFFmpeg(t.Context(), FFmpegInstallOptions{
		Root:   t.TempDir(),
		GOOS:   "darwin",
		GOARCH: "arm64",
	})
	if err == nil || !strings.Contains(err.Error(), "brew install ffmpeg") {
		t.Fatalf("darwin deveria orientar o brew, foi: %v", err)
	}
}

func TestLookPathFFmpegMissingWithoutBinariesOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, _, ok := LookPathFFmpeg(); ok {
		t.Fatal("sem ffmpeg no PATH, deveria devolver ok=false")
	}
}

func TestFFmpegManagedPathsKeepExeSuffixOnWindows(t *testing.T) {
	ffmpegPath, ffprobePath := FFmpegManagedPaths(filepath.Join("r", "root"), "windows", "amd64")
	if !strings.HasSuffix(ffmpegPath, "ffmpeg.exe") || !strings.HasSuffix(ffprobePath, "ffprobe.exe") {
		t.Fatalf("sufixo .exe ausente: %q %q", ffmpegPath, ffprobePath)
	}
}
