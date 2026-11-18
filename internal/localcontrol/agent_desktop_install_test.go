package localcontrol

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTarEntry struct {
	name     string
	typeflag byte
	mode     int64
	body     string
	linkname string
}

func buildTarGzBytes(t *testing.T, entries []fakeTarEntry) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: cmp.Or(e.typeflag, byte(tar.TypeReg)),
			Mode:     cmp.Or(e.mode, int64(0o755)),
			Linkname: e.linkname,
		}
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%q): %v", e.name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("Write(%q): %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func validFakeTarForTarget(t *testing.T, goos, goarch, prefix string) ([]byte, string) {
	t.Helper()
	spec, ok := AgentDesktopAssetFor(goos, goarch)
	if !ok {
		t.Fatalf("missing asset spec for %s/%s", goos, goarch)
	}
	var entries []fakeTarEntry
	if prefix != "" {
		entries = append(entries, fakeTarEntry{name: prefix + "/", typeflag: tar.TypeDir})
		prefix += "/"
	}
	entries = append(entries, fakeTarEntry{name: prefix + spec.ExecutableName, body: "#!/bin/sh\necho agent-desktop\n"})
	if spec.CompanionBinary != "" {
		entries = append(entries, fakeTarEntry{name: prefix + spec.CompanionBinary, body: "#!/bin/sh\necho helper\n"})
	}
	return buildTarGzBytes(t, entries)
}

func canonicalPath(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", p, err)
	}
	return resolved
}

func TestAgentDesktopAssetTableCoversSixSupportedTargets(t *testing.T) {
	if AgentDesktopVersion != "v1.3.1" {
		t.Fatalf("AgentDesktopVersion = %q, want v1.3.1", AgentDesktopVersion)
	}
	want := []struct{ goos, goarch, asset, sha256, exe, companion string }{
		{"darwin", "arm64", "agent-desktop-v1.3.1-aarch64-apple-darwin.tar.gz", "9b7b7cfc74892eab46c2c85d6cb7dc4863bf8dab0a222cfcfb8ae4604c69c86c", "agent-desktop", "agent-desktop-macos-helper"},
		{"darwin", "amd64", "agent-desktop-v1.3.1-x86_64-apple-darwin.tar.gz", "469da10cfb68928c58caff88b92b30526e0ca895ac06bc86b7f533b1d9b309b3", "agent-desktop", "agent-desktop-macos-helper"},
		{"linux", "arm64", "agent-desktop-v1.3.1-aarch64-unknown-linux-gnu.tar.gz", "438990a703fbeb19775596c341c3701435e80adbfa1b2f64b82ff17567d5573b", "agent-desktop", "agent-desktop-linux-clipboard-owner"},
		{"linux", "amd64", "agent-desktop-v1.3.1-x86_64-unknown-linux-gnu.tar.gz", "ca77b6b75034eeb12e537dd8c0a4f2a3424119253f38b7e6bd79ba0d2fef1264", "agent-desktop", "agent-desktop-linux-clipboard-owner"},
		{"windows", "arm64", "agent-desktop-v1.3.1-aarch64-pc-windows-msvc.tar.gz", "805f1f61890f9983fceaf38cda94a37a049c379435d65607db5cedd22c8ef97b", "agent-desktop.exe", ""},
		{"windows", "amd64", "agent-desktop-v1.3.1-x86_64-pc-windows-msvc.tar.gz", "6ee4b1e078f4e78792e79f16e32e4a78b9919464aa3c55629b7e27ed53a08c13", "agent-desktop.exe", ""},
	}
	for _, tc := range want {
		got, ok := AgentDesktopAssetFor(tc.goos, tc.goarch)
		if !ok || got.AssetName != tc.asset || got.SHA256 != tc.sha256 || got.ExecutableName != tc.exe || got.CompanionBinary != tc.companion {
			t.Fatalf("AgentDesktopAssetFor(%q, %q) = (%+v, %v)", tc.goos, tc.goarch, got, ok)
		}
	}
}

func TestDefaultAgentDesktopRootUsesQGConfigBase(t *testing.T) {
	cfgBase := canonicalPath(t, t.TempDir())
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(k, cfgBase)
	}
	got, err := DefaultAgentDesktopRoot()
	if err != nil {
		t.Fatalf("DefaultAgentDesktopRoot() error = %v", err)
	}
	if want := filepath.Join(cfgBase, "qg", "tools", "agent-desktop"); got != want {
		t.Fatalf("DefaultAgentDesktopRoot() = %q, want %q", got, want)
	}
}

