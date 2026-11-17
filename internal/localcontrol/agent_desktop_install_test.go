package localcontrol

import (
	"archive/tar"
	"bytes"
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
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o755
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: typeflag,
			Mode:     mode,
			Linkname: e.linkname,
		}
		if typeflag == tar.TypeReg {
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
	raw := buf.Bytes()
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:])
}

func validFakeTarForTarget(t *testing.T, goos, goarch string) ([]byte, string) {
	t.Helper()
	spec, ok := AgentDesktopAssetFor(goos, goarch)
	if !ok {
		t.Fatalf("missing asset spec for %s/%s", goos, goarch)
	}
	entries := []fakeTarEntry{
		{
			name:     spec.ExecutableName,
			typeflag: tar.TypeReg,
			mode:     0o755,
			body:     "#!/bin/sh\necho agent-desktop " + AgentDesktopVersion + "\n",
		},
	}
	if spec.CompanionBinary != "" {
		entries = append(entries, fakeTarEntry{
			name:     spec.CompanionBinary,
			typeflag: tar.TypeReg,
			mode:     0o755,
			body:     "#!/bin/sh\necho helper\n",
		})
	}
	return buildTarGzBytes(t, entries)
}

func setHermeticUserConfigEnv(t *testing.T, base string) {
	t.Helper()
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("APPDATA", base)
	t.Setenv("LOCALAPPDATA", base)
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
	want := []struct {
		goos      string
		goarch    string
		asset     string
		sha256    string
		exe       string
		companion string
	}{
		{
			goos:      "darwin",
			goarch:    "arm64",
			asset:     "agent-desktop-v1.3.1-aarch64-apple-darwin.tar.gz",
			sha256:    "9b7b7cfc74892eab46c2c85d6cb7dc4863bf8dab0a222cfcfb8ae4604c69c86c",
			exe:       "agent-desktop",
			companion: "agent-desktop-macos-helper",
		},
		{
			goos:      "darwin",
			goarch:    "amd64",
			asset:     "agent-desktop-v1.3.1-x86_64-apple-darwin.tar.gz",
			sha256:    "469da10cfb68928c58caff88b92b30526e0ca895ac06bc86b7f533b1d9b309b3",
			exe:       "agent-desktop",
			companion: "agent-desktop-macos-helper",
		},
		{
			goos:      "linux",
			goarch:    "arm64",
			asset:     "agent-desktop-v1.3.1-aarch64-unknown-linux-gnu.tar.gz",
			sha256:    "438990a703fbeb19775596c341c3701435e80adbfa1b2f64b82ff17567d5573b",
			exe:       "agent-desktop",
			companion: "agent-desktop-linux-clipboard-owner",
		},
		{
			goos:      "linux",
			goarch:    "amd64",
			asset:     "agent-desktop-v1.3.1-x86_64-unknown-linux-gnu.tar.gz",
			sha256:    "ca77b6b75034eeb12e537dd8c0a4f2a3424119253f38b7e6bd79ba0d2fef1264",
			exe:       "agent-desktop",
			companion: "agent-desktop-linux-clipboard-owner",
		},
		{
			goos:      "windows",
			goarch:    "arm64",
			asset:     "agent-desktop-v1.3.1-aarch64-pc-windows-msvc.tar.gz",
			sha256:    "805f1f61890f9983fceaf38cda94a37a049c379435d65607db5cedd22c8ef97b",
			exe:       "agent-desktop.exe",
			companion: "",
		},
		{
			goos:      "windows",
			goarch:    "amd64",
			asset:     "agent-desktop-v1.3.1-x86_64-pc-windows-msvc.tar.gz",
			sha256:    "6ee4b1e078f4e78792e79f16e32e4a78b9919464aa3c55629b7e27ed53a08c13",
			exe:       "agent-desktop.exe",
			companion: "",
		},
	}

	for _, tc := range want {
		got, ok := AgentDesktopAssetFor(tc.goos, tc.goarch)
		if !ok {
			t.Fatalf("AgentDesktopAssetFor(%q, %q) not found", tc.goos, tc.goarch)
		}
		if got.AssetName != tc.asset || got.SHA256 != tc.sha256 || got.ExecutableName != tc.exe || got.CompanionBinary != tc.companion {
			t.Fatalf("AgentDesktopAssetFor(%q, %q) = %+v, want asset=%q sha256=%q exe=%q companion=%q",
				tc.goos, tc.goarch, got, tc.asset, tc.sha256, tc.exe, tc.companion)
		}
	}
}

