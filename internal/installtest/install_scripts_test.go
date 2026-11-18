package installtest

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnixInstallerScriptMatchesReleaseContracts(t *testing.T) {
	script := readRepoText(t, "scripts/install.sh")

	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Fatalf("install.sh shebang = %q, want bash", strings.SplitN(script, "\n", 2)[0])
	}
	containsAll(t, script, []string{
		"set -euo pipefail",
		`QG_REPO="${QG_REPO:-quebragalho-dev/quebragalho-cli}"`,
		`QG_INSTALL_DIR="${QG_INSTALL_DIR:-/usr/local/bin}"`,
		`archive_name="qg-v${version}-${platform}-${arch}.tar.gz"`,
		`checksum_name="${archive_name}.sha256"`,
		"curl --fail --location --show-error --silent --header 'Accept: application/vnd.github+json'",
		`verify_checksum "$checksum_name"`,
		`tar -xzf "$archive_path" -C "$extract_dir"`,
		`find_extracted_binary "$extract_dir"`,
		`install_file "$binary_path" "$QG_INSTALL_DIR/qg"`,
		`copy_optional_file "qg-linux-sandbox"`,
		`copy_optional_file "qg-seccomp"`,
		`copy_optional_dir "helpers"`,
		"  QG_REPO             Repositório no GitHub. Padrão: quebragalho-dev/quebragalho-cli.",
	})
}

func TestPowerShellInstallerScriptMatchesWindowsReleaseContracts(t *testing.T) {
	script := readRepoText(t, "scripts/install.ps1")

	containsAll(t, script, []string{
		`[string]$Repository = $(if ($env:QG_REPO)`,
		`Join-Path $env:LOCALAPPDATA "qg\bin"`,
		`$archiveName = "qg-v$releaseVersion-windows-$arch.zip"`,
		`$checksumName = "$archiveName.sha256"`,
		`Get-FileHash -Path $archivePath -Algorithm SHA256`,
		`Expand-Archive -Path $archivePath -DestinationPath $extractDir -Force`,
		`Find-QGExtractedFile -Root $extractDir -FileName $fileName`,
		`"qg-windows-command-runner.exe"`,
		`"qg-windows-sandbox-setup.exe"`,
		`$destPath = Join-Path $InstallDir $fileName`,
		`Rename-Item -Path $destPath -NewName $trashName -Force`,
		`.qg-install-`,
		`.trash`,
		`Copy-Item -Path $sourcePath -Destination $destPath -Force`,
		`a versao anterior foi mantida`,
		`Find-QGOptionalExtractedDirectory -Root $extractDir -DirectoryName "helpers"`,
		`Copy-Item -Path $helpersPath -Destination $targetHelpersPath -Recurse -Force`,
	})
	// The updater treats <base>.*.old as unresolved recovery state and blocks
	// promotion while such a file exists (internal/update/stage_windows.go), so
	// the installer must never sideline executables under a .old name.
	if strings.Contains(script, ".old") {
		t.Fatalf("install.ps1 must not use .old aside names; use .qg-install-<guid>.trash")
	}
}