func TestEnsureAgentDesktopTLSServerExtractsExecutableAndCompanion(t *testing.T) {
	for _, target := range []struct{ name, goos, goarch, prefix, exe, companion string }{
		{"linux-root", "linux", "amd64", "", "agent-desktop", "agent-desktop-linux-clipboard-owner"},
		{"darwin-topdir", "darwin", "arm64", "agent-desktop-v1.3.1", "agent-desktop", "agent-desktop-macos-helper"},
		{"windows-root", "windows", "amd64", "", "agent-desktop.exe", ""},
	} {
		t.Run(target.name, func(t *testing.T) {
			spec, _ := AgentDesktopAssetFor(target.goos, target.goarch)
			archiveBytes, archiveSHA := validFakeTarForTarget(t, target.goos, target.goarch, target.prefix)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/"+spec.AssetName {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(archiveBytes)
			}))
			t.Cleanup(srv.Close)

			binPath, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
				Root: canonicalPath(t, t.TempDir()), HTTPClient: srv.Client(), BaseURL: srv.URL,
				GOOS: target.goos, GOARCH: target.goarch, expectedSHA256: archiveSHA,
			})
			if err != nil {
				t.Fatalf("EnsureAgentDesktop: %v", err)
			}
			if !filepath.IsAbs(binPath) || filepath.Base(binPath) != target.exe {
				t.Fatalf("binPath = %q, want absolute path ending in %q", binPath, target.exe)
			}
			for _, name := range []string{target.exe, target.companion} {
				if name == "" {
					continue
				}
				info, err := os.Stat(filepath.Join(filepath.Dir(binPath), name))
				if err != nil || !info.Mode().IsRegular() {
					t.Fatalf("Stat(%q): info=%v err=%v", name, info, err)
				}
				if runtime.GOOS != "windows" && target.goos != "windows" && info.Mode()&0o111 == 0 {
					t.Fatalf("%q missing exec bit: %v", name, info.Mode())
				}
			}
		})
	}
}

func TestEnsureAgentDesktopRejectsWrongSHA256BeforeExtract(t *testing.T) {
	spec, _ := AgentDesktopAssetFor("linux", "amd64")
	badBytes, _ := buildTarGzBytes(t, []fakeTarEntry{
		{name: "../should-never-extract", body: "nope"},
		{name: spec.ExecutableName, body: "tampered"},
	})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+spec.AssetName {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(badBytes)
	}))
	t.Cleanup(srv.Close)

	parent := t.TempDir()
	_, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
		Root: filepath.Join(parent, "install-root"), HTTPClient: srv.Client(), BaseURL: srv.URL, GOOS: "linux", GOARCH: "amd64",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "sha256") {
		t.Fatalf("error = %v, want SHA256 mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(parent, "should-never-extract")); !os.IsNotExist(statErr) {
		t.Fatalf("tampered archive must not be extracted: %v", statErr)
	}
}

func TestEnsureAgentDesktopRejectsUnsafeOrUnexpectedTarEntries(t *testing.T) {
	spec, _ := AgentDesktopAssetFor("linux", "amd64")
	base := []fakeTarEntry{{name: spec.ExecutableName, body: "bin"}, {name: spec.CompanionBinary, body: "helper"}}

	for _, tc := range []struct {
		name    string
		entries []fakeTarEntry
	}{
		{"dotdot-escape", append([]fakeTarEntry{{name: "../escaped-file", body: "bad"}}, base...)},
		{"nested-dotdot", []fakeTarEntry{{name: "sub/../agent-desktop", body: "bin"}, {name: spec.CompanionBinary, body: "helper"}}},
		{"unix-abs", []fakeTarEntry{{name: "/tmp/agent-desktop", body: "bad"}}},
		{"windows-abs", []fakeTarEntry{{name: `C:\evil\agent-desktop.exe`, body: "bad"}}},
		{"unexpected-file", append([]fakeTarEntry{{name: "unexpected.sh", body: "bad"}}, base...)},
		{"symlink-rejected", append([]fakeTarEntry{{name: "link", typeflag: tar.TypeSymlink, linkname: spec.ExecutableName}}, base...)},
		{"hardlink-rejected", append(base, fakeTarEntry{name: "hlink", typeflag: tar.TypeLink, linkname: spec.ExecutableName})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archiveBytes, archiveSHA := buildTarGzBytes(t, tc.entries)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archiveBytes) }))
			t.Cleanup(srv.Close)

			parent := t.TempDir()
			root := filepath.Join(parent, "root")
			if _, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
				Root: root, HTTPClient: srv.Client(), BaseURL: srv.URL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: archiveSHA,
			}); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if _, statErr := os.Stat(filepath.Join(parent, "escaped-file")); !os.IsNotExist(statErr) {
				t.Fatalf("escaped-file written outside root: %v", statErr)
			}
			if entries, err := os.ReadDir(root); err == nil {
				for _, e := range entries {
					if e.IsDir() {
						t.Fatalf("leftover directory %q in root after rejected tar", e.Name())
					}
				}
			}
		})
	}
}