func TestDefaultAgentDesktopRootUsesQGConfigBase(t *testing.T) {
	cfgBase := canonicalPath(t, t.TempDir())
	setHermeticUserConfigEnv(t, cfgBase)

	got, err := DefaultAgentDesktopRoot()
	if err != nil {
		t.Fatalf("DefaultAgentDesktopRoot() error = %v", err)
	}
	want := filepath.Join(cfgBase, "qg", "tools", "agent-desktop")
	if got != want {
		t.Fatalf("DefaultAgentDesktopRoot() = %q, want %q", got, want)
	}
}

func TestEnsureAgentDesktopTLSServerExtractsExecutableAndCompanion(t *testing.T) {
	for _, target := range []struct {
		goos      string
		goarch    string
		exe       string
		companion string
	}{
		{"linux", "amd64", "agent-desktop", "agent-desktop-linux-clipboard-owner"},
		{"darwin", "arm64", "agent-desktop", "agent-desktop-macos-helper"},
		{"windows", "amd64", "agent-desktop.exe", ""},
	} {
		t.Run(target.goos+"-"+target.goarch, func(t *testing.T) {
			spec, ok := AgentDesktopAssetFor(target.goos, target.goarch)
			if !ok {
				t.Fatalf("missing spec for %s/%s", target.goos, target.goarch)
			}
			archiveBytes, archiveSHA := validFakeTarForTarget(t, target.goos, target.goarch)

			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/"+spec.AssetName {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(archiveBytes)
			}))
			t.Cleanup(srv.Close)

			root := canonicalPath(t, t.TempDir())
			binPath, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
				Root:           root,
				HTTPClient:     srv.Client(),
				BaseURL:        srv.URL,
				GOOS:           target.goos,
				GOARCH:         target.goarch,
				expectedSHA256: archiveSHA,
			})
			if err != nil {
				t.Fatalf("EnsureAgentDesktop: %v", err)
			}
			if !filepath.IsAbs(binPath) {
				t.Fatalf("binPath = %q, want absolute path", binPath)
			}
			if filepath.Base(binPath) != target.exe {
				t.Fatalf("filepath.Base(binPath) = %q, want %q", filepath.Base(binPath), target.exe)
			}
			info, err := os.Stat(binPath)
			if err != nil || !info.Mode().IsRegular() {
				t.Fatalf("Stat(%q) = %v, err = %v", binPath, info, err)
			}
			if runtime.GOOS != "windows" && target.goos != "windows" && info.Mode()&0o111 == 0 {
				t.Fatalf("executable %q missing exec permission: mode=%v", binPath, info.Mode())
			}
			if target.companion != "" {
				companionPath := filepath.Join(filepath.Dir(binPath), target.companion)
				cInfo, err := os.Stat(companionPath)
				if err != nil || !cInfo.Mode().IsRegular() {
					t.Fatalf("companion %q missing next to executable: err=%v", companionPath, err)
				}
				if runtime.GOOS != "windows" && cInfo.Mode()&0o111 == 0 {
					t.Fatalf("companion %q missing exec permission: mode=%v", companionPath, cInfo.Mode())
				}
			}
		})
	}
}

