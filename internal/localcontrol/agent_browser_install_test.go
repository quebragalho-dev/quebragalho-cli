package localcontrol

import (
	"bytes"
	"cmp"
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

	"github.com/quebragalho-dev/quebragalho-cli/internal/config"
)

func fakeAgentBrowserBinary(body string) ([]byte, string) {
	data := []byte(body)
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:])
}

func TestAgentBrowserAssetTableCoversSixSupportedTargets(t *testing.T) {
	if AgentBrowserVersion != "v0.43.0" {
		t.Fatalf("AgentBrowserVersion = %q, want v0.43.0", AgentBrowserVersion)
	}
	want := []struct{ goos, goarch, asset, sha256, exe string }{
		{"darwin", "arm64", "agent-browser-darwin-arm64", "05338a8eeddf0b3f9ebf0c87a6c21aedc10685608c68618e1a8483c43eae9c3f", "agent-browser"},
		{"darwin", "amd64", "agent-browser-darwin-x64", "49a5ab27297213c1bc0005701a6a4542399abfc9d2cb7520ab519ebb94b8b13d", "agent-browser"},
		{"linux", "arm64", "agent-browser-linux-arm64", "34be55c2b92d8d199fb9d1d62a905ee5b6ac5b1f3e8c24e5b20597f370d905e5", "agent-browser"},
		{"linux", "amd64", "agent-browser-linux-x64", "8e69c9fb662514c9cf36e4911e94e913742ba066e55102486350e049dd847612", "agent-browser"},
		{"windows", "amd64", "agent-browser-win32-x64.exe", "ebcef5ebea93c7ccf0fe3f8c18eee908ae9174dfef8e52a3dda6f24a59f630dc", "agent-browser.exe"},
		{"windows", "arm64", "agent-browser-win32-x64.exe", "ebcef5ebea93c7ccf0fe3f8c18eee908ae9174dfef8e52a3dda6f24a59f630dc", "agent-browser.exe"},
	}
	for _, tc := range want {
		got, ok := AgentBrowserAssetFor(tc.goos, tc.goarch)
		if !ok || got.AssetName != tc.asset || got.SHA256 != tc.sha256 || got.ExecutableName != tc.exe {
			t.Fatalf("AgentBrowserAssetFor(%q, %q) = (%+v, %v)", tc.goos, tc.goarch, got, ok)
		}
		if tc.goos == "linux" && strings.Contains(got.AssetName, "musl") {
			t.Fatalf("linux asset %q must target glibc, not musl", got.AssetName)
		}
	}
}

func TestDefaultAgentBrowserRootUsesQGConfigBase(t *testing.T) {
	cfgBase := canonicalPath(t, t.TempDir())
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(k, cfgBase)
	}
	got, err := DefaultAgentBrowserRoot()
	if err != nil {
		t.Fatalf("DefaultAgentBrowserRoot() error = %v", err)
	}
	want := filepath.Join(cfgBase, "qg", "tools", "agent-browser")
	if got != want {
		t.Fatalf("DefaultAgentBrowserRoot() = %q, want %q", got, want)
	}

	spec, _ := AgentBrowserAssetFor("linux", "amd64")
	binBytes, binSHA := fakeAgentBrowserBinary("#!/bin/sh\necho default-root\n")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+spec.AssetName {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(binBytes)
	}))
	t.Cleanup(srv.Close)

	binPath, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
		HTTPClient: srv.Client(), BaseURL: srv.URL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: binSHA,
	})
	if err != nil {
		t.Fatalf("EnsureAgentBrowser with default root: %v", err)
	}
	if wantBin := filepath.Join(want, "v0.43.0-linux-amd64", "agent-browser"); binPath != wantBin {
		t.Fatalf("binPath = %q, want %q", binPath, wantBin)
	}
}

func TestEnsureAgentBrowserTLSServerInstallsExecutable(t *testing.T) {
	for _, target := range []struct{ name, goos, goarch, wantAsset, wantDir, exe string }{
		{"linux-amd64", "linux", "amd64", "agent-browser-linux-x64", "v0.43.0-linux-amd64", "agent-browser"},
		{"darwin-arm64", "darwin", "arm64", "agent-browser-darwin-arm64", "v0.43.0-darwin-arm64", "agent-browser"},
		{"windows-amd64", "windows", "amd64", "agent-browser-win32-x64.exe", "v0.43.0-windows-amd64", "agent-browser.exe"},
		{"windows-arm64-uses-x64-asset", "windows", "arm64", "agent-browser-win32-x64.exe", "v0.43.0-windows-arm64", "agent-browser.exe"},
	} {
		t.Run(target.name, func(t *testing.T) {
			binBytes, binSHA := fakeAgentBrowserBinary("#!/bin/sh\necho agent-browser " + target.name + "\n")
			var requestedPath string
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestedPath = r.URL.Path
				if r.URL.Path != "/"+target.wantAsset {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(binBytes)
			}))
			t.Cleanup(srv.Close)

			root := canonicalPath(t, t.TempDir())
			binPath, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
				Root: root, HTTPClient: srv.Client(), BaseURL: srv.URL,
				GOOS: target.goos, GOARCH: target.goarch, expectedSHA256: binSHA,
			})
			if err != nil {
				t.Fatalf("EnsureAgentBrowser: %v", err)
			}
			if requestedPath != "/"+target.wantAsset {
				t.Fatalf("requested path = %q, want /%s", requestedPath, target.wantAsset)
			}
			wantPath := filepath.Join(root, target.wantDir, target.exe)
			if binPath != wantPath {
				t.Fatalf("binPath = %q, want %q", binPath, wantPath)
			}
			info, err := os.Stat(binPath)
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("Stat(%q): info=%v err=%v", binPath, info, err)
			}
			if runtime.GOOS != "windows" && target.goos != "windows" && info.Mode()&0o111 == 0 {
				t.Fatalf("%q missing exec bit: %v", binPath, info.Mode())
			}
			gotBytes, err := os.ReadFile(binPath)
			if err != nil || !bytes.Equal(gotBytes, binBytes) {
				t.Fatalf("ReadFile(%q) mismatch: err=%v", binPath, err)
			}
		})
	}
}