func TestEnsureAgentDesktopReusesExistingAndRepairsIncompleteInstall(t *testing.T) {
	spec, _ := AgentDesktopAssetFor("linux", "amd64")
	archiveBytes, archiveSHA := validFakeTarForTarget(t, "linux", "amd64", "")

	var downloads atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+spec.AssetName {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(archiveBytes)
	}))

	opts := AgentDesktopInstallOptions{
		Root: canonicalPath(t, t.TempDir()), HTTPClient: srv.Client(), BaseURL: srv.URL,
		GOOS: "linux", GOARCH: "amd64", expectedSHA256: archiveSHA,
	}
	first, err := EnsureAgentDesktop(context.Background(), opts)
	if err != nil || downloads.Load() != 1 {
		t.Fatalf("first call = (%q, %v), downloads=%d", first, err, downloads.Load())
	}
	if err := os.Remove(filepath.Join(filepath.Dir(first), spec.CompanionBinary)); err != nil {
		t.Fatalf("Remove companion: %v", err)
	}
	if repaired, err := EnsureAgentDesktop(context.Background(), opts); err != nil || repaired != first || downloads.Load() != 2 {
		t.Fatalf("repair = (%q, %v), downloads=%d", repaired, err, downloads.Load())
	}
	srv.Close()
	if reused, err := EnsureAgentDesktop(context.Background(), opts); err != nil || reused != first || downloads.Load() != 2 {
		t.Fatalf("offline reuse = (%q, %v), downloads=%d", reused, err, downloads.Load())
	}
}

func TestEnsureAgentDesktopSubprocessWorker(t *testing.T) {
	root := os.Getenv("QG_TEST_AGENT_DESKTOP_SUBPROC_ROOT")
	if root == "" {
		return
	}
	got, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
		Root: root, BaseURL: os.Getenv("QG_TEST_AGENT_DESKTOP_SUBPROC_URL"),
		GOOS: "linux", GOARCH: "amd64", expectedSHA256: os.Getenv("QG_TEST_AGENT_DESKTOP_SUBPROC_SHA"),
	})
	if err != nil {
		t.Fatalf("subprocess EnsureAgentDesktop: %v", err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("subprocess Stat(%q): %v", got, err)
	}
}

func TestEnsureAgentDesktopConcurrentCallsDownloadOnce(t *testing.T) {
	spec, _ := AgentDesktopAssetFor("linux", "amd64")
	archiveBytes, archiveSHA := validFakeTarForTarget(t, "linux", "amd64", "")

	t.Run("goroutines-tls", func(t *testing.T) {
		var downloads atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/"+spec.AssetName {
				http.NotFound(w, r)
				return
			}
			downloads.Add(1)
			time.Sleep(80 * time.Millisecond)
			_, _ = w.Write(archiveBytes)
		}))
		t.Cleanup(srv.Close)

		root := canonicalPath(t, t.TempDir())
		const workers = 8
		start := make(chan struct{})
		var wg sync.WaitGroup
		paths := make([]string, workers)
		errs := make([]error, workers)
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				paths[idx], errs[idx] = EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
					Root: root, HTTPClient: srv.Client(), BaseURL: srv.URL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: archiveSHA,
				})
			}(i)
		}
		close(start)
		wg.Wait()
		for i := range workers {
			if errs[i] != nil || paths[i] != paths[0] {
				t.Fatalf("worker %d = (%q, %v), want %q", i, paths[i], errs[i], paths[0])
			}
		}
		if got := downloads.Load(); got != 1 {
			t.Fatalf("concurrent goroutine downloads = %d, want 1", got)
		}
	})

	t.Run("subprocesses", func(t *testing.T) {
		var downloads atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/"+spec.AssetName {
				http.NotFound(w, r)
				return
			}
			downloads.Add(1)
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write(archiveBytes)
		}))
		t.Cleanup(srv.Close)

		root := canonicalPath(t, t.TempDir())
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				cmd := exec.Command(os.Args[0], "-test.run=^TestEnsureAgentDesktopSubprocessWorker$")
				cmd.Env = append(os.Environ(),
					"QG_TEST_AGENT_DESKTOP_SUBPROC_ROOT="+root,
					"QG_TEST_AGENT_DESKTOP_SUBPROC_URL="+srv.URL,
					"QG_TEST_AGENT_DESKTOP_SUBPROC_SHA="+archiveSHA,
				)
				if out, err := cmd.CombinedOutput(); err != nil {
					errs[idx] = &os.PathError{Op: "subproc", Path: string(out), Err: err}
				}
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("subprocess %d failed: %v", i, err)
			}
		}
		if got := downloads.Load(); got != 1 {
			t.Fatalf("concurrent subprocess downloads = %d, want 1", got)
		}
	})
}