func TestEnsureAgentDesktopRejectsWrongSHA256BeforeExtract(t *testing.T) {
	spec, ok := AgentDesktopAssetFor("linux", "amd64")
	if !ok {
		t.Fatal("missing spec for linux/amd64")
	}
	// Include a traversal entry too: if SHA256 is verified BEFORE extraction, the
	// archive is rejected on checksum mismatch and tar extraction is never run.
	badBytes, _ := buildTarGzBytes(t, []fakeTarEntry{
		{name: "../should-never-extract", typeflag: tar.TypeReg, mode: 0o644, body: "nope"},
		{name: "agent-desktop", typeflag: tar.TypeReg, mode: 0o755, body: "tampered"},
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
	root := filepath.Join(parent, "install-root")
	// No expectedSHA256 override: exercises the pinned SHA256 from the table.
	_, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
		Root:       root,
		HTTPClient: srv.Client(),
		BaseURL:    srv.URL,
		GOOS:       "linux",
		GOARCH:     "amd64",
	})
	if err == nil {
		t.Fatal("expected SHA256 mismatch error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "sha256") {
		t.Fatalf("error = %q, want mention of sha256", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(parent, "should-never-extract")); !os.IsNotExist(statErr) {
		t.Fatalf("tampered archive must not be extracted: statErr=%v", statErr)
	}
	entries, readErr := os.ReadDir(root)
	if readErr == nil {
		for _, e := range entries {
			if e.IsDir() {
				t.Fatalf("unexpected directory left in root after hash failure: %s", e.Name())
			}
		}
	}
}

func TestEnsureAgentDesktopRejectsUnsafeTarEntries(t *testing.T) {
	spec, ok := AgentDesktopAssetFor("linux", "amd64")
	if !ok {
		t.Fatal("missing spec for linux/amd64")
	}

	cases := []struct {
		name    string
		entries []fakeTarEntry
	}{
		{
			name: "dotdot-escape",
			entries: []fakeTarEntry{
				{name: "../escaped-file", typeflag: tar.TypeReg, mode: 0o644, body: "bad"},
				{name: "agent-desktop", typeflag: tar.TypeReg, mode: 0o755, body: "bin"},
				{name: spec.CompanionBinary, typeflag: tar.TypeReg, mode: 0o755, body: "helper"},
			},
		},
		{
			name: "nested-dotdot-segment",
			entries: []fakeTarEntry{
				{name: "sub/../agent-desktop", typeflag: tar.TypeReg, mode: 0o755, body: "bin"},
				{name: spec.CompanionBinary, typeflag: tar.TypeReg, mode: 0o755, body: "helper"},
			},
		},
		{
			name: "unix-absolute-path",
			entries: []fakeTarEntry{
				{name: "/tmp/evil-agent-desktop", typeflag: tar.TypeReg, mode: 0o755, body: "bad"},
			},
		},
		{
			name: "windows-absolute-path",
			entries: []fakeTarEntry{
				{name: `C:\evil\agent-desktop.exe`, typeflag: tar.TypeReg, mode: 0o755, body: "bad"},
			},
		},
		{
			name: "escaping-symlink",
			entries: []fakeTarEntry{
				{name: "agent-desktop", typeflag: tar.TypeReg, mode: 0o755, body: "bin"},
				{name: spec.CompanionBinary, typeflag: tar.TypeReg, mode: 0o755, body: "helper"},
				{name: "out-link", typeflag: tar.TypeSymlink, linkname: "../../escaped-target"},
			},
		},
		{
			name: "absolute-symlink",
			entries: []fakeTarEntry{
				{name: "agent-desktop", typeflag: tar.TypeReg, mode: 0o755, body: "bin"},
				{name: spec.CompanionBinary, typeflag: tar.TypeReg, mode: 0o755, body: "helper"},
				{name: "abs-link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
			},
		},
		{
			name: "chained-symlink-escape",
			entries: []fakeTarEntry{
				{name: "d", typeflag: tar.TypeSymlink, linkname: "."},
				{name: "d/s", typeflag: tar.TypeSymlink, linkname: ".."},
				{name: "agent-desktop", typeflag: tar.TypeReg, mode: 0o755, body: "bin"},
			},
		},
		{
			name: "escaping-hardlink",
			entries: []fakeTarEntry{
				{name: "agent-desktop", typeflag: tar.TypeReg, mode: 0o755, body: "bin"},
				{name: "hard-escape", typeflag: tar.TypeLink, linkname: "../outside"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archiveBytes, archiveSHA := buildTarGzBytes(t, tc.entries)
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(archiveBytes)
			}))
			t.Cleanup(srv.Close)

			parent := t.TempDir()
			root := filepath.Join(parent, "root")
			_, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
				Root:           root,
				HTTPClient:     srv.Client(),
				BaseURL:        srv.URL,
				GOOS:           "linux",
				GOARCH:         "amd64",
				expectedSHA256: archiveSHA,
			})
			if err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			if _, statErr := os.Stat(filepath.Join(parent, "escaped-file")); !os.IsNotExist(statErr) {
				t.Fatalf("escaped-file written outside root: %v", statErr)
			}
			entries, err := os.ReadDir(root)
			if err == nil {
				for _, e := range entries {
					if e.IsDir() {
						t.Fatalf("leftover directory %q in root after rejected tar", e.Name())
					}
				}
			}
		})
	}
}

