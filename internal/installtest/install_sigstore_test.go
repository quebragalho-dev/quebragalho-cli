package installtest

import (
	"os"
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

func TestPowerShellInstallerGatesSigstoreOnReleaseVersion(t *testing.T) {
	script := readRepoText(t, "scripts/install.ps1")
	containsAll(t, script, []string{
		`[version]"0.0.22"`,
		"$bundlePresent",
		"nao publica o bundle Sigstore",
		"nao tem assinatura Sigstore",
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

// O comando manual impresso tem os argumentos na ordem certa: copiável.
func TestUnixInstallerManualVerifyCommandIsCopyPasteable(t *testing.T) {
	if _, err := exec.LookPath("cosign"); err == nil {
		t.Skip("cosign is on PATH, cannot test the fallback hint")
	}
	fixture := newUnixInstallFixture(t)

	_, stderr, err := runUnixInstaller(t, fixture)
	if err != nil {
		t.Fatalf("install.sh without cosign failed: %v\nstderr:\n%s", err, stderr)
	}
	pairing := "--bundle " + fixture.bundleName + " --certificate-identity-regexp '"
	if !strings.Contains(stderr, pairing) {
		t.Fatalf("manual verify command pairs bundle and identity wrongly, want %q in:\n%s", pairing, stderr)
	}
	if !strings.Contains(stderr, "@refs/tags/v0.1.0") {
		t.Fatalf("manual verify command should pin the installed tag, got:\n%s", stderr)
	}
}

func removeFixtureBundle(t *testing.T, fixture unixInstallFixture) {
	t.Helper()
	if err := os.Remove(fixture.bundlePath); err != nil {
		t.Fatalf("Remove bundle: %v", err)
	}
}

// Release antiga (anterior a 0.0.22) sem bundle: com cosign no PATH, avisa
// numa linha e segue só com o SHA-256, sem chamar o cosign.
func TestUnixInstallerWarnsAndContinuesForUnsignedOldRelease(t *testing.T) {
	fixture := newUnixInstallFixtureVersion(t, "0.0.21")
	removeFixtureBundle(t, fixture)
	cosignLog := filepath.Join(t.TempDir(), "cosign-args")
	writeFakeCosign(t, fixture.mockBin, cosignLog, 0)

	stdout, stderr, err := runUnixInstallerWithEnv(t, fixture, "", "0.0.21")
	if err != nil {
		t.Fatalf("install.sh 0.0.21 without bundle failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "v0.0.21 não tem assinatura Sigstore") {
		t.Fatalf("stderr missing old-release warning:\n%s", stderr)
	}
	if _, err := os.Stat(cosignLog); !os.IsNotExist(err) {
		t.Fatal("cosign must not run when the old release has no bundle")
	}
}

// Release nova sem bundle: com cosign no PATH, recusa em vez de instalar.
func TestUnixInstallerRefusesNewReleaseWithoutBundle(t *testing.T) {
	fixture := newUnixInstallFixture(t)
	removeFixtureBundle(t, fixture)
	cosignLog := filepath.Join(t.TempDir(), "cosign-args")
	writeFakeCosign(t, fixture.mockBin, cosignLog, 0)

	_, stderr, err := runUnixInstaller(t, fixture)
	if err == nil {
		t.Fatal("install.sh succeeded without the required bundle")
	}
	if !strings.Contains(stderr, "não publica o bundle Sigstore") {
		t.Fatalf("stderr missing missing-bundle refusal:\n%s", stderr)
	}
	if _, err := os.Stat(cosignLog); !os.IsNotExist(err) {
		t.Fatal("cosign must not run without a bundle to verify")
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