func TestUnixInstallerInstallsFromPrefixedReleaseArchiveWithoutNetwork(t *testing.T) {
	fixture := newUnixInstallFixture(t)
	stdout, stderr, err := runUnixInstaller(t, fixture)
	if err != nil {
		t.Fatalf("install.sh failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	previous := -1
	for _, stage := range []string{"Resolvendo versão", "Baixando", "Verificando SHA-256", "Instalando"} {
		at := strings.Index(stderr, stage)
		if at <= previous {
			t.Fatalf("install.sh progress missing or out of order at %q: %s", stage, stderr)
		}
		previous = at
	}
	if !strings.Contains(stdout, "Instalado "+filepath.Join(fixture.installDir, "qg")) {
		t.Fatalf("install.sh stdout missing install path:\n%s", stdout)
	}

	installed := readFile(t, filepath.Join(fixture.installDir, "qg"))
	if !strings.Contains(string(installed), "mock-qg") {
		t.Fatalf("installed binary = %q, want mock-qg script", string(installed))
	}
	if _, err := os.Stat(filepath.Join(fixture.installDir, "helpers", "node_modules", ".bin", "agent-browser")); err != nil {
		t.Fatalf("installed helper package missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.installDir, "qg-linux-sandbox")); err != nil {
		t.Fatalf("installed linux sandbox helper missing: %v", err)
	}

	// Reinstalling over a live install must swap the binary atomically
	// (rename, never cp over): the installed file must be a new inode and
	// no .qg.* temp file may be left behind.
	before, err := os.Stat(filepath.Join(fixture.installDir, "qg"))
	if err != nil {
		t.Fatalf("stat installed qg before reinstall: %v", err)
	}
	if _, _, err := runUnixInstaller(t, fixture); err != nil {
		t.Fatalf("install.sh reinstall failed: %v", err)
	}
	after, err := os.Stat(filepath.Join(fixture.installDir, "qg"))
	if err != nil {
		t.Fatalf("stat installed qg after reinstall: %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatalf("reinstall kept the same installed file, want atomic rename")
	}
	assertNoInstallTempFiles(t, fixture.installDir)

	// A failing copy must fail the install, keep the previous binary, clean
	// the temp file, and say the previous version was kept.
	failBin := t.TempDir()
	writeFile(t, filepath.Join(failBin, "cp"), []byte("#!/usr/bin/env sh\nexit 1\n"), 0o755)
	_, failStderr, err := runUnixInstallerWithPath(t, fixture, failBin)
	if err == nil {
		t.Fatalf("install.sh succeeded with a failing cp")
	}
	kept, err := os.Stat(filepath.Join(fixture.installDir, "qg"))
	if err != nil {
		t.Fatalf("stat installed qg after failed copy: %v", err)
	}
	if !os.SameFile(after, kept) {
		t.Fatalf("failed copy replaced the installed binary, want the previous version kept")
	}
	assertNoInstallTempFiles(t, fixture.installDir)
	if !strings.Contains(failStderr, "a versão anterior foi mantida") {
		t.Fatalf("failed copy stderr missing kept-version message:\n%s", failStderr)
	}
}

func assertNoInstallTempFiles(t *testing.T, installDir string) {
	t.Helper()
	entries, err := os.ReadDir(installDir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", installDir, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".qg.") {
			t.Fatalf("temporary file left in %s: %s", installDir, entry.Name())
		}
	}
}

func TestUnixInstallerRejectsChecksumMismatchWithoutNetwork(t *testing.T) {
	fixture := newUnixInstallFixture(t)
	writeFile(t, fixture.checksumPath, []byte(strings.Repeat("0", 64)+"  "+fixture.archiveName+"\n"), 0o644)

	stdout, stderr, err := runUnixInstaller(t, fixture)
	if err == nil {
		t.Fatalf("install.sh succeeded with a bad checksum\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	output := stdout + "\n" + stderr
	if !strings.Contains(output, fixture.archiveName) {
		t.Fatalf("checksum failure output missing archive name:\n%s", output)
	}
	if !strings.Contains(output, "FAILED") || !strings.Contains(strings.ToLower(output), "checksum") {
		t.Fatalf("checksum failure output missing checksum mismatch detail:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(fixture.installDir, "qg")); !os.IsNotExist(err) {
		t.Fatalf("installed binary exists after checksum failure: %v", err)
	}
}

type unixInstallFixture struct {
	bash         string
	mockBin      string
	installDir   string
	archiveName  string
	archivePath  string
	checksumPath string
	bundleName   string
	bundlePath   string
}

func newUnixInstallFixture(t *testing.T) unixInstallFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer smoke does not run on Windows")
	}
	bash := requireCommand(t, "bash")
	tar := requireCommand(t, "tar")

	releasePlatform := unixReleasePlatform(t)
	releaseArch := unixReleaseArch(t)
	packageName := fmt.Sprintf("qg-v0.1.0-%s-%s", releasePlatform, releaseArch)
	archiveName := packageName + ".tar.gz"
	checksumName := archiveName + ".sha256"
	bundleName := archiveName + ".sigstore.json"
	root := t.TempDir()
	fixture := unixInstallFixture{
		bash:         bash,
		mockBin:      filepath.Join(root, "bin"),
		installDir:   filepath.Join(root, "install"),
		archiveName:  archiveName,
		archivePath:  filepath.Join(root, "release", archiveName),
		checksumPath: filepath.Join(root, "release", checksumName),
		bundleName:   bundleName,
		bundlePath:   filepath.Join(root, "release", bundleName),
	}
	packageDir := filepath.Join(root, "package", packageName)

	mustMkdirAll(t, fixture.mockBin)
	mustMkdirAll(t, packageDir)
	mustMkdirAll(t, filepath.Dir(fixture.archivePath))
	mustMkdirAll(t, fixture.installDir)
	mustMkdirAll(t, filepath.Join(packageDir, "helpers", "node_modules", ".bin"))
	writeFile(t, filepath.Join(packageDir, "qg"), []byte("#!/usr/bin/env sh\necho mock-qg\n"), 0o755)
	writeFile(t, filepath.Join(packageDir, "qg-linux-sandbox"), []byte("#!/usr/bin/env sh\n"), 0o755)
	writeFile(t, filepath.Join(packageDir, "qg-seccomp"), []byte("#!/usr/bin/env sh\n"), 0o755)
	mustMkdirAll(t, filepath.Join(packageDir, "helpers", "node_modules", "agent-browser", "bin"))
	writeFile(t, filepath.Join(packageDir, "helpers", "node_modules", "agent-browser", "bin", "agent-browser.js"), []byte("#!/usr/bin/env node\n"), 0o755)
	if err := os.Symlink("../agent-browser/bin/agent-browser.js", filepath.Join(packageDir, "helpers", "node_modules", ".bin", "agent-browser")); err != nil {
		t.Fatalf("Symlink agent-browser helper: %v", err)
	}
	writeFile(t, filepath.Join(packageDir, "helpers", "node_modules", ".bin", "tuistory"), []byte("#!/usr/bin/env sh\n"), 0o755)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tarCommand := exec.CommandContext(ctx, tar, "-C", filepath.Join(root, "package"), "-czf", fixture.archivePath, packageName)
	if output, err := tarCommand.CombinedOutput(); err != nil {
		t.Fatalf("tar failed: %v\n%s", err, output)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("tar timed out: %v", err)
	}

	archiveBytes := readFile(t, fixture.archivePath)
	sum := sha256.Sum256(archiveBytes)
	writeFile(t, fixture.checksumPath, []byte(fmt.Sprintf("%x  %s\n", sum, archiveName)), 0o644)
	writeFile(t, fixture.bundlePath, []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle+json"}`+"\n"), 0o644)

	mockCurl := fmt.Sprintf(`#!/usr/bin/env sh
set -eu
output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output)
      output="$2"
      shift 2
      ;;
    --output=*)
      output="${1#--output=}"
      shift
      ;;
    --header)
      shift 2
      ;;
    --header=*)
      shift
      ;;
    --fail|--location|--show-error|--silent)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done

case "$url" in
  */%s)
    /bin/cp %s "$output"
    ;;
  */%s)
    /bin/cp %s "$output"
    ;;
  */%s)
    /bin/cp %s "$output"
    ;;
  *)
    echo "unexpected url: $url" >&2
    exit 2
    ;;
esac
`, archiveName, shellQuote(fixture.archivePath), checksumName, shellQuote(fixture.checksumPath), bundleName, shellQuote(fixture.bundlePath))
	writeFile(t, filepath.Join(fixture.mockBin, "curl"), []byte(mockCurl), 0o755)

	return fixture
}

func runUnixInstaller(t *testing.T, fixture unixInstallFixture) (string, string, error) {
	t.Helper()
	return runUnixInstallerWithPath(t, fixture, "")
}

func runUnixInstallerWithPath(t *testing.T, fixture unixInstallFixture, pathPrefix string) (string, string, error) {
	t.Helper()
	return runUnixInstallerWithEnv(t, fixture, pathPrefix, "0.1.0")
}

// runUnixInstallerWithEnv runs install.sh for version with extraEnv appended
// after the defaults, so an entry there overrides the default of the same name.
func runUnixInstallerWithEnv(t *testing.T, fixture unixInstallFixture, pathPrefix string, version string, extraEnv ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, fixture.bash, filepath.Join(repoRoot(t), "scripts/install.sh"), "--version", version, "--install-dir", fixture.installDir)
	command.Dir = repoRoot(t)
	path := fixture.mockBin + string(os.PathListSeparator) + os.Getenv("PATH")
	if pathPrefix != "" {
		path = pathPrefix + string(os.PathListSeparator) + path
	}
	command.Env = append(os.Environ(),
		"PATH="+path,
		"QG_GITHUB_BASE_URL=https://example.test",
		"QG_REPO=quebragalho-dev/quebragalho-cli",
	)
	command.Env = append(command.Env, extraEnv...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	runErr := command.Run()
	if err := ctx.Err(); err != nil {
		t.Fatalf("install.sh timed out: %v", err)
	}
	return stdout.String(), stderr.String(), runErr
}

func unixReleasePlatform(t *testing.T) string {
	t.Helper()
	switch runtime.GOOS {
	case "linux":
		return "linux"
	case "darwin":
		return "macos"
	default:
		t.Skipf("Unix installer smoke does not support GOOS=%s", runtime.GOOS)
		return ""
	}
}

func unixReleaseArch(t *testing.T) string {
	t.Helper()
	switch runtime.GOARCH {
	case "amd64", "x86_64":
		return "x64"
	case "arm64":
		return "arm64"
	default:
		t.Skipf("Unix installer smoke does not support GOARCH=%s", runtime.GOARCH)
		return ""
	}
}

func containsAll(t *testing.T, value string, wants []string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(value, want) {
			t.Fatalf("expected content to contain %q", want)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readRepoText(t *testing.T, relativePath string) string {
	t.Helper()
	return string(readFile(t, filepath.Join(repoRoot(t), relativePath)))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return data
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
	if mode&0o111 != 0 {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatalf("Chmod %s: %v", path, err)
		}
	}
}

func requireCommand(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not available", name)
	}
	return path
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

// loggingCurl puts a curl ahead of the fixture's mock that records every
// invocation's arguments, one line per call, and then runs the mock.
func loggingCurl(t *testing.T, fixture unixInstallFixture) (pathPrefix string, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "curl.log")
	script := "#!/usr/bin/env sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\nexec " + shellQuote(filepath.Join(fixture.mockBin, "curl")) + " \"$@\"\n"
	writeFile(t, filepath.Join(dir, "curl"), []byte(script), 0o755)
	return dir, logPath
}

// TestUnixInstallerRefusesAPlainHTTPDownloadBase pins that an http download base is refused before curl runs.
func TestUnixInstallerRefusesAPlainHTTPDownloadBase(t *testing.T) {
	fixture := newUnixInstallFixture(t)
	pathPrefix, logPath := loggingCurl(t, fixture)

	stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, pathPrefix, "0.1.0", "QG_GITHUB_BASE_URL=http://example.test")
	if err == nil {
		t.Fatalf("install.sh installed from a plain http base\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "https://") || !strings.Contains(stderr, "QG_ALLOW_INSECURE_DOWNLOAD=1") {
		t.Fatalf("the refusal must name https and the opt-in, got:\n%s", stderr)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Fatalf("curl ran for a plain http base: %s", readFile(t, logPath))
	}
	if _, statErr := os.Stat(filepath.Join(fixture.installDir, "qg")); !os.IsNotExist(statErr) {
		t.Fatalf("a binary was installed from a plain http base: %v", statErr)
	}
}

// TestUnixInstallerRefusesAPlainHTTPAPIBase pins that the latest-release lookup refuses an http API base.
func TestUnixInstallerRefusesAPlainHTTPAPIBase(t *testing.T) {
	fixture := newUnixInstallFixture(t)
	pathPrefix, logPath := loggingCurl(t, fixture)

	_, stderr, err := runUnixInstallerWithEnv(t, fixture, pathPrefix, "latest", "QG_GITHUB_API=http://api.example.test")
	if err == nil {
		t.Fatal("install.sh resolved the latest release over plain http")
	}
	if !strings.Contains(stderr, "QG_ALLOW_INSECURE_DOWNLOAD=1") {
		t.Fatalf("the refusal must name the opt-in, got:\n%s", stderr)
	}
	if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
		t.Fatalf("curl ran for a plain http API base: %s", readFile(t, logPath))
	}
}

// TestUnixInstallerOptInIsExactlyOneAndLoopbackIsNotExempt pins the two edges
// of the rule: only the literal value 1 opens the opt-in, and a loopback base
// gets no exemption without it.
func TestUnixInstallerOptInIsExactlyOneAndLoopbackIsNotExempt(t *testing.T) {
	cases := map[string][]string{
		"opt-in set to 0":      {"QG_GITHUB_BASE_URL=http://example.test", "QG_ALLOW_INSECURE_DOWNLOAD=0"},
		"opt-in set to true":   {"QG_GITHUB_BASE_URL=http://example.test", "QG_ALLOW_INSECURE_DOWNLOAD=true"},
		"opt-in set to empty":  {"QG_GITHUB_BASE_URL=http://example.test", "QG_ALLOW_INSECURE_DOWNLOAD="},
		"localhost base":       {"QG_GITHUB_BASE_URL=http://localhost:8080"},
		"loopback address":     {"QG_GITHUB_BASE_URL=http://127.0.0.1:8080"},
		"scheme-less base":     {"QG_GITHUB_BASE_URL=example.test"},
		"https only as a path": {"QG_GITHUB_BASE_URL=http://example.test/https://"},
	}
	for name, extraEnv := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newUnixInstallFixture(t)
			pathPrefix, logPath := loggingCurl(t, fixture)

			stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, pathPrefix, "0.1.0", extraEnv...)
			if err == nil {
				t.Fatalf("install.sh installed from an insecure base\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			if !strings.Contains(stderr, "QG_ALLOW_INSECURE_DOWNLOAD=1") {
				t.Fatalf("the refusal must name the opt-in, got:\n%s", stderr)
			}
			if _, statErr := os.Stat(logPath); !os.IsNotExist(statErr) {
				t.Fatalf("curl ran for an insecure base: %s", readFile(t, logPath))
			}
		})
	}
}

// TestUnixInstallerAllowsPlainHTTPOnlyWithTheExplicitOptIn pins that the insecure opt-in restores local http mirrors.
func TestUnixInstallerAllowsPlainHTTPOnlyWithTheExplicitOptIn(t *testing.T) {
	fixture := newUnixInstallFixture(t)
	pathPrefix, logPath := loggingCurl(t, fixture)

	stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, pathPrefix, "0.1.0", "QG_GITHUB_BASE_URL=http://example.test", "QG_ALLOW_INSECURE_DOWNLOAD=1")
	if err != nil {
		t.Fatalf("install.sh refused the explicit opt-in: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.Contains(string(readFile(t, logPath)), "--proto") {
		t.Fatalf("the opt-in must not pin curl to https:\n%s", readFile(t, logPath))
	}
}

// TestUnixInstallerPinsCurlToHTTPSAcrossRedirects pins that curl stays on https even across redirects.
func TestUnixInstallerPinsCurlToHTTPSAcrossRedirects(t *testing.T) {
	fixture := newUnixInstallFixture(t)
	pathPrefix, logPath := loggingCurl(t, fixture)

	stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, pathPrefix, "0.1.0")
	if err != nil {
		t.Fatalf("install.sh failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	calls := strings.Split(strings.TrimSpace(string(readFile(t, logPath))), "\n")
	if len(calls) != 2 {
		t.Fatalf("expected the archive and checksum downloads, got %d curl calls:\n%s", len(calls), strings.Join(calls, "\n"))
	}
	for _, call := range calls {
		if !strings.Contains(call, "--proto =https") || !strings.Contains(call, "--proto-redir =https") {
			t.Fatalf("curl was not pinned to https for the request and its redirects: %s", call)
		}
	}
}

// TestPowerShellInstallerRefusesPlainHTTPBases pins that install.ps1 guards both URL bases with the same rule.
func TestPowerShellInstallerRefusesPlainHTTPBases(t *testing.T) {
	script := readRepoText(t, "scripts/install.ps1")

	containsAll(t, script, []string{
		`function Assert-QGSecureUrl {`,
		`$env:QG_ALLOW_INSECURE_DOWNLOAD -eq "1"`,
		`$Url.StartsWith("https://", [System.StringComparison]::OrdinalIgnoreCase)`,
		`Assert-QGSecureUrl -Url $latestUrl`,
		`Assert-QGSecureUrl -Url $releaseUrl`,
	})
}

// TestPowerShellInstallerBindsTheChecksumToTheArchive pins that the checksum line must name the downloaded archive.
func TestPowerShellInstallerBindsTheChecksumToTheArchive(t *testing.T) {
	script := readRepoText(t, "scripts/install.ps1")

	containsAll(t, script, []string{
		`'^([0-9a-fA-F]{64})(?:\s+\*?(.+))?$'`,
		`-cne $archiveName`,
	})
	if strings.Contains(script, `($checksumLine -split "\s+")[0]`) {
		t.Fatal("install.ps1 still takes the first token of the checksum file whatever it is")
	}
	// An empty checksum file yields nothing from Get-Content, and a cast keeps
	// it null: only the quoted form gives the parser a string to refuse.
	if !strings.Contains(script, `$checksumLine = "$(Get-Content -Path $checksumPath -TotalCount 1)"`) {
		t.Fatal("install.ps1 must read the checksum line as text even when the file is empty")
	}
}

// TestPowerShellInstallerStaysASCII pins the BOM-less ANSI readability of install.ps1 on Windows PowerShell 5.1.
func TestPowerShellInstallerStaysASCII(t *testing.T) {
	for index, b := range readFile(t, filepath.Join(repoRoot(t), "scripts/install.ps1")) {
		if b > 0x7e || (b < 0x20 && b != '\n' && b != '\r' && b != '\t') {
			t.Fatalf("install.ps1 has a non-ASCII byte 0x%02x at offset %d: Windows PowerShell 5.1 reads the file as ANSI", b, index)
		}
	}
}

// wgetOnlyPath builds a PATH directory containing essential POSIX utilities and
// wgetPath as "wget", with curl omitted so scripts/install.sh takes its wget
// fallback branch.
func wgetOnlyPath(t *testing.T, wgetPath string) string {
	t.Helper()
	dir := t.TempDir()
	for _, cmd := range []string{
		"uname", "sed", "tar", "gzip", "mktemp", "head", "grep", "openssl",
		"shasum", "sha256sum",
		"mkdir", "rm", "cp", "chmod", "mv", "ln", "cat", "env", "sh", "bash",
	} {
		target, err := exec.LookPath(cmd)
		if err != nil {
			continue
		}
		if err := os.Symlink(target, filepath.Join(dir, cmd)); err != nil {
			t.Fatalf("symlink %s: %v", cmd, err)
		}
	}
	if err := os.Symlink(wgetPath, filepath.Join(dir, "wget")); err != nil {
		t.Fatalf("symlink wget: %v", err)
	}
	return dir
}

// writeMockRedirectWget writes a sh stand-in for GNU wget that reproduces
// GNU wget's redirect behavior: without --max-redirect=0 it follows redirects
// (even from https:// to http://, including when --https-only is passed); with
// --max-redirect=0 it stops at the 3xx hop, prints indented --server-response
// headers to stderr, and exits 8.
func writeMockRedirectWget(t *testing.T, fixture unixInstallFixture, mode string) (wgetBin string, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "wget.log")
	wgetBin = filepath.Join(dir, "mock-wget")
	checksumName := fixture.archiveName + ".sha256"
	latestJSON := filepath.Join(dir, "latest.json")
	writeFile(t, latestJSON, []byte(`{"tag_name":"v0.1.0"}`+"\n"), 0o644)

	script := fmt.Sprintf(`#!/usr/bin/env sh
set -eu
if [ "${1:-}" = "--help" ]; then
  printf 'GNU Wget mock\n  --max-redirect\n  --https-only\n'
  exit 0
fi
printf '%%s\n' "$*" >> %s

max_redirect=-1
server_response=0
output=""
url=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --max-redirect=*)
      max_redirect="${1#--max-redirect=}"
      shift
      ;;
    --max-redirect)
      max_redirect="$2"
      shift 2
      ;;
    --server-response|-S)
      server_response=1
      shift
      ;;
    --output-document|-O)
      output="$2"
      shift 2
      ;;
    --output-document=*)
      output="${1#--output-document=}"
      shift
      ;;
    --header)
      shift 2
      ;;
    --header=*|--quiet|-q|--no-verbose|-nv|--https-only)
      shift
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done

emit_redirect() {
  code="$1"
  loc="$2"
  [ -n "$output" ] && : > "$output"
  if [ "$server_response" = "1" ]; then
    printf '  HTTP/1.1 %%s Redirect\r\n  Location: %%s\r\n' "$code" "$loc" >&2
  fi
  exit 8
}

serve_file() {
  /bin/cp "$1" "$output"
  if [ "$server_response" = "1" ]; then
    printf '  HTTP/1.1 200 OK\r\n' >&2
  fi
  exit 0
}

mode=%s
case "$mode:$url" in
  downgrade-archive:https://example.test/*/releases/download/*/%s)
    if [ "$max_redirect" = "0" ]; then
      emit_redirect 302 "http://mirror.example.test/%s"
    fi
    url="http://mirror.example.test/%s"
    printf 'FOLLOWED %%s\n' "$url" >> %s
    serve_file %s
    ;;
  downgrade-archive:http://mirror.example.test/%s)
    serve_file %s
    ;;
  downgrade-archive:https://example.test/*/releases/download/*/%s)
    if [ "$max_redirect" = "0" ]; then
      emit_redirect 302 "http://mirror.example.test/%s"
    fi
    url="http://mirror.example.test/%s"
    printf 'FOLLOWED %%s\n' "$url" >> %s
    serve_file %s
    ;;
  downgrade-archive:http://mirror.example.test/%s)
    serve_file %s
    ;;
  downgrade-api:https://api.example.test/repos/*/releases/latest)
    if [ "$max_redirect" = "0" ]; then
      emit_redirect 302 "http://api.example.test/insecure-latest"
    fi
    url="http://api.example.test/insecure-latest"
    printf 'FOLLOWED %%s\n' "$url" >> %s
    serve_file %s
    ;;
  downgrade-api:http://api.example.test/insecure-latest)
    serve_file %s
    ;;
  https-chain:https://example.test/*/releases/download/*/%s)
    if [ "$max_redirect" = "0" ]; then
      emit_redirect 302 "/hop/%s"
    fi
    serve_file %s
    ;;
  https-chain:https://example.test/hop/%s)
    if [ "$max_redirect" = "0" ]; then
      emit_redirect 301 "https://cdn.example.test/assets/%s"
    fi
    serve_file %s
    ;;
  https-chain:https://cdn.example.test/assets/%s)
    serve_file %s
    ;;
  https-chain:https://example.test/*/releases/download/*/%s)
    if [ "$max_redirect" = "0" ]; then
      emit_redirect 302 "https://cdn.example.test/assets/%s"
    fi
    serve_file %s
    ;;
  https-chain:https://cdn.example.test/assets/%s)
    serve_file %s
    ;;
  insecure-opt-in:http://example.test/*/%s)
    serve_file %s
    ;;
  insecure-opt-in:http://example.test/*/%s)
    serve_file %s
    ;;
esac

echo "unexpected mock-wget call ($mode): $url" >&2
exit 4
`,
		shellQuote(logPath),
		shellQuote(mode),
		fixture.archiveName, fixture.archiveName, fixture.archiveName, shellQuote(logPath), shellQuote(fixture.archivePath),
		fixture.archiveName, shellQuote(fixture.archivePath),
		checksumName, checksumName, checksumName, shellQuote(logPath), shellQuote(fixture.checksumPath),
		checksumName, shellQuote(fixture.checksumPath),
		shellQuote(logPath), shellQuote(latestJSON),
		shellQuote(latestJSON),
		fixture.archiveName, fixture.archiveName, shellQuote(fixture.archivePath),
		fixture.archiveName, fixture.archiveName, shellQuote(fixture.archivePath),
		fixture.archiveName, shellQuote(fixture.archivePath),
		checksumName, checksumName, shellQuote(fixture.checksumPath),
		checksumName, shellQuote(fixture.checksumPath),
		fixture.archiveName, shellQuote(fixture.archivePath),
		checksumName, shellQuote(fixture.checksumPath),
	)
	writeFile(t, wgetBin, []byte(script), 0o755)
	return wgetBin, logPath
}

func TestUnixInstallerWgetRefusesHTTPSToHTTPRedirects(t *testing.T) {
	t.Run("archive redirect to http is refused", func(t *testing.T) {
		fixture := newUnixInstallFixture(t)
		wgetBin, logPath := writeMockRedirectWget(t, fixture, "downgrade-archive")
		pathOnlyWget := wgetOnlyPath(t, wgetBin)

		stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, "", "0.1.0", "PATH="+pathOnlyWget)
		if err == nil {
			t.Fatalf("install.sh with wget followed an https->http redirect\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if !strings.Contains(stderr, "https://") || !strings.Contains(stderr, "QG_ALLOW_INSECURE_DOWNLOAD=1") {
			t.Fatalf("expected https redirect refusal naming QG_ALLOW_INSECURE_DOWNLOAD=1, got:\n%s", stderr)
		}
		logText := string(readFile(t, logPath))
		if strings.Contains(logText, "http://") {
			t.Fatalf("wget fetched plain http target:\n%s", logText)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.installDir, "qg")); !os.IsNotExist(statErr) {
			t.Fatalf("binary installed across an https->http redirect: %v", statErr)
		}
	})

	t.Run("latest release metadata redirect to http is refused", func(t *testing.T) {
		fixture := newUnixInstallFixture(t)
		wgetBin, logPath := writeMockRedirectWget(t, fixture, "downgrade-api")
		pathOnlyWget := wgetOnlyPath(t, wgetBin)

		stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, "", "latest",
			"PATH="+pathOnlyWget,
			"QG_GITHUB_API=https://api.example.test",
		)
		if err == nil {
			t.Fatalf("install.sh with wget followed an https->http API redirect\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if !strings.Contains(stderr, "https://") || !strings.Contains(stderr, "QG_ALLOW_INSECURE_DOWNLOAD=1") {
			t.Fatalf("expected https redirect refusal naming QG_ALLOW_INSECURE_DOWNLOAD=1, got:\n%s", stderr)
		}
		if logText := string(readFile(t, logPath)); strings.Contains(logText, "http://") {
			t.Fatalf("wget fetched plain http API target:\n%s", logText)
		}
	})

	t.Run("https redirects including relative Location keep working", func(t *testing.T) {
		fixture := newUnixInstallFixture(t)
		wgetBin, _ := writeMockRedirectWget(t, fixture, "https-chain")
		pathOnlyWget := wgetOnlyPath(t, wgetBin)

		stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, "", "0.1.0", "PATH="+pathOnlyWget)
		if err != nil {
			t.Fatalf("install.sh with wget failed across https redirects: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.installDir, "qg")); statErr != nil {
			t.Fatalf("installed binary missing after https redirects: %v", statErr)
		}
	})

	t.Run("explicit opt-in still allows plain http with wget", func(t *testing.T) {
		fixture := newUnixInstallFixture(t)
		wgetBin, _ := writeMockRedirectWget(t, fixture, "insecure-opt-in")
		pathOnlyWget := wgetOnlyPath(t, wgetBin)

		stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, "", "0.1.0",
			"PATH="+pathOnlyWget,
			"QG_GITHUB_BASE_URL=http://example.test",
			"QG_ALLOW_INSECURE_DOWNLOAD=1",
		)
		if err != nil {
			t.Fatalf("install.sh with wget refused explicit opt-in: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
	})

	t.Run("real wget refuses https to http redirect", func(t *testing.T) {
		realWget := requireCommand(t, "wget")
		fixture := newUnixInstallFixture(t)
		pathOnlyWget := wgetOnlyPath(t, realWget)
		archiveData := readFile(t, fixture.archivePath)
		checksumData := readFile(t, fixture.checksumPath)
		checksumName := fixture.archiveName + ".sha256"

		var plainHits atomic.Int32
		plainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plainHits.Add(1)
			switch {
			case strings.HasSuffix(r.URL.Path, "/"+fixture.archiveName):
				_, _ = w.Write(archiveData)
			case strings.HasSuffix(r.URL.Path, "/"+checksumName):
				_, _ = w.Write(checksumData)
			default:
				http.NotFound(w, r)
			}
		}))
		defer plainServer.Close()

		secureServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plainServer.URL+r.URL.Path, http.StatusFound)
		}))
		defer secureServer.Close()

		wgetrc := filepath.Join(t.TempDir(), "wgetrc")
		writeFile(t, wgetrc, []byte("check_certificate = off\n"), 0o600)

		stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, "", "0.1.0",
			"PATH="+pathOnlyWget,
			"WGETRC="+wgetrc,
			"QG_GITHUB_BASE_URL="+secureServer.URL,
		)
		if err == nil {
			t.Fatalf("install.sh with real wget installed across an https->http redirect\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if got := plainHits.Load(); got != 0 {
			t.Fatalf("real wget requested the plain http redirect target %d time(s)", got)
		}
		if !strings.Contains(stderr, "https://") || !strings.Contains(stderr, "QG_ALLOW_INSECURE_DOWNLOAD=1") {
			t.Fatalf("expected https redirect refusal naming QG_ALLOW_INSECURE_DOWNLOAD=1, got:\n%s", stderr)
		}
	})
}

