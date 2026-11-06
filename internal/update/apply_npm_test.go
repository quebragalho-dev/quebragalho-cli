package update

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// npmApplyFixture builds a fake global npm layout:
// <prefix>/lib/node_modules/@quebragalho/cli with a binary, a marker,
// and a wrapper manifest at version. It returns the prefix and the binary.
func npmApplyFixture(t *testing.T, version string) (string, string) {
	t.Helper()
	prefix := t.TempDir()
	// Apply resolves symlinks on the executable path (macOS /var -> /private/var),
	// so canonicalize the prefix the same way before comparing.
	resolved, err := filepath.EvalSymlinks(prefix)
	if err != nil {
		t.Fatal(err)
	}
	prefix = resolved
	root := filepath.Join(prefix, "lib", "node_modules", "@quebragalho", "cli")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNpmWrapperPackage(t, root, version)
	binary := filepath.Join(root, "qg")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return prefix, binary
}

func stubCurrentExecutable(t *testing.T, binary string) {
	t.Helper()
	original := currentExecutable
	currentExecutable = func() (string, error) { return binary, nil }
	t.Cleanup(func() { currentExecutable = original })
}

// stubNpmRun replaces runNpm with a fake that records its arguments and runs
// install instead. It returns the recorded arguments.
func stubNpmRun(t *testing.T, install func()) *[]string {
	t.Helper()
	var got []string
	original := runNpm
	runNpm = func(_ context.Context, _ string, args []string) error {
		got = append([]string{}, args...)
		if install != nil {
			install()
		}
		return nil
	}
	t.Cleanup(func() { runNpm = original })
	return &got
}

// TestApplyNpmUpdateUsesPrefixAndVerifiesVersion is the regression test for
// the --prefix bug: a global install owned by another prefix must be updated
// in place, and success requires the invoked install to carry the new
// version.
func TestApplyNpmUpdateUsesPrefixAndVerifiesVersion(t *testing.T) {
	prefix, binary := npmApplyFixture(t, "0.1.0")
	stubCurrentExecutable(t, binary)
	root := filepath.Join(prefix, "lib", "node_modules", "@quebragalho", "cli")
	gotArgs := stubNpmRun(t, func() {
		writeNpmWrapperPackage(t, root, "0.2.0")
	})

	result, err := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.2.0", "linux", "amd64"), nil
		},
	})
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Applied || result.InstallMethod != InstallMethodNpm {
		t.Fatalf("unexpected result: %#v", result)
	}
	wantArgs := []string{"install", "-g", "--prefix", prefix, npmPackageName + "@0.2.0"}
	if !reflect.DeepEqual(*gotArgs, wantArgs) {
		t.Fatalf("npm args = %q, want %q", *gotArgs, wantArgs)
	}
	// The machine contract stays in English even though humans read pt-BR.
	if result.Message != "updated via npm to 0.2.0" {
		t.Fatalf("Message = %q, want the English machine message", result.Message)
	}
	human := FormatApply(result)
	if !strings.Contains(human, "atualizado via npm para 0.2.0") {
		t.Fatalf("human output = %q, want the pt-BR success line", human)
	}
	if strings.Contains(human, "updated via npm to") {
		t.Fatalf("human output kept the English message: %q", human)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Message != "updated via npm to 0.2.0" {
		t.Fatalf("JSON message = %q, want byte-identical English", decoded.Message)
	}
}