func TestEnsureAgentBrowserRejectsWrongSHA256BeforeWrite(t *testing.T) {
	spec, _ := AgentBrowserAssetFor("linux", "amd64")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+spec.AssetName {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("tampered-agent-browser-binary"))
	}))
	t.Cleanup(srv.Close)

	root := canonicalPath(t, t.TempDir())
	_, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
		Root: root, HTTPClient: srv.Client(), BaseURL: srv.URL, GOOS: "linux", GOARCH: "amd64",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "sha256") {
		t.Fatalf("error = %v, want SHA256 mismatch", err)
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		t.Fatalf("ReadDir(%q): %v", root, readErr)
	}
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("unexpected directory %q left in root after SHA256 rejection", e.Name())
		}
	}
}

func TestEnsureAgentBrowserReusesExistingWithoutNetworkAndRepairsIncomplete(t *testing.T) {
	spec, _ := AgentBrowserAssetFor("linux", "amd64")
	binBytes, binSHA := fakeAgentBrowserBinary("#!/bin/sh\necho agent-browser\n")

	var downloads atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+spec.AssetName {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(binBytes)
	}))

	opts := AgentBrowserInstallOptions{
		Root: canonicalPath(t, t.TempDir()), HTTPClient: srv.Client(), BaseURL: srv.URL,
		GOOS: "linux", GOARCH: "amd64", expectedSHA256: binSHA,
	}
	first, err := EnsureAgentBrowser(context.Background(), opts)
	if err != nil || downloads.Load() != 1 {
		t.Fatalf("first call = (%q, %v), downloads=%d", first, err, downloads.Load())
	}
	if err := os.Remove(first); err != nil {
		t.Fatalf("Remove(%q): %v", first, err)
	}
	if repaired, err := EnsureAgentBrowser(context.Background(), opts); err != nil || repaired != first || downloads.Load() != 2 {
		t.Fatalf("repair = (%q, %v), downloads=%d", repaired, err, downloads.Load())
	}
	srv.Close()
	if reused, err := EnsureAgentBrowser(context.Background(), opts); err != nil || reused != first || downloads.Load() != 2 {
		t.Fatalf("offline reuse = (%q, %v), downloads=%d", reused, err, downloads.Load())
	}
}

func TestEnsureAgentBrowserSubprocessWorker(t *testing.T) {
	root := os.Getenv("QG_TEST_AGENT_BROWSER_SUBPROC_ROOT")
	if root == "" {
		return
	}
	got, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
		Root: root, BaseURL: os.Getenv("QG_TEST_AGENT_BROWSER_SUBPROC_URL"),
		GOOS: "linux", GOARCH: "amd64", expectedSHA256: os.Getenv("QG_TEST_AGENT_BROWSER_SUBPROC_SHA"),
	})
	if err != nil {
		t.Fatalf("subprocess EnsureAgentBrowser: %v", err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("subprocess Stat(%q): %v", got, err)
	}
}

func TestEnsureAgentBrowserConcurrentCallsDownloadOnce(t *testing.T) {
	spec, _ := AgentBrowserAssetFor("linux", "amd64")
	binBytes, binSHA := fakeAgentBrowserBinary("#!/bin/sh\necho agent-browser\n")

	t.Run("goroutines-tls", func(t *testing.T) {
		var downloads atomic.Int32
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/"+spec.AssetName {
				http.NotFound(w, r)
				return
			}
			downloads.Add(1)
			time.Sleep(80 * time.Millisecond)
			_, _ = w.Write(binBytes)
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
				paths[idx], errs[idx] = EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
					Root: root, HTTPClient: srv.Client(), BaseURL: srv.URL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: binSHA,
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
			_, _ = w.Write(binBytes)
		}))
		t.Cleanup(srv.Close)

		root := canonicalPath(t, t.TempDir())
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				cmd := exec.Command(os.Args[0], "-test.run=^TestEnsureAgentBrowserSubprocessWorker$")
				cmd.Env = append(os.Environ(),
					"QG_TEST_AGENT_BROWSER_SUBPROC_ROOT="+root,
					"QG_TEST_AGENT_BROWSER_SUBPROC_URL="+srv.URL,
					"QG_TEST_AGENT_BROWSER_SUBPROC_SHA="+binSHA,
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

func TestEnsureAgentBrowserUnsupportedPlatformPortugueseError(t *testing.T) {
	for _, tc := range [][2]string{{"plan9", "amd64"}, {"linux", "386"}, {"freebsd", "arm64"}} {
		_, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{Root: t.TempDir(), GOOS: tc[0], GOARCH: tc[1]})
		wantSub := "o controle do navegador não tem suporte para " + tc[0] + "/" + tc[1]
		if err == nil || !strings.Contains(err.Error(), wantSub) {
			t.Fatalf("(%s/%s) error = %v, want substring %q", tc[0], tc[1], err, wantSub)
		}
	}
}