func TestEnsureAgentDesktopSecondCallReusesWithoutNetwork(t *testing.T) {
	spec, ok := AgentDesktopAssetFor("linux", "amd64")
	if !ok {
		t.Fatal("missing spec for linux/amd64")
	}
	archiveBytes, archiveSHA := validFakeTarForTarget(t, "linux", "amd64")

	var downloads atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+spec.AssetName {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(archiveBytes)
	}))
	client := srv.Client()
	baseURL := srv.URL

	root := canonicalPath(t, t.TempDir())
	opts := AgentDesktopInstallOptions{
		Root:           root,
		HTTPClient:     client,
		BaseURL:        baseURL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		expectedSHA256: archiveSHA,
	}

	first, err := EnsureAgentDesktop(context.Background(), opts)
	if err != nil {
		t.Fatalf("first EnsureAgentDesktop: %v", err)
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("downloads after first call = %d, want 1", got)
	}

	// Shut down the server completely so any network attempt on the second call fails.
	srv.Close()

	second, err := EnsureAgentDesktop(context.Background(), opts)
	if err != nil {
		t.Fatalf("second EnsureAgentDesktop (offline reuse): %v", err)
	}
	if second != first {
		t.Fatalf("second path = %q, want %q", second, first)
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("downloads after second call = %d, want 1", got)
	}
}

func TestEnsureAgentDesktopRepairsMissingCompanionBinary(t *testing.T) {
	spec, ok := AgentDesktopAssetFor("linux", "amd64")
	if !ok {
		t.Fatal("missing spec for linux/amd64")
	}
	archiveBytes, archiveSHA := validFakeTarForTarget(t, "linux", "amd64")

	var downloads atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+spec.AssetName {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = w.Write(archiveBytes)
	}))
	t.Cleanup(srv.Close)

	root := canonicalPath(t, t.TempDir())
	opts := AgentDesktopInstallOptions{
		Root:           root,
		HTTPClient:     srv.Client(),
		BaseURL:        srv.URL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		expectedSHA256: archiveSHA,
	}

	binPath, err := EnsureAgentDesktop(context.Background(), opts)
	if err != nil {
		t.Fatalf("initial EnsureAgentDesktop: %v", err)
	}
	companionPath := filepath.Join(filepath.Dir(binPath), spec.CompanionBinary)
	if err := os.Remove(companionPath); err != nil {
		t.Fatalf("Remove(%q): %v", companionPath, err)
	}

	repairedPath, err := EnsureAgentDesktop(context.Background(), opts)
	if err != nil {
		t.Fatalf("repair EnsureAgentDesktop: %v", err)
	}
	if repairedPath != binPath {
		t.Fatalf("repairedPath = %q, want %q", repairedPath, binPath)
	}
	if got := downloads.Load(); got != 2 {
		t.Fatalf("downloads after repair = %d, want 2", got)
	}
	if _, err := os.Stat(companionPath); err != nil {
		t.Fatalf("companion binary not restored at %q: %v", companionPath, err)
	}
}

func TestEnsureAgentDesktopSubprocessWorker(t *testing.T) {
	root := os.Getenv("QG_TEST_AGENT_DESKTOP_SUBPROC_ROOT")
	if root == "" {
		return
	}
	baseURL := os.Getenv("QG_TEST_AGENT_DESKTOP_SUBPROC_URL")
	expectedSHA := os.Getenv("QG_TEST_AGENT_DESKTOP_SUBPROC_SHA")
	got, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
		Root:           root,
		BaseURL:        baseURL,
		GOOS:           "linux",
		GOARCH:         "amd64",
		expectedSHA256: expectedSHA,
	})
	if err != nil {
		t.Fatalf("subprocess EnsureAgentDesktop: %v", err)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("subprocess Stat(%q): %v", got, err)
	}
}

func TestEnsureAgentDesktopConcurrentProcessesPerformSingleDownload(t *testing.T) {
	spec, ok := AgentDesktopAssetFor("linux", "amd64")
	if !ok {
		t.Fatal("missing spec for linux/amd64")
	}
	archiveBytes, archiveSHA := validFakeTarForTarget(t, "linux", "amd64")

	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+spec.AssetName {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		time.Sleep(120 * time.Millisecond)
		_, _ = w.Write(archiveBytes)
	}))
	t.Cleanup(srv.Close)

	root := canonicalPath(t, t.TempDir())
	const procs = 4
	var wg sync.WaitGroup
	errs := make([]error, procs)

	for i := 0; i < procs; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestEnsureAgentDesktopSubprocessWorker$")
			cmd.Env = append(os.Environ(),
				"QG_TEST_AGENT_DESKTOP_SUBPROC_ROOT="+root,
				"QG_TEST_AGENT_DESKTOP_SUBPROC_URL="+srv.URL,
				"QG_TEST_AGENT_DESKTOP_SUBPROC_SHA="+archiveSHA,
			)
			out, err := cmd.CombinedOutput()
			if err != nil {
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
		t.Fatalf("cross-process downloads = %d, want 1", got)
	}
}