// TestApplyNpmUpdateFailsWhenInvokedVersionUnchanged covers the fake-success
// the bug produced: npm exits 0 but the invoked install is untouched (it
// installed elsewhere), so Apply must error instead of reporting "updated".
func TestApplyNpmUpdateFailsWhenInvokedVersionUnchanged(t *testing.T) {
	prefix, binary := npmApplyFixture(t, "0.1.0")
	stubCurrentExecutable(t, binary)
	// The fake npm succeeds without touching the invoked root, as a bare
	// `npm install -g` into another prefix does.
	gotArgs := stubNpmRun(t, nil)

	_, err := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.2.0", "linux", "amd64"), nil
		},
	})
	if err == nil {
		t.Fatal("expected an error when the invoked install did not move")
	}
	if !strings.Contains(err.Error(), "não ficou ativa") {
		t.Fatalf("error = %v, want the honest pt-BR failure", err)
	}
	if !strings.Contains(err.Error(), "0.1.0") {
		t.Fatalf("error = %v, want it to name the stuck version", err)
	}
	if !strings.Contains(err.Error(), `npm install -g --prefix "`+prefix+`"`) {
		t.Fatalf("error = %v, want the manual command with the invoked prefix", err)
	}
	wantArgs := []string{"install", "-g", "--prefix", prefix, npmPackageName + "@0.2.0"}
	if !reflect.DeepEqual(*gotArgs, wantArgs) {
		t.Fatalf("npm args = %q, want %q", *gotArgs, wantArgs)
	}
}

// TestApplyNpmUpdateFailsWhenVersionMovesElsewhere guards the exact-version
// comparison: a partial move (some other version lands in the root) is still
// a failure, not an update.
func TestApplyNpmUpdateFailsWhenVersionMovesElsewhere(t *testing.T) {
	_, binary := npmApplyFixture(t, "0.1.0")
	stubCurrentExecutable(t, binary)
	root := filepath.Dir(binary)
	stubNpmRun(t, func() {
		writeNpmWrapperPackage(t, root, "0.1.5")
	})

	_, err := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.2.0", "linux", "amd64"), nil
		},
	})
	if err == nil {
		t.Fatal("expected an error when the invoked install moved to the wrong version")
	}
	if !strings.Contains(err.Error(), "não ficou ativa") || !strings.Contains(err.Error(), "0.1.5") {
		t.Fatalf("error = %v, want it to name the version actually installed", err)
	}
}

// TestApplyNpmUpdateRefusesWithoutPrefix pins the fail-closed no-prefix
// rule: when no prefix can be inferred the updater runs nothing and reports
// the manual command, instead of installing elsewhere and failing after.
func TestApplyNpmUpdateRefusesWithoutPrefix(t *testing.T) {
	dir := t.TempDir()
	writeNpmWrapperPackage(t, dir, "0.1.0")
	binary := filepath.Join(dir, "qg")
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubCurrentExecutable(t, binary)
	var calls int
	original := runNpm
	runNpm = func(context.Context, string, []string) error {
		calls++
		return nil
	}
	t.Cleanup(func() { runNpm = original })

	_, err := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.2.0", "linux", "amd64"), nil
		},
	})
	if err == nil {
		t.Fatal("expected an error when no npm prefix can be inferred")
	}
	if calls != 0 {
		t.Fatalf("runNpm ran %d times, want 0: nothing may be installed without a prefix", calls)
	}
	for _, want := range []string{
		"não foi possível determinar o prefixo npm",
		"nada foi instalado",
		"Para atualizar à mão, rode: npm install -g @quebragalho/cli@0.2.0",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "--prefix") {
		t.Fatalf("error = %v, must not invent a prefix it could not infer", err)
	}
}

// TestIsWritePermissionError pins the permission hint: EACCES-family text,
// and npm's EACCES exit status 243 observed on a real unwritable prefix.
func TestIsWritePermissionError(t *testing.T) {
	for _, text := range []string{
		"EACCES: permission denied, mkdir '/x'",
		"npm error code EPERM",
		"operation not permitted",
		"Access is denied",
	} {
		if !isWritePermissionError(errors.New(text)) {
			t.Errorf("isWritePermissionError(%q) = false, want true", text)
		}
	}
	if isWritePermissionError(errors.New("registry unreachable")) {
		t.Error("isWritePermissionError(plain failure) = true, want false")
	}
	if runtime.GOOS == "windows" {
		t.Skip("exit-status probe needs POSIX sh; Windows is covered by E2E VMs")
	}
	if err := exec.CommandContext(context.Background(), "sh", "-c", "exit 243").Run(); !isWritePermissionError(err) {
		t.Errorf("isWritePermissionError(exit 243) = false, want true (npm EACCES status)")
	}
	if err := exec.CommandContext(context.Background(), "sh", "-c", "exit 1").Run(); isWritePermissionError(err) {
		t.Errorf("isWritePermissionError(exit 1) = true, want false")
	}
}