func TestEnsureAgentBrowserURLSecurityAndLimits(t *testing.T) {
	spec, _ := AgentBrowserAssetFor("linux", "amd64")
	binBytes, binSHA := fakeAgentBrowserBinary("#!/bin/sh\necho agent-browser\n")

	for _, badURL := range []string{"http://example.com/r", "http://127.0.0.1.example.com/r", "http://localhost.example.com/r", "ftp://127.0.0.1/r"} {
		if _, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
			Root: t.TempDir(), BaseURL: badURL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: binSHA,
		}); err == nil {
			t.Fatalf("expected URL %q to be refused", badURL)
		}
	}

	httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(binBytes) }))
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
		_, _ = w.Write(binBytes)
	}))
	t.Cleanup(tlsSrv.Close)

	if _, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
		Root: t.TempDir(), HTTPClient: tlsSrv.Client(), BaseURL: tlsSrv.URL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: binSHA,
	}); err != nil {
		t.Fatalf("HTTPS->HTTPS redirect failed: %v", err)
	}
	if _, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
		Root: t.TempDir(), HTTPClient: tlsSrv.Client(), BaseURL: tlsSrv.URL, GOOS: "linux", GOARCH: "amd64",
		expectedSHA256: binSHA, maxBytes: int64(len(binBytes)) - 1,
	}); err == nil {
		t.Fatal("expected oversized download to be refused")
	}
	downgrade.Store(true)
	if _, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
		Root: t.TempDir(), HTTPClient: tlsSrv.Client(), BaseURL: tlsSrv.URL, GOOS: "linux", GOARCH: "amd64", expectedSHA256: binSHA,
	}); err == nil {
		t.Fatal("expected HTTPS->HTTP downgrade redirect to be refused")
	}
	slow.Store(true)
	if _, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{
		Root: t.TempDir(), HTTPClient: tlsSrv.Client(), BaseURL: tlsSrv.URL, GOOS: "linux", GOARCH: "amd64",
		expectedSHA256: binSHA, timeout: 50 * time.Millisecond,
	}); err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestBrowserOptionsFromConfigSetsEnsureOnlyForDefaultDriver(t *testing.T) {
	for _, tc := range []struct {
		driver     string
		wantEnsure bool
	}{
		{driver: "", wantEnsure: true},
		{driver: DefaultBrowserDriver, wantEnsure: true},
		{driver: "custom-browser-driver", wantEnsure: false},
	} {
		var cfg config.LocalControlConfig
		cfg.Browser.Driver = tc.driver
		if got := BrowserOptionsFromConfig(cfg).Ensure != nil; got != tc.wantEnsure {
			t.Fatalf("driver %q: Ensure set = %v, want %v", tc.driver, got, tc.wantEnsure)
		}
	}
}

func TestEnsureAgentBrowserRealDownload(t *testing.T) {
	if os.Getenv("QG_AGENT_BROWSER_REAL_TEST") == "" {
		t.Skip("defina QG_AGENT_BROWSER_REAL_TEST=1 para rodar o teste real de download e navegação do agent-browser")
	}
	homeDir := canonicalPath(t, t.TempDir())
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(k, homeDir)
	}
	root := cmp.Or(strings.TrimSpace(os.Getenv("QG_AGENT_BROWSER_REAL_ROOT")), canonicalPath(t, t.TempDir()))
	binPath, err := EnsureAgentBrowser(context.Background(), AgentBrowserInstallOptions{Root: root})
	if err != nil {
		t.Fatalf("EnsureAgentBrowser real download: %v", err)
	}
	info, err := os.Stat(binPath)
	if err != nil || info.Mode()&fs.ModeType != 0 {
		t.Fatalf("Stat(%q): info=%v err=%v", binPath, info, err)
	}
	t.Cleanup(func() {
		_ = exec.Command(binPath, "close").Run()
	})

	for _, args := range [][]string{
		{"--version"},
		{"open", "https://example.com"},
		{"snapshot", "-i"},
		{"close"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		out, runErr := exec.CommandContext(ctx, binPath, args...).CombinedOutput()
		cancel()
		if runErr != nil {
			t.Fatalf("%s %s failed: %v\n%s", binPath, strings.Join(args, " "), runErr, string(out))
		}
		t.Logf("$ %s %s\n%s", filepath.Base(binPath), strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
}