func TestEnsureAgentDesktopConcurrentCallsPerformSingleDownload(t *testing.T) {
	spec, ok := AgentDesktopAssetFor("linux", "amd64")
	if !ok {
		t.Fatal("missing spec for linux/amd64")
	}
	archiveBytes, archiveSHA := validFakeTarForTarget(t, "linux", "amd64")

	var downloads atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+spec.AssetName {
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		// Hold the download briefly so all concurrent callers contend on the lock.
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

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			paths[idx], errs[idx] = EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
				Root:           root,
				HTTPClient:     srv.Client(),
				BaseURL:        srv.URL,
				GOOS:           "linux",
				GOARCH:         "amd64",
				expectedSHA256: archiveSHA,
			})
		}(i)
	}
	close(start)
	wg.Wait()

	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Fatalf("worker %d error: %v", i, errs[i])
		}
		if paths[i] != paths[0] {
			t.Fatalf("worker %d path = %q, want %q", i, paths[i], paths[0])
		}
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("concurrent downloads = %d, want 1", got)
	}
}

func TestEnsureAgentDesktopUnsupportedPlatformPortugueseError(t *testing.T) {
	for _, tc := range []struct {
		goos   string
		goarch string
	}{
		{"plan9", "amd64"},
		{"linux", "386"},
		{"freebsd", "arm64"},
	} {
		t.Run(tc.goos+"-"+tc.goarch, func(t *testing.T) {
			_, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
				Root:   t.TempDir(),
				GOOS:   tc.goos,
				GOARCH: tc.goarch,
			})
			wantSub := "o controle do computador não tem suporte para " + tc.goos + "/" + tc.goarch
			if err == nil || !strings.Contains(err.Error(), wantSub) {
				t.Fatalf("error = %v, want substring %q", err, wantSub)
			}
		})
	}
}