func newLocalhostTLSServer(t *testing.T, handler http.Handler) (baseURL string, certPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPath = filepath.Join(t.TempDir(), "ca.pem")
	writeFile(t, certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)

	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{{
			Certificate: [][]byte{der},
			PrivateKey:  priv,
		}},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("https://localhost:%d", port), certPath
}

func TestUnixInstallerBusyBoxWget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix installer smoke does not run on Windows")
	}
	busybox := requireCommand(t, "busybox")

	t.Run("https download with https redirect installs", func(t *testing.T) {
		fixture := newUnixInstallFixture(t)
		pathOnlyBusyBox := wgetOnlyPath(t, busybox)
		archiveData := readFile(t, fixture.archivePath)
		checksumData := readFile(t, fixture.checksumPath)
		checksumName := fixture.archiveName + ".sha256"

		var baseURL atomic.Value // string: publicada antes do instalador rodar, lida pelos handlers HTTP
		redirectBase := func() string { return baseURL.Load().(string) }
		mux := http.NewServeMux()
		mux.HandleFunc("/repos/quebragalho-dev/quebragalho-cli/releases/latest", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectBase()+"/api-final/latest", http.StatusFound)
		})
		mux.HandleFunc("/api-final/latest", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tag_name":"v0.1.0"}`))
		})
		mux.HandleFunc("/quebragalho-dev/quebragalho-cli/releases/download/v0.1.0/"+fixture.archiveName, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/hop/"+fixture.archiveName, http.StatusFound)
		})
		mux.HandleFunc("/hop/"+fixture.archiveName, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectBase()+"/cdn/"+fixture.archiveName, http.StatusMovedPermanently)
		})
		mux.HandleFunc("/cdn/"+fixture.archiveName, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(archiveData)
		})
		mux.HandleFunc("/quebragalho-dev/quebragalho-cli/releases/download/v0.1.0/"+checksumName, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, redirectBase()+"/cdn/"+checksumName, http.StatusFound)
		})
		mux.HandleFunc("/cdn/"+checksumName, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(checksumData)
		})
		serverURL, certPath := newLocalhostTLSServer(t, mux)
		baseURL.Store(serverURL)

		stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, "", "latest",
			"PATH="+pathOnlyBusyBox,
			"SSL_CERT_FILE="+certPath,
			"QG_GITHUB_API="+redirectBase(),
			"QG_GITHUB_BASE_URL="+redirectBase(),
		)
		if err != nil {
			t.Fatalf("install.sh with busybox wget failed across https redirects: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		installed := readFile(t, filepath.Join(fixture.installDir, "qg"))
		if !strings.Contains(string(installed), "mock-qg") {
			t.Fatalf("installed binary = %q, want mock-qg", string(installed))
		}
	})

	t.Run("https to http redirect is refused and downloaded file is removed", func(t *testing.T) {
		fixture := newUnixInstallFixture(t)
		pathOnlyBusyBox := wgetOnlyPath(t, busybox)
		archiveData := readFile(t, fixture.archivePath)
		checksumData := readFile(t, fixture.checksumPath)
		checksumName := fixture.archiveName + ".sha256"

		var plainHits atomic.Int32
		plainServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			plainHits.Add(1)
			switch {
			case strings.HasSuffix(r.URL.Path, "/"+fixture.archiveName):
				_, _ = w.Write(archiveData)
			case strings.HasSuffix(r.URL.Path, "/"+checksumName):
				_, _ = w.Write(checksumData)
			default:
				http.NotFound(w, r)
			}
		}))
		defer plainServer.Close()

		baseURL, certPath := newLocalhostTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plainServer.URL+r.URL.Path, http.StatusFound)
		}))

		customTmp := t.TempDir()
		stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, "", "0.1.0",
			"PATH="+pathOnlyBusyBox,
			"SSL_CERT_FILE="+certPath,
			"TMPDIR="+customTmp,
			"QG_GITHUB_BASE_URL="+baseURL,
		)
		if err == nil {
			t.Fatalf("install.sh with busybox wget succeeded across https->http redirect\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
		}
		if !strings.Contains(stderr, "redirecionamento inseguro recusado") || !strings.Contains(stderr, "https://") {
			t.Fatalf("expected insecure redirect refusal in stderr, got:\n%s", stderr)
		}
		// BusyBox wget cannot stop before following the 302, so it hits the
		// archive URL once and secure_wget must abort before checksum/install.
		if got := plainHits.Load(); got != 1 {
			t.Fatalf("plain HTTP server hits = %d, want 1 (aborted right after first download)", got)
		}
		if _, statErr := os.Stat(filepath.Join(fixture.installDir, "qg")); !os.IsNotExist(statErr) {
			t.Fatalf("installed binary exists after refused redirect: %v", statErr)
		}
		entries, readErr := os.ReadDir(customTmp)
		if readErr != nil {
			t.Fatalf("ReadDir %s: %v", customTmp, readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("downloaded/temp files left behind in %s: %v", customTmp, entries)
		}
	})
}