func TestEnsureAgentDesktopUnsupportedPlatformPortugueseError(t *testing.T) {
	for _, tc := range [][2]string{{"plan9", "amd64"}, {"linux", "386"}, {"freebsd", "arm64"}} {
		_, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{Root: t.TempDir(), GOOS: tc[0], GOARCH: tc[1]})
		wantSub := "o controle do computador não tem suporte para " + tc[0] + "/" + tc[1]
		if err == nil || !strings.Contains(err.Error(), wantSub) {
			t.Fatalf("(%s/%s) error = %v, want substring %q", tc[0], tc[1], err, wantSub)
		}
	}
}

func TestEnsureAgentDesktopURLSecurityAndLimits(t *testing.T) {
	spec, _ := AgentDesktopAssetFor("linux", "amd64")
	archiveBytes, archiveSHA := validFakeTarForTarget(t, "linux", "amd64", "")

	for _, badURL := range []string{"http://example.com/r", "http://127.0.0.1.example.com/r", "http://localhost.example.com/r", "ftp://127.0.0.1/r"} {
		if _, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
			Root: t.TempDir(), BaseURL: badURL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: archiveSHA,
		}); err == nil {
			t.Fatalf("expected URL %q to be refused", badURL)
		}
	}

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archiveBytes) }))
	t.Cleanup(httpSrv.Close)

	var tlsSrv *httptest.Server
	var downgrade, slow atomic.Bool
	tlsSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slow.Load() {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		if r.URL.Path == "/"+spec.AssetName {
			if downgrade.Load() {
				http.Redirect(w, r, httpSrv.URL+"/"+spec.AssetName, http.StatusFound)
				return
			}
			http.Redirect(w, r, tlsSrv.URL+"/cdn/"+spec.AssetName, http.StatusFound)
			return
		}
		_, _ = w.Write(archiveBytes)
	}))
	t.Cleanup(tlsSrv.Close)

	if _, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
		Root: t.TempDir(), HTTPClient: tlsSrv.Client(), BaseURL: tlsSrv.URL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: archiveSHA,
	}); err != nil {
		t.Fatalf("HTTPS->HTTPS redirect failed: %v", err)
	}
	if _, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
		Root: t.TempDir(), HTTPClient: tlsSrv.Client(), BaseURL: tlsSrv.URL, GOOS: "linux", GOARCH: "amd64",
		expectedSHA256: archiveSHA, maxBytes: int64(len(archiveBytes)) - 1,
	}); err == nil {
		t.Fatal("expected oversized download to be refused")
	}
	downgrade.Store(true)
	if _, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
		Root: t.TempDir(), HTTPClient: tlsSrv.Client(), BaseURL: tlsSrv.URL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: archiveSHA,
	}); err == nil {
		t.Fatal("expected HTTPS->HTTP downgrade redirect to be refused")
	}
	slow.Store(true)
	if _, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
		Root: t.TempDir(), HTTPClient: tlsSrv.Client(), BaseURL: tlsSrv.URL, GOOS: "linux", GOARCH: "amd64",
		expectedSHA256: archiveSHA, timeout: 50 * time.Millisecond,
	}); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestEnsureAgentDesktopRealDownload(t *testing.T) {
	if os.Getenv("QG_AGENT_DESKTOP_REAL_TEST") == "" {
		t.Skip("defina QG_AGENT_DESKTOP_REAL_TEST=1 para rodar o teste real de download do agent-desktop")
	}
	root := cmp.Or(strings.TrimSpace(os.Getenv("QG_AGENT_DESKTOP_REAL_ROOT")), canonicalPath(t, t.TempDir()))
	binPath, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{Root: root})
	if err != nil {
		t.Fatalf("EnsureAgentDesktop real download: %v", err)
	}
	info, err := os.Stat(binPath)
	if err != nil || info.Mode()&fs.ModeType != 0 {
		t.Fatalf("Stat(%q): info=%v err=%v", binPath, info, err)
	}
	if spec, ok := AgentDesktopAssetFor(runtime.GOOS, runtime.GOARCH); ok && spec.CompanionBinary != "" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(binPath), spec.CompanionBinary)); err != nil {
			t.Fatalf("missing companion binary %q: %v", spec.CompanionBinary, err)
		}
	}
	verOut, err := exec.Command(binPath, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s version failed: %v\n%s", binPath, err, string(verOut))
	}
	t.Logf("version output:\n%s", strings.TrimSpace(string(verOut)))
}