func TestEnsureAgentDesktopURLSecurityAndLimits(t *testing.T) {
	spec, ok := AgentDesktopAssetFor("linux", "amd64")
	if !ok {
		t.Fatal("missing spec for linux/amd64")
	}
	archiveBytes, archiveSHA := validFakeTarForTarget(t, "linux", "amd64")

	t.Run("rejects-plain-http-non-loopback", func(t *testing.T) {
		for _, badURL := range []string{
			"http://example.com/releases",
			"http://127.0.0.1.example.com/releases",
			"http://localhost.example.com/releases",
			"ftp://127.0.0.1/releases",
		} {
			_, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
				Root:           t.TempDir(),
				BaseURL:        badURL,
				GOOS:           "linux",
				GOARCH:         "amd64",
				expectedSHA256: archiveSHA,
			})
			if err == nil {
				t.Fatalf("expected URL %q to be refused", badURL)
			}
		}
	})

	t.Run("follows-https-to-https-redirect", func(t *testing.T) {
		var tlsSrv *httptest.Server
		tlsSrv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/" + spec.AssetName:
				http.Redirect(w, r, tlsSrv.URL+"/cdn/"+spec.AssetName, http.StatusFound)
			case "/cdn/" + spec.AssetName:
				_, _ = w.Write(archiveBytes)
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(tlsSrv.Close)

		binPath, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
			Root:           t.TempDir(),
			HTTPClient:     tlsSrv.Client(),
			BaseURL:        tlsSrv.URL,
			GOOS:           "linux",
			GOARCH:         "amd64",
			expectedSHA256: archiveSHA,
		})
		if err != nil {
			t.Fatalf("HTTPS-to-HTTPS redirect should succeed: %v", err)
		}
		if _, err := os.Stat(binPath); err != nil {
			t.Fatalf("Stat(%q): %v", binPath, err)
		}
	})

	t.Run("allows-loopback-http-test-server", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/"+spec.AssetName {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(archiveBytes)
		}))
		t.Cleanup(srv.Close)

		binPath, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
			Root:           t.TempDir(),
			HTTPClient:     srv.Client(),
			BaseURL:        srv.URL,
			GOOS:           "linux",
			GOARCH:         "amd64",
			expectedSHA256: archiveSHA,
		})
		if err != nil {
			t.Fatalf("loopback HTTP server should be accepted in tests: %v", err)
		}
		if _, statErr := os.Stat(binPath); statErr != nil {
			t.Fatalf("Stat(%q): %v", binPath, statErr)
		}
	})

	t.Run("rejects-https-to-http-downgrade-redirect", func(t *testing.T) {
		httpSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(archiveBytes)
		}))
		t.Cleanup(httpSrv.Close)

		tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, httpSrv.URL+"/"+spec.AssetName, http.StatusFound)
		}))
		t.Cleanup(tlsSrv.Close)

		_, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
			Root:           t.TempDir(),
			HTTPClient:     tlsSrv.Client(),
			BaseURL:        tlsSrv.URL,
			GOOS:           "linux",
			GOARCH:         "amd64",
			expectedSHA256: archiveSHA,
		})
		if err == nil {
			t.Fatal("expected https -> http redirect downgrade to be refused")
		}
	})

	t.Run("rejects-oversized-download", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(archiveBytes)
		}))
		t.Cleanup(srv.Close)

		_, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
			Root:           t.TempDir(),
			HTTPClient:     srv.Client(),
			BaseURL:        srv.URL,
			GOOS:           "linux",
			GOARCH:         "amd64",
			expectedSHA256: archiveSHA,
			maxBytes:       int64(len(archiveBytes)) - 1,
		})
		if err == nil {
			t.Fatal("expected oversized download to be refused")
		}
	})

	t.Run("enforces-timeout", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}))
		t.Cleanup(srv.Close)

		_, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
			Root:           t.TempDir(),
			HTTPClient:     srv.Client(),
			BaseURL:        srv.URL,
			GOOS:           "linux",
			GOARCH:         "amd64",
			expectedSHA256: archiveSHA,
			timeout:        50 * time.Millisecond,
		})
		if err == nil {
			t.Fatal("expected timeout error, got nil")
		}
	})
}

// TestEnsureAgentDesktopRealDownload exercises the real GitHub release download
// on demand (never during standard offline/CI `go test`).
func TestEnsureAgentDesktopRealDownload(t *testing.T) {
	if os.Getenv("QG_AGENT_DESKTOP_REAL_TEST") == "" {
		t.Skip("defina QG_AGENT_DESKTOP_REAL_TEST=1 para rodar o teste real de download do agent-desktop")
	}
	root := strings.TrimSpace(os.Getenv("QG_AGENT_DESKTOP_REAL_ROOT"))
	if root == "" {
		root = canonicalPath(t, t.TempDir())
	}
	binPath, err := EnsureAgentDesktop(context.Background(), AgentDesktopInstallOptions{
		Root: root,
	})
	if err != nil {
		t.Fatalf("EnsureAgentDesktop real download: %v", err)
	}
	info, err := os.Stat(binPath)
	if err != nil || info.Mode()&fs.ModeType != 0 {
		t.Fatalf("Stat(%q): info=%v err=%v", binPath, info, err)
	}
	spec, ok := AgentDesktopAssetFor(runtime.GOOS, runtime.GOARCH)
	if !ok {
		t.Fatalf("no spec for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if spec.CompanionBinary != "" {
		companion := filepath.Join(filepath.Dir(binPath), spec.CompanionBinary)
		if _, err := os.Stat(companion); err != nil {
			t.Fatalf("missing companion binary %q: %v", companion, err)
		}
	}

	verOut, err := exec.Command(binPath, "version").CombinedOutput()
	if err != nil {
		t.Fatalf("%s version failed: %v\n%s", binPath, err, string(verOut))
	}
	t.Logf("version output:\n%s", strings.TrimSpace(string(verOut)))

	helpOut, err := exec.Command(binPath, "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("%s --help failed: %v\n%s", binPath, err, string(helpOut))
	}
	lines := strings.Split(string(helpOut), "\n")
	if len(lines) > 10 {
		lines = lines[:10]
	}
	t.Logf("--help | head output:\n%s", strings.Join(lines, "\n"))
}