func TestPowerShellInstallerRefusesHTTPSToHTTPRedirects(t *testing.T) {
	script := readRepoText(t, "scripts/install.ps1")

	containsAll(t, script, []string{
		`function Invoke-QGSecureWebRequest`,
		`MaximumRedirection = 0`,
		`New-Object System.Uri`,
		`Redirecionamento inseguro recusado para`,
		`Invoke-QGSecureWebRequest -Uri "$releaseUrl/$archiveName"`,
		`Invoke-QGSecureWebRequest -Uri "$releaseUrl/$checksumName"`,
	})
	if strings.Contains(script, `Invoke-WebRequest -Uri "$releaseUrl/`) {
		t.Fatal("install.ps1 still calls unguarded Invoke-WebRequest for release assets")
	}
	if strings.Contains(script, `Invoke-RestMethod`) {
		t.Fatal("install.ps1 still calls unguarded Invoke-RestMethod without redirect scheme validation")
	}

	psExe := ""
	for _, candidate := range []string{"pwsh", "powershell"} {
		if found, err := exec.LookPath(candidate); err == nil {
			psExe = found
			break
		}
	}
	if psExe == "" {
		return
	}

	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	harnessPath := filepath.Join(root, "run-test.ps1")
	scriptPath := filepath.Join(repoRoot(t), "scripts", "install.ps1")
	harness := fmt.Sprintf(`$ErrorActionPreference = "Stop"
$env:QG_ALLOW_INSECURE_DOWNLOAD = ""
$env:PROCESSOR_ARCHITECTURE = "AMD64"
$env:PROCESSOR_ARCHITEW6432 = ""
function Invoke-WebRequest {
  param(
    [string]$Uri,
    [string]$OutFile,
    [hashtable]$Headers,
    [int]$TimeoutSec,
    [int]$MaximumRedirection = -1,
    [switch]$UseBasicParsing,
    [switch]$PassThru
  )
  if ($Uri -like "http://*") {
    throw "PLAIN_HTTP_FETCHED:$Uri"
  }
  if ($MaximumRedirection -ne 0) {
    throw "REDIRECT_NOT_GUARDED"
  }
  if ($OutFile) {
    Set-Content -LiteralPath $OutFile -Value "redirect-body"
  }
  return [PSCustomObject]@{
    StatusCode = 302
    Headers    = @{ Location = "http://evil.example.test/qg.zip" }
    Content    = ""
  }
}
& %s -Version "0.1.0" -InstallDir %s -GitHubBaseUrl "https://example.test"
`, quotePowerShellSingle(scriptPath), quotePowerShellSingle(installDir))
	writeFile(t, harnessPath, []byte(harness), 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, psExe, "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", harnessPath).CombinedOutput()
	if err == nil {
		t.Fatalf("install.ps1 succeeded across an https->http redirect:\n%s", out)
	}
	text := string(out)
	if strings.Contains(text, "PLAIN_HTTP_FETCHED") || strings.Contains(text, "REDIRECT_NOT_GUARDED") {
		t.Fatalf("install.ps1 did not guard Invoke-WebRequest redirects:\n%s", text)
	}
	if !strings.Contains(text, "https://") || !strings.Contains(text, "QG_ALLOW_INSECURE_DOWNLOAD=1") {
		t.Fatalf("install.ps1 error missing https redirect refusal:\n%s", text)
	}
}

func quotePowerShellSingle(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}