// TestApplyNpmUpdateReportsMissingNpm pins the fail-closed no-npm path and
// its pt-BR guidance.
func TestApplyNpmUpdateReportsMissingNpm(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, binary := npmApplyFixture(t, "0.1.0")
	err := applyNpmUpdate(context.Background(), binary, "0.2.0")
	if err == nil {
		t.Fatal("expected an error when npm is not on PATH")
	}
	if !strings.Contains(err.Error(), "npm não encontrado") {
		t.Fatalf("error = %v, want the pt-BR missing-npm guidance", err)
	}
	if strings.Contains(err.Error(), "npm not found") {
		t.Fatalf("error = %v, kept the English missing-npm message", err)
	}
}

// TestApplyNpmUpdateMissingNpmNamesPrefixedManual pins the --prefix command
// in the missing-npm guidance when the prefix is inferable.
func TestApplyNpmUpdateMissingNpmNamesPrefixedManual(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	prefix, binary := npmApplyFixture(t, "0.1.0")
	err := applyNpmUpdate(context.Background(), binary, "0.2.0")
	if err == nil {
		t.Fatal("expected an error when npm is not on PATH")
	}
	want := `npm install -g --prefix "` + prefix + `" @quebragalho/cli@0.2.0`
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want the quoted manual command %q", err, want)
	}
}

// TestApplyNpmUpdateReportsPermissionError pins the EACCES path: the error
// names the unwritable prefix and appends the manual command.
func TestApplyNpmUpdateReportsPermissionError(t *testing.T) {
	prefix, binary := npmApplyFixture(t, "0.1.0")
	stubCurrentExecutable(t, binary)
	original := runNpm
	runNpm = func(context.Context, string, []string) error {
		return errors.New("EACCES: permission denied, mkdir '/opt/prefix/lib/node_modules'")
	}
	t.Cleanup(func() { runNpm = original })

	_, err := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.2.0", "linux", "amd64"), nil
		},
	})
	if err == nil {
		t.Fatal("expected an error when npm cannot write the prefix")
	}
	for _, want := range []string{
		"npm install -g --prefix",
		"Sem permissão de escrita em " + prefix,
		"Para atualizar à mão, rode: npm install -g",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want it to contain %q", err, want)
		}
	}
}

// TestApplyNpmUpdateReportsNpmFailure keeps the npm error path intact: a
// failing npm run is an error, never a success.
func TestApplyNpmUpdateReportsNpmFailure(t *testing.T) {
	_, binary := npmApplyFixture(t, "0.1.0")
	stubCurrentExecutable(t, binary)
	wantErr := errors.New("registry unreachable")
	original := runNpm
	runNpm = func(context.Context, string, []string) error { return wantErr }
	t.Cleanup(func() { runNpm = original })

	_, err := Apply(context.Background(), Options{
		CurrentVersion: "0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		Fetch: func(_ context.Context, _ string) (Release, error) {
			return releaseForTarget(t, "v0.2.0", "linux", "amd64"), nil
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Apply error = %v, want the npm failure %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "Para atualizar à mão, rode: npm install -g") {
		t.Fatalf("Apply error = %v, want the manual command appended", err)
	}
	if strings.Contains(err.Error(), "Sem permissão") {
		t.Fatalf("Apply error = %v, must not claim a permission refusal for a plain failure", err)
	}
}
