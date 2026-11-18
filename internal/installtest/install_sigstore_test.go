package installtest

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Scripts citam o fluxo Sigstore e recusam pacote com assinatura inválida.
func TestUnixInstallerScriptMentionsSigstoreVerification(t *testing.T) {
	script := readRepoText(t, "scripts/install.sh")
	containsAll(t, script, []string{
		`bundle_name="${archive_name}.sigstore.json"`,
		"command -v cosign",
		"cosign verify-blob --bundle",
		"--certificate-identity-regexp",
		"--certificate-oidc-issuer https://token.actions.githubusercontent.com",
		"release-artifacts",
		"@refs/tags/",
		"assinatura Sigstore inválida",
		"cosign ausente, assinatura Sigstore não verificada",
		"e rode: cosign verify-blob",
	})
}

func TestPowerShellInstallerScriptMentionsSigstoreVerification(t *testing.T) {
	script := readRepoText(t, "scripts/install.ps1")
	containsAll(t, script, []string{
		`$bundleName = "$archiveName.sigstore.json"`,
		"Get-Command cosign",
		"cosign verify-blob --bundle",
		"--certificate-identity-regexp",
		"--certificate-oidc-issuer",
		"token.actions.githubusercontent.com",
		"release-artifacts\\.yml@refs/tags/",
		"Assinatura Sigstore invalida",
		"cosign ausente, assinatura Sigstore nao verificada",
	})
}

func writeFakeCosign(t *testing.T, mockBin string, logPath string, exitCode int) {
	t.Helper()
	script := "#!/usr/bin/env sh\nprintf '%s\\n' \"$@\" > " + logPath + "\nexit " + strconv.Itoa(exitCode) + "\n"
	writeFile(t, filepath.Join(mockBin, "cosign"), []byte(script), 0o755)
}

// Com cosign no PATH, o instalador baixa o bundle e só instala se o
// cosign confirmar a assinatura com a identidade do workflow da release.
func TestUnixInstallerVerifiesSigstoreBundleWhenCosignPresent(t *testing.T) {
	fixture := newUnixInstallFixture(t)
	logPath := filepath.Join(t.TempDir(), "cosign-args")
	writeFakeCosign(t, fixture.mockBin, logPath, 0)

	stdout, stderr, err := runUnixInstaller(t, fixture)
	if err != nil {
		t.Fatalf("install.sh with verifying cosign failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "Verificando assinatura Sigstore") {
		t.Fatalf("stderr missing Sigstore verification stage:\n%s", stderr)
	}
	args := string(readFile(t, logPath))
	containsAll(t, args, []string{
		"verify-blob",
		"--bundle",
		fixture.bundleName,
		"--certificate-identity-regexp",
		"release-artifacts\\.yml@refs/tags/v0.1.0",
		"--certificate-oidc-issuer",
		"https://token.actions.githubusercontent.com",
		fixture.archiveName,
	})
}

// Assinatura recusada pelo cosign recusa a instalação, em português.
func TestUnixInstallerRefusesInvalidSigstoreSignature(t *testing.T) {
	fixture := newUnixInstallFixture(t)
	writeFakeCosign(t, fixture.mockBin, filepath.Join(t.TempDir(), "cosign-args"), 1)

	_, stderr, err := runUnixInstaller(t, fixture)
	if err == nil {
		t.Fatal("install.sh succeeded with a rejected Sigstore signature")
	}
	if !strings.Contains(stderr, "assinatura Sigstore inválida") {
		t.Fatalf("stderr missing refusal message:\n%s", stderr)
	}
}

// Sem cosign no PATH, segue com SHA-256 e diz como verificar manualmente.
func TestUnixInstallerPrintsManualVerifyWithoutCosign(t *testing.T) {
	if _, err := exec.LookPath("cosign"); err == nil {
		t.Skip("cosign is on PATH, cannot test the fallback hint")
	}
	fixture := newUnixInstallFixture(t)

	_, stderr, err := runUnixInstaller(t, fixture)
	if err != nil {
		t.Fatalf("install.sh without cosign failed: %v\nstderr:\n%s", err, stderr)
	}
	containsAll(t, stderr, []string{
		"cosign ausente, assinatura Sigstore não verificada",
		"cosign verify-blob --bundle",
		fixture.bundleName,
		"release-artifacts\\.yml@refs/tags/v0.1.0",
	})
}
