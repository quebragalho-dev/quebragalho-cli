package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCredentialDenyReadPathsCoversIssue815Stores(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows sandbox isolation is handled separately")
	}
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	sshDir := filepath.Join(home, ".ssh")
	gnupgDir := filepath.Join(home, ".gnupg")
	awsDir := filepath.Join(home, ".aws")
	azureDir := filepath.Join(home, ".azure")
	gcloudDir := filepath.Join(configHome, "gcloud")
	kubeDir := filepath.Join(home, ".kube")
	dockerDir := filepath.Join(home, ".docker")
	ghDir := filepath.Join(configHome, "gh")
	qgDir := filepath.Join(configHome, "qg")

	for _, dir := range []string{sshDir, gnupgDir, awsDir, azureDir, gcloudDir, kubeDir, dockerDir, ghDir, qgDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	files := []string{
		filepath.Join(sshDir, "id_ed25519"),
		filepath.Join(gnupgDir, "private-keys-v1.d"),
		filepath.Join(awsDir, "credentials"),
		filepath.Join(azureDir, "accessTokens.json"),
		filepath.Join(gcloudDir, "credentials.db"),
		filepath.Join(kubeDir, "config"),
		filepath.Join(kubeDir, "client.key"),
		filepath.Join(dockerDir, "config.json"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, ".git-credentials"),
		filepath.Join(home, ".npmrc"),
		filepath.Join(home, ".pypirc"),
		filepath.Join(ghDir, "hosts.yml"),
		filepath.Join(ghDir, "config.yml"),
		filepath.Join(qgDir, "credentials.enc"),
		filepath.Join(qgDir, "credentials.json"),
	}
	for _, file := range files {
		if err := os.WriteFile(file, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("GNUPGHOME", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("NPM_CONFIG_USERCONFIG", "")
	t.Setenv("npm_config_userconfig", "")
	t.Setenv("GH_CONFIG_DIR", "")
	t.Setenv("NETRC", "")
	t.Setenv("DOCKER_CONFIG", "")
	t.Setenv("KUBECONFIG", "")

	profile := permissionProfileFromPolicy(t.TempDir(), DefaultPolicy(), nil, "", nil)
	denied := profile.FileSystem.DenyReadIfExists

	isCovered := func(target string) bool {
		norm := normalizeProfilePath(target)
		for _, entry := range denied {
			if entry == norm || pathWithinRoot(entry, norm) {
				return true
			}
		}
		return false
	}

	for _, target := range append([]string{
		sshDir,
		gnupgDir,
		awsDir,
		azureDir,
		gcloudDir,
		kubeDir,
		filepath.Join(dockerDir, "config.json"),
		filepath.Join(home, ".netrc"),
		filepath.Join(home, ".git-credentials"),
		filepath.Join(home, ".npmrc"),
		filepath.Join(home, ".pypirc"),
		ghDir,
		qgDir,
	}, files...) {
		if !isCovered(target) {
			t.Errorf("credential path %q is not denied in DenyReadIfExists = %#v", target, denied)
		}
	}

	// Verify both Seatbelt and Linux bwrap plans enforce the newly added stores.
	sbpl := seatbeltProfileFromPermissionProfile(profile, DefaultPolicy(), "")
	for _, mustDeny := range []string{sshDir, gnupgDir, kubeDir, filepath.Join(home, ".pypirc"), ghDir} {
		norm := normalizeProfilePath(mustDeny)
		if !strings.Contains(sbpl, sandboxProfileString(norm)) {
			t.Errorf("seatbelt profile missing deny rule for %q:\n%s", norm, sbpl)
		}
	}

	bwrap := buildLinuxBwrapFilesystemPlan(profile)
	joinedBwrap := strings.Join(bwrap.Args, "\x00")
	for _, dir := range []string{sshDir, gnupgDir, kubeDir, ghDir} {
		norm := normalizeProfilePath(dir)
		if !strings.Contains(joinedBwrap, "--perms\x00000\x00--tmpfs\x00"+norm) {
			t.Errorf("bwrap args missing unreadable dir mask for %q: %#v", norm, bwrap.Args)
		}
	}
	pypircNorm := normalizeProfilePath(filepath.Join(home, ".pypirc"))
	if !strings.Contains(joinedBwrap, "--ro-bind\x00/dev/null\x00"+pypircNorm) {
		t.Errorf("bwrap args missing /dev/null mask for %q: %#v", pypircNorm, bwrap.Args)
	}
}

func TestScrubSensitiveEnvCoversIssue815TokensAndRedactionKeys(t *testing.T) {
	inputEnv := []string{
		"PATH=/usr/bin",
		"SAFE_VAR=hello",
		"AWS_PROFILE=staging",
		"GITHUB_TOKEN=ghp_secret",
		"GH_TOKEN=gho_secret",
		"NPM_TOKEN=npm_secret",
		"NODE_AUTH_TOKEN=node_secret",
		"QUEBRAGALHO_API_KEY=qg_secret",
		"QG_API_KEY=qg_primary_secret",
		"AWS_SECRET_ACCESS_KEY=aws_secret",
		"AWS_SESSION_TOKEN=aws_session",
		"AZURE_CLIENT_SECRET=azure_secret",
		"PYPI_TOKEN=pypi_secret",
		"TWINE_PASSWORD=twine_secret",
		"QG_OAUTH_STORAGE=file",
	}

	scrubbed := scrubSensitiveEnv(inputEnv)
	got := make(map[string]string, len(scrubbed))
	for _, entry := range scrubbed {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			got[k] = v
		}
	}

	for _, leaked := range []string{
		"GITHUB_TOKEN",
		"GH_TOKEN",
		"NPM_TOKEN",
		"NODE_AUTH_TOKEN",
		"QUEBRAGALHO_API_KEY",
		"QG_API_KEY",
		"AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN",
		"AZURE_CLIENT_SECRET",
		"PYPI_TOKEN",
		"TWINE_PASSWORD",
	} {
		if _, present := got[leaked]; present {
			t.Errorf("scrubSensitiveEnv leaked %q in %v", leaked, got)
		}
	}
	for _, keep := range []string{"PATH", "SAFE_VAR", "AWS_PROFILE", "QG_OAUTH_STORAGE"} {
		if _, present := got[keep]; !present {
			t.Errorf("scrubSensitiveEnv unexpectedly dropped %q in %v", keep, got)
		}
	}
}
